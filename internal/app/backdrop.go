package app

import (
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"

	"mygo-music/internal/player"
)

// The backdrop of Now Playing paints the cover's colors behind it, as
// a full-screen player does: a deep gradient of its main color, lighter at
// the top, with the cover itself, blurred, faintly through it. It stays
// still, so it is painted once a song, into a small canvas an eighth of the
// window across, which shows scaled up as smooth as it is: what moves over
// it then draws one image under it.
//
// The experimental fog (settings.Fog) makes it alive, as a mobile player's
// player's is: fog of the cover's color, billowing as it rises slowly
// through a deep shade of it, with specks of dust glinting in it, brighter
// and thicker with the bass. A shader paints it into a canvas, half the
// window across on the GPU, thirty times a second while the music plays;
// the CPU paints it still. Paused, the fog glides to a stop, and the
// canvas stays as it is.
//
// Another song's backdrop fades in over the last one's, which stays as it
// was, in a canvas of its own, until it is gone.

// fogSource paints the fog: noise of five octaves, warped by noise of
// its own so that it billows rather than slides, rising with Time, Fog
// over Deep where it is thick; thickest a little above the middle, and
// thinning toward the top and the controls at the foot.
const fogSource = `package main

var Time float
var Bass float
var Deep vec4
var Fog vec4

func hash(p vec2) float {
	return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453)
}

func noise(p vec2) float {
	i := floor(p)
	f := fract(p)
	u := f * f * (3.0 - 2.0*f)
	a := hash(i)
	b := hash(i + vec2(1, 0))
	c := hash(i + vec2(0, 1))
	d := hash(i + vec2(1, 1))
	return mix(mix(a, b, u.x), mix(c, d, u.x), u.y)
}

func fbm(p vec2) float {
	v := 0.0
	amp := 0.5
	for i := 0; i < 5; i++ {
		v += amp * noise(p)
		p = p*2.03 + vec2(1.7, 9.2)
		amp *= 0.5
	}
	return v
}

func Fragment(dst vec4, pos vec2) vec4 {
	size := rectSize()
	uv := pos / size
	p := pos / size.y * 2.4
	t := Time
	rise := vec2(0, t*0.1)
	q := vec2(fbm(p+rise+vec2(0, t*0.04)), fbm(p+rise+vec2(5.2, 1.3)-vec2(t*0.05, 0)))
	f := fbm(p + rise + 0.9*q)
	// Broad banks of it, and the puffs within them.
	bank := fbm(p*0.45 + rise*0.6 + vec2(3.7, 0))
	cloud := smoothstep(0.3-0.06*Bass, 0.78, f) * (0.45 + 0.75*smoothstep(0.3, 0.65, bank))
	// Thickest a little above the middle.
	m := smoothstep(-0.1, 0.4, uv.y) * (1.0 - 0.5*smoothstep(0.65, 1.0, uv.y))
	c := mix(Deep.rgb, Fog.rgb, min(cloud*m*(0.85+0.35*Bass), 1.0))
	// A broad glow behind it all, darker at the corners.
	g := length((uv - vec2(0.5, 0.42)) * vec2(1.0, 1.25))
	c *= 1.08 - 0.45*smoothstep(0.2, 0.85, g)
	// Specks of dust, rising with the fog, each glinting in its own time.
	cell := size.y / 22
	sp := (pos + vec2(0, t*cell*0.9)) / cell
	id := floor(sp)
	h := hash(id + vec2(3.1, 7.7))
	if h > 0.93 {
		at := vec2(hash(id+vec2(1.3, 2.9)), hash(id+vec2(8.1, 4.3)))*0.8 + 0.1
		d := length((fract(sp)-at)*cell) / max(cell*0.05, 0.7)
		glint := 0.5 + 0.5*sin(t*(1.5+2.0*h)+h*40.0)
		c += vec3(0.75, 0.82, 1.0) * exp(-d*d) * glint * (0.25 + 0.3*m)
	}
	return vec4(c, 1)
}
`

var fogShader = sync.OnceValue(func() *shader.Shader { return shader.MustCompile(fogSource) })

// backdropFrame is how often the fog is painted while it moves.
const backdropFrame = time.Second / 30

// backdropFade is how long another song's backdrop takes to fade in.
const backdropFade = 700 * time.Millisecond

// backdropCache is the backdrop as last painted, and where the fog's
// drift and its pulse are.
type backdropCache struct {
	canvas *ui.Canvas
	before *ui.Canvas // the song before's, fading out over it
	fadeAt time.Time  // when it began to
	art    *artwork
	tint   ui.Color
	fog    bool
	hasBmp bool // 当前画布是否显示清晰封面，用于在封面就绪或开关变化后重绘。
	soft   float32
	shade  float32
	at     time.Time // when it was painted
	drift  float64   // the clouds' time, in seconds, which stops on pause
	speed  float64   // how fast drift goes: 1 playing, 0 paused
	level  barState
	last   time.Time
}

// browseBackdropCache 单独维护浏览页的封面氛围状态，避免与正在播放页互相踩踏。
type browseBackdropCache struct {
	art    *artwork
	before *artwork
	fadeAt time.Time
	track  string
}

// browseBackdropView 在主界面底层绘制克制的封面漫射氛围，切歌平滑过渡、静止零动画开销。
func (a *app) browseBackdropView(c *ui.Context) {
	tr := a.pl.Current()
	var art *artwork
	key := ""
	if tr != nil && tr.Album.ImageURL != "" {
		key = tr.Source + "\x00" + tr.ID + "\x00" + tr.Album.ImageURL
		art = a.art.get(sized(tr.Album.ImageURL, 600), true)
	}
	bb := &a.browseBackdrop
	// 仅在已知封面已加载完毕或当前切到无封面曲目时更新目标，加载期间保持上一首氛围防闪烁。
	if tr == nil || tr.Album.ImageURL == "" {
		if bb.art != nil {
			bb.before, bb.art, bb.fadeAt = bb.art, nil, c.Now()
		}
		bb.track = ""
	} else {
		shown := art
		if shown != nil && shown.failed {
			shown = nil
		}
		if art != nil || bb.track != key {
			if art != nil && shown != bb.art {
				bb.before, bb.art, bb.fadeAt = bb.art, shown, c.Now()
			} else if art != nil && art.failed && bb.art != nil {
				bb.before, bb.art, bb.fadeAt = bb.art, nil, c.Now()
			}
			bb.track = key
		}
	}

	bg := a.pal.bg
	dark := a.pal.dark
	reduce := c.Preferences().ReduceMotion
	ui.Box(c.Key("browse-backdrop")).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, bg, 0)
		now := p.Now()
		alpha := float32(1)
		if bb.before != nil {
			if reduce {
				bb.before = nil
			} else if k := float32(now.Sub(bb.fadeAt)) / float32(backdropFade); k < 1 {
				k = 1 - (1-k)*(1-k)*(1-k)
				alpha = k
				drawBrowseWash(p, r, bb.before, 1-k, bg, dark)
				p.AnimationFrame()
			} else {
				bb.before = nil
			}
		}
		if bb.art != nil {
			drawBrowseWash(p, r, bb.art, alpha, bg, dark)
		}
	})
}

// drawBrowseWash 将模糊封面漫射光晕铺满整个浏览页背景，并叠加与主题一致的柔和保护渐变。
func drawBrowseWash(p *ui.Painter, r ui.Rect, art *artwork, alpha float32, bg ui.Color, dark bool) {
	if art == nil || art.failed || alpha <= 0.004 {
		return
	}
	img := art.backdrop
	if img == nil {
		img = art.bmp
	}
	if img != nil {
		p.ImageRegion(img, ui.Rect{}, r, ui.ImageOptions{Tint: white.Alpha(alpha)})
	} else {
		p.Fill(r, art.tint.Mix(bg, 0.45).Alpha(alpha), 0)
	}
	// 浅色主题顶部轻透封面色并迅速融回纯净浅底；深色主题略加深氛围以衬托玻璃面板层次。
	if dark {
		p.FillGradient(r, ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
			{Color: bg.Alpha(0.68 * alpha), At: 0},
			{Color: bg.Alpha(0.84 * alpha), At: 0.42},
			{Color: bg.Alpha(0.94 * alpha), At: 1},
		}}, 0)
		return
	}
	p.FillGradient(r, ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: bg.Alpha(0.74 * alpha), At: 0},
		{Color: bg.Alpha(0.89 * alpha), At: 0.42},
		{Color: bg.Alpha(0.96 * alpha), At: 1},
	}}, 0)
}

// backdrop paints the backdrop behind Now Playing, still or fog.
func (a *app) backdrop(c *ui.Context, art *artwork, tint ui.Color, materialBlur bool) {
	pl := a.pl
	fog, still := a.settings.Fog, c.Preferences().ReduceMotion
	coverBackdrop := a.settings.CoverBackdrop
	style := a.settings.CoverBackdropStyle
	ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		// 默认关闭雾效且已有清晰封面时，使用窗口分辨率画布保留封面细节；仅雾效或回退渐变降采样绘制。
		gpu := p.Accelerated()
		hasBmp := !fog && coverBackdrop && art != nil && !art.failed && art.bmp != nil
		var soft, shade float32
		if hasBmp {
			shade = style.shadeScale()
			if !materialBlur {
				// 关闭透明效果时使用已缓存的模糊封面，避免系统材质填成实色。
				soft = clamp01(float32(style.Blur / 100))
			}
		}
		div := float32(8)
		if fog && gpu {
			div = 2
		} else if hasBmp {
			div = 1
		}
		cw, ch := max(1, int(r.W/div)+1), max(1, int(r.H/div)+1)
		bd := &a.backdropCache
		if bd.canvas != nil {
			if w, h := bd.canvas.Size(); w != cw || h != ch {
				bd.canvas, bd.before = nil, nil
			}
		}
		now := p.Now()
		if bd.canvas != nil && (bd.art != art || bd.tint != tint || (!fog && bd.hasBmp != hasBmp)) {
			// Another song or newly decoded cover: fade from the previous canvas.
			bd.canvas, bd.before, bd.fadeAt = bd.before, bd.canvas, now
		}
		moving := fog && gpu && !still
		lv, live := pl.Levels()
		moved := bd.step(now, pl.Playing() && moving, lv, live && moving)
		due := now.Sub(bd.at) >= backdropFrame-2*time.Millisecond
		if bd.canvas == nil || bd.art != art || bd.tint != tint || bd.fog != fog || bd.hasBmp != hasBmp ||
			bd.soft != soft || bd.shade != shade || moved && due {
			if bd.canvas == nil {
				bd.canvas = ui.NewCanvas(cw, ch)
			}
			bd.art, bd.tint, bd.fog, bd.hasBmp, bd.at = art, tint, fog, hasBmp, now
			bd.soft, bd.shade = soft, shade
			bass := bd.level.group(0, 6)
			bd.canvas.Draw(p, func(cp *ui.Painter) {
				box := ui.Rect{W: float32(cw), H: float32(ch)}
				if fog {
					drawFog(cp, box, tint, float32(bd.drift), bass)
				} else {
					drawBackdrop(cp, box, art, tint, hasBmp, soft, shade)
				}
			})
		}
		p.Image(bd.canvas.Bitmap(), r, ui.FillBox)
		if bd.before != nil {
			if k := float32(now.Sub(bd.fadeAt)) / float32(backdropFade); k < 1 {
				// Eased out, as it fades.
				k = 1 - (1-k)*(1-k)*(1-k)
				p.ImageRegion(bd.before.Bitmap(), ui.Rect{}, r, ui.ImageOptions{Tint: white.Alpha(1 - k)})
				p.AnimationFrame()
			} else {
				bd.before = nil
			}
		}
		if moved {
			p.After(backdropFrame)
		}
	})
}

// step moves the drift and the pulse on to now, and reports whether
// either still moves.
func (bd *backdropCache) step(now time.Time, playing bool, lv [player.Bands]float32, live bool) bool {
	dt := now.Sub(bd.last).Seconds()
	if bd.last.IsZero() || dt > 0.2 {
		dt = 1.0 / 30
	}
	bd.last = now
	to := 0.0
	if playing {
		to = 1
	}
	// The drift eases in as the music plays, and glides to a stop.
	bd.speed += (to - bd.speed) * min(1, 2.5*dt)
	if math.Abs(to-bd.speed) < 0.005 {
		bd.speed = to
	}
	bd.drift += bd.speed * dt
	bd.level.step(now, lv, live)
	return bd.speed > 0 || bd.level.group(0, 6) > 0.004
}

// drawBackdrop paints the still backdrop in r.
func drawBackdrop(p *ui.Painter, r ui.Rect, art *artwork, tint ui.Color, hasBmp bool, soft, shade float32) {
	if hasBmp {
		// 清晰封面满铺底层，同时叠温和压暗梯度保护左侧边缘、顶部胶囊、右侧歌词与底部播控对比度。
		top, foot := backdropColors(tint)
		p.FillGradient(r, ui.LinearGradient{Angle: 180, Oklab: true, Stops: []ui.GradientStop{
			{Color: top, At: 0}, {Color: foot, At: 1},
		}}, 0)
		p.Image(art.bmp, r, ui.Cover)
		if soft > 0 && art.backdrop != nil {
			p.ImageRegion(art.backdrop, ui.Rect{}, coverRect(r, 48, 48), ui.ImageOptions{Tint: white.Alpha(soft)})
		}
		mask := func(alpha float32) ui.Color { return ui.RGBA(8, 9, 13, min(1, alpha*shade)) }
		p.FillGradient(r, ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
			{Color: mask(0.18), At: 0},
			{Color: mask(0.36), At: 0.42},
			{Color: mask(0.62), At: 1},
		}}, 0)
		p.FillGradient(r, ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
			{Color: mask(0.36), At: 0},
			{Color: mask(0.18), At: 0.25},
			{Color: mask(0.28), At: 0.62},
			{Color: mask(0.68), At: 1},
		}}, 0)
		return
	}
	top, foot := backdropColors(tint)
	p.FillGradient(r, ui.LinearGradient{Angle: 180, Oklab: true, Stops: []ui.GradientStop{
		{Color: top, At: 0}, {Color: foot, At: 1},
	}}, 0)
	if art != nil && art.backdrop != nil {
		// The cover, blurred, as a faint wash of its own colors.
		p.ImageRegion(art.backdrop, ui.Rect{W: 48, H: 48}, coverRect(r, 48, 48), ui.ImageOptions{Tint: white.Alpha(0.1)})
	}
	// Darker toward the foot, under the controls.
	p.FillGradient(r, ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
		{Color: ui.RGBA(0, 0, 0, 0), At: 0.55}, {Color: ui.RGBA(0, 0, 0, 0.18), At: 1},
	}}, 0)
}

// coverRect returns where an image of w×h goes to cover r, centered.
func coverRect(r ui.Rect, w, h float32) ui.Rect {
	s := max(r.W/w, r.H/h)
	return ui.Rect{X: r.X + (r.W-w*s)/2, Y: r.Y + (r.H-h*s)/2, W: w * s, H: h * s}
}

// backdropColors returns the top and the foot of the backdrop's gradient
// for a cover of tint: its hue, deep and rich, at the same depth whatever
// the cover, so that white reads over it.
// A gray cover stays gray.
func backdropColors(tint ui.Color) (top, foot ui.Color) {
	h, s, _ := hsv(float64(tint.R)/255, float64(tint.G)/255, float64(tint.B)/255)
	if s > 0.12 {
		s = min(max(s*1.3, 0.45), 0.66)
	}
	return hsvColor(h, s, 0.35), hsvColor(h, min(s*1.05, 0.7), 0.24)
}

// drawFog paints the fog of tint in r at drift seconds, lit by bass.
func drawFog(p *ui.Painter, r ui.Rect, tint ui.Color, drift, bass float32) {
	deep, fog := fogColors(tint)
	fogShader().Draw(p, r, shader.Options{Uniforms: map[string]any{
		"Time": drift, "Bass": min(bass, 1), "Deep": deep, "Fog": fog,
	}})
}

// fogColors returns the deep shade behind the fog and the fog's own for a
// cover of tint: its hue, rich, at the same depths whatever the cover, so
// that white reads over them. A gray cover stays gray.
func fogColors(tint ui.Color) (deep, fog ui.Color) {
	h, s, _ := hsv(float64(tint.R)/255, float64(tint.G)/255, float64(tint.B)/255)
	if s > 0.12 {
		s = min(max(s*1.3, 0.6), 0.85)
	}
	return hsvColor(h, s, 0.3), hsvColor(h, s*0.6, 0.72)
}

// hsvColor is the color of hue h (degrees), saturation s and value v.
func hsvColor(h, s, v float64) ui.Color {
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	m := v - c
	return ui.RGB(uint8((r+m)*255+0.5), uint8((g+m)*255+0.5), uint8((b+m)*255+0.5))
}

// backdropOver paints the backdrop again over band, a strip of win, the
// box the backdrop fills, as opaque at each height in band as alpha says
// for it, from 0 at its top to 1 at its foot: what moves under the strip
// fades into the backdrop with no edge, the very pixels around it where
// it is opaque.
func (a *app) backdropOver(p *ui.Painter, win, band ui.Rect, alpha func(t float32) float32) {
	bd := &a.backdropCache
	if bd.canvas == nil || win.W <= 0 || win.H <= 0 || band.H <= 0 {
		return
	}
	cw, ch := bd.canvas.Size()
	sx, sy := float32(cw)/win.W, float32(ch)/win.H
	// Its song's fading in over the song before's, as the backdrop.
	before := float32(0)
	if bd.before != nil {
		if k := float32(p.Now().Sub(bd.fadeAt)) / float32(backdropFade); k < 1 {
			before = (1 - k) * (1 - k) * (1 - k)
		}
	}
	const strip = 2
	for y := float32(0); y < band.H; y += strip {
		o := alpha((y + strip/2) / band.H)
		if o <= 0.004 {
			continue
		}
		dst := ui.Rect{X: band.X, Y: band.Y + y, W: band.W, H: min(strip, band.H-y)}
		src := ui.Rect{X: (dst.X - win.X) * sx, Y: (dst.Y - win.Y) * sy, W: dst.W * sx, H: dst.H * sy}
		p.ImageRegion(bd.canvas.Bitmap(), src, dst, ui.ImageOptions{Tint: white.Alpha(o)})
		if before > 0 {
			p.ImageRegion(bd.before.Bitmap(), src, dst, ui.ImageOptions{Tint: white.Alpha(o * before)})
		}
	}
}
