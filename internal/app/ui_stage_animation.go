package app

import (
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
	"mygo-music/internal/player"
)

// 播放和预览使用同一时刻的画面输入，预览无需改播放器或用户设置。
type stageFrame struct {
	style                               string
	lines                               []lyrics.Line
	current                             int
	pos                                 time.Duration
	w, h, motion                        float32
	accent                              ui.Color
	levels                              [player.Bands]float32
	hasLevels, sparks, preview, playing bool
	art                                 *artwork
	trackKey, rushBackground            string
	rushRhythm                          string
	// 黑胶唱片的碟心与歌名行要用；其他样式不读。
	title, artist string
}

func (a *app) animationStyle(tr *music.Track) string {
	return a.stageDirector.style(a.settings, a.lyricKey(tr))
}

func (a *app) newStage(c *ui.Context, tr *music.Track, lines []lyrics.Line, art *artwork, style string, accent ui.Color) {
	w, h := c.Size()
	pos := a.pl.Position()
	current := lyrics.Index(lines, pos)
	levels, hasLevels := a.pl.Levels()
	// 顶栏浮在舞台上面，单独留出安全区，避免滚动歌词挡住切换按钮。
	stage := ui.Box(c.Key("animated-stage")).Grow(1).Basis(0).MinHeight(0).Clip().Lang(lyrics.Lang(lines))
	if r := stage.LayoutBounds(); r.H > 0 && r.W == w {
		h = r.H
	} else {
		h = max(1, h-topBar(c)-96)
	}
	h = max(1, h-npTabsHeight)
	frame := stageFrame{style: style, lines: lines, current: current, pos: pos, w: w, h: h,
		motion: a.stageMotion(c), accent: accent, levels: levels, hasLevels: hasLevels, sparks: a.settings.StageSparks,
		playing: a.pl.Playing(), art: art, trackKey: a.lyricKey(tr), rushBackground: a.settings.StageAnimation.RushBackground,
		rushRhythm: a.settings.StageAnimation.RushRhythm, title: tr.Name, artist: tr.ArtistNames()}
	// 间奏里热评升上来时，律动画面淡出，和歌词页一致（ui_quiet.go）。
	dim, _ := a.quietShown(c, stage, tr, lines, true)
	stage.Children(func() {
		ui.Box(c.Key("safe-area")).Absolute().Top(npTabsHeight).Right(0).Bottom(0).Left(0).Children(func() {
			quietUnder(ui.Box(c.Key("stage-scene")).Absolute().Top(0).Right(0).Bottom(0).Left(0).PassThrough(), dim).Children(func() {
				a.renderStageAnimation(c, frame)
				if a.settings.StageAnimation.Spectrum && frame.hasLevels {
					stageSpectrum(c, w, frame.motion, accent, frame.levels)
				}
			})
			a.stageQuiet(c, dim, min(64, max(36, w*0.045)), w)
		})
	})
	if frame.playing {
		switch {
		case frame.motion == 0 || style == "classic" && !a.settings.StageAnimation.Spectrum:
			c.After(stageBoundaryWait(lines, current, pos))
		default:
			c.After(time.Second / 30)
		}
	}
}

func (a *app) renderStageAnimation(c *ui.Context, s stageFrame) {
	ui.Box(c.Key(s.style)).Absolute().Top(0).Right(0).Bottom(0).Left(0).Clip().Children(func() {
		switch s.style {
		case "classic", "rush", "vinyl":
			a.renderStageListMode(c, s)
		case "rainbow", "52hz", "miside", "ripple":
			renderStageColorMode(c, s)
		case "marquee", "galaxy", "jump":
			a.renderStageSceneMode(c, s)
		}
	})
}

func stageSpectrum(c *ui.Context, w, motion float32, accent ui.Color, levels [player.Bands]float32) {
	if motion == 0 {
		return
	}
	ui.Box(c.Key("stage-spectrum")).Absolute().Bottom(8).Left(w * 0.3).Right(w * 0.3).Height(26).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		step := r.W / float32(len(levels))
		for i, level := range levels {
			bar := 2 + level*(r.H-2)*min(1, motion)
			p.Fill(ui.Rect{X: r.X + float32(i)*step, Y: r.Y + r.H - bar, W: step * 0.64, H: bar}, accent.Alpha(0.55), 2)
		}
	})
}
