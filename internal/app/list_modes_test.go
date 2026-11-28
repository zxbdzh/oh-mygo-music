package app

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

func stageListTestLines() []lyrics.Line {
	lines := make([]lyrics.Line, 24)
	for i := range lines {
		at := time.Duration(i+1) * 2 * time.Second
		lines[i] = lyrics.Line{At: at, End: at + time.Second, Text: fmt.Sprintf("Lyric %d", i)}
	}
	return lines
}

func stageListTestView(c *ui.Context, a *app, frame stageFrame) {
	frame.w, frame.h = c.Size()
	ui.Box(c.Key(frame.style)).Fill().Clip().Background(ui.RGB(17, 18, 25)).Lang(lyrics.Lang(frame.lines)).Children(func() {
		a.renderStageListMode(c, frame)
	})
}

// Every live list seeks when a listener clicks a line, including before the first lyric starts.
func TestStageListClickSeeks(t *testing.T) {
	for _, style := range []string{"classic", "rush", "vinyl"} {
		t.Run(style, func(t *testing.T) {
			a := newTestApp()
			a.pl.Cue([]music.Track{{ID: "list-test", Source: "fake", Duration: 60000}})
			lines := stageListTestLines()
			tt := ui.NewTester(func(c *ui.Context) {
				pos := a.pl.Position()
				stageListTestView(c, a, stageFrame{style: style, lines: lines, current: lyrics.Index(lines, pos), pos: pos})
			}, 360, 460)
			if err := tt.Click(lines[0].Text); err != nil {
				t.Fatal(err)
			}
			if got := a.pl.Position(); got != lines[0].At {
				t.Errorf("clicked %q: position %v, want %v", lines[0].Text, got, lines[0].At)
			}
		})
	}
}

// A touchpad scroll keeps the listener's place across lyric changes, then catches up even while paused.
func TestStageListReturnsToCurrent(t *testing.T) {
	a := newTestApp()
	lines := stageListTestLines()
	current := 5
	tt := ui.NewTester(func(c *ui.Context) {
		stageListTestView(c, a, stageFrame{style: "rush", lines: lines, current: current, pos: lines[current].At, motion: 1})
	}, 700, 440)
	st := &a.stageList
	start := st.scroll.Y
	tt.Scroll(320, 230, 0, 110)
	manual := st.scroll.Y
	if manual <= start {
		t.Fatalf("scroll failed: offset %v → %v", start, manual)
	}
	current++
	tt.Advance(2 * time.Second)
	tt.Frame()
	if got := st.scroll.Y; got != manual {
		t.Fatalf("playhead moved the list while browsing: offset %v, want %v", got, manual)
	}
	tt.Advance(time.Second + 100*time.Millisecond)
	tt.Advance(time.Second)
	if got, want := st.scroll.Y, st.layout.target(st.layout.active(stageFrame{style: "rush", current: current, pos: lines[current].At})); got != want {
		t.Errorf("list did not resume following: offset %v, want %v", got, want)
	}
}

// Dragging a lyric browses without turning the release into a seek.
func TestStageListDragDoesNotSeek(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{{ID: "list-drag", Source: "fake", Duration: 60000}})
	lines := stageListTestLines()
	a.pl.Seek(lines[3].At)
	tt := ui.NewTester(func(c *ui.Context) {
		pos := a.pl.Position()
		stageListTestView(c, a, stageFrame{style: "classic", lines: lines, current: lyrics.Index(lines, pos), pos: pos})
	}, 640, 400)
	st := &a.stageList
	start := st.scroll.Y
	next, ok := tt.Find(lines[4].Text)
	if !ok {
		t.Fatal("next lyric is outside the drag viewport")
	}
	x, y := next.X+next.W/2, next.Y+next.H/2
	tt.Press(x, y)
	tt.Move(x, y-23)
	tt.Move(x, y-73)
	tt.Release(x, y-73)
	if got := st.scroll.Y; got <= start {
		t.Errorf("drag did not browse: offset %v → %v", start, got)
	}
	if got := a.pl.Position(); got != lines[3].At {
		t.Errorf("drag sought to %v, want to remain at %v", got, lines[3].At)
	}
}

// A preview can be clicked or scrolled without disturbing the live list or the playback position.
func TestStageListPreviewIsolated(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{{ID: "list-preview", Source: "fake", Duration: 60000}})
	lines := stageListTestLines()
	a.pl.Seek(lines[3].At)
	a.stageList.scroll.Y = 37
	a.stageList.manual = time.Unix(43, 0)
	tt := ui.NewTester(func(c *ui.Context) {
		stageListTestView(c, a, stageFrame{style: "rush", lines: lines, current: 0, pos: lines[0].At, preview: true, motion: 1})
	}, 480, 250)
	tt.ClickAt(120, 102)
	tt.Scroll(120, 102, 0, 110)
	if s := a.stageList; s.scroll.Y != 37 || s.manual != time.Unix(43, 0) || s.layout.rows != nil {
		t.Fatalf("preview modified live state: %v, %v, %d rows", s.scroll.Y, s.manual, len(s.layout.rows))
	}
	if got := a.pl.Position(); got != lines[3].At {
		t.Errorf("preview changed playback position to %v", got)
	}
}

// Rush respects the source word starts and widths, while estimated lyrics sweep across the full sentence.
func TestStageListRushTiming(t *testing.T) {
	lines := lyrics.FromSource(&music.Lyrics{Lines: []music.LyricLine{
		{Start: 1000, End: 4000, Text: "Hi 🌙", Words: []music.LyricWord{{Text: "Hi", Start: 1000, End: 1600}, {Text: "🌙", Start: 2400, End: 2800}}},
		{Start: 9500, End: 11500, Text: "End now"},
	}})
	layout := makeStageListLayout(stageFrame{style: "rush", lines: lines}, stageListLook{font: 28, anchor: 0.34},
		stageListLayoutKey{width: 380, height: 250})
	pos := 2 * time.Second
	active := layout.active(stageFrame{style: "rush", current: 0, pos: pos})
	if active < 0 || layout.rows[active].index != 0 {
		t.Fatal("the first lyric should follow the intro")
	}
	from, to, progress := stageListHighlight(&layout.rows[active], pos)
	if from != 2 || to != 5 || progress != 0 {
		t.Errorf("source timing in the gap between words: got %d, %d, %v", from, to, progress)
	}
	pos = 6 * time.Second
	active = layout.active(stageFrame{style: "rush", current: 0, pos: pos})
	if active < 0 || layout.rows[active].index != -1 {
		t.Fatal("the instrumental break should take focus")
	}
	pos = 10500 * time.Millisecond
	active = layout.active(stageFrame{style: "rush", current: 1, pos: pos})
	if active < 0 || layout.rows[active].index != 1 {
		t.Fatal("the next lyric should take focus after the break")
	}
	from, to, progress = stageListHighlight(&layout.rows[active], pos)
	if from != 0 || to != 7 || progress != 0.5 {
		t.Errorf("line-sized estimated timing: got %d, %d, %v", from, to, progress)
	}
}

// Capture the three compositions and the narrow, reduced-motion record for visual review.
func TestStageListModesFrame(t *testing.T) {
	dir := os.Getenv("MM_FRAMES")
	if dir == "" {
		t.Skip("set MM_FRAMES to save stage list frames")
	}
	a := newTestApp()
	img := coverImage(ui.RGB(53, 131, 166), ui.RGB(235, 159, 83))
	art := &artwork{bmp: ui.NewBitmap(img), tint: ui.RGB(116, 184, 212)}
	accent := ui.RGB(116, 184, 212)
	lines := lyrics.FromSource(&music.Lyrics{Lines: []music.LyricLine{
		{Start: 1800, End: 3000, Text: "晚风越过山丘", Translation: "The evening wind crosses the hill"},
		{Start: 3600, End: 6000, Text: "跟着星光，继续唱", Translation: "Follow the starlight and sing on", Words: []music.LyricWord{
			{Text: "跟着", Start: 3600, End: 4000}, {Text: "星光", Start: 4000, End: 4700}, {Text: "继续唱", Start: 4800, End: 6000},
		}},
		{Start: 7000, End: 8500, Text: "让回声落在海面"},
		{Start: 15000, End: 18000, Text: "天色慢慢亮起来"},
	}})
	for _, cfg := range []struct {
		style, name, bg string
		w, h            int
		motion          float32
	}{
		{"classic", "classic", "", 920, 520, 1},
		{"rush", "rush", "flow", 920, 520, 1},
		{"vinyl", "vinyl", "", 920, 520, 1},
		{"vinyl", "vinyl-narrow-still", "", 285, 450, 0},
	} {
		tt := ui.NewTester(func(c *ui.Context) {
			stageListTestView(c, a, stageFrame{style: cfg.style, lines: lines, current: 1, pos: 4350 * time.Millisecond,
				preview: true, playing: true, motion: cfg.motion, art: art, accent: accent, rushBackground: cfg.bg})
		}, cfg.w, cfg.h)
		tt.Frame()
		saveFrame(t, dir, "list-"+cfg.name+".png", tt.Image())
	}
}
