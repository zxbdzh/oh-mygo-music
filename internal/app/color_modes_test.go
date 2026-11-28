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

func colorModeTestCanvas(s *stageFrame) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		w, h := c.Size()
		frame := *s
		frame.w, frame.h = w, h
		ui.Box(c).Size(w, h).Clip().Lang("zh-CN").Background(ui.Hex("#151b2b")).Children(func() {
			renderStageColorMode(c, frame)
		})
	}, int(s.w), int(s.h))
}

// 同一句歌词在倒拖、暂停和恢复原尺寸后，应回到同一帧，而不是继续上一次的动效。
func TestStageColorSeekFrame(t *testing.T) {
	for _, style := range []string{"rainbow", "52hz", "miside", "ripple"} {
		t.Run(style, func(t *testing.T) {
			const start, later = 3400 * time.Millisecond, 4400 * time.Millisecond
			s := stageFrame{style: style, lines: stagePreviewLyricsZH, current: 1, pos: start,
				w: 130, h: 60, motion: 1, preview: true, playing: true}
			tt := colorModeTestCanvas(&s)
			original := slices.Clone(tt.Image().Pix)
			s.pos = later
			tt.Frame()
			if bytes.Equal(original, tt.Image().Pix) {
				t.Fatal("同一句歌词内改变播放位置，画面没有变化")
			}
			s.pos, s.playing = start, false
			tt.Advance(time.Second)
			tt.Frame()
			if !bytes.Equal(original, tt.Image().Pix) {
				t.Fatal("回到原播放位置并暂停后，画面发生变化")
			}
			tt.SetSize(720, 340)
			tt.Frame()
			tt.SetSize(int(s.w), int(s.h))
			tt.Frame()
			restored := tt.Image().Pix
			if len(restored) != len(original) {
				t.Fatal("未恢复到原尺寸")
			}
			// 缩放会重绘文字边缘，允许每个色阶差 1；歌词位置和亮度变化仍应触发失败。
			for i, want := range original {
				if diff := int(restored[i]) - int(want); diff < -1 || diff > 1 {
					t.Fatalf("恢复原尺寸后，画面在通道 %d 处发生变化：%d → %d", i, want, restored[i])
				}
			}

			s.motion = 0
			tt.Frame()
			still := slices.Clone(tt.Image().Pix)
			s.pos = later
			tt.Frame()
			if !bytes.Equal(still, tt.Image().Pix) {
				t.Fatal("减少动画时，同一句歌词内的画面仍在变化")
			}
		})
	}
}

// 52Hz 换句时两套年轮共存，长间奏保留髓心并且倒拖可以还原同一帧。
func TestRingsSourceTransition(t *testing.T) {
	lines := []lyrics.Line{
		{At: time.Second, End: 3500 * time.Millisecond, Text: "晚风 Crossing"},
		{At: 3500 * time.Millisecond, End: 6 * time.Second, Text: "星光 Starlight"},
	}
	s := stageFrame{style: "52hz", lines: lines, current: 1, pos: 4700 * time.Millisecond,
		w: 240, h: 140, motion: 1, playing: true, ringEnergy: .75}
	tt := colorModeTestCanvas(&s)
	atLine := slices.Clone(tt.Image().Pix)
	s.playing = false
	tt.Advance(time.Second)
	tt.Frame()
	if !bytes.Equal(atLine, tt.Image().Pix) {
		t.Fatal("暂停时同一响度和播放位置画面发生变化")
	}
	s.ringEnergy = .15
	tt.Frame()
	if bytes.Equal(atLine, tt.Image().Pix) {
		t.Fatal("年轮没有响应响度变化")
	}
	s.playing = true
	s.pos = 7 * time.Second
	tt.Frame()
	interlude := slices.Clone(tt.Image().Pix)
	if bytes.Equal(atLine, interlude) {
		t.Fatal("进入间奏后仍显示原歌词帧")
	}
	s.pos = 12 * time.Second
	tt.Frame()
	if bytes.Equal(interlude, tt.Image().Pix) {
		t.Fatal("间奏髓心没有推进")
	}
	s.pos, s.ringEnergy = 4700*time.Millisecond, .75
	tt.Frame()
	if !bytes.Equal(atLine, tt.Image().Pix) {
		t.Fatal("从间奏倒拖后没有还原换句年轮")
	}
}

// 保存逐字、落地和窄预览的关键帧，供无头目视验收。
func TestStageColorFrames(t *testing.T) {
	dir := framesDir(t)
	for _, style := range []string{"rainbow", "52hz", "miside", "ripple"} {
		s := stageFrame{style: style, lines: stagePreviewLyricsZH, w: 720, h: 340, motion: 1, sparks: true}
		tt := colorModeTestCanvas(&s)
		for i, pos := range []time.Duration{4200 * time.Millisecond, 6800 * time.Millisecond, 7350 * time.Millisecond} {
			s.pos, s.current = pos, lyrics.Index(s.lines, pos)
			tt.Frame()
			saveFrame(t, dir, fmt.Sprintf("color-%s-%d.png", style, i), tt.Image())
		}
		s.w, s.h, s.pos, s.current, s.preview = 130, 60, 4200*time.Millisecond, 1, true
		saveFrame(t, dir, "color-"+style+"-card.png", colorModeTestCanvas(&s).Image())
	}
}
