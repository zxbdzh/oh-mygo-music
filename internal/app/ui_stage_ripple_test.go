package app

import (
	"testing"
	"time"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// A fallen word ends up lying on the ground, keeps its spin, and fades only after the hold.
func TestMisideFallSettles(t *testing.T) {
	const unit = 1
	// 字框顶在 100，高 40，地面 500：底部 460，落点 460。
	body := misideFall(3, 100, 100, 460, 0, -80, 20, 20, 60, 1400, .34, unit)
	if body.y != 460 {
		t.Errorf("after 3s the word is at %v, want the ground 460", body.y)
	}
	if body.rx != 0 || body.ry != 0 {
		t.Errorf("a settled word lies flat: rx %v ry %v", body.rx, body.ry)
	}
	if body.alpha != 1 {
		t.Errorf("faded before the 2.5s hold: alpha %v", body.alpha)
	}
	// 落定 2.5 秒后才开始淡出，1.5 秒淡完。
	if later := misideFall(6, 100, 100, 460, 0, -80, 20, 20, 60, 1400, .34, unit); later.alpha > .05 {
		t.Errorf("still visible after settling and fading: alpha %v", later.alpha)
	}
}

// A word in the air keeps turning around the tilt limit while it falls.
func TestMisideFallTurns(t *testing.T) {
	body := misideFall(.15, 100, 100, 460, 0, 0, 90, 0, 0, 1400, .34, 1)
	if body.y <= 100 {
		t.Errorf("the word did not fall: y %v", body.y)
	}
	if body.rx > misideTiltLimit || body.rx < -misideTiltLimit {
		t.Errorf("pitch %v outside ±%v", body.rx, misideTiltLimit)
	}
}

// The watermark takes the longest word, and a credit line takes the name after its colon.
func TestRippleKeyword(t *testing.T) {
	lines := lyrics.FromSource(&music.Lyrics{Lines: []music.LyricLine{{
		Start: 0, End: 4000, Text: "我们越过山丘",
		Words: []music.LyricWord{{Text: "越过", Start: 1000, End: 2600}, {Text: "山丘", Start: 2600, End: 4000}},
	}}})
	if got := rippleKeyword(&lines[0], lines[0].Text); got != "越过" {
		t.Errorf("watermark %q, want the longest word 越过", got)
	}
	credit := lyrics.Line{At: time.Second, End: 2 * time.Second, Text: "作词：方文山"}
	if got := rippleKeyword(&credit, credit.Text); got != "方文山" {
		t.Errorf("credit line watermark %q, want 方文山", got)
	}
}
