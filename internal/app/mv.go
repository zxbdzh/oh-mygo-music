package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	key          string // 当前播放器的绑定键，Source\0TrackID
	player       *ui.VideoPlayer
	frame        *ui.Bitmap // 无头验收的占位画面
	proxy        *mv.Proxy
	info         mv.StreamInfo
	err          string // 只保留错误类别，原始错误可能带直链
	failureStage string
	loading      bool
	forceSync    bool
	syncAt       time.Time // 上次硬跳的时间，用来做 1 秒冷却
	epoch        uint64
	cancel       context.CancelFunc
	pan          ui.Point
	panKey       string
	panSize      ui.Point
}

var mvResolve = func(ctx context.Context, videoID string, options mv.QualityOptions, cookie string) (*mv.Resolved, error) {
	client := mv.NewClient()
	client.Cookie, client.Quality = cookie, options
	return client.Resolve(ctx, videoID)
}

func mvStreamResolver(videoID string, quality mv.QualityOptions, cookie string) func(context.Context) (*mv.Resolved, error) {
	resolve := mvResolve
	return func(ctx context.Context) (*mv.Resolved, error) {
		return resolve(ctx, videoID, quality, cookie)
	}
}

func (a *app) mvQualityOptions() mv.QualityOptions {
	return mv.QualityOptions{MaxHeight: a.settings.MVQuality, Prefer60FPS: a.settings.MV60FPS}
}

// mvShown 只在独立 MV 页启用视频，关闭播放页或减少动效时立刻停止加载。
func (a *app) mvShown(c *ui.Context) bool {
	return a.nowPlaying && a.npPane == paneMV && a.settings.MVBackground &&
		a.pl.Current() != nil && a.pl.Current().ShowID == "" && !c.Preferences().ReduceMotion
}

// mvFailureLabel 是 MV 加载失败时角落提示的文案；只有当前这首仍保留失败状态时显示。
func (a *app) mvFailureLabel(tr *music.Track) string {
	if tr == nil || a.mv.key != bindingKeyOf(tr) || a.mv.err == "" {
		return ""
	}
	return T("MV couldn't load · Choose another")
}

// hasPicture 在首帧到达前保留封面和歌词。
func (st *mvState) hasPicture(tr *music.Track) bool {
	return tr != nil && st.key == bindingKeyOf(tr) && (st.frame != nil || st.player != nil && st.player.Ready())
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
		a.mvFailed("player", "video-playback-failed")
		return
	}
	if !p.Ready() {
		c.After(120 * time.Millisecond)
		return
	}
	target := a.pl.Position() + time.Duration(a.mvOffset(tr))*time.Millisecond
	if dur := p.Duration(); dur > 0 {
		// 视频循环，按视频时长取模定位，MV 比歌短时也有画面。
		target = ((target % dur) + dur) % dur
	}
	if !a.pl.Playing() {
		p.Pause()
		if st.forceSync {
			p.Seek(target)
			p.SetRate(1)
			st.forceSync = false
		}
		return
	}
	if !p.Playing() {
		p.Play()
		st.forceSync = true
	}
	if st.forceSync {
		p.Seek(target)
		p.SetRate(1)
		st.syncAt = c.Now()
		st.forceSync = false
	} else {
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
	if a.mv.key != "" {
		a.closeMV()
	}
	binding, ok := a.mvStore.Binding(tr.Source, tr.ID)
	if !ok {
		// 没有绑定：开着自动匹配就先找一条，下一帧按新绑定加载。
		a.autoMatchMV(tr)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	a.mv.key, a.mv.loading, a.mv.forceSync, a.mv.cancel = key, true, true, cancel
	epoch := a.mv.epoch
	// 初次解析和后续刷新共用本轮设置，登录切换或画质变更会重建这一轮。
	cookie, quality := a.mvCookie(), a.mvQualityOptions()
	resolve := mvStreamResolver(binding.VideoID, quality, cookie)

	// 先解析，主线程确认请求仍有效后再创建代理和播放器。
	go func() {
		defer cancel()
		resolved, err := resolve(ctx)
		a.post(func() {
			// 只比较歌曲键会让同一首歌的旧画质结果覆盖新结果。
			current := a.pl.Current()
			if a.quitting || a.mv.epoch != epoch || !a.nowPlaying || a.npPane != paneMV ||
				current == nil || bindingKeyOf(current) != key {
				return
			}
			a.mv.cancel = nil
			if err != nil {
				kind := "network-or-service"
				switch {
				case errors.Is(err, mv.ErrNoCompatibleStream):
					kind = "unsupported-stream"
				case errors.Is(err, context.DeadlineExceeded):
					kind = "timeout"
				}
				a.mvFailed("resolve", kind)
				return
			}
			a.mvReady(resolved, resolve, cookie)
		})
	}()
}

// mvReady 在主线程上接住解析结果，打开播放器。
func (a *app) mvReady(resolved *mv.Resolved, resolve func(context.Context) (*mv.Resolved, error), cookie string) {
	a.mv.info = resolved.StreamInfo
	proxy, err := mv.NewProxy(resolved, resolve, cookie)
	if err != nil {
		a.mvFailed("proxy", "local-proxy-unavailable")
		return
	}
	a.mv.proxy = proxy
	player, err := video.OpenURL(proxy.URL())
	if err != nil {
		a.mvFailed("player", "decoder-open-failed")
		return
	}
	player.SetMuted(true).SetLoop(true)
	a.mv.player, a.mv.loading = player, false
}

func (a *app) mvFailed(stage, kind string) {
	a.mv.info = a.mv.streamInfo()
	a.releaseMV()
	a.mv.loading, a.mv.failureStage, a.mv.err = false, stage, kind
}

func (st *mvState) streamInfo() mv.StreamInfo {
	if st.proxy != nil {
		return st.proxy.StreamInfo()
	}
	return st.info
}

// mvDiagnostics 仅包含播放阶段与轨道档位，不复制链接、标题、账号或原始错误。
func (a *app) mvDiagnostics() string {
	stage := "idle"
	switch {
	case a.mv.err != "":
		switch a.mv.failureStage {
		case "resolve", "refresh", "proxy", "player":
			stage = a.mv.failureStage
		default:
			stage = "playback"
		}
	case a.mv.loading:
		stage = "resolving"
	case a.mv.player != nil && a.mv.player.Err() != nil:
		stage = "player-error"
	case a.mv.player != nil && a.mv.player.Buffering():
		stage = "buffering"
	case a.mv.player != nil && !a.mv.player.Ready():
		stage = "waiting-for-frame"
	case a.mv.player != nil && a.mv.player.Playing():
		stage = "playing"
	case a.mv.player != nil || a.mv.frame != nil:
		stage = "paused"
	}
	failure := ""
	switch a.mv.err {
	case "unsupported-stream", "timeout", "network-or-service", "local-proxy-unavailable", "decoder-open-failed", "video-playback-failed":
		failure = a.mv.err
	case "":
	default:
		failure = "other"
	}
	info := a.mv.streamInfo()
	kind := "unknown"
	switch info.Kind {
	case "dash-avc1", "progressive":
		kind = info.Kind
	}
	resolution := "unknown"
	if pixels := info.Resolution(); pixels > 0 {
		resolution = fmt.Sprintf("%dp", pixels)
	}
	fps := "unknown"
	if info.FPS > 0 {
		fps = fmt.Sprintf("%d", info.FPS)
	}
	return fmt.Sprintf("MV stage: %s\nfailure: %s\nquality limit: %dp\nprefer 60 fps: %t\nselected resolution: %s\nselected fps: %s\nstream type: %s",
		stage, failure, a.mvQualityOptions().Limit(), a.settings.MV60FPS, resolution, fps, kind)
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
	if a.mv.cancel != nil {
		a.mv.cancel()
	}
	a.releaseMV()
	a.mv = mvState{epoch: a.mv.epoch + 1}
}

// autoMatchMV 在没有绑定时，后台为这首歌找一条 MV 并绑上。
//
// 只在 openMV 里调用，也就是 MV 页签真的会占用画面的时候：离开页签、关掉 MV
// 背景或减少动效都不会联网。同一首歌只试一次，真的什么都搜不到就当没有 MV，
// 退回封面和歌词，不打扰正在听歌的人。
func (a *app) autoMatchMV(tr *music.Track) {
	if !a.settings.MVAutoMatch || a.mvStore == nil || tr == nil {
		return
	}
	key := bindingKeyOf(tr)
	if a.mvAutoTried == nil {
		a.mvAutoTried = map[string]bool{}
	}
	if a.mvAutoTried[key] {
		return
	}
	query := strings.TrimSpace(tr.Name + " " + tr.ArtistNames())
	if query == "" {
		return
	}
	a.mvAutoTried[key] = true
	track, epoch := *tr, a.mv.epoch
	cookie := a.mvCookie()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		pick, ok := autoPickMV(ctx, track, cookie)
		a.post(func() { a.autoMatchDone(epoch, key, track, pick, ok) })
	}()
}

// autoPickMV 为没有绑定的歌挑一条 MV：先按「歌名 歌手」找有把握的，
// 找不到就用歌名再搜一次、直接取第一条。
//
// 兜底取第一条是听众定的规则：这类歌常常只有手书、翻制或音频版上传，
// 没有把握的严格门槛会让 MV 页一直空着；宁可先给一个画面，不好再换。
func autoPickMV(ctx context.Context, track music.Track, cookie string) (mv.Candidate, bool) {
	duration := time.Duration(track.Duration) * time.Millisecond
	if found, err := mvSearchCandidates(ctx, strings.TrimSpace(track.Name+" "+track.ArtistNames()), cookie); err == nil {
		ranked := mv.Rank(found, track.Name, track.ArtistNames(), duration, 8)
		if pick, ok := mv.AutoMatch(ranked, track.Name, duration); ok {
			return pick, true
		}
	}
	// 兜底：只按歌名搜，取排在第一的那条。
	found, err := mvSearchCandidates(ctx, strings.TrimSpace(track.Name), cookie)
	if err != nil {
		return mv.Candidate{}, false
	}
	ranked := mv.Rank(found, track.Name, track.ArtistNames(), duration, 8)
	if len(ranked) == 0 {
		return mv.Candidate{}, false
	}
	return ranked[0], true
}

// autoMatchDone 在主线程收尾：挑到了就写绑定，两条搜索都空就记下“没搜到”，
// 让 MV 页把原因说出来，而不是让人以为功能没生效。
func (a *app) autoMatchDone(epoch uint64, key string, tr music.Track, pick mv.Candidate, ok bool) {
	// 期间切歌或离开 MV 页签了，这条结果已经过期。
	if a.quitting || a.mv.epoch != epoch {
		return
	}
	if !ok {
		if a.mvAutoNone == nil {
			a.mvAutoNone = map[string]bool{}
		}
		a.mvAutoNone[key] = true
		return
	}
	a.autoMatched(epoch, tr, pick)
}

// mvAutoRefused 报告自动匹配试过但没有把握：MV 页据此说明原因。
func (a *app) mvAutoRefused(tr *music.Track) bool {
	if tr == nil || !a.settings.MVAutoMatch || a.mvStore == nil || !a.mvAutoNone[bindingKeyOf(tr)] {
		return false
	}
	_, bound := a.mvStore.Binding(tr.Source, tr.ID)
	return !bound
}

// autoMatched 在主线程写入自动匹配的绑定，下一帧的 openMV 会照它加载画面。
func (a *app) autoMatched(epoch uint64, tr music.Track, pick mv.Candidate) {
	// 期间切歌或离开 MV 页签了，这条结果已经过期。
	if a.quitting || a.mv.epoch != epoch || a.mvStore == nil {
		return
	}
	// 听众自己已经挑过一条，自动结果不能覆盖。
	if _, ok := a.mvStore.Binding(tr.Source, tr.ID); ok {
		return
	}
	_ = a.mvStore.PutBinding(mv.Binding{
		Source:      tr.Source,
		TrackID:     tr.ID,
		TrackName:   tr.Name,
		TrackArtist: tr.ArtistNames(),
		VideoID:     pick.VideoID,
		Title:       pick.Title,
		Uploader:    pick.Uploader,
		Auto:        true,
	})
}

// ensureMVStore 第一次需要时读取绑定与缓存；失败不阻塞播放。
func (a *app) ensureMVStore() {
	if a.mvStore != nil {
		return
	}
	path, err := mvStorePath()
	if err != nil {
		return
	}
	store, err := mv.Open(path)
	if err != nil {
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

// mvCookie 返回主线程持有的 B 站登录凭据。
func (a *app) mvCookie() string { return a.mvCredential }
