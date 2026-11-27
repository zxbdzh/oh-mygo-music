package app

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	a.mvStore = newTestMVStore(t, "other", "other")
	a.nowPlaying, a.npPane = true, paneMV
	a.openMV()
	if a.mv.loading || a.mv.key != "" {
		t.Fatalf("mv started for an unbound track: %+v", a.mv)
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
