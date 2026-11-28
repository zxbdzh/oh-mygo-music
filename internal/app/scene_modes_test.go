package app

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

func stageSceneTestCanvas(a *app, s *stageFrame) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Size(s.w, s.h).Clip().Lang("zh-CN").Children(func() { a.renderStageSceneMode(c, *s) })
	}, int(s.w), int(s.h))
}

// 同一句的画面随媒体进度变化，倒拖并暂停后回到对应帧。
func TestStageSceneSeekFrame(t *testing.T) {
	for _, style := range []string{"marquee", "galaxy", "jump"} {
		t.Run(style, func(t *testing.T) {
			s := stageFrame{style: style, lines: stagePreviewLyricsZH, current: 1, pos: 4200 * time.Millisecond,
				w: 130, h: 60, accent: ui.Hex("#aab4fa"), motion: 1, preview: true, playing: true}
			tester := stageSceneTestCanvas(&app{}, &s)
			tester.Frame()
			before := slices.Clone(tester.Image().Pix)
			s.pos = 4750 * time.Millisecond
			tester.Frame()
			if bytes.Equal(before, tester.Image().Pix) {
				t.Fatal("同一句的画面没有跟随媒体进度变化")
			}
			s.pos, s.playing = 4200*time.Millisecond, false
			tester.Advance(time.Second)
			tester.Frame()
			if !bytes.Equal(before, tester.Image().Pix) {
				t.Fatal("倒拖或暂停让同一播放位置的画面发生了变化")
			}
		})
	}
}

// 保存换版、光点、起跳以及静态缩略图，供无头目视验收。
func TestStageSceneFrames(t *testing.T) {
	dir := framesDir(t)
	for _, style := range []string{"marquee", "galaxy", "jump"} {
		for i, pos := range []time.Duration{1100 * time.Millisecond, 4750 * time.Millisecond, 7400 * time.Millisecond} {
			s := stageFrame{style: style, lines: stagePreviewLyricsZH, pos: pos, current: lyrics.Index(stagePreviewLyricsZH, pos),
				w: 720, h: 340, accent: ui.Hex("#aab4fa"), motion: 1, sparks: true}
			saveFrame(t, dir, fmt.Sprintf("scene-%s-%d.png", style, i), stageSceneTestCanvas(&app{}, &s).Image())
		}
		for _, motion := range []float32{0, 1} {
			s := stageFrame{style: style, lines: stagePreviewLyricsZH, current: 1, pos: 4200 * time.Millisecond,
				w: 130, h: 60, accent: ui.Hex("#aab4fa"), motion: motion, preview: true}
			saveFrame(t, dir, fmt.Sprintf("scene-%s-card-%g.png", style, motion), stageSceneTestCanvas(&app{}, &s).Image())
		}
		// 用后两句模拟带前奏的曲目，检查首句还没进入时的卡片画面。
		s := stageFrame{style: style, lines: stagePreviewLyricsZH[1:], current: -1, pos: time.Second,
			w: 130, h: 60, accent: ui.Hex("#aab4fa"), motion: 1, preview: true}
		saveFrame(t, dir, fmt.Sprintf("scene-%s-intro.png", style), stageSceneTestCanvas(&app{}, &s).Image())
	}
}
