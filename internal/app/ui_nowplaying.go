package app

import (
	"math"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

var white = ui.RGB(255, 255, 255)

// npSpring is how Now Playing comes and goes, sliding or growing out of the
// player bar's cover and shrinking back into it, and how it springs back
// from a pull too short to close it.
var npSpring = ui.SpringOptions{Duration: 340 * time.Millisecond}

// npFade is how Now Playing comes and goes while the desktop asks for less
// motion: it fades, in place.
var npFade = ui.Animation{Duration: 200 * time.Millisecond, NonMotion: true}

// npState tracks Now Playing opening and closing, and reports whether it is
// open and whether it has come to rest, open or closed, as it last showed.
// Call it once a build, before the window's content.
func (a *app) npState(c *ui.Context) (open, settled bool) {
	open = a.nowPlaying && a.pl.Current() != nil
	if open != a.npOpen {
		a.npOpen = open
		// The window buttons light over its dark backdrop, and as the
		// theme over the pages (Windows).
		if a.win != nil {
			buttons := mygo.ThemeSystem
			if open {
				buttons = mygo.ThemeDark
			}
			a.win.SetWindowButtonsTheme(buttons)
		}
	}
	return open, a.npAtRest(open)
}

// npAtRest reports whether Now Playing showed at rest: all open, with
// nothing pulling on it, or all gone.
func (a *app) npAtRest(open bool) bool {
	if open {
		return a.npT >= 1 && a.npShift == 0 && !a.npHeld
	}
	return a.npT <= 0
}

// nowPlayingLayer holds Now Playing over the whole window. It slides up
// from below the window as it opens, as it is, and back down out of it as
// it closes; with less motion, it only fades. Growing out of the cover, an
// experiment the listener turns on, it grows out of the cover in the
// player bar as it opens, a card that takes the window, the cover flying
// out of the bar to its place, and shrinks back into it as it closes.
// Pulled down, it follows the pointer, growing out of the cover smaller and
// rounder the farther it goes, and let go far or fast enough it falls the
// rest of the way; otherwise it springs back. The layer stays on top all
// the way, rather than leaving as an exit transition would, under the rest
// of the window: that would show it going over the sidebar's material
// while the page above it hid it at once.
func (a *app) nowPlayingLayer(c *ui.Context, open, settled bool) {
	w, h := c.Size()
	layer := ui.Box(c.Key("now-playing-layer")).Absolute().Top(0).Left(0).Right(0).Bottom(0)
	// Unless it grows out of the cover, an experiment, it slides up, or
	// with less motion only fades.
	reduce := c.Preferences().ReduceMotion
	a.npStill = reduce || !a.settings.GrowFromCover
	slide := a.npStill && !reduce
	spring := npSpring
	_, dy, held := layer.Dragged()
	held = held && open
	if held {
		a.npPull += dy
	}
	if _, vy, ok := layer.DragEnded(); ok && open {
		if a.npPull > h*0.2 || a.npPull > 24 && vy > 700 {
			// Let go far enough, or thrown: it falls the rest of the way
			// as fast as it was thrown.
			a.nowPlaying, open = false, false
			spring.Velocity = -max(vy, 0) / h
		}
	}
	if !held {
		a.npPull = 0
	}
	a.npHeld = held

	target := float32(0)
	if open {
		target = 1
	}
	var t, shift float32
	if reduce {
		t = layer.AnimateTo("open", target, npFade)
	} else {
		t = layer.AnimateTo("open", target, ui.Animation{Spring: spring})
	}
	if held {
		// Pulled up, it gives less and less.
		shift = layer.Jump("pull", ui.Rubberband(a.npPull, 0, h, h))
	} else {
		shift = layer.AnimateTo("pull", 0, ui.Animation{Spring: npSpring})
	}
	fresh, was := a.npT <= 0 && t > 0, a.npT
	a.npT, a.npShift = t, shift
	if a.npAtRest(open) != settled {
		// Another build, for the page under it to come or go, and the
		// bar's cover with it.
		c.AnimationFrame()
	}
	// Back in the bar, it shows a frame more, its cover over the bar's:
	// the bar, built before it, hid its own cover in this frame, and
	// shows it from the next.
	if t <= 0 && !open && was <= 0 || a.pl.Current() == nil {
		// Closed: nothing to paint or click.
		layer.Invisible()
		a.queueAt, a.wallState.track = -1, ""
		return
	}
	switch {
	case fresh:
		// Its first frame lays it out, for what flies to its place to know
		// where that is; it shows from the next.
		layer.Opacity(0)
		c.AnimationFrame()
	case slide:
		// It follows a pull down, as it does growing out of the cover.
		if k := 1 - clamp01(t); k > 0 || shift != 0 {
			layer.Translate(0, h*k+shift)
		}
	case a.npStill:
		if t < 1 {
			layer.Opacity(max(t, 0))
		}
	default:
		a.npCard(layer, t, shift, w, h)
	}
	if !open {
		// On its way out, the window under it takes the clicks.
		layer.PassThrough()
	}
	layer.Children(func() { a.nowPlayingView(c) })
}

// npCard clips the layer to the card Now Playing is, t of the way open,
// from the player bar's cover to the window, and moves it shift down, a
// little smaller and rounder for it.
func (a *app) npCard(layer ui.Element, t, shift, w, h float32) {
	k := 1 - clamp01(t) // how near the bar's cover it is
	pulled := clamp01(shift / h)
	if k == 0 && shift == 0 {
		return
	}
	from := a.npFromRect(w, h)
	radius := npFromRadius*k + 28*clamp01(shift/120)
	layer.ClipInset(from.Y*k, (w-from.X-from.W)*k, (h-from.Y-from.H)*k, from.X*k, radius)
	if shift != 0 {
		s := 1 - 0.12*pulled
		layer.TransformOrigin(0.5, 0).Scale(s, s).Translate(0, shift)
	}
}

// npFromRect is the player bar's cover, in a window w by h, that Now
// Playing grows out of: where the bar last showed it, or where the bar
// would, before it has.
func (a *app) npFromRect(w, h float32) ui.Rect {
	if a.npFrom.W > 0 {
		return a.npFrom
	}
	// 对齐底部悬浮玻璃播放胶囊（Left:16, PaddingX:16, Bottom:12, Height:72）内小封面默认位置。
	return ui.Rect{X: sidebarWidth + 32, Y: h - 72, W: 48, H: 48}
}

// npMorph moves e, a cover of Now Playing, out of the player bar's cover
// to its place as Now Playing opens, and back into it as it closes: the
// song in the bar grows into the view.
func (a *app) npMorph(c *ui.Context, e ui.Element) ui.Element {
	if a.npT >= 1 || a.npStill {
		return e
	}
	b := e.LayoutBounds()
	if b.W <= 0 {
		// Not laid out yet: the next frame places it.
		c.AnimationFrame()
		return e.Opacity(0)
	}
	w, h := c.Size()
	from := a.npFromRect(w, h)
	k := 1 - a.npT
	s := 1 + (from.W/b.W-1)*k
	return e.TransformOrigin(0, 0).Scale(s, s).Translate((from.X-b.X)*k, (from.Y-b.Y)*k)
}

// npBreathe scales e, a cover that breathes, to scale: there as a pose
// moves it, while Now Playing is open, and at once on its way to or from
// the bar, coming to its full size as it gets there, the size of the
// bar's cover it takes the place of.
func (a *app) npBreathe(e ui.Element, scale float32) {
	if a.npT < 1 && !a.npStill {
		s := lerp(1, scale, clamp01(a.npT))
		e.Scale(s, s)
		return
	}
	e.AnimatePose(ui.Pose{Scale: scale}, ui.Animation{Spring: ui.SpringOptions{Duration: 600 * time.Millisecond, Bounce: 0.25}})
}

// npRadius is the corner radius, r at rest, of a cover size across that
// npMorph moves: its corners, shrinking with it, stay as round on screen
// as they are on its way to the bar's cover's, and are that round as it
// gets there, rather than going sharp and rounding at once as the bar's
// cover takes its place.
func (a *app) npRadius(c *ui.Context, r, size float32) float32 {
	if a.npT >= 1 || a.npStill {
		return r
	}
	w, h := c.Size()
	k := 1 - clamp01(a.npT)
	s := 1 + (a.npFromRect(w, h).W/size-1)*k // as npMorph scales it
	return min(lerp(r, npFromRadius, k)/s, size/2)
}

// npFromRadius is the corner radius of the player bar's cover.
const npFromRadius = 8

// npRise fades e in and lifts it into place as Now Playing opens, once the
// cover is on its way: the nth of the things that do, one after another,
// going again the other way as it closes.
func (a *app) npRise(e ui.Element, n int) ui.Element {
	if a.npT >= 1 || a.npStill {
		return e
	}
	from := 0.3 + 0.07*float32(n)
	k := smoothstep(from, from+0.4, a.npT)
	return e.Opacity(k).Translate(0, 24*(1-k))
}

// nowPlayingView fills the window with what plays: its cover over a slow
// drift of its own colors, the controls, and lyrics that follow the song.
func (a *app) nowPlayingView(c *ui.Context) {
	pl := a.pl
	tr := pl.Current()
	w, h := c.Size()
	art := a.art.get(sized(tr.Album.ImageURL, 600), true)
	// The colors stay the last song's while its cover loads, rather than
	// going gray between the two.
	shown := art
	if art == nil && tr.Album.ImageURL != "" {
		shown = a.npArt
	}
	a.npArt = shown
	tint := ui.RGB(120, 120, 130)
	if shown != nil && !shown.failed {
		tint = shown.tint
	}
	accent := tint.Mix(white, 0.45)

	// Near the bar, the card is only its cover, the rest gone clear.
	bg := ui.Hex("#0b0b0d")
	if t := a.npT; t < 1 && !a.npStill {
		bg = bg.Alpha(smoothstep(0, 0.35, t))
	}
	root := ui.Box(c.Key("now-playing")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
		Background(bg).TextColor(white)
	// The pointer is over the view whenever it is in the window.
	pointerIn := root.Hovered()
	root.Children(func() {
		// 在线 MV 背景：歌有绑定且当前视图该显示时，静音视频铺在最底层。
		// 无绑定或关闭时保持原封面背景，不占解码器。
		if a.mvShown() {
			a.openMV()
			a.mvLayer(c, tr, w, h)
		}
		// Its colors bloom out of the bar's cover as it opens.
		bloom := ui.Box(c.Key("bloom")).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough()
		if t := a.npT; t < 1 && !a.npStill {
			from := a.npFromRect(w, h)
			s := 1 + 0.3*(1-clamp01(t))
			bloom.Opacity(smoothstep(0.05, 0.55, t)).
				TransformOrigin((from.X+from.W/2)/w, (from.Y+from.H/2)/h).Scale(s, s)
		}
		bloom.Children(func() {
			// 减少透明度时，模糊材质会填充系统主题底色，盖掉沉浸页的深色封面背景。
			useMaterial := !c.Preferences().ReduceTransparency && a.npPane != paneWall
			// MV 已经是背景了，封面背景不再画，免得把视频盖住。
			if !a.mvShown() || !a.mv.hasBinding(tr) {
				a.backdrop(c, shown, tint, useMaterial)
			}
			style := a.settings.CoverBackdropStyle
			if a.settings.Fog {
				style = defaultCoverBackdropStyle()
			}
			radius := 80 * clamp01(float32(style.Blur/100))
			blur := a.settings.CoverBackdrop && useMaterial && radius > 0
			switch {
			case a.npPane == paneStage && a.settings.CoverBackdrop:
				// 律动舞台无左侧卡片，轻柔模糊底层封面并压低亮度，避免封面细节与大字歌词争夺视线。
				shade := ui.Box(c.Key("np-stage-blur")).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().
					Background(ui.RGBA(8, 9, 13, 0.24*style.shadeScale()))
				if blur {
					shade.Material(glass.Blur{Radius: radius * 0.65})
				}
			case blur && !(a.mvShown() && a.mv.hasBinding(tr)):
				// blur.go 只读取 Mask.From/To 端点透明度；从窗口 26% 处向右侧歌词区渐进模糊，保留左侧清晰封面光影。
				ui.Box(c.Key("np-pane-blur")).Absolute().Top(0).Left(w * 0.26).Right(0).Bottom(0).PassThrough().
					Material(glass.Blur{Radius: radius, Mask: &ui.LinearGradient{From: ui.Transparent, To: ui.RGB(0, 0, 0), Angle: 90}})
			}
		})
		// Over the backdrop, which is positioned too: positioned elements
		// paint over those in the flow.
		ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Children(func() { a.nowPlayingBody(c, tr, art, accent, w, h, pointerIn) })
	})
}

// mvLayer 画 MV 背景与暗色遮罩，遮罩的形状照 ECHO：
// 横向两边重、中间轻，纵向从上到下渐深，让歌词落在画面较暗的区域上。
func (a *app) mvLayer(c *ui.Context, tr *music.Track, w, h float32) {
	a.ensureMVStore()
	if a.mv.player == nil && a.mv.frame == nil {
		return
	}
	// 视频不在当前 pane 时同步循环也要跑，否则切回歌词页会有一次长漂移。
	if a.mv.player != nil {
		a.mvSync(c, tr)
	}
	ui.Box(c.Key("mv-background")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Children(func() {
		if a.mv.player != nil {
			ui.Video(c, a.mv.player).Fill().Fit(ui.Cover).Scale(1.15, 1.15)
			return
		}
		ui.Image(c, a.mv.frame).Fill().Fit(ui.Cover).Scale(1.15, 1.15)
	})
	// 遮罩画在视频上、正文下：0.42 只是暗色主题的下限，不是整体盖一层灰。
	ui.Box(c.Key("mv-shade")).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().
		Draw(func(p *ui.Painter, r ui.Rect) {
			// ECHO 的横向遮罩是 0.72/0.42/0.62，但它在浏览器里是两层叠加后的
			// 透明度合成。MyGo 里两层叠画会更重，直接按合成后的单层画。
			p.FillGradient(r, ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
				{Color: ui.RGBA(8, 11, 16, 0.42), At: 0},
				{Color: ui.RGBA(8, 11, 16, 0.18), At: 0.48},
				{Color: ui.RGBA(8, 11, 16, 0.3), At: 1},
			}}, 0)
			p.FillGradient(r, ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
				{Color: ui.RGBA(8, 11, 16, 0.12), At: 0},
				{Color: ui.RGBA(8, 11, 16, 0.4), At: 1},
			}}, 0)
		})
}

func (a *app) nowPlayingBody(c *ui.Context, tr *music.Track, art *artwork, accent ui.Color, w, h float32, pointerIn bool) {
	pl := a.pl
	a.commentsFor(tr)
	// An episode has what it is about and its captions in place of lyrics; a
	// song, lyrics in place of those. Comments are for a source that has them.
	switch episode := tr.ShowID != ""; {
	case episode && a.npPane == paneWall:
		a.npPane = paneQueue
	case episode && (a.npPane == paneLyrics || a.npPane == paneStage):
		a.npPane = paneShow
		if a.captionsOn() {
			a.npPane = paneCaptions
		}
	case episode && a.npPane == paneCaptions && !a.captionsShown():
		a.npPane = paneShow
	case !episode && (a.npPane == paneShow || a.npPane == paneCaptions):
		a.npPane = a.lyricsPaneChosen()
	}
	if a.npPane == paneComments && !a.trackLib(tr).can("comments") {
		a.npPane = paneQueue
	}
	if a.npPane != paneQueue {
		a.queueAt = -1
	}
	if a.npPane != paneWall {
		a.wallState.track = ""
	}
	// MV 开启时不走普通双列：视频是主体，封面卡片与玻璃胶囊反而碍事。
	if a.mvShown() && a.mv.hasBinding(tr) {
		a.mvBody(c, tr, accent, w, h, pointerIn)
		return
	}
	{
		// The top strip drags the window; the chevron closes the view,
		// showing only while the pointer is in the window, as the pane tabs do.
		a.npRise(ui.Row(c).Height(topBar(c)).Justify(ui.Center).AlignItems(ui.Center).DragWindow(), 0).Children(func() {
			reveal(ui.Box(c.Key("close-reveal")), pointerIn).Children(func() {
				closeBtn := npGlassControlDark(iconButton(c.Key("close"), iconDown, 18, white.Alpha(0.85), T("Close")))
				if closeBtn.Clicked() {
					a.nowPlaying = false
				}
			})
		})
		if a.npPane == paneStage {
			// The stage takes the whole view: the lyrics are the show, the
			// controls a slim bar at the foot.
			a.npRise(reveal(npTabsRow(c, w), pointerIn), 0).Children(func() {
				a.paneTabs(c)
				a.mvButton(c, tr)
				a.stageStyleButton(c)
				a.stageLanguageButton(c)
			})
			a.stage(c, tr, accent)
			a.stageBar(c, tr, art, accent)
			return
		}
		if a.npPane == paneWall {
			// The wall takes the whole view too, under the song playing.
			a.wallPane(c, tr, art, accent, w, h, pointerIn)
			return
		}
		side := float32(math.Min(float64(w)*0.36, 440))
		// The cover leaves room above it for the tabs, starting as far
		// under them as the wall's song does.
		coverSize := min(side, h-436)
		// The column is as wide as the cover, for the cover, what plays
		// and the tabs over it to start at one edge, but wide enough for
		// the controls.
		side = max(coverSize, 280)
		padX := npSideX(w)
		a.npRise(reveal(npTabsRow(c, w), pointerIn), 0).Children(func() {
			a.paneTabs(c)
			a.mvButton(c, tr)
			if tr.ShowID == "" {
				a.lyricDisplayButton(c, tr)
			}
		})
		ui.Row(c).Grow(1).Padding(0, padX, 44, padX).Gap(72).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(side).Gap(0).Children(func() {
				// The column sits in the middle of its room, but never
				// closer to the tabs than npCoverTop: what shows only at
				// times, such as the radio's chip over the title, would
				// lift the cover up against them, so it pushes the
				// controls down into the room under them instead.
				ui.Box(c.Key("over-cover")).Grow(1).Basis(0).MinHeight(npCoverTop)
				// The cover flies out of the bar's as Now Playing opens.
				a.npMorph(c, ui.Box(c.Key("cover-morph")).Size(coverSize, coverSize).AlignSelf(ui.Start)).Children(func() {
					a.npCover(c, tr, art, coverSize)
				})

				// 保留左侧圆角艺术封面卡片，将下方曲目信息、进度、播控与音量收进紧凑圆角玻璃控制胶囊。
				ctrl := a.npRise(ui.Column(c.Key("np-controls")).Margin(18, 0, 0, 0).Padding(14, 16).Radius(20).
					Material(forcedGlass{}).
					Border(1, white.Alpha(0.14)).Shadow(0, 12, 32, -8, ui.RGBA(0, 0, 0, 0.35)), 1)
				ctrl.Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(0, 0, 12, 0).Children(func() {
						ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(4).Children(func() {
							if r := pl.Radio(); r != "" {
								a.radioChip(c, r)
							}
							ui.Text(c, tr.Name).FontSize(21).FontWeight(700).LetterSpacing(-0.3).SingleLine()
							a.artistLinks(c, tr, white.Alpha(0.65), white, true).FontSize(14.5).SingleLine()
						})
						a.likeButton(c.Key("like"), tr, 21, white.Alpha(0.75))
						a.songMenu(c.Key("more"), tr, 21, white.Alpha(0.75)).Margin(0, -8, 0, 0)
					})
					a.scrubber(c.Key("np-scrub"), &a.npScrub, white.Alpha(0.18), white.Alpha(0.9), 16, accent.Mix(white, 0.35))
					// As tall with the quality's badge as without it: the badge goes
					// while the next song loads, and the column would move with it.
					ui.Row(c).Height(16).AlignItems(ui.Center).Children(func() {
						ui.Text(c, clock(pl.Position())).FontSize(11).FontFeatures("tnum").TextColor(white.Alpha(0.55))
						ui.Spacer(c)
						fb := pl.Feedback()
						a.syncPlaybackDetailsState(tr, fb)
						if fb.Failure != "" || fb.Stage != "" || pl.Loading() {
							sub, isErr := a.playbackStatusText(fb)
							fg, bg := white.Alpha(0.85), white.Alpha(0.12)
							if isErr {
								fg, bg = ui.Hex("#f5d58a"), ui.Hex("#f5d58a").Alpha(0.16)
							}
							a.playbackDetailsTrigger(c, "np", tr, fb, fg, bg, sub, false, true, true)
						} else if st := pl.Stream(); st != nil {
							if outcome := a.playbackOutcomeText(tr, st, fb); outcome != "" {
								fg, bg := white.Alpha(0.75), white.Alpha(0.1)
								if st.Quality == "lossless" || st.Type == "flac" || st.Type == "wav" {
									fg, bg = ui.Hex("#f5d58a"), ui.Transparent
								}
								a.playbackDetailsTrigger(c, "np", tr, fb, fg, bg, outcome, false, true, true)
							}
						}
						ui.Spacer(c)
						status := "-" + clock(pl.Duration()-pl.Position())
						if pl.Loading() && fb.Stage == "" {
							status = T("Buffering…")
						}
						ui.Text(c, status).FontSize(11).FontFeatures("tnum").TextColor(white.Alpha(0.55))
					})
					ui.Box(c).Margin(10, 0, 12, 0).Children(func() {
						a.transport(c, white, white.Alpha(0.45), accent, 27)
					})
					ui.Row(c).Justify(ui.Center).AlignItems(ui.Center).Gap(8).Children(func() {
						a.volumeSlider(c, white.Alpha(0.75), white.Alpha(0.18), min(168, side-72))
						a.sleepTimerButton(c, "np", white.Alpha(0.75), true, true)
					})
				})
				ui.Box(c.Key("under-controls")).Grow(1).Basis(0)
			})

			pane := ui.Column(c.Key("pane")).Grow(1).Basis(0).MinWidth(0)
			a.npRise(pane, 2)
			pane.Children(func() {
				// The pane starts where the cover does as tall as its room,
				// below the tabs, which start level with the pane's top: they
				// come and go with the pointer, so they sit out of the flow,
				// never over it. In a tall window, where the cover sinks into
				// the middle of its column, the pane stays up there: level
				// with the cover, it left the top of the window empty, cutting
				// off the lines before the one sung.
				ui.Box(c).Height(npCoverTop)
				a.npZoomFusion(c, a.npPane, func() {
					switch a.npPane {
					case paneLyrics:
						a.lyricsPane(c, tr, h)
					case paneComments:
						a.commentsPane(c, tr)
					case paneShow:
						a.showPane(c, tr)
					case paneCaptions:
						a.captionsPane(c, tr, h)
					default:
						a.queuePane(c)
					}
				})
			})
		})
	}
}

// mvBody 是 MV 开启时的正文：歌词居中单列，底部一条纯色控制条。
// 不用玻璃模糊：视频逐帧变化，实时模糊会糊成噪点且更费。
func (a *app) mvBody(c *ui.Context, tr *music.Track, accent ui.Color, w, h float32, pointerIn bool) {
	// 顶部保留关闭按钮，不另加标题行，让画面尽量干净。
	a.npRise(ui.Row(c).Height(topBar(c)).Justify(ui.Center).AlignItems(ui.Center).DragWindow(), 0).Children(func() {
		reveal(ui.Box(c.Key("close-reveal")), pointerIn).Children(func() {
			a.mvButton(c, tr)
			closeBtn := iconButton(c.Key("close"), iconDown, 18, white.Alpha(0.8), T("Close"))
			if closeBtn.Clicked() {
				a.nowPlaying = false
			}
		})
	})
	// 歌词列居中，最大 620px；顶栏 72px，底部控制条 72px。
	ui.Column(c).Absolute().Top(72).Left(max((w-620)/2, 24)).Width(min(w-48, 620)).
		Bottom(72).Children(func() {
		a.npZoomFusion(c, a.npPane, func() {
			switch a.npPane {
			case paneStage:
				a.stage(c, tr, accent)
			default:
				a.lyricsPane(c, tr, h)
			}
		})
	})
	// 底部控制条：纯色暗调，歌名在左，播控在右，一条细白边分界。
	ui.Row(c.Key("mv-bar")).Absolute().Left(0).Right(0).Bottom(0).Height(72).
		Padding(0, 24).AlignItems(ui.Center).Gap(16).
		Background(ui.RGBA(6, 8, 12, 0.5)).
		Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, tr.Name).FontSize(16).FontWeight(700).SingleLine()
				ui.Text(c, tr.ArtistNames()).FontSize(12).TextColor(white.Alpha(0.6)).SingleLine()
			})
			ui.Spacer(c)
			a.transport(c, white, white.Alpha(0.5), accent, 24)
		})
}

// npZoomFusion 让窗格切换有个过渡：画面型窗格缩放加模糊，列表型窗格短位移淡入。
// 这类进入/退出过渡由布局驱动，只在切换后的三四百毫秒内重建视图；
// 静止后不再请求帧。减少动效时直接切到目标。
func (a *app) npZoomFusion(c *ui.Context, pane int, fn func()) {
	prev := a.npPrevPane
	a.npPrevPane = pane

	// 外层容器保持同一个 key，只让内部窗格按 pane 换 key：
	// 这样旧窗格才能以退出动画离开，新窗格以入场动画进来。
	wrap := ui.Column(c).Grow(1)
	if pane == prev || a.npT < 1 || c.Preferences().ReduceMotion {
		wrap.Children(func() { ui.Column(c.Key(pane)).Grow(1).Children(fn) })
		return
	}

	// 画面型窗格（歌词、律动、照片墙）保留缩放与模糊；
	// 列表型窗格只做短位移与淡入，不把长列表拉糊。
	visual := func(p int) bool { return p == paneLyrics || p == paneStage || p == paneWall }
	anim := ui.Animation{Spring: ui.SpringOptions{Duration: 450 * time.Millisecond, Bounce: 0.1}}
	enter := &ui.Motion{Scale: 0.95, Opacity: 0, Blur: 4}
	exit := &ui.Motion{Scale: 1.05, Opacity: 0, Blur: 4}
	if !visual(pane) || !visual(prev) {
		anim = ui.Animation{Duration: 240 * time.Millisecond, Ease: ui.EaseOut}
		enter = &ui.Motion{Y: 4, Opacity: 0}
		exit = &ui.Motion{Y: -4, Opacity: 0}
	}
	wrap.Transition(ui.ElementTransition{Animation: &anim, Enter: enter, Exit: exit})
	wrap.Children(func() { ui.Column(c.Key(pane)).Grow(1).Children(fn) })
}

// npMaxWidth is how wide the cover and the pane beside it go, at most.
const npMaxWidth = 1280

// npSideX is how far in from the sides of a view w wide the cover and the
// pane beside it are: in a wide window they keep together in the middle,
// rather than the pane taking all the width the cover leaves.
func npSideX(w float32) float32 {
	return max(72, (w-npMaxWidth)/2)
}

// npTabsHeight is how tall the row of Now Playing's tabs is, for what is
// under it to start below it.
const npTabsHeight = 32

// npCoverTop is how far under the strip along the top Now Playing's cover
// starts, at the least, and the pane beside it: clear of the tabs over
// them. It is where the cover starts as tall as its room.
const npCoverTop = npTabsHeight + 22

// npTabsRow is the row of Now Playing's tabs, in the same place for every
// pane, so that they stay put going from one to another: at the top, level
// with the cover's left. They start from the left, so that only the last,
// the comments, moves as its count changes.
func npTabsRow(c *ui.Context, w float32) ui.Element {
	return ui.Row(c.Key("tabs")).Absolute().Top(topBar(c)).Left(npSideX(w)).Height(npTabsHeight).Gap(8).AlignItems(ui.Center)
}

// reveal fades e in while the pointer is in the window, and out when it
// leaves.
func reveal(e ui.Element, shown bool) ui.Element {
	pose := ui.Pose{Fade: 1}
	if shown {
		pose = ui.Pose{}
	}
	return e.AnimatePose(pose, ui.Animation{Duration: 200 * time.Millisecond})
}

// lyricsPaneChosen is the pane of lyrics the listener chose last: what
// the app starts on, before the player is made. Now Playing shows what an
// episode is about in place of either.
func (a *app) lyricsPaneChosen() int {
	if a.settings.LyricsStage {
		return paneStage
	}
	return paneLyrics
}

// The panes of Now Playing.
const (
	paneLyrics = iota
	paneQueue
	paneStage
	paneComments
	paneShow
	paneCaptions
	paneWall
)

// npTab is a tab of Now Playing: its pane, and what it says.
type npTab struct {
	pane  int
	label string
}

// npTabs returns the tabs of Now Playing, in order, for what plays: ⌘1
// chooses the first, ⌘2 the second, and on.
func (a *app) npTabs() []npTab {
	tr := a.pl.Current()
	var tabs []npTab
	if tr != nil && tr.ShowID != "" {
		tabs = []npTab{{paneShow, T("About")}}
		if a.captionsShown() {
			tabs = append(tabs, npTab{paneCaptions, T("Captions")})
		}
		tabs = append(tabs, npTab{paneQueue, T("Up Next")})
	} else {
		tabs = []npTab{{paneLyrics, T("Lyrics")}, {paneStage, T("Stage")}, {paneQueue, T("Up Next")}, {paneWall, T("Wall")}}
	}
	if tr != nil && a.trackLib(tr).can("comments") {
		tabs = append(tabs, npTab{paneComments, T("Comments")})
	}
	return tabs
}

// npGlassControl 是按钮型半透明胶囊：按下轻微生长，减少动效时 MyGo 直接跳到终态。
func npGlassControl(e ui.Element) ui.Element {
	return e.Radius(999).
		Material(forcedGlass{interactive: true}).
		Border(1, white.Alpha(0.14))
}

// npGlassControlDark 是浮在深色封面上的顶部按钮用的暗底胶囊，避免浅底发白。
func npGlassControlDark(e ui.Element) ui.Element {
	return e.Radius(999).
		Material(forcedGlass{interactive: true, dark: true}).
		Border(1, white.Alpha(0.14))
}

// paneTabs 照 ToolNest segmentedControl 做：等宽段 + 弹簧滑移的白色高亮胶囊。
// 高亮位置只由 npPane 的索引驱动，不绑定分段控件的可变选中值，避免二者打架。
func (a *app) paneTabs(c *ui.Context) {
	tabs := a.npTabs()
	selected := 0
	for i, t := range tabs {
		if t.pane == a.npPane {
			selected = i
			break
		}
	}

	theme := c.Theme()
	size := float32(13)
	const inset = 2
	segW := float32(58)
	for _, t := range tabs {
		w, _ := c.MeasureText(0, ui.Span{Text: t.label, Size: size, Weight: 600})
		segW = max(segW, w+22)
	}

	track := ui.Row(c.Key("np-tabs-pill")).Width(segW*float32(len(tabs)) + 2*inset).Height(30).
		Shrink(0).Gap(0).Padding(inset).Radius(15).
		Background(ui.Hex("#ffffff").Alpha(0.1)).FocusRing(false)
	if theme.Dark {
		track.Background(ui.Hex("#000000").Alpha(0.16))
	}
	// 胶囊位置用弹簧滑移，切换页签时从旧位置平滑滑到新位置。
	position := track.AnimateTo("np-seg-pos", float32(selected),
		ui.Animation{Spring: ui.SpringOptions{Duration: 220 * time.Millisecond}})
	track.Draw(func(p *ui.Painter, bounds ui.Rect) {
		pill := ui.Rect{X: bounds.X + inset + position*segW, Y: bounds.Y + inset,
			W: segW, H: bounds.H - 2*inset}
		fill := ui.Hex("#ffffff").Alpha(0.92)
		if theme.Dark {
			fill = ui.Hex("#ffffff").Alpha(0.22)
		}
		p.Fill(pill, fill, 13)
	})
	track.Children(func() {
		for i, t := range tabs {
			tab := t
			seg := ui.ButtonBase(c.Key(tab.pane)).Width(segW).Height(26).Shrink(0).
				Radius(13).AlignItems(ui.Center).Justify(ui.Center).FocusRing(false)
			fg := white.Alpha(0.75)
			if i == selected {
				if theme.Dark {
					fg = white
				} else {
					fg = ui.Hex("#282a30")
				}
			}
			seg.Children(func() {
				if tab.pane != paneComments {
					ui.Text(c, tab.label).FontSize(size).FontWeight(600).TextColor(fg).SingleLine()
					return
				}
				n, text := a.commentCount(), ""
				if n > 0 {
					text = " " + count(n)
				}
				ui.Text(c, tab.label).FontSize(size).FontWeight(600).TextColor(fg)
				rollCount(c, &a.tabCount, n, text, fg)
			})
			if seg.Clicked() {
				a.choosePane(tab.pane)
			}
		}
	})
}

// choosePane shows pane i in Now Playing, remembering lyrics or the stage,
// whichever was chosen, for the next song.
func (a *app) choosePane(i int) {
	a.npPane = i
	if (i == paneLyrics || i == paneStage) && a.settings.LyricsStage != (i == paneStage) {
		a.settings.LyricsStage = i == paneStage
		a.settings.save()
	}
}

// radioChip says which radio plays, over the title: a click on it, or on
// its cross, leaves the radio, playing on the song.
func (a *app) radioChip(c *ui.Context, name string) {
	exit := Tf("Leave %s", radioLabel(name))
	chip := ui.ButtonBase(c.Key("radio-chip")).Gap(5).Padding(3, 6, 3, 8).Margin(0, 0, 4, -8).Radius(999).
		AlignItems(ui.Center).AlignSelf(ui.Start).FocusRing(false).Label(exit).Tooltip(exit)
	bg, fg, cross := ui.Transparent, white.Alpha(0.6), float32(0.5)
	if chip.Hovered() {
		bg, fg, cross = white.Alpha(0.12), white.Alpha(0.85), 1
	}
	chip.Background(chip.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond})).TextColor(fg)
	chip.Children(func() {
		ui.Icon(c, iconRadio).Size(12, 12)
		ui.Text(c, radioLabel(name)).FontSize(11.5).FontWeight(700).LetterSpacing(0.3)
		ui.Icon(c, iconX).Size(11, 11).Opacity(cross)
	})
	if chip.Clicked() {
		a.exitRadio()
	}
}

// stageBar is the slim bar along the foot of the stage: what plays, where
// it is, and the controls.
func (a *app) stageBar(c *ui.Context, tr *music.Track, art *artwork, accent ui.Color) {
	pl := a.pl
	ui.Column(c).Padding(0, 48, 28, 48).Gap(10).Children(func() {
		a.npRise(a.scrubber(c.Key("stage-scrub"), &a.npScrub, white.Alpha(0.18), white.Alpha(0.9), 14, accent.Mix(white, 0.35)), 2)
		ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Grow(1).Basis(0).MinWidth(0).Gap(12).AlignItems(ui.Center).Children(func() {
				if bmp := bitmap(art); bmp != nil {
					cov := a.npMorph(c, ui.Box(c.Key(tr.ID)).Size(44, 44))
					in := artIn(cov, bmp)
					cov.Children(func() { artImage(c, bmp, a.npRadius(c, 8, 44), in) })
				}
				// All but the cover, which flies to the bar's, fade as the
				// rest of Now Playing does.
				a.npRise(ui.Row(c).Shrink(1).MinWidth(0).Gap(12).AlignItems(ui.Center), 3).Children(func() {
					ui.Column(c).Shrink(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, tr.Name).FontSize(14).FontWeight(700).SingleLine()
						a.artistLinks(c, tr, white.Alpha(0.55), white).FontSize(12).SingleLine()
					})
					a.likeButton(c.Key("like"), tr, 17, white.Alpha(0.6))
					a.songMenu(c.Key("more"), tr, 17, white.Alpha(0.6)).Margin(0, 0, 0, -10)
				})
			})
			a.npRise(ui.Row(c), 3).Children(func() { a.transport(c, white, white.Alpha(0.45), accent, 22) })
			a.npRise(ui.Row(c).Grow(1).Basis(0).MinWidth(0).Justify(ui.End).AlignItems(ui.Center).Gap(8), 4).Children(func() {
				fb := pl.Feedback()
				a.syncPlaybackDetailsState(tr, fb)
				if fb.Failure != "" || fb.Stage != "" || pl.Loading() {
					sub, isErr := a.playbackStatusText(fb)
					fg, bg := white.Alpha(0.85), white.Alpha(0.12)
					if isErr {
						fg, bg = ui.Hex("#f5d58a"), ui.Hex("#f5d58a").Alpha(0.16)
					}
					a.playbackDetailsTrigger(c, "stage", tr, fb, fg, bg, sub, false, true, true)
				} else if st := pl.Stream(); st != nil {
					if outcome := a.playbackOutcomeText(tr, st, fb); outcome != "" {
						fg, bg := white.Alpha(0.75), white.Alpha(0.1)
						if st.Quality == "lossless" || st.Type == "flac" || st.Type == "wav" {
							fg, bg = ui.Hex("#f5d58a"), ui.Transparent
						}
						a.playbackDetailsTrigger(c, "stage", tr, fb, fg, bg, outcome, false, true, true)
					}
				}
				ui.Text(c, clock(pl.Position())+" / "+clock(pl.Duration())).FontSize(11.5).FontFeatures("tnum").TextColor(white.Alpha(0.5))
				a.sleepTimerButton(c, "stage", white.Alpha(0.65), true, false)
				a.volumeButton(c, white.Alpha(0.65))
			})
		})
	})
}

func (a *app) lyricsPane(c *ui.Context, tr *music.Track, h float32) {
	pl := a.pl
	lines, loaded := pl.Lyrics()
	lines = a.displayedLyrics(tr, lines)
	if len(lines) == 0 {
		col := ui.Column(c).Grow(1)
		if loaded {
			col.Center()
		}
		dim, _ := a.quietShown(c, col, tr, lines, loaded)
		quietUnder(col, dim)
		col.Children(func() {
			if !loaded {
				lyricsSkeleton(c, npShimmer(c))
				return
			}
			ui.Text(c, T("No lyrics. Enjoy the music.")).FontSize(24).FontWeight(700).TextColor(white.Alpha(0.4))
		})
		a.quietCard(c, dim, h*0.18)
		return
	}
	pos := pl.Position()
	cur := lyrics.Index(lines, pos)
	// Redraw as the next line begins.
	if pl.Playing() && cur+1 < len(lines) {
		c.After(lines[cur+1].At - pos + 20*time.Millisecond)
	}
	if key := tr.Source + "\x00" + tr.ID; a.lyricTrack != key {
		a.lyricTrack, a.lyricAt = key, -2
		a.lyricScroll, a.lyricGlide = ui.ScrollState{}, lyricGlide{}
	}
	lag := a.lyricGlide.step(c, &a.lyricScroll)

	scroll := ui.Scroll(c.Key("lyrics")).Grow(1).TrackScroll(&a.lyricScroll).Padding(0, 8, h*0.5, 0)
	dim, quiet := a.quietShown(c, scroll, tr, lines, loaded)
	if dim > 0.98 {
		// Under the hot comment, gone, not drawn faint.
		scroll.Invisible()
	}
	defer a.quietCard(c, dim, h*0.18)
	a.timedLines(c, scroll, lines, pos, cur, lag, linesStyle{size: 30, weight: 800, lineHeight: 1.25, quiet: quiet, shadow: a.mvShown()}, nil)
}

// linesStyle is how timedLines sets its lines: the size, weight and
// leading of their type; quiet nearly hides them, under a hot comment.
type linesStyle struct {
	size       float32
	weight     int
	lineHeight float32
	quiet      bool
	shadow     bool
}

// timedLines fills scroll with lines that follow the track playing at
// pos: the line at cur lit, and brought to a third of the way down once
// it changes, but none with cur -1 before the first; a click on one seeks
// to it. The lines are held back by lag, how far the glide to the line
// lit has still to go (lyricGlide). foot, if set, follows the lines.
func (a *app) timedLines(c *ui.Context, scroll ui.Element, lines []lyrics.Line, pos time.Duration, cur int, lag float32, st linesStyle, foot func()) {
	pl := a.pl
	var active ui.Element
	// Japanese lyrics are drawn in a Japanese font, kanji and kana alike;
	// their translations in the app's language.
	lang := lyrics.Lang(lines)
	scroll.Children(func() {
		ui.Column(c).Gap(6).Translate(0, -lag).Children(func() {
			for i, l := range lines {
				line := ui.ButtonBase(c.Key(i)).Column().AlignItems(ui.Start).Padding(10, 12).Radius(12).Gap(6).FocusRing(false).Label(l.Text)
				on := i == cur
				alpha := float32(0.28)
				if on {
					alpha = 1
				} else if line.Hovered() {
					alpha = 0.55
				}
				if st.quiet {
					// Nearly gone under a hot comment.
					alpha *= quietFaded
				}
				if line.Hovered() {
					line.Background(white.Alpha(0.06))
				}
				scale := float32(0.94)
				if on {
					scale = 1
				}
				line.TransformOrigin(0, 0.5).AnimatePose(ui.Pose{Scale: scale}, ui.Animation{Spring: ui.SpringOptions{Duration: 600 * time.Millisecond, Bounce: 0.1}})
				line.Children(func() {
					// The line fades as a pose, in frames that only paint, rather
					// than building the view at the display's rate as it does.
					text := ui.Column(c).AlignItems(ui.Start).Gap(6)
					text.AnimatePose(ui.Pose{Fade: 1 - alpha}, ui.Animation{Duration: 380 * time.Millisecond})
					if st.shadow {
						// MV 画面明暗会变，歌词加一层轻阴影保证浅色帧上也读得清。
						text.TextShadow(0, 1, 4, ui.RGBA(0, 0, 0, 0.65))
					}
					text.Children(func() {
						if on && len(l.Words) > 0 {
							// The line sung lights up word by word as it is sung.
							ui.RichText(c.Key("words")).Lang(lang).FontSize(st.size).FontWeight(st.weight).LineHeight(st.lineHeight).LetterSpacing(-0.4).Children(func() {
								for wi, wd := range l.Words {
									lit := clamp01(float32((pos - wd.Start).Seconds() / max(0.05, (wd.End-wd.Start).Seconds())))
									ui.Text(c.Key(wi), wd.Text).TextColor(white.Alpha(0.38 + 0.62*lit))
								}
							})
							if pl.Playing() {
								// Words brighten smoothly enough at 25 frames a second.
								c.After(40 * time.Millisecond)
							}
						} else {
							lineText := ui.Text(c, l.Text).Lang(lang).FontSize(st.size).FontWeight(st.weight).LineHeight(st.lineHeight).LetterSpacing(-0.4).TextColor(white)
							if st.shadow {
								lineText.TextShadow(0, 1, 4, ui.RGBA(0, 0, 0, 0.65))
							}
						}
						if l.Translation != "" {
							trans := ui.Text(c, l.Translation).FontSize(17).FontWeight(600).TextColor(white.Alpha(0.7))
							if st.shadow {
								trans.TextShadow(0, 1, 3, ui.RGBA(0, 0, 0, 0.55))
							}
						}
					})
				})
				if line.Clicked() {
					pl.Seek(l.At)
				}
				if on {
					active = line
				}
			}
		})
		if foot != nil {
			foot()
		}
	})
	// Bring the line sung to a third of the way down, once it changed.
	if cur != a.lyricAt {
		box := scroll.Bounds()
		target := float32(0)
		if active.Valid() {
			// Where the glide is going, without where it still is.
			if lb := active.LayoutBounds(); lb.H > 0 && box.H > 0 {
				target = lb.Y - box.Y + a.lyricScroll.Y - box.H*0.3
			} else {
				return // not laid out yet: the next frame scrolls
			}
		}
		a.lyricAt = cur
		a.lyricGlide.to(c, &a.lyricScroll, min(max(0, target), a.lyricScroll.MaxY))
	}
}

// lyricGlide scrolls the lyrics to the line sung, as a critically damped
// spring, as ScrollTo's would. ScrollTo moves the offset in every frame,
// and MyGo builds the view again in the frame after each that moved it,
// and its spring creeps over the last pixel for half a second: with a line
// following another every second or two, most of a song. So the offset
// goes to the line at once, in one frame, and the lines are held back by
// what the glide has still to go, its lag, which settles to nothing within
// half a pixel.
type lyricGlide struct {
	on bool
	// The lag t after start: (a + b·t)·e^(−ωt), in DIPs.
	a, b  float64
	start time.Time
	// The offset it goes to, set in the next frame (pending), with the lag
	// that holds the lines back from it: the layout of the frame that
	// chose it takes the offset too, the lines built without the lag.
	target  float32
	pending bool
	set     float32 // the offset it set
}

// lyricOmega is the spring's ω: a line or two away, it comes within half a
// pixel in about 800 ms.
const lyricOmega = 10

// at returns the lag at now, and how fast it changes, in DIPs a second.
func (g *lyricGlide) at(now time.Time) (lag, v float64) {
	if !g.on {
		return 0, 0
	}
	t := now.Sub(g.start).Seconds()
	e := math.Exp(-lyricOmega * t)
	return (g.a + g.b*t) * e, (g.b - lyricOmega*(g.a+g.b*t)) * e
}

// to scrolls s to target from the next frame, gliding from where it
// shows, at the speed it went if it was on its way.
func (g *lyricGlide) to(c *ui.Context, s *ui.ScrollState, target float32) {
	now := c.Now()
	lag, v := g.at(now)
	shown := float64(s.Y) + lag
	g.target, g.pending = target, true
	g.on = !c.Preferences().ReduceMotion
	g.start, g.a = now, shown-float64(target)
	g.b = v + lyricOmega*g.a
	c.AnimationFrame()
}

// step returns the lag of the lines in this frame, and asks for the next
// while there is one. The listener scrolling stops the glide where it
// shows.
func (g *lyricGlide) step(c *ui.Context, s *ui.ScrollState) float32 {
	if g.pending {
		s.Y, g.set, g.pending = g.target, g.target, false
	}
	if !g.on {
		return 0
	}
	lag, v := g.at(c.Now())
	if s.Y != g.set {
		// The listener took over: the offset takes what showed.
		s.Y, g.on = max(0, min(s.Y+float32(lag), s.MaxY)), false
		return 0
	}
	if math.Abs(lag) < 0.5 && math.Abs(v) < 20 {
		g.on = false
		return 0
	}
	c.AnimationFrame()
	return float32(lag)
}

// queuePane lists the whole queue in play order, the track playing marked
// among the others: a click plays one there, and the list stays as it is.
func (a *app) queuePane(c *ui.Context) {
	if a.queueAt < 0 {
		a.queuePaneView.current = 0
	}
	a.queueRows(c, &a.queuePaneView, white)
	a.queueAt = 0
}
