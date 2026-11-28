package app

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// The stage shows the lyrics in the style chosen in the stage panel: the ten
// styles ported from myMusic, every one of them drawn from the playback
// position alone, so seeking shows the same frame playing would.

// lineRandom is a small random number generator seeded by a line and a word,
// so a line is laid out the same way every time it shows.
func lineRandom(seed, offset float64) float32 {
	x := math.Sin(seed+offset) * 10000
	return float32(x - math.Floor(x))
}

func clamp01(v float32) float32 { return min(1, max(0, v)) }

func lerp(a, b, t float32) float32 { return a + (b-a)*t }

func easeOutCubic(t float32) float32 {
	t = clamp01(t)
	return 1 - (1-t)*(1-t)*(1-t)
}

// stage shows the lyrics of tr on the whole stage, or says there are none.
func (a *app) stage(c *ui.Context, tr *music.Track, art *artwork, style string, accent ui.Color) {
	lines, loaded := a.pl.Lyrics()
	lines = a.stageLyrics(tr, lines)
	if len(lines) > 0 {
		a.newStage(c, tr, lines, art, style, accent)
		return
	}
	w, _ := c.Size()
	f := min(64, max(36, w*0.045))
	box := ui.Box(c.Key("stage")).Grow(1)
	a.npRise(box, 1)
	// In a quiet part a hot comment takes the middle, the lyrics nearly
	// gone under it (ui_quiet.go).
	dim, _ := a.quietShown(c, box, tr, lines, loaded)
	box.Children(func() {
		quietUnder(ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center(), dim).Children(func() {
			if !loaded {
				stageSkeleton(c, npShimmer(c), f, w)
				return
			}
			ui.Text(c, T("No lyrics. Enjoy the music.")).FontSize(28).FontWeight(800).TextColor(white.Alpha(0.4))
		})
		a.stageQuiet(c, dim, f, w)
	})
}

// Reduce Motion keeps lyric changes but sleeps between word boundaries.
func stageBoundaryWait(lines []lyrics.Line, current int, position time.Duration) time.Duration {
	wait := time.Second
	for index := max(current, 0); index <= min(current+1, len(lines)-1); index++ {
		line := lines[index]
		for _, word := range line.Words {
			for _, boundary := range []time.Duration{word.Start, word.End} {
				if boundary > position {
					wait = min(wait, boundary-position)
				}
			}
		}
		for _, boundary := range []time.Duration{line.At, line.End} {
			if boundary > position {
				wait = min(wait, boundary-position)
			}
		}
	}
	return max(20*time.Millisecond, wait+20*time.Millisecond)
}
