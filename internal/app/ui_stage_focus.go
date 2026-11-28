package app

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/lyrics"
)

// 聚光与声波共用稳定的逐词排版，强度只改变位移和光效，不改真实时间。
func (a *app) stageFocusLine(c *ui.Context, line *lyrics.Line, position time.Duration, size, fade float32, accent ui.Color) {
	motion := a.stageMotion(c)
	wave := a.settings.StageStyle == "wave"
	levels, available := a.pl.Levels()
	if wave && available && a.pl.Playing() && motion > 0 {
		c.After(33 * time.Millisecond)
	}
	progress := clamp01(float32((position - line.At).Seconds() / max(0.1, (line.End-line.At).Seconds())))
	ui.Box(c.Key(line.At)).Absolute().Top(0).Left(0).Right(0).Bottom(0).Opacity(fade).PassThrough().Children(func() {
		if !wave {
			ui.Box(c.Key("spotlight")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Draw(func(p *ui.Painter, bounds ui.Rect) {
				centerX := bounds.X + bounds.W*(0.5+(progress-0.5)*0.3*motion)
				centerY := bounds.Y + bounds.H*0.46
				for ring := 12; ring > 0; ring-- {
					radius := float32(ring) * 18 * (0.8 + 0.2*motion)
					p.Fill(ui.Rect{X: centerX - radius, Y: centerY - radius, W: radius * 2, H: radius * 2}, accent.Alpha(0.035*motion), radius)
				}
			})
		}
		ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(36).Padding(30, 72).Children(func() {
			ui.Row(c.Key("focus-words")).Wrap().Justify(ui.Center).AlignItems(ui.Center).Gap(size * 0.06).Children(func() {
				for index, word := range line.Words {
					since := float32((position - word.Start).Seconds())
					active := position >= word.Start && position < word.End
					color, scale := white.Alpha(0.45), float32(1)
					if position >= word.Start {
						color = white.Alpha(0.88)
					}
					if active {
						color = white
						scale += 0.055 * motion * float32(math.Sin(math.Pi*float64(clamp01(since/float32(max(0.1, (word.End-word.Start).Seconds()))))))
					}
					text := ui.Text(c.Key(index), word.Text).FontSize(size).FontWeight(700).TextColor(color).Scale(scale, scale)
					if a.settings.StageSparks {
						stageSparks(text, since, float64(word.Start/time.Millisecond), index == len(line.Words)-1, accent)
					}
				}
			})
			if wave {
				ui.Box(c.Key("sound-wave")).Width(300).Height(56).Draw(func(p *ui.Painter, bounds ui.Rect) {
					if !available {
						// 没有音频数据时只显示安静的基线，不能伪造频谱。
						p.Line(bounds.X, bounds.Y+bounds.H/2, bounds.X+bounds.W, bounds.Y+bounds.H/2, 1, accent.Alpha(0.35))
						return
					}
					for index, level := range levels {
						height := 2 + clamp01(level)*bounds.H*0.8*motion
						pitch := bounds.W / float32(len(levels))
						p.Fill(ui.Rect{X: bounds.X + float32(index)*pitch, Y: bounds.Y + (bounds.H-height)/2, W: max(2, pitch-4), H: height}, accent.Alpha(0.8), 2)
					}
				})
			}
		})
	})
}
