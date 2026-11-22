package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 大歌单恢复位置后，静止列表不能因恢复逻辑而持续请求新帧。
func TestNavigationFrameBudget(t *testing.T) {
	a, _ := navigationApp()
	tracks := make([]music.Track, 10000)
	for index := range tracks {
		tracks[index] = song(fmt.Sprint(index), fmt.Sprintf("大歌单歌曲%05d", index))
	}
	a.playlists["long"].Tracks = tracks
	a.pl.Cue(tracks[:1])
	tester := ui.NewTester(a.view, 1200, 800)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tester)
	tester.Scroll(650, 420, 0, 20000)
	settled(tester)
	a.open(route{page: pageAlbum, id: "detail"})
	settled(tester)
	tester.Key(0, ui.KeyBack)
	settled(tester)
	if count := framesAt(a, tester, 0); count > 1 {
		t.Fatalf("restored idle list requested %d frames per second", count)
	}
}

// playingApp is an app playing a track with lyrics, at a position the test
// sets with pl.Seek, and the number of times its view was built.
func playingApp() (*app, *ui.Tester, *int) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("7", "宝船", "DOUDOU")})
	// A line every 10 seconds, sung over the first 5 of them.
	var lrc string
	for i := range 12 {
		lrc += "[0" + string(rune('0'+i*10/60)) + ":" + string(rune('0'+i*10%60/10)) + "0.00]一二三四五六\n"
	}
	lines := lyrics.Build(lrc, "")
	for i := range lines {
		l := &lines[i]
		l.End = l.At + 5*time.Second
		l.Words = lyrics.SpreadWords(l.Text, l.At, l.End)
	}
	a.pl.SetLyrics(lines)
	a.pl.Hold(0)
	builds := 0
	tt := ui.NewTester(func(c *ui.Context) { builds++; a.view(c) }, 1200, 800)
	return a, tt, &builds
}

// framesAt counts the frames the view asks for over a second, at 60 a
// second, with playback held at pos.
func framesAt(a *app, tt *ui.Tester, pos time.Duration) int {
	a.pl.Seek(pos)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	asked := 0
	for range 60 {
		tt.Advance(time.Second / 60)
		if tt.FramePending() {
			asked++
			tt.Frame()
		}
	}
	return asked
}

// Now Playing asks for frames only as fast as what it shows changes. The
// Tester runs timers as it settles a frame, so this counts only what does
// not change for a while: a frame log of the app (MYGO_FRAME_STATS) measures
// the rest.
func TestNowPlayingFrameBudget(t *testing.T) {
	a, tt, _ := playingApp()
	if n := framesAt(a, tt, 12*time.Second); n > 3 {
		t.Errorf("a page under the player: %d frames a second", n)
	}
	// Its rows still, the wall asks for no more than the lyrics beside it
	// do: it moves only as it is moved.
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	a.nowPlaying, a.npPane = true, paneLyrics
	lyricsFrames := framesAt(a, tt, 12*time.Second)
	a.npPane = paneWall
	if n := framesAt(a, tt, 12*time.Second); n > lyricsFrames+3 {
		t.Errorf("the wall, still: %d frames a second, the lyrics %d", n, lyricsFrames)
	}
	tt.SetPreferences(ui.Preferences{})
	a.npPane = paneStage
	a.settings.StageSparks = true
	for _, s := range stageStyles {
		a.settings.StageStyle = s.id
		if n := framesAt(a, tt, 18*time.Second); n > 12 {
			t.Errorf("the stage between lines, in %s: %d frames a second", s.label, n)
		}
	}
}

// Home with a source's own page, its feature card at rest while nothing plays,
// asks for no frames once shown.
func TestHomeFrameBudget(t *testing.T) {
	a := newTestApp()
	a.home = &music.Browse{Sections: []music.Section{
		{Title: "For You", Tracks: []music.Track{song("1", "一"), song("2", "二")}},
		{Title: "New Albums", Albums: []music.Album{{ID: "8", Name: "迷跡波"}}},
	}}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Advance(2 * time.Second)
	tt.Frame()
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Error("Home asks for frames while idle")
	}
}

// Another song's backdrop fades in over the last one's, which is then let
// go, so that the backdrop stops asking for frames.
func TestBackdropFadeEnds(t *testing.T) {
	a, tt, _ := coverApp(t)
	a.art.art[sized("a", 600)+"#backdrop"] = &artwork{tint: ui.RGB(200, 40, 40)}
	a.art.art[sized("b", 600)+"#backdrop"] = &artwork{tint: ui.RGB(40, 40, 200)}
	q := a.pl.Queue()
	q[1].Album.ImageURL, q[2].Album.ImageURL = "a", "b"
	tt.Frame()
	a.pl.Next()
	tt.Frame()
	if a.backdropCache.before == nil {
		t.Fatal("the backdrop changed songs without fading")
	}
	tt.Advance(backdropFade + 100*time.Millisecond)
	tt.Frame()
	if a.backdropCache.before != nil {
		t.Error("the last song's backdrop still fades after the fade")
	}
}

// A retro player paused, its record or reels still and its tonearm at
// rest, asks for no frames.
func TestRetroPlayerPausedFrameBudget(t *testing.T) {
	for _, style := range playerStyles[1:] {
		a := newTestApp()
		a.settings.PlayerStyle = style.id
		a.pl.Cue([]music.Track{song("7", "宝船", "DOUDOU")})
		a.pl.Seek(12 * time.Second) // cued, not playing
		a.nowPlaying, a.npPane = true, paneQueue
		tt := ui.NewTester(a.view, 1200, 800)
		settled(tt)
		tt.Advance(2 * time.Second)
		tt.Frame()
		tt.Advance(time.Second)
		if tt.FramePending() {
			t.Errorf("%s paused: asks for frames", style.id)
		}
	}
}

// 已结束的任务和隐藏的运行任务不持续请求画面，避免后台任务空转。
func TestBackgroundTaskIdleFrameBudget(t *testing.T) {
	for _, state := range []backgroundTaskState{backgroundTaskRunning, backgroundTaskSucceeded, backgroundTaskFailed, backgroundTaskCanceled} {
		a := newTestApp()
		task := a.newBackgroundTask("Share", "Track")
		task.state = state
		task.hidden = state == backgroundTaskRunning
		tester := ui.NewTester(a.view, 1200, 800)
		settled(tester)
		tester.Advance(time.Second)
		if tester.FramePending() {
			t.Fatalf("idle task state %d keeps requesting frames", state)
		}
	}
}

// 首页特色卡播放时继续请求唱片动画帧。
func TestHomeFeatureCardPlayingFrameBudget(t *testing.T) {
	a := newTestApp()
	tracks := framesTracks(a)
	a.pl.Cue(tracks[:1])
	a.pl.Hold(10 * time.Second)
	a.home = &music.Browse{Sections: []music.Section{{Title: "For You", Tracks: a.pl.Queue()}}}
	tester := ui.NewTester(a.view, 1180, 780)
	tester.Advance(time.Second)
	if !tester.FramePending() || a.dailySpin.speed <= 0 {
		t.Fatal("the feature card stopped requesting frames while its record plays")
	}
}

// 暂停队列侧栏和空队列静止后不持续请求装饰帧。
func TestQueueDrawerIdleFrameBudget(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("1", "一")})
	tester := ui.NewTester(a.view, 1180, 780)
	settled(tester)
	if err := tester.Click("Queue"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if tester.FramePending() {
		t.Fatal("paused queue drawer continuously requests frames")
	}
	a.pl.RemoveQueued(a.pl.QueueEntryID(0))
	settled(tester)
	if tester.FramePending() {
		t.Fatal("empty queue drawer continuously requests frames")
	}
}
