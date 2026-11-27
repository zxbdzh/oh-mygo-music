package app

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
	"mygo-music/internal/mv"
)

// 有绑定的歌在歌词页打开时请求 MV；切走后释放。
func TestMVOpensForBoundTrackAndClosesOnTrackChange(t *testing.T) {
	a := newTestApp()
	a.settings.MVBackground = true
	tr := a.pl.Current()
	if tr == nil {
		t.Skip("no current track in the test app")
	}
	a.mvStore = newTestMVStore(t, tr.Source, tr.ID)
	a.nowPlaying, a.npPane = true, paneMV
	a.openMV()
	if a.mv.key != bindingKeyOf(tr) {
		t.Fatalf("mv key %q, want %q", a.mv.key, bindingKeyOf(tr))
	}
	// 解析是异步的，这里只验证请求发起，不验证真实流。
	if !a.mv.loading {
		t.Fatal("mv did not start loading")
	}
	a.closeMV()
	if a.mv.player != nil || a.mv.proxy != nil || a.mv.key != "" {
		t.Fatalf("mv state not cleared: %+v", a.mv)
	}
}

// 没绑定的歌不请求 MV。
func TestMVSkipsUnboundTrack(t *testing.T) {
	a := newTestApp()
	a.settings.MVBackground = true
	// 自动匹配会为没绑定的歌联网搜索，这条只验证手动路径不发请求。
	a.settings.MVAutoMatch = false
	a.mvStore = newTestMVStore(t, "other", "other")
	a.nowPlaying, a.npPane = true, paneMV
	a.openMV()
	if a.mv.loading || a.mv.key != "" {
		t.Fatalf("mv started for an unbound track: %+v", a.mv)
	}
}

// 没绑定的歌在 MV 页签显示时搜一次并写成自动绑定，之后按它加载画面。
func TestMVAutoMatchBindsWhenMVPageShows(t *testing.T) {
	a := newTestApp()
	old, oldResolve := mvSearchCandidates, mvResolve
	defer func() { mvSearchCandidates, mvResolve = old, oldResolve }()
	searches := 0
	mvSearchCandidates = func(_ context.Context, query, _ string) ([]mv.Candidate, error) {
		searches++
		return []mv.Candidate{{VideoID: "BVAUTO", Title: query, Duration: 200 * time.Second}}, nil
	}
	mvResolve = func(context.Context, string, mv.QualityOptions, string) (*mv.Resolved, error) {
		return nil, errors.New("offline in tests")
	}
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	a.mvStore = store
	tr := song("auto-1", "晴天", "周杰伦")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	ch := posts(a)
	a.nowPlaying, a.npPane = true, paneMV
	// 每帧都会调用 openMV，同一首歌只该搜一次。
	a.openMV()
	a.openMV()
	runPost(t, ch)
	if searches != 1 {
		t.Fatalf("searched %d times, want once", searches)
	}
	binding, ok := store.Binding("fake", "auto-1")
	if !ok || binding.VideoID != "BVAUTO" || !binding.Auto {
		t.Fatalf("binding %+v, %v", binding, ok)
	}
	// 下一次 openMV 有绑定可用了，画面开始加载。
	a.openMV()
	if a.mv.key != bindingKeyOf(&tr) {
		t.Fatalf("mv key %q, want %q", a.mv.key, bindingKeyOf(&tr))
	}
}

// 关掉自动匹配后，没绑定的歌不去联网。
func TestMVAutoMatchOffDoesNotSearch(t *testing.T) {
	a := newTestApp()
	old := mvSearchCandidates
	defer func() { mvSearchCandidates = old }()
	mvSearchCandidates = func(context.Context, string, string) ([]mv.Candidate, error) {
		t.Error("searched with auto match off")
		return nil, nil
	}
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	a.mvStore = store
	a.settings.MVAutoMatch = false
	tr := song("auto-off", "晴天", "周杰伦")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.nowPlaying, a.npPane = true, paneMV
	a.openMV()
}

// 有把握的没找到时，用歌名再搜一次并直接绑第一条。
func TestMVAutoMatchFallsBackToFirstResult(t *testing.T) {
	a := newTestApp()
	old, oldResolve := mvSearchCandidates, mvResolve
	defer func() { mvSearchCandidates, mvResolve = old, oldResolve }()
	var queries []string
	mvSearchCandidates = func(_ context.Context, query, _ string) ([]mv.Candidate, error) {
		queries = append(queries, query)
		// 两条都不满足时长要求，严格门槛会拒绝，兜底才该接手。
		return []mv.Candidate{
			{VideoID: "FIRST", Title: "Emoji 手书", Duration: 90 * time.Second},
			{VideoID: "SECOND", Title: "别的歌", Duration: 200 * time.Second},
		}, nil
	}
	mvResolve = func(context.Context, string, mv.QualityOptions, string) (*mv.Resolved, error) {
		return nil, errors.New("offline in tests")
	}
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	a.mvStore = store
	tr := song("fallback-1", "Emoji", "GUMI")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	ch := posts(a)
	a.nowPlaying, a.npPane = true, paneMV
	a.openMV()
	runPost(t, ch)
	binding, ok := store.Binding("fake", "fallback-1")
	if !ok || binding.VideoID != "FIRST" || !binding.Auto {
		t.Fatalf("binding %+v, %v: want the first result", binding, ok)
	}
	// 兜底那次只用歌名搜。
	if len(queries) != 2 || queries[1] != "Emoji" {
		t.Fatalf("queries %v, want the fallback to search the song name", queries)
	}
}

// 两次都没搜到东西时，MV 页的按钮说明“没搜到”，而不是静悄悄什么都不发生。
func TestMVAutoMatchSaysWhenNothingFound(t *testing.T) {
	a := newTestApp()
	old := mvSearchCandidates
	defer func() { mvSearchCandidates = old }()
	mvSearchCandidates = func(context.Context, string, string) ([]mv.Candidate, error) {
		return nil, nil
	}
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	a.mvStore = store
	tr := song("nothing-1", "Emoji", "GUMI")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	ch := posts(a)
	a.nowPlaying, a.npPane = true, paneMV
	a.openMV()
	runPost(t, ch)
	if !a.mvAutoRefused(&tr) {
		t.Fatal("the refusal wasn't recorded")
	}
	tester := ui.NewTester(a.view, 1180, 780)
	settled(tester)
	if _, ok := tester.Find(T("No MV found · Choose")); !ok {
		t.Fatal("the MV page didn't say nothing was found")
	}
}

// 自动结果不覆盖听众自己挑的那一条。
func TestMVAutoMatchKeepsManualBinding(t *testing.T) {
	a := newTestApp()
	a.mvStore = newTestMVStore(t, "fake", "manual-1")
	tr := song("manual-1", "晴天", "周杰伦")
	tr.Source = "fake"
	a.autoMatched(0, tr, mv.Candidate{VideoID: "BVAUTO", Title: "晴天 MV"})
	binding, ok := a.mvStore.Binding("fake", "manual-1")
	if !ok || binding.VideoID != "BVTEST" || binding.Auto {
		t.Fatalf("binding %+v, %v: the manual choice was replaced", binding, ok)
	}
}

// 离开 MV 页签后才回来的自动结果不写入。
func TestMVAutoMatchDropsStaleResult(t *testing.T) {
	a := newTestApp()
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	a.mvStore = store
	tr := song("stale-1", "晴天", "周杰伦")
	tr.Source = "fake"
	epoch := a.mv.epoch
	a.closeMV() // 切歌或离开页签会让在途的结果过期
	a.autoMatched(epoch, tr, mv.Candidate{VideoID: "BVAUTO", Title: "晴天 MV"})
	if _, ok := store.Binding("fake", "stale-1"); ok {
		t.Fatal("a stale auto match was written")
	}
}

// 候选写入绑定时必须带音源；同一曲目 ID 在不同音源上不能相撞。
func TestMVBindingUsesSourceAndTrack(t *testing.T) {
	a := newTestApp()
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	a.mvStore = store
	tr := song("same-id", "晴天", "周杰伦")
	tr.Source = "net"
	a.mvDialog = mvDialogState{open: true, track: &tr}
	a.bindMV(mv.Candidate{VideoID: "BV1", Title: "晴天 MV", Uploader: "周杰伦"})
	got, ok := store.Binding("net", "same-id")
	if !ok || got.VideoID != "BV1" {
		t.Fatalf("binding %+v, %v", got, ok)
	}
	if _, ok := store.Binding("other", "same-id"); ok {
		t.Fatal("binding leaked to another source")
	}
}

// 打开面板时默认用「歌名 歌手」搜索，候选只截前 8 条。
func TestMVSearchDefaultsToSongAndArtist(t *testing.T) {
	a := newTestApp()
	old := mvSearchCandidates
	defer func() { mvSearchCandidates = old }()
	var asked string
	mvSearchCandidates = func(_ context.Context, query, _ string) ([]mv.Candidate, error) {
		asked = query
		return []mv.Candidate{{VideoID: "BV1", Title: "晴天 MV"}}, nil
	}
	ch := posts(a)
	tr := song("t1", "晴天", "周杰伦")
	tr.Source = "net"
	a.openMVDialog(&tr)
	runPost(t, ch)
	if asked != "晴天 周杰伦" || a.mvDialog.query != "晴天 周杰伦" {
		t.Fatalf("query %q / %q", asked, a.mvDialog.query)
	}
	if len(a.mvDialog.candidates) != 1 || a.mvDialog.candidates[0].VideoID != "BV1" {
		t.Fatalf("candidates %+v", a.mvDialog.candidates)
	}
}

// 同名歌在另一个音源已有 MV 时只提示，不改变当前绑定。
func TestMVCrossSourceHint(t *testing.T) {
	a := newTestApp()
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(mv.Binding{
		Source: "other", TrackID: "o1", TrackName: "晴天", TrackArtist: "周杰伦", VideoID: "BV9",
	}); err != nil {
		t.Fatal(err)
	}
	a.mvStore = store
	old := mvSearchCandidates
	defer func() { mvSearchCandidates = old }()
	mvSearchCandidates = func(context.Context, string, string) ([]mv.Candidate, error) { return nil, nil }
	ch := posts(a)
	tr := song("t1", "晴天", "周杰伦")
	tr.Source = "net"
	a.openMVDialog(&tr)
	runPost(t, ch)
	if a.mvDialog.hint == "" {
		t.Fatal("no cross-source hint")
	}
}

// 没有绑定的歌也要能从普通歌词页打开 MV 面板。
func TestMVButtonShowsForSongs(t *testing.T) {
	a := newTestApp()
	tr := song("t1", "晴天", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.nowPlaying, a.npPane = true, paneLyrics
	tt := ui.NewTester(a.view, 1180, 780)
	tt.Frame()
	if _, ok := tt.Find("MV"); !ok {
		t.Fatal("no MV button")
	}
}

// 减少动态效果时不请求 MV，普通封面继续显示。
func TestMVReducedMotionDoesNotLoadVideo(t *testing.T) {
	a := newTestApp()
	tr := song("t1", "晴天", "周杰伦")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.mvStore = newTestMVStore(t, "fake", "t1")
	a.nowPlaying, a.npPane = true, paneMV
	tt := ui.NewTester(a.view, 1180, 780)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if a.mv.loading || a.mv.key != "" {
		t.Fatalf("video loaded under reduce motion: %+v", a.mv)
	}
}

// 画质上限和 60 帧偏好写入后，在重新读取设置时保留。
func TestMVQualityPreferencesSurviveRestart(t *testing.T) {
	userDataPath, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mygo.App.SetPath(mygo.PathUserData, dir)
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, userDataPath) })
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	preferences := defaultSettings()
	preferences.MVQuality, preferences.MV60FPS, preferences.path = 720, false, path
	if err := preferences.save(); err != nil {
		t.Fatal(err)
	}
	loaded := loadSettings()
	if loaded.MVQuality != 720 || loaded.MV60FPS {
		t.Fatalf("restored MV preferences %d / %v", loaded.MVQuality, loaded.MV60FPS)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"mvQuality": 720`) {
		t.Fatalf("saved preferences %s, %v", raw, err)
	}
	if err := os.WriteFile(path, []byte(`{"mvBackground":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded = loadSettings()
	if loaded.MVQuality != 1080 || !loaded.MV60FPS {
		t.Fatalf("legacy settings lost the default MV quality preference: %+v", loaded)
	}
}

// 刷新失败只返回通用错误，复制的诊断不包含 Cookie、视频 ID 或上游消息。
func TestMVRefreshUsesCapturedQualityAndSanitizesFailure(t *testing.T) {
	a := newTestApp()
	a.settings.MVQuality, a.mvCredential = 720, "SESSDATA=secret-value"
	oldResolver := mvResolve
	mvResolve = func(_ context.Context, videoID string, options mv.QualityOptions, cookie string) (*mv.Resolved, error) {
		if videoID != "BVTEST" || options.MaxHeight != 720 || cookie != "SESSDATA=secret-value" {
			t.Errorf("MV resolver received unexpected video or preferences: %q, %+v", videoID, options)
		}
		return nil, errors.New("upstream error contains SESSDATA=secret-value")
	}
	defer func() { mvResolve = oldResolver }()
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer stale.Close()
	options, cookie := a.mvQualityOptions(), a.mvCookie()
	proxy, err := mv.NewProxy(
		&mv.Resolved{
			URL: stale.URL, ExpiresAt: time.Now().Add(time.Hour),
			StreamInfo: mv.StreamInfo{Quality: 80, FPS: 60, Kind: "dash-avc1"},
		},
		mvStreamResolver("BVTEST", options, cookie),
		cookie,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	response, err := http.Get(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("refresh status %d, body %q, error %v", response.StatusCode, body, err)
	}
	if strings.Contains(string(body), "secret-value") || strings.Contains(string(body), "upstream error") {
		t.Fatalf("proxy response exposed an upstream error: %s", body)
	}
	a.mv.key = "bound-track"
	a.mv.proxy = proxy
	a.mvFailed("refresh", "network-or-service")
	diagnostics := a.mvDiagnostics()
	if strings.Contains(diagnostics, "secret-value") || strings.Contains(diagnostics, "upstream error") ||
		strings.Contains(diagnostics, "BVTEST") || strings.Contains(diagnostics, "http") {
		t.Fatalf("diagnostics include private stream details: %s", diagnostics)
	}
	if !strings.Contains(diagnostics, "selected resolution: 1080p") || !strings.Contains(diagnostics, "selected fps: 60") {
		t.Fatalf("diagnostics omitted the selected stream quality: %s", diagnostics)
	}
}

// 同一首歌切换偏好后，旧请求迟到失败不能覆盖新请求状态。
func TestMVDiscardsSupersededLoadResult(t *testing.T) {
	a := newTestApp()
	tr := song("mv-epoch", "MV load")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.mvStore = newTestMVStore(t, tr.Source, tr.ID)
	ch := posts(a)
	firstStarted, firstRelease := make(chan struct{}), make(chan struct{})
	secondStarted, secondRelease := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	oldResolver := mvResolve
	mvResolve = func(context.Context, string, mv.QualityOptions, string) (*mv.Resolved, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-firstRelease
			return nil, mv.ErrNoCompatibleStream
		default:
			close(secondStarted)
			<-secondRelease
			return nil, errors.New("new request")
		}
	}
	defer func() { mvResolve = oldResolver }()
	defer func() {
		select {
		case firstRelease <- struct{}{}:
		default:
		}
	}()
	a.nowPlaying, a.npPane = true, paneMV
	a.openMV()
	<-firstStarted
	a.closeMV()
	a.openMV()
	<-secondStarted
	secondRelease <- struct{}{}
	runPost(t, ch)
	if a.mv.err != "network-or-service" || a.mv.failureStage != "resolve" || a.mv.loading {
		t.Fatalf("new request state %+v", a.mv)
	}
	firstRelease <- struct{}{}
	runPost(t, ch)
	if a.mv.failureStage != "resolve" || a.mv.err != "network-or-service" {
		t.Fatalf("late result replaced current request state: %+v", a.mv)
	}
}

// 管理菜单复制的 MV 诊断不包含绑定标识或登录凭据。
func TestMVCopyDiagnosticsMenuOmitsPrivateData(t *testing.T) {
	a := newTestApp()
	tr := song("private-track-id", "Private song")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.mvStore = newTestMVStore(t, tr.Source, tr.ID)
	a.mv.key = bindingKeyOf(&tr)
	a.mvCredential = "SESSDATA=private-cookie"
	a.mv.err, a.mv.failureStage = "synthetic upstream details", "untrusted stage"
	a.nowPlaying, a.npPane = true, paneMV
	var copied string
	oldCopy := copyText
	copyText = func(value string) error {
		copied = value
		return nil
	}
	defer func() { copyText = oldCopy }()
	tester := ui.NewTester(a.view, 880, 650)
	tester.Move(160, 60)
	settled(tester)
	if err := tester.Click(T("Manage MV")); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(T("Copy MV diagnostics")); err != nil {
		t.Fatal(err)
	}
	if copied == "" || strings.Contains(copied, "private-cookie") || strings.Contains(copied, "private-track-id") ||
		strings.Contains(copied, "Private song") || strings.Contains(copied, "synthetic upstream details") ||
		strings.Contains(copied, "untrusted stage") {
		t.Fatalf("copied diagnostics exposed private data: %s", copied)
	}
}

// 选择 MV 画质上限后保存实际的质量档位。
func TestMVQualitySelectionSavesPreference(t *testing.T) {
	a := newTestApp()
	a.settingsPage, a.settingsQuery = "playback", "MV Quality Limit"
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	tester := ui.NewTester(a.settingsView, 900, 700)
	settled(tester)
	if err := tester.Click("1080p"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("720p"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if a.settings.MVQuality != 720 {
		t.Fatalf("MV quality limit %d, want 720", a.settings.MVQuality)
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil || !strings.Contains(string(data), `"mvQuality": 720`) {
		t.Fatalf("saved settings %s, %v", data, err)
	}
}

// 拖动 MV 画面后松手，位置应写入设置并在下一次进入时恢复。
func TestMVBackgroundDragPersistsPosition(t *testing.T) {
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	tr := song("mv-drag", "晴天", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.mv.key = bindingKeyOf(&tr)
	frame := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 32, 18)))
	a.mv.frame = frame
	a.nowPlaying, a.npPane, a.npT = true, paneMV, 1

	tester := ui.NewTester(a.view, 1180, 780)
	tester.Frame()
	picture, ok := tester.Find("MV picture")
	if !ok {
		t.Fatal("MV picture not present")
	}
	x, y := picture.X+picture.W*.08, picture.Y+picture.H*.25
	tester.Press(x, y)
	for step := 1; step <= 6; step++ {
		tester.Advance(16 * time.Millisecond)
		tester.Move(x+40*float32(step)/6, y+20*float32(step)/6)
	}
	tester.Release(x+40, y+20)
	tester.Advance(time.Second)
	if a.mv.pan.X == 0 && a.mv.pan.Y == 0 {
		t.Fatalf("drag did not move the canvas: %+v, picture=%+v", a.mv.pan, picture)
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &a.settings); err != nil {
		t.Fatal(err)
	}
	pan := a.mv.pan
	a.closeMV()
	a.mv.key, a.mv.frame = bindingKeyOf(&tr), frame
	tester.Frame()
	if difference := a.mv.pan.X - pan.X; difference < -0.01 || difference > 0.01 {
		t.Fatalf("restored position %+v, want %+v", a.mv.pan, pan)
	}
}

// 隐藏歌词只影响视频画面，减少动态效果时歌词要恢复。
func TestMVHiddenLyricsReturnWithoutVideo(t *testing.T) {
	a := newTestApp()
	tr := song("mv-lyrics", "Lyrics fallback")
	a.pl.Cue([]music.Track{tr})
	const line = "A line that stays when the video goes away"
	a.pl.SetLyrics(lyrics.Build("[00:01]"+line, ""))
	a.mv.key = bindingKeyOf(&tr)
	a.mv.frame = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 32, 18)))
	a.nowPlaying, a.npPane, a.npT = true, paneMV, 1
	a.settings.MVHideLyrics = true
	tester := ui.NewTester(a.view, 880, 650)
	settled(tester)
	if _, ok := tester.Find(line); ok {
		t.Fatal("lyrics visible over the video")
	}
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tester)
	if _, ok := tester.Find(line); !ok {
		t.Fatal("fallback lyrics missing without video")
	}
}

// 窄窗里通过管理菜单修改音画偏移，仍保留这首歌的绑定。
func TestMVManageMenuChangesOffset(t *testing.T) {
	a := newTestApp()
	tr := song("mv-menu", "MV menu")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.mvStore = newTestMVStore(t, tr.Source, tr.ID)
	a.mv.key, a.nowPlaying, a.npPane, a.npT = bindingKeyOf(&tr), true, paneMV, 1
	tester := ui.NewTester(a.view, 880, 650)
	tester.Move(160, 60)
	settled(tester)
	tester.Click(T("Manage MV"))
	if err := tester.ChooseMenuItem(T("Advance video by 100 ms")); err != nil {
		t.Fatal(err)
	}
	binding, ok := a.mvStore.Binding(tr.Source, tr.ID)
	if !ok || binding.OffsetMs != 100 || !a.mv.forceSync {
		t.Fatalf("expected a persisted +100 ms offset and forced sync, got %+v", binding)
	}
}

// 加载失败时保留绑定键并显示可点击的回退提示。
func TestMVFailureShowsFallbackStatus(t *testing.T) {
	a := newTestApp()
	tr := song("t1", "晴天", "周杰伦")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.mvStore = newTestMVStore(t, "fake", "t1")
	a.nowPlaying, a.npPane = true, paneLyrics
	a.settings.MVBackground = false
	tt := ui.NewTester(a.view, 1180, 780)
	settled(tt)
	a.settings.MVBackground = true
	a.npPane = paneMV
	a.mv.key = bindingKeyOf(&tr)
	a.mv.err = "synthetic failure"
	label := a.mvFailureLabel(a.pl.Current())
	if label == "" {
		t.Fatalf("failure state missing: key=%q current=%q err=%q", a.mv.key, bindingKeyOf(a.pl.Current()), a.mv.err)
	}
	tt.Frame()
	if a.mv.err != "synthetic failure" {
		t.Fatalf("failure state changed: err=%q key=%q current=%q", a.mv.err, a.mv.key, bindingKeyOf(a.pl.Current()))
	}
	if _, ok := tt.Find(label); !ok {
		t.Fatal("no MV failure status")
	}
	if a.mv.key == "" {
		t.Fatal("failure cleared the binding key")
	}
}

func newTestMVStore(t *testing.T, source, trackID string) *mv.Store {
	t.Helper()
	store, err := mv.Open(t.TempDir() + "/mv.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(mv.Binding{Source: source, TrackID: trackID, VideoID: "BVTEST"}); err != nil {
		t.Fatal(err)
	}
	return store
}
