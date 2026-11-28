package app

import (
	"github.com/egoist/mygo/ui"
)

func (a *app) renderStageSceneMode(c *ui.Context, s stageFrame) {
	if s.w < 2 || s.h < 2 {
		return
	}
	s.current = min(max(s.current, -1), len(s.lines)-1)
	// 「活跃」可放大位移和速度；用于淡入或缩放插值时仍限制在 0–1。
	s.motion = min(1.25, max(0, s.motion))
	switch s.style {
	case "marquee":
		stageSceneMarquee(c, s)
	case "galaxy":
		stageSceneStardrift(c, s)
	case "jump":
		a.stageSceneJump(c, s)
	}
}

func stageSceneCanvas(c *ui.Context) ui.Element {
	return ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough()
}

func stageSceneLabel(c *ui.Context, text string, x, y, width, size float32, weight int, color ui.Color) ui.Element {
	return ui.Text(c, text).Absolute().Left(x-width/2).Top(y).Width(width).
		FontSize(size).FontWeight(weight).LineHeight(1.08).NoWrap().TextAlign(ui.Center).
		TextColor(color).TranslatePercent(0, -50).PassThrough()
}

func stageSceneFit(c *ui.Context, text string, width, size float32, weight int) float32 {
	measured, _ := c.MeasureText(0, ui.Span{Text: text, Size: size, Weight: weight})
	return size * min(1, width/max(measured, 0.1))
}

func stageSceneEmpty(c *ui.Context, s stageFrame) {
	text := "···"
	if len(s.lines) == 0 {
		text = T("No lyrics. Enjoy the music.")
	}
	size := stageSceneFit(c, text, s.w*0.8, min(s.w, s.h)*0.075, 600)
	stageSceneLabel(c, text, s.w/2, s.h/2, s.w*0.9, size, 600, white.Alpha(0.5))
}

func stageSceneTranslation(c *ui.Context, s stageFrame, y float32) {
	if s.current < 0 || s.lines[s.current].Translation == "" {
		return
	}
	text := s.lines[s.current].Translation
	size := stageSceneFit(c, text, s.w*0.8, min(22, min(s.w, s.h)*0.052), 600)
	stageSceneLabel(c, text, s.w/2, y, s.w*0.86, size, 600, white.Alpha(0.72)).Lang(c.Locale())
}
