package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"mygo-music/internal/music"
	"mygo-music/internal/mv"
)

// mvState 是一首歌的 MV 背景在该设备上的全部状态。
//
// 只有真的在放 MV 时才持有播放器与代理：解码器会一直占着显存和带宽，
// 切歌、关闭视图或退出时都要放掉。
type mvState struct {
	key     string // 当前播放器的绑定键，Source\0TrackID
	player  *ui.VideoPlayer
	frame   *ui.Bitmap // 解码前与无头测试的占位画面
	proxy   *mv.Proxy
	err     string
	loading bool
	syncAt  time.Time // 上次硬跳的时间，用来做 1 秒冷却
}

// mvShown 决定当前视图要不要显示 MV 背景：
// 普通歌词与舞台显示，墙、评论、队列、节目页都不显示。
func (a *app) mvShown(c *ui.Context) bool {
	if !a.settings.MVBackground || a.pl.Current() == nil || c.Preferences().ReduceMotion {
		return false
	}
	switch a.npPane {
	case paneMV:
		return true
	default:
		return false
	}
}

// mvFailureLabel 是 MV 加载失败时角落提示的文案；只有当前这首仍保留失败状态时显示。
func (a *app) mvFailureLabel(tr *music.Track) string {
	if tr == nil || a.mv.key != bindingKeyOf(tr) || a.mv.err == "" {
		return ""
	}
	return T("MV couldn't load · Choose another")
}

// hasBinding 只让已经解出画面的歌改成 MV 正文；加载中仍显示原播放页，
// 避免绑定后出现一块没有画面的黑底。
func (st *mvState) hasBinding(tr *music.Track) bool {
	if st == nil || tr == nil || st.key != bindingKeyOf(tr) {
		return false
	}
	return st.player != nil || st.frame != nil
}

// mvSync 让画面跟上音频。
//
// 声音永远来自音频引擎，视频静音，只负责跟时间轴：小漂移微调播放速率，
// 大漂移直接跳，和 ECHO 的三档方案一致；首版固定「平衡」档。
func (a *app) mvSync(c *ui.Context, tr *music.Track) {
	st := &a.mv
	if st.player == nil || !a.mvShown(c) {
		return
	}
	p := st.player
	if p.Err() != nil {
		a.mvFailed(st.key, p.Err())
		return
	}
	if !p.Ready() {
		c.After(120 * time.Millisecond)
		return
	}
	// 暂停时画面冻结在当前帧，不跟随。
	if a.pl.Playing() {
		target := a.pl.Position() + time.Duration(a.mvOffset(tr))*time.Millisecond
		if dur := p.Duration(); dur > 0 {
			// 视频循环，按视频时长取模定位，MV 比歌短时也有画面。
			target = ((target % dur) + dur) % dur
		}
		drift := target - p.Position()
		switch {
		case drift >= mvTolerance && drift < mvHardSeek:
			// 微调速率让画面追上，不跳帧。
			rate := 1 + float64(drift)/float64(mvHardSeek)
			p.SetRate(min(max(rate, 1-mvMaxRate), 1+mvMaxRate))
		case drift <= -mvTolerance && drift > -mvHardSeek:
			rate := 1 + float64(drift)/float64(mvHardSeek)
			p.SetRate(min(max(rate, 1-mvMaxRate), 1+mvMaxRate))
		case drift >= mvHardSeek || drift <= -mvHardSeek:
			// 超过硬阈值才跳，1 秒冷却防抖。
			if c.Now().Sub(st.syncAt) > time.Second {
				p.Seek(target)
				st.syncAt = c.Now()
			}
		default:
			p.SetRate(1)
		}
	}
	c.After(400 * time.Millisecond)
}

func (a *app) mvOffset(tr *music.Track) int {
	if a.mvStore == nil {
		return 0
	}
	if b, ok := a.mvStore.Binding(tr.Source, tr.ID); ok {
		return b.OffsetMs
	}
	return 0
}

// 平衡档参数，和 ECHO 一致；档位常量留在代码里，不做设置项。
const (
	mvTolerance = 450 * time.Millisecond
	mvHardSeek  = 2 * time.Second
	mvMaxRate   = 0.12
)

// openMV 为当前歌曲准备背景视频。同一绑定已经加载时直接复用。
func (a *app) openMV() {
	tr := a.pl.Current()
	if tr == nil {
		return
	}
	a.ensureMVStore()
	if a.mvStore == nil {
		return
	}
	key := bindingKeyOf(tr)
	// 当前这首还没加载完时不要打扰它。
	if a.mv.key == key {
		return
	}
	a.closeMV()
	binding, ok := a.mvStore.Binding(tr.Source, tr.ID)
	if !ok {
		return
	}
	a.mv.key, a.mv.loading, a.mv.err = key, true, ""

	// 解析和启动代理都在后台；视图线程只收结果。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client := mv.NewClient()
		client.Cookie = a.mvCookie()
		if _, err := client.Resolve(ctx, binding.VideoID); err != nil {
			a.post(func() { a.mvFailed(key, err) })
			return
		}
		videoID := binding.VideoID
		proxy, err := mv.NewProxy(func(ctx context.Context) (*mv.Resolved, error) {
			// 直链过期时重新解析；登录态在请求时再取，退出登录立即生效。
			fresh := mv.NewClient()
			fresh.Cookie = a.mvCookie()
			return fresh.Resolve(ctx, videoID)
		}, a.mvCookie)
		if err != nil {
			a.post(func() { a.mvFailed(key, err) })
			return
		}
		a.post(func() { a.mvReady(key, proxy) })
	}()
}

// mvReady 在主线程上接住解析结果，打开播放器。
func (a *app) mvReady(key string, proxy *mv.Proxy) {
	// 切歌发生在解析途中：放掉这次结果，别让它盖掉新歌。
	if a.mv.key != key {
		proxy.Close()
		return
	}
	player, err := video.OpenURL(proxy.URL())
	if err != nil {
		proxy.Close()
		a.mvFailed(key, err)
		return
	}
	player.SetMuted(true).SetLoop(true)
	a.mv.player, a.mv.proxy = player, proxy
	a.mv.loading, a.mv.err = false, ""
}

func (a *app) mvFailed(key string, err error) {
	if a.mv.key != key {
		return
	}
	a.releaseMV()
	a.mv.loading, a.mv.err = false, err.Error()
}

// releaseMV 只释放解码器与代理，保留失败状态或当前绑定键供界面提示。
func (a *app) releaseMV() {
	if a.mv.player != nil {
		a.mv.player.Close()
		a.mv.player = nil
	}
	if a.mv.proxy != nil {
		a.mv.proxy.Close()
		a.mv.proxy = nil
	}
}

// closeMV 释放播放器与代理，并清空这首的失败状态。切歌、关闭视图、退出时调用。
func (a *app) closeMV() {
	a.releaseMV()
	a.mv = mvState{}
}

// ensureMVStore 第一次需要时读取绑定与缓存；失败不阻塞播放。
func (a *app) ensureMVStore() {
	if a.mvStore != nil {
		return
	}
	path, err := mvStorePath()
	if err != nil {
		a.mv.err = err.Error()
		return
	}
	store, err := mv.Open(path)
	if err != nil {
		a.mv.err = err.Error()
		return
	}
	a.mvStore = store
}

func mvStorePath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mv.json"), nil
}

func bindingKeyOf(tr *music.Track) string {
	return tr.Source + "\x00" + tr.ID
}

// mvCookie 返回 B 站登录态。首版还没有登录界面，之后接系统凭据管理器。
func (a *app) mvCookie() string { return a.mvCredential }
