package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// Dragging the progress bar seeks once, where it is let go: not as the
// pointer moves, even off the thin bar, and not to the end, which would
// play the next song, before it is let go.
func TestScrubSeeksOnRelease(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("7", "宝船"), {ID: "8", Name: "下一首"}})
	a.pl.SetLyrics(nil)
	out := audio.NewPlayer(&silence{})
	out.SetVolume(0)
	out.Play()
	a.pl.Attach(out)
	defer out.Close()
	a.nowPlaying = true
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Advance(time.Second)
	tt.Frame()
	bar, ok := tt.Find("Progress")
	if !ok {
		t.Fatal("no progress bar")
	}
	y := bar.Y + bar.H/2
	at := func(f float32) float32 { return bar.X + bar.W*f }

	tt.Press(at(0.25), y)
	tt.Frame()
	tt.Move(at(0.6), y-40) // off the bar, above it
	tt.Frame()
	tt.Move(at(1)+30, y+30) // past its end, below it
	tt.Frame()
	if pos := a.pl.Position(); pos > 10*time.Second {
		t.Fatalf("seeked to %v while dragging", pos)
	}
	if a.pl.Current().ID != "7" {
		t.Fatal("went to the next song while dragging")
	}
	tt.Move(at(0.6), y-40)
	tt.Frame()
	tt.Release(at(0.6), y-40)
	tt.Frame()
	tt.Frame()
	if pos := a.pl.Position(); pos < 110*time.Second || pos > 130*time.Second {
		t.Errorf("let go at 60%%, at %v", pos)
	}
}

// 贴底进度覆盖侧栏一侧，拖动左端不能误打开封面或在松手前跳播。
func TestDesktopDockSeeksFromSidebarEdge(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("7", "宝船"), song("8", "下一首")})
	a.pl.SetLyrics(nil)
	out := audio.NewPlayer(&silence{})
	out.SetVolume(0)
	out.Play()
	a.pl.Attach(out)
	defer out.Close()
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	bar, ok := tester.Find("Progress")
	if !ok {
		t.Fatal("no progress control")
	}
	y := bar.Y + bar.H/2
	tester.Press(72, y)
	tester.Frame()
	tester.Move(120, y)
	tester.Frame()
	if a.pl.Position() > 5*time.Second {
		t.Fatal("desktop progress seeks before release")
	}
	tester.Release(120, y)
	tester.Frame()
	tester.Frame()
	if position := a.pl.Position(); position < 15*time.Second || position > 25*time.Second {
		t.Fatalf("desktop progress is not reachable over the sidebar: position=%v", position)
	}
	if a.nowPlaying || a.pl.Current().ID != "7" {
		t.Fatal("seeking opened the cover or changed the song")
	}
}
