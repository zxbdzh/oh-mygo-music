package app

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

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
	a.nowPlaying, a.npPane = true, paneLyrics
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
	a.nowPlaying, a.npPane = true, paneLyrics
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
	a.nowPlaying, a.npPane = true, paneLyrics
	tt := ui.NewTester(a.view, 1180, 780)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if a.mv.loading || a.mv.key != "" {
		t.Fatalf("video loaded under reduce motion: %+v", a.mv)
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
