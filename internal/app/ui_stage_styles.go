package app

import (
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// The stage styles, ported from myMusic: Classic scrolls the lyrics through
// the current line, and each of the others draws the words of a line with a
// scene of its own (see ui_stage_list_modes.go, ui_stage_color_modes.go and
// ui_stage_scene_modes.go).
type stageStyleSpec struct {
	id, label, detail string
}

var stageStyles = []stageStyleSpec{
	{"classic", N("Classic Lyrics"), N("Scroll with the current line")},
	{"rush", N("Rush"), N("Soft word-by-word highlight")},
	{"rainbow", N("Rainbow"), N("Words rise in color")},
	{"52hz", N("52 Hz"), N("Rings ripple out from each word")},
	{"miside", N("MiSide Drop"), N("Words fall and bounce")},
	{"ripple", N("Dynamic Ripples"), N("Waves surround the lyrics")},
	{"vinyl", N("Vinyl Record"), N("Spinning record beside the lyrics")},
	{"marquee", N("Marquee"), N("Lyrics on a giant LED display")},
	{"galaxy", N("Star Tide: Galaxy"), N("Stars gather into lyrics")},
	{"jump", N("Jump"), N("Follow the lyrics up the steps")},
}

func stageStyleLabel(style string) string {
	for _, s := range stageStyles {
		if s.id == style {
			return T(s.label)
		}
	}
	return T("Classic Lyrics")
}

var stageIntensities = []struct{ id, label string }{
	{"gentle", N("Gentle")}, {"standard", N("Balanced")}, {"lively", N("Lively")},
}

func (a *app) stageMotion(c *ui.Context) float32 {
	if c.Preferences().ReduceMotion {
		return 0
	}
	switch a.settings.StageIntensity {
	case "gentle":
		return 0.45
	case "lively":
		return 1.25
	default:
		return 1
	}
}

// 顶栏显示舞台实际在用的样式，面板内另显示尚未保存的选择。
func (a *app) stageStyleButton(c *ui.Context, style string) {
	b := npGlassControl(ui.ButtonBase(c.Key("stage-style")).Padding(6, 12).Gap(4).AlignItems(ui.Center).
		FocusRing(false).Label(T("Stage Style")).Tooltip(T("Stage Style"))).Bind(&a.stagePanel.anchor)
	fg := white.Alpha(0.82)
	if b.Hovered() {
		fg = white
	}
	b.Children(func() {
		ui.Text(c, stageStyleLabel(style)).FontSize(13).FontWeight(600).TextColor(b.AnimateColor("fg", fg, ui.Animation{Duration: 120 * time.Millisecond}))
		ui.Icon(c, iconDown).Size(12, 12).TextColor(fg)
	})
	if b.Clicked() {
		a.openStagePanel()
	}
}

func abs32(v float32) float32 { return max(v, -v) }

// isMark says r breaks a line: a space or punctuation.
func isMark(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }
