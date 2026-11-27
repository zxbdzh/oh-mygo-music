package app

import (
	"testing"

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
