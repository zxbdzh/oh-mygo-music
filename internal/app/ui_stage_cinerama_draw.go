package app

import (
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// 巨幕的画法：一块“屏”（舞台区）上的扫描线、暗角、三种版式的正文和跑马灯带。
// 排版在每句第一次上屏时量一次、按句缓存，逐帧只换位置、透明度、裁切。
//
// 移植取舍（MyGo 没有离屏层 / drawTextOnPath / 字形描边 / 真模糊）：
// - 组透明度改成把每个色的 alpha 乘下去（文字和光本来就叠在同一处，视觉一致）；缩放用变换绕屏心。
// - 描边填色的“描边”一侧画成三成透明度的实心字，远看与细描边一致。
// - 四边的环形文字逐字算圆角矩形上的位置与切线，再逐字旋转贴上去。
// - 文字辉光交给 MyGo 的真模糊文字阴影，模糊半径按原版 30px（乘屏折算）走。
const (
	cinSubtitleGapDp = 14
	cinSubtitleLines = 1.5
	cinBandFontRatio = .55
	cinBandTrack     = .12
	cinRibbonTrackEm = .05
	cinRibbonCount   = 6
	cinClearance     = 1.25
	cinBandRatio     = .12
	cinBandTextRatio = 1.6
	cinGlowRatio     = 30
)

type cinGeometry struct {
	w, h         float32
	band         float32
	unit         float32
	subtitleBand float32
}

func (g cinGeometry) clear() float32 { return g.band * cinClearance }

// cinText 是一次塑形的结果：字形、宽度，以及上下缘（用于垂直居中与基线）。
type cinText struct {
	glyphs                []ui.Glyph
	w, h, ascent, descent float32
	// 辉光只能用排版画（阴影挂在 Span 上），所以留住原文和字体；
	// 各浓度档的排版存在 glow 里，值拷贝共用同一份缓存。
	src  string
	font ui.Font
	glow *cinGlow
}

func cinShape(text string, font ui.Font) cinText {
	m := font.Metrics()
	t := cinText{src: text, font: font, ascent: m.Ascent, descent: m.Descent}
	t.h = t.ascent + t.descent
	if text == "" {
		return t
	}
	t.glyphs = ui.Shape(text, font)
	if n := len(t.glyphs); n > 0 {
		t.w = t.glyphs[n-1].X + t.glyphs[n-1].Advance
	}
	return t
}

// trackWidth 是加上字距后的整段宽。
func (t cinText) trackWidth(track float32) float32 {
	if len(t.glyphs) < 2 {
		return t.w
	}
	return t.w + track*float32(len(t.glyphs)-1)
}

type cinCache struct {
	w, h    float32
	sig     string
	program *cinProgram
	arts    map[int]*cinArt
}

type cinArt struct {
	small  *cinSmallArt
	hero   *cinHeroArt
	ribbon *cinRibbonArt
	bands  *cinBandsArt
}

type cinSmallArt struct {
	units  []cinUnit
	texts  []*cinText
	x, y   []float32
	jitter []float32
}

type cinChunkArt struct {
	chunk  cinChunk
	text   cinText
	fontPx float32
	// 摊开字距的块逐字画：每个字的排版和它在块里的起点。
	glyphs []cinText
	glyphX []float32
	glyphW float32
}

type cinHeroArt struct {
	chunks   []cinChunkArt
	centerX  float32
	centerY  float32
	clip     ui.Rect
	poster   []cinText
	posterAt []float32
}

type cinStripArt struct {
	strip                  cinStrip
	cx, cy, length         float32
	thickness              float32
	text                   cinText
	textUnit               float32
	shade                  ui.LinearGradient
	glowExtent, dropExtent float32
	glowStops              []float32
	dropAboveStops         []float32
	dropBelowStops         []float32
}

type cinRibbonArt struct {
	strips  []cinStripArt
	area    ui.Rect
	clampPx float64
	spanSec float64
}

type cinBandsArt struct {
	marquee     *cinMarquee
	top, bottom *cinText
	ring        *cinRingArt
}

// cinRingArt 是四边一圈的字：整段重复文字的排版、一圈长度、每份宽，以及每个字的排版缓存。
type cinRingArt struct {
	source   string
	glyphs   []ui.Glyph
	unit     float32
	orbit    int
	font     ui.Font
	perRune  map[rune]cinText
}

type cinPaint struct {
	p      *ui.Painter
	s      stageFrame
	x, y   float32
	geo    cinGeometry
	accent ui.Color
	still  bool
}

// stageSceneMarquee 画巨幕。
func stageSceneMarquee(c *ui.Context, s stageFrame) {
	if s.w < 2 || s.h < 2 {
		return
	}
	canvas := stageSceneCanvas(c.Key("cin-screen"))
	cache := ui.Local(canvas, "cinerama", func() cinCache { return cinCache{} })
	sig := cinSignature(s)
	if cache.w != s.w || cache.h != s.h || cache.sig != sig {
		*cache = cinCache{w: s.w, h: s.h, sig: sig, arts: map[int]*cinArt{}}
		cache.program = cinCompile(s.lines, s.trackKey, "", cinDefaultWeights, true)
	}
	geo := cinGeometryFor(s)
	still := s.motion == 0
	t := s.pos.Seconds()
	between := still && s.current < 0
	frame := cinEvaluate(cache.program, t, still, between)

	canvas.Draw(func(p *ui.Painter, r ui.Rect) {
		d := cinPaint{p: p, s: s, x: r.X, y: r.Y, geo: geo, accent: s.accent, still: still}
		d.chrome()
		if len(cache.program.plans) == 0 {
			return
		}
		if frame.outgoing >= 0 {
			d.drawPlan(cache, frame.outgoing, t, frame.out)
		}
		if frame.current >= 0 {
			d.drawPlan(cache, frame.current, t, frame.now)
		}
	})

	if len(s.lines) == 0 {
		stageSceneEmpty(c, s)
		return
	}
	// 宿主副行落在正文下沿下面（带子之上）。
	clear := float32(0)
	if frame.current >= 0 && frame.current < len(cache.program.plans) {
		clear = geo.clearOf(&cache.program.plans[frame.current])
	}
	sub := geo.subtitleSize()
	stageSceneTranslation(c, s, s.h-clear-sub/2-6*geo.unit)
}

func cinSignature(s stageFrame) string {
	h := fnv32(s.trackKey)
	for i := range s.lines {
		l := &s.lines[i]
		h = h*31 + fnv32(l.Text)
		h = h*31 + fnv32(l.Translation)
		h = h*31 + uint32(l.At/1e6)
	}
	return strconv.FormatUint(uint64(h), 36)
}

func cinGeometryFor(s stageFrame) cinGeometry {
	textPx := min(76, s.w*.066, s.h*.18)
	return cinGeometry{
		w: s.w, h: s.h,
		band: max(1, min(cinBandRatio*min(s.w, s.h), textPx*cinBandTextRatio)),
		unit: max(.25, min(s.w, s.h)/720),
	}
}

// ---------------------------------------------------------------- 屏面家具

func (d cinPaint) chrome() {
	w, h := d.geo.w, d.geo.h
	// 舞台没有宿主背景，屏面自己铺一层压暗的主色（原版让位给宿主的模糊封面）。
	d.p.Fill(ui.Rect{X: d.x, Y: d.y, W: w, H: h}, d.base(), 0)
	// 扫描线：3dp 一行、顶部 1dp 是文字色 8%，整层再按 0.5 画。
	density := max(1, d.p.Scale())
	period := max(2, float32(math.Round(float64(3*density))))
	line := min(period-1, max(1, float32(math.Round(float64(density)))))
	for y := float32(0); y < h; y += period {
		d.p.Fill(ui.Rect{X: d.x, Y: d.y + y, W: w, H: line}, white.Alpha(.08*.5), 0)
	}
	d.vignette()
}

// base 是屏面底色：压暗的主色掺一点强调色。
func (d cinPaint) base() ui.Color { return ui.Hex("#0c0912").Mix(d.accent, .10) }

// vignette 是四边的暗角（白 16%，按高斯尾巴往屏心衰减）。
func (d cinPaint) vignette() {
	w, h := d.geo.w, d.geo.h
	blur := float64(cinGlowRatio * d.geo.unit * 5)
	extent := min(float32(blur*1.5), min(w, h)/2)
	if extent <= 1 {
		return
	}
	stops := cinShadowStops(.16, blur, 0, float64(extent))
	d.edgeGradient(ui.Rect{X: d.x, Y: d.y, W: w, H: extent}, stops, 180)
	d.edgeGradient(ui.Rect{X: d.x, Y: d.y + h - extent, W: w, H: extent}, stops, 0)
	d.edgeGradient(ui.Rect{X: d.x, Y: d.y, W: extent, H: h}, stops, 90)
	d.edgeGradient(ui.Rect{X: d.x + w - extent, Y: d.y, W: extent, H: h}, stops, 270)
}

// ribbonFade 把丝带在四边擦淡：原版给丝带层加 DstIn 遮罩（边上透出宿主），MyGo 没有混合模式，
// 改用底色盖一条“边上不透明、往里 8% 透明”的渐变来近似。
func (d cinPaint) ribbonFade(reserve float32) {
	w, h := d.geo.w, d.geo.h-reserve
	by := min(24*d.geo.unit, h*.08)
	bx := min(32*d.geo.unit, w*.06)
	y := float32(min(.5, by/max(1, h)))
	x := float32(min(.5, bx/max(1, w)))
	base := d.base()
	fade := func(r ui.Rect, angle float32, edge float32) {
		d.p.FillGradient(r, ui.LinearGradient{Angle: angle, Stops: []ui.GradientStop{
			{Color: base, At: 0}, {Color: base.Alpha(0), At: edge},
		}}, 0)
	}
	fade(ui.Rect{X: d.x, Y: d.y, W: w, H: h}, 180, y)
	fade(ui.Rect{X: d.x, Y: d.y, W: w, H: h}, 0, y)
	fade(ui.Rect{X: d.x, Y: d.y, W: w, H: h}, 90, x)
	fade(ui.Rect{X: d.x, Y: d.y, W: w, H: h}, 270, x)
}

// edgeGradient 从“近边”（At 0）往“远边”（At 1）衰减。近边靠角度挑：180 从上往下、0 从下往上、
// 90 从左往右、270 从右往左。stops 必须按 At 升序，倒着排会让 MyGo 画成一块平色。
func (d cinPaint) edgeGradient(r ui.Rect, stops []float32, angle float32) {
	list := make([]ui.GradientStop, len(stops))
	for i, a := range stops {
		list[i] = ui.GradientStop{At: float32(cinGlowStops[i]), Color: white.Alpha(a)}
	}
	d.p.FillGradient(r, ui.LinearGradient{Angle: angle, Stops: list}, 0)
}

// ---------------------------------------------------------------- 一句

func (d cinPaint) drawPlan(cache *cinCache, index int, t float64, env cinEnvelope) {
	plan := &cache.program.plans[index]
	art := d.artFor(cache, plan)
	switch {
	case art.hero != nil:
		d.drawHero(art.hero, t, env)
	case art.small != nil:
		d.drawSmall(art.small, t, env)
	case art.ribbon != nil:
		d.drawRibbon(art.ribbon, plan, t, env)
		d.ribbonFade(d.subtitleReserve(plan))
		d.vignette()
	}
	if art.bands != nil && env.bands > .002 {
		d.drawBands(art.bands, t, float32(env.bands))
	}
}

func (d cinPaint) artFor(cache *cinCache, plan *cinPlan) *cinArt {
	if a, ok := cache.arts[plan.index]; ok {
		return a
	}
	if len(cache.arts) >= 4 {
		for k := range cache.arts {
			if k < plan.index-1 || k > plan.index+1 {
				delete(cache.arts, k)
			}
		}
	}
	reserve := d.subtitleReserve(plan)
	art := &cinArt{bands: nil}
	switch plan.style {
	case cinHero:
		art.hero = d.buildHero(plan, reserve)
	case cinRibbon:
		art.ribbon = d.buildRibbon(plan, reserve)
	default:
		art.small = d.buildSmall(plan, reserve)
	}
	if plan.marquee != nil {
		art.bands = d.buildBands(plan.marquee)
	}
	cache.arts[plan.index] = art
	return art
}

// subtitleSize 是宿主副行这一行字的高度：和 stageSceneTranslation 的取法一致。
func (g cinGeometry) subtitleSize() float32 {
	return min(22, min(g.w, g.h)*.052)
}

// clearOf 是这句的跑马灯带给正文让出的高度；没有带子就不用让。
func (g cinGeometry) clearOf(plan *cinPlan) float32 {
	if plan.marquee == nil {
		return 0
	}
	return g.clear()
}

// subtitleReserve 是屏底要给宿主副行留的高度：带子让出的高度 + 副行一行字 + 一点间距。
func (d cinPaint) subtitleReserve(plan *cinPlan) float32 {
	if d.s.current < 0 || d.s.current >= len(d.s.lines) || strings.TrimSpace(d.s.lines[d.s.current].Translation) == "" {
		return 0
	}
	return d.geo.clearOf(plan) + d.geo.subtitleSize() + 6*d.geo.unit
}

// content 是正文能用的区域：屏去掉跑马灯带和宿主副行；四边带时两侧也让开。
func (d cinPaint) content(plan *cinPlan, reserve float32) ui.Rect {
	w, h := d.geo.w, d.geo.h
	clear := float32(0)
	side := float32(0)
	if plan.marquee != nil {
		clear = d.geo.clear()
		if plan.marquee.kind == cinFrame {
			side = clear
		}
	}
	top := clear
	bottom := max(top+1, h-max(clear, reserve))
	return ui.Rect{X: side, Y: top, W: max(1, w-2*side), H: max(1, bottom-top)}
}

// ---------------------------------------------------------------- 小字报

func (d cinPaint) buildSmall(plan *cinPlan, reserve float32) *cinSmallArt {
	w, h := d.geo.w, d.geo.h
	if len(plan.units) == 0 {
		return &cinSmallArt{}
	}
	layout := cinLayouts[plan.layout]
	clear := float32(0)
	side := float32(0)
	if plan.marquee != nil {
		clear = d.geo.clear()
		if plan.marquee.kind == cinFrame {
			side = clear
		}
	}
	padTop := max(layout.padY*h, clear)
	padBottom := max(layout.padY*h, max(clear, reserve))
	padSide := max(layout.padX*w, side)
	area := ui.Rect{X: padSide, Y: padTop, W: max(1, w-2*padSide), H: max(1, h-padTop-padBottom)}
	boxWidth := area.W * layout.maxWidthRatio
	font := cinClamp(float64(layout.fontScale*.14), .05, .26) * float64(h)
	font = math.Max(font, 20*float64(d.geo.unit))
	font = math.Min(font, float64(boxWidth/7))

	units := plan.units
	n := len(units)
	lang := d.lang()
	track := layout.trackEm
	blank := make([]bool, n)
	baseWidths := make([]float32, n)
	for i := range units {
		blank[i] = strings.TrimSpace(units[i].text) == ""
		baseWidths[i] = cinShape(units[i].text, ui.Font{Size: float32(font), Weight: 600, Lang: lang}).trackWidth(track)
	}
	reference := font
	widths := make([]float32, n)
	var pos []float32
	var boxW, boxH float32
	for attempt := 0; attempt < 12; attempt++ {
		k := float32(font / reference)
		for i := range widths {
			widths[i] = baseWidths[i] * k
		}
		pos, boxW, boxH = cinFlow(widths, blank, float32(font*float64(layout.lineHeight)), boxWidth)
		if boxH <= area.H && boxW <= boxWidth+.5 {
			break
		}
		font *= .88
	}
	fontPx := float32(font)
	rowHeight := fontPx * layout.lineHeight
	centerX := area.X + area.W/2
	switch layout.alignX {
	case -1:
		centerX = area.X + boxW/2
	case 1:
		centerX = area.X + area.W - boxW/2
	}
	top := area.Y + (area.H-boxH)/2
	switch layout.alignY {
	case -1:
		top = area.Y
	case 1:
		top = area.Y + area.H - boxH
	}
	left := centerX - boxW/2

	art := &cinSmallArt{units: units, texts: make([]*cinText, n), x: make([]float32, n), y: make([]float32, n), jitter: make([]float32, n)}
	for i := range units {
		if blank[i] || math.IsNaN(float64(pos[i*2])) {
			continue
		}
		t := cinShape(units[i].text, ui.Font{Size: fontPx, Weight: 600, Lang: lang})
		art.texts[i] = &t
		art.x[i] = left + pos[i*2]
		art.y[i] = top + pos[i*2+1] + (rowHeight-t.h)/2
		art.jitter[i] = float32(cinHash01(plan.seed, i, 7))
	}
	return art
}

func (d cinPaint) drawSmall(art *cinSmallArt, t float64, env cinEnvelope) {
	opacity := float32(env.opacity)
	scale := float32(env.scale)
	for i, unit := range art.units {
		text := art.texts[i]
		if text == nil {
			continue
		}
		p := float32(1)
		if !d.still {
			p = float32(cinUnitProgress(unit.start, unit.end, t, cinUnitEnter))
		}
		if p <= .002 {
			continue
		}
		cy := art.y[i] + text.h/2
		alpha := p * opacity
		ink := d.accent.Alpha(alpha)
		if p >= .999 && scale >= .999 {
			d.glowText(text, art.x[i], cy, alpha, ink)
			continue
		}
		dy := (1 - p) * (8 + art.jitter[i]*10) / 100 * text.h
		s := (.94 + .06*p) * scale
		d.p.Transform(ui.Scaling(s, s), d.x+art.x[i]+text.w/2, d.y+cy+dy, func() {
			d.glowText(text, art.x[i], cy+dy, alpha, ink)
		})
	}
}

// ---------------------------------------------------------------- 大字报

func (d cinPaint) buildHero(plan *cinPlan, reserve float32) *cinHeroArt {
	w, h := d.geo.w, d.geo.h
	box := d.content(plan, reserve)
	side := float64(0)
	if plan.marquee != nil && plan.marquee.kind == cinFrame {
		side = float64(d.geo.clear() * 2)
	}
	metrics := cinMetricsOf(float64(w), float64(h), float64(box.W), float64(box.H), side)
	chunks := cinHeroChunks(plan, metrics)
	clip := ui.Rect{X: d.x, Y: d.y, W: w, H: max(1, h-reserve)}
	minFont := 28 * d.geo.unit
	art := &cinHeroArt{centerX: box.X + box.W/2, centerY: box.Y + box.H/2, clip: clip}
	for _, chunk := range chunks {
		font := max(minFont, float32(chunk.font))
		fontArg := ui.Font{Size: font, Weight: 600, Italic: chunk.italic, Lang: d.lang()}
		text := cinShape(chunk.text, fontArg)
		fit := min(box.W*.98/max(1, text.w), box.H/max(1, text.h))
		if fit < 1 {
			font *= fit
			fontArg.Size = font
			text = cinShape(chunk.text, fontArg)
		}
		ca := cinChunkArt{chunk: chunk, text: text, fontPx: font}
		if !d.still && chunk.motion.tracks() {
			runes := []rune(chunk.text)
			ca.glyphs = make([]cinText, len(runes))
			ca.glyphX = make([]float32, len(runes))
			cursor := float32(0)
			for i, r := range runes {
				g := cinShape(string(r), fontArg)
				ca.glyphs[i] = g
				ca.glyphX[i] = cursor
				if g.w <= 0 && r == ' ' {
					cursor += font * .28
				} else {
					cursor += g.w
				}
			}
			ca.glyphW = cursor
		}
		art.chunks = append(art.chunks, ca)
	}
	if d.still && len(art.chunks) > 0 {
		art.buildPoster(box)
	}
	return art
}

// buildPoster 是减少动效时的整句海报：所有块按各自字号排成一整行（放不下就一起缩）。
func (art *cinHeroArt) buildPoster(box ui.Rect) {
	n := len(art.chunks)
	widths := make([]float32, n*2-1)
	blank := make([]bool, n*2-1)
	gap := float32(4)
	rowHeight := float32(0)
	for i, c := range art.chunks {
		if i == 0 {
			gap = max(1, c.fontPx*.3)
		}
		gap = min(gap, max(1, c.fontPx*.3))
		rowHeight = max(rowHeight, c.text.h)
	}
	for i := range blank {
		blank[i] = i%2 == 1
	}
	k := float32(1)
	var boxW, boxH float32
	for attempt := 0; attempt < 24; attempt++ {
		for i := 0; i < n; i++ {
			widths[i*2] = art.chunks[i].text.w * k
			if i+1 < n {
				widths[i*2+1] = gap * k
			}
		}
		_, boxW, boxH = cinFlow(widths, blank, rowHeight*k, box.W)
		if boxW <= box.W+.5 && boxH <= box.H {
			break
		}
		k *= .92
	}
	art.poster = make([]cinText, n)
	art.posterAt = make([]float32, n*2)
	for i, c := range art.chunks {
		if k >= .999 {
			art.poster[i] = c.text
		} else {
			art.poster[i] = cinShape(c.chunk.text, ui.Font{Size: c.fontPx * k, Weight: 600, Italic: c.chunk.italic, Lang: ""})
		}
		widths[i*2] = art.poster[i].w
		if i+1 < n {
			widths[i*2+1] = gap * k
		}
	}
	row := rowHeight * k
	pos, totalW, totalH := cinFlow(widths, blank, row, box.W)
	left := box.X + box.W/2 - totalW/2
	top := box.Y + box.H/2 - totalH/2
	for i := range art.chunks {
		art.posterAt[i*2] = left + pos[i*4]
		art.posterAt[i*2+1] = top + pos[i*4+1] + (row-art.poster[i].h)/2
	}
}

func (d cinPaint) drawHero(art *cinHeroArt, t float64, env cinEnvelope) {
	if len(art.chunks) == 0 {
		return
	}
	opacity := float32(env.opacity)
	if art.poster != nil {
		for i := range art.poster {
			text := &art.poster[i]
			cy := art.posterAt[i*2+1] + text.h/2
			d.glowText(text, art.posterAt[i*2], cy, opacity, d.accent.Alpha(opacity))
		}
		return
	}
	scale := float32(env.scale)
	d.p.Clip(art.clip, 0, func() {
		for i := range art.chunks {
			c := &art.chunks[i]
			ok, look := cinChunkLook(c.chunk, t)
			if !ok {
				continue
			}
			cx := art.centerX + float32(look.driftX)/100*d.geo.w
			cy := art.centerY + float32(look.driftY)/100*d.geo.h
			alpha := float32(look.opacity) * opacity
			if alpha <= .002 {
				continue
			}
			ink := d.accent.Alpha(alpha)
			s := float32(look.scale) * scale
			d.p.Transform(ui.Scaling(s, s), d.x+cx, d.y+cy, func() {
				d.drawChunk(c, cx, cy, float32(look.trackEm), alpha, ink, float32(look.fill))
			})
		}
	})
}

func (d cinPaint) drawChunk(c *cinChunkArt, cx, cy, trackEm, alpha float32, ink ui.Color, fill float32) {
	if len(c.glyphs) > 0 {
		track := trackEm * c.fontPx
		left := cx - (c.glyphW+track*float32(len(c.glyphs)-1))/2
		for i := range c.glyphs {
			x := left + c.glyphX[i] + track*float32(i)
			d.glowText(&c.glyphs[i], x, cy, alpha, ink)
		}
		return
	}
	text := &c.text
	track := trackEm * c.fontPx
	left := cx - text.trackWidth(track)/2
	if !c.chunk.fill {
		d.glowText(text, left, cy, alpha, ink)
		return
	}
	// 描边填色：没扫到的一侧画成三成透明的实心字（细描边的近似），扫到的一侧画实心字。
	d.glowText(text, left, cy, alpha*.32, ink.Alpha(alpha*.32))
	pad := text.h * .3
	top := cy - text.h/2
	if c.chunk.fillVertical {
		split := top + text.h*fill
		d.p.Clip(ui.Rect{X: d.x + left - pad, Y: d.y + top - pad, W: text.w + 2*pad, H: max(0, split-top+pad)}, 0, func() {
			d.glowText(text, left, cy, alpha, ink)
		})
		return
	}
	split := left + text.w*fill
	d.p.Clip(ui.Rect{X: d.x + left - pad, Y: d.y + top - pad, W: max(0, split-left+pad), H: text.h + 2*pad}, 0, func() {
		d.glowText(text, left, cy, alpha, ink)
	})
}

// ---------------------------------------------------------------- 斜切丝带

func (d cinPaint) buildRibbon(plan *cinPlan, reserve float32) *cinRibbonArt {
	w, h := d.geo.w, d.geo.h
	area := ui.Rect{X: 0, Y: 0, W: w, H: max(1, h-reserve)}
	strips := cinRibbonStrips(plan.seed, cinRibbonCount, plan.line.Text, plan.line.Translation)
	span := plan.end - plan.start
	clampPx := cinRibbonDriftClamp(strips, span, float64(area.H))
	u := float64(d.geo.unit)
	glowExtent := float32(36 * u)
	dropExtent := float32(30 * u)
	art := &cinRibbonArt{area: area, clampPx: clampPx, spanSec: span}
	glowStops := cinShadowStops(.6, 24*u, 0, float64(glowExtent))
	dropAbove := cinShadowStops(.45, 16*u, -6*u, float64(dropExtent))
	dropBelow := cinShadowStops(.45, 16*u, 6*u, float64(dropExtent))
	// 原版从下往上叠：先画靠上的条，再让下面的盖住。
	sorted := append([]cinStrip(nil), strips...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].topPct > sorted[j-1].topPct; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for _, strip := range sorted {
		thickness := float32(strip.thicknessPct * float64(area.H) / 100)
		cy := float32(strip.topPct*float64(area.H)/100) + thickness/2
		length := float32(cinRibbonLength(float64(area.W), float64(area.H), strip, clampPx))
		font := min(thickness, max(float32(10*u), float32(strip.fontPct()*float64(area.H)/100)))
		text := cinShape(strip.text+"　", ui.Font{Size: font, Weight: 800, Lang: d.lang()})
		art.strips = append(art.strips, cinStripArt{
			strip: strip, cx: area.X + area.W/2, cy: cy, length: length, thickness: thickness,
			text: text, textUnit: max(1, text.trackWidth(cinRibbonTrackEm*text.h)),
			shade:          cinShadeGradient(strip),
			glowExtent:     glowExtent,
			dropExtent:     dropExtent,
			glowStops:      glowStops,
			dropAboveStops: dropAbove,
			dropBelowStops: dropBelow,
		})
	}
	return art
}

// cinShadeGradient 是 CSS linear-gradient 的斜向光泽：亮带白色、暗带黑色。
func cinShadeGradient(strip cinStrip) ui.LinearGradient {
	stops := cinShadeStops(strip)
	light := float32(min(.55, strip.shadeStrength*1.4))
	dark := float32(min(.55, strip.shadeStrength*.5))
	colors := []ui.Color{
		white.Alpha(0), white.Alpha(light), white.Alpha(0),
		ui.RGB(0, 0, 0).Alpha(0), ui.RGB(0, 0, 0).Alpha(dark), ui.RGB(0, 0, 0).Alpha(0),
	}
	out := make([]ui.GradientStop, len(stops))
	for i := range stops {
		out[i] = ui.GradientStop{At: float32(stops[i]), Color: colors[i]}
	}
	return ui.LinearGradient{Angle: float32(strip.shadeAngle), Stops: out}
}

func (d cinPaint) drawRibbon(art *cinRibbonArt, plan *cinPlan, t float64, env cinEnvelope) {
	elapsed := t - plan.start
	if d.still {
		elapsed = 0
	}
	w := float64(art.area.W)
	h := float64(art.area.H)
	scroll := float32(cinRibbonScroll(elapsed, w, art.spanSec))
	ink := d.base() // 带身是强调色，字用屏面底色
	opacity := float32(env.opacity)
	d.p.Clip(ui.Rect{X: d.x, Y: d.y, W: art.area.W, H: art.area.H}, 0, func() {
		for i := range art.strips {
			s := &art.strips[i]
			drift := float32(cinRibbonDrift(s.strip, elapsed, h, art.clampPx))
			half := s.thickness / 2
			l := s.length / 2
			cx, cy := d.x+s.cx, d.y+s.cy+drift
			d.p.Transform(ui.Rotation(float32(s.strip.angle)), cx, cy, func() {
				// 带下沿一条 1dp 的白线，让丝带有厚度感（原版 lineH = unit）。
				d.p.Fill(ui.Rect{X: cx - l, Y: cy + half, W: s.length, H: d.geo.unit}, white.Alpha(.3*opacity), 0)
				// 外发光与投影：从带边往外按高斯尾巴衰减。
				d.stripGlow(ui.Rect{X: cx - l, Y: cy - half - s.dropExtent, W: s.length, H: s.dropExtent}, s.dropAboveStops, true, opacity, true)
				d.stripGlow(ui.Rect{X: cx - l, Y: cy + half, W: s.length, H: s.dropExtent}, s.dropBelowStops, false, opacity, true)
				d.stripGlow(ui.Rect{X: cx - l, Y: cy - half - s.glowExtent, W: s.length, H: s.glowExtent}, s.glowStops, true, opacity, false)
				d.stripGlow(ui.Rect{X: cx - l, Y: cy + half, W: s.length, H: s.glowExtent}, s.glowStops, false, opacity, false)
				d.p.Fill(ui.Rect{X: cx - l, Y: cy - half, W: s.length, H: s.thickness}, d.accent.Alpha(opacity), 0)
				d.p.FillGradient(ui.Rect{X: cx - l, Y: cy - half, W: s.length, H: s.thickness}, s.shade, 0)
				d.p.Clip(ui.Rect{X: cx - l, Y: cy - half, W: s.length, H: s.thickness}, 0, func() {
					unit := s.textUnit
					x := cx - l - scroll
					for x < cx+l {
						d.text(s.text, x, cy, cinRibbonTrackEm*s.text.h, ink.Alpha(opacity))
						x += unit
					}
				})
			})
		}
	})
}

// stripGlow 画带子一侧或屏面一侧的衰减渐变；above 为真时带边在矩形下沿。
func (d cinPaint) stripGlow(r ui.Rect, alphas []float32, above bool, alpha float32, dark bool) {
	ink := d.accent
	if dark {
		ink = ui.RGB(0, 0, 0)
	}
	list := make([]ui.GradientStop, len(alphas))
	for i, a := range alphas {
		list[i] = ui.GradientStop{At: float32(cinGlowStops[i]), Color: ink.Alpha(a * alpha)}
	}
	// above 时近边（At 0）在矩形下沿，所以渐变线从下往上。
	angle := float32(180)
	if above {
		angle = 0
	}
	d.p.FillGradient(r, ui.LinearGradient{Angle: angle, Stops: list}, 0)
}

// ---------------------------------------------------------------- 跑马灯带

func (d cinPaint) buildBands(m *cinMarquee) *cinBandsArt {
	bandFont := max(12*d.geo.unit, d.geo.band*cinBandFontRatio)
	art := &cinBandsArt{marquee: m}
	if m.kind == cinBands {
		if m.top != "" {
			t := cinShape(m.top+"　", ui.Font{Size: bandFont, Weight: 600, Lang: d.lang()})
			art.top = &t
		}
		if m.bottom != "" {
			t := cinShape(m.bottom+"　", ui.Font{Size: bandFont, Weight: 600, Lang: d.lang()})
			art.bottom = &t
		}
		return art
	}
	if m.ring == "" {
		return art
	}
	font := ui.Font{Size: bandFont, Weight: 600, Lang: d.lang()}
	one := m.ring + "　"
	oneText := cinShape(one, font)
	period := oneText.trackWidth(cinBandTrack * bandFont)
	radius := float64(min(d.geo.w, d.geo.h) * .06)
	lap := cinRingLap(float64(d.geo.w-2*d.geo.band), float64(d.geo.h-2*d.geo.band), radius)
	copies, _ := cinRingCopies(lap, float64(period), len([]rune(one)))
	source := strings.Repeat(one, copies)
	glyphs := ui.Shape(source, font)
	for i := range glyphs {
		glyphs[i].X += cinBandTrack * bandFont * float32(i)
	}
	art.ring = &cinRingArt{
		source: source, glyphs: glyphs, unit: float32(period), orbit: m.orbit,
		font: font, perRune: map[rune]cinText{},
	}
	return art
}

func (d cinPaint) drawBands(art *cinBandsArt, t float64, alpha float32) {
	m := art.marquee
	w, h := d.geo.w, d.geo.h
	band := d.geo.band
	if m.kind == cinFrame {
		if art.ring != nil {
			d.drawRing(art.ring, t, alpha)
			d.ringEdges(alpha)
		}
		return
	}
	unit := float64(0)
	if art.top != nil {
		unit = float64(art.top.trackWidth(cinBandTrack * art.top.h))
	}
	shift := 0.0
	if !d.still {
		shift = cinBandShift(t, m.screensPerSec, 1, float64(w), unit)
	}
	if art.top != nil {
		d.bandText(*art.top, ui.Rect{X: 0, Y: 0, W: w, H: band}, cinBandSlot(shift, m.orbit, false, m.mirror), false, alpha)
	}
	if art.bottom != nil {
		d.bandText(*art.bottom, ui.Rect{X: 0, Y: h - band, W: w, H: band}, cinBandSlot(shift, m.orbit, true, m.mirror), m.mirror, alpha)
	}
	d.bandEdges(alpha)
}

// bandText 把“字　”一份接一份铺满整条，按 slot（以份为单位）平移；镜像的下带整条转 180°。
func (d cinPaint) bandText(t cinText, rect ui.Rect, slot float64, flipped bool, alpha float32) {
	unit := t.trackWidth(cinBandTrack * t.h)
	if unit <= 1 {
		return
	}
	cy := rect.Y + rect.H/2
	ink := d.accent.Alpha(alpha)
	d.p.Clip(ui.Rect{X: d.x + rect.X, Y: d.y + rect.Y, W: rect.W, H: rect.H}, 0, func() {
		draw := func() {
			x := rect.X + float32(cinFrac(slot))*unit - unit
			for x < rect.W {
				d.text(t, x, cy, cinBandTrack*t.h, ink)
				x += unit
			}
		}
		if flipped {
			d.p.Transform(ui.Rotation(180), d.x+rect.X+rect.W/2, d.y+cy, draw)
			return
		}
		draw()
	})
}

// bandEdges 画双带的边线辉光：只画朝屏心的那条长边。
func (d cinPaint) bandEdges(alpha float32) {
	glow := cinEdgeGlowOf(float64(d.geo.band))
	e := glow.extent
	if e <= .5 {
		return
	}
	w, h, band := d.geo.w, d.geo.h, d.geo.band
	top, bottom := band, h-band
	// 近边是朝屏心的那条带边：上带的下沿、下带的上沿。
	d.edgeGlow(ui.Rect{X: d.x, Y: d.y + top - e, W: w, H: e}, glow.band, 0, alpha)
	d.edgeGlow(ui.Rect{X: d.x, Y: d.y + top, W: w, H: e}, glow.screen, 180, alpha)
	d.edgeGlow(ui.Rect{X: d.x, Y: d.y + bottom, W: w, H: e}, glow.band, 180, alpha)
	d.edgeGlow(ui.Rect{X: d.x, Y: d.y + bottom - e, W: w, H: e}, glow.screen, 0, alpha)
}

// edgeGlow 是一条带边辉光：alphas 按 cinGlowStops 取样，近边（At 0）由角度决定。
func (d cinPaint) edgeGlow(r ui.Rect, alphas []float32, angle, alpha float32) {
	list := make([]ui.GradientStop, len(alphas))
	for i, a := range alphas {
		list[i] = ui.GradientStop{At: float32(cinGlowStops[i]), Color: d.accent.Alpha(a * alpha)}
	}
	d.p.FillGradient(r, ui.LinearGradient{Angle: angle, Stops: list}, 0)
}

// ringEdges 是四边带的边线辉光。原版用梯形把四个角按对角线分开，这里用矩形近似，角上会略微叠亮一点。
func (d cinPaint) ringEdges(alpha float32) {
	glow := cinEdgeGlowOf(float64(d.geo.band))
	e := glow.extent
	if e <= .5 {
		return
	}
	w, h, b := d.geo.w, d.geo.h, d.geo.band
	l, tp, r, btm := b, b, w-b, h-b
	if r-l < 2*e || btm-tp < 2*e {
		return
	}
	// 往屏心
	d.edgeGlow(ui.Rect{X: d.x + l, Y: d.y + tp, W: r - l, H: e}, glow.screen, 180, alpha)
	d.edgeGlow(ui.Rect{X: d.x + l, Y: d.y + btm - e, W: r - l, H: e}, glow.screen, 0, alpha)
	d.edgeGlow(ui.Rect{X: d.x + l, Y: d.y + tp, W: e, H: btm - tp}, glow.screen, 90, alpha)
	d.edgeGlow(ui.Rect{X: d.x + r - e, Y: d.y + tp, W: e, H: btm - tp}, glow.screen, 270, alpha)
	// 往带子
	d.edgeGlow(ui.Rect{X: d.x + l - e, Y: d.y + tp - e, W: r - l + 2*e, H: e}, glow.band, 0, alpha)
	d.edgeGlow(ui.Rect{X: d.x + l - e, Y: d.y + btm, W: r - l + 2*e, H: e}, glow.band, 180, alpha)
	d.edgeGlow(ui.Rect{X: d.x + l - e, Y: d.y + tp, W: e, H: btm - tp}, glow.band, 270, alpha)
	d.edgeGlow(ui.Rect{X: d.x + r, Y: d.y + tp, W: e, H: btm - tp}, glow.band, 90, alpha)
}

// drawRing 把一段重复文字逐字摆在圆角矩形上，每个字按切线旋转。
func (d cinPaint) drawRing(ring *cinRingArt, t float64, alpha float32) {
	// 文字路径走在带厚的中线上（原版 inset 半个带厚）。
	g := d.geo
	inset := g.band / 2
	l, tp := inset, inset
	r, b := g.w-inset, g.h-inset
	radius := min(g.band*.4, max(0, min(r-l, b-tp)/2-1))
	lap := cinRingLap(float64(r-l), float64(b-tp), float64(radius))
	if lap <= 0 || ring.unit <= 1 {
		return
	}
	shift := 0.0
	if !d.still {
		shift = cinBandShift(t, m0Speed, 1, float64(g.w), float64(ring.unit))
	}
	offset := float64(cinRingOffset(shift, ring.orbit, float64(ring.unit)))
	ink := d.accent.Alpha(alpha)
	src := []rune(ring.source)
	for _, glyph := range ring.glyphs {
		dist := cinFrac((offset + float64(glyph.X+glyph.Advance/2)) / lap)
		px, py, angle := cinRingPoint(float64(l+radius), float64(tp+radius), float64(r-l), float64(b-tp), float64(radius), dist*lap)
		ch := ' '
		if glyph.Cluster >= 0 && glyph.Cluster < len(src) {
			ch = src[glyph.Cluster]
		}
		gt, ok := ring.perRune[ch]
		if !ok {
			gt = cinShape(string(ch), ring.font)
			ring.perRune[ch] = gt
		}
		if gt.w <= 0 {
			continue
		}
		d.p.Transform(ui.Rotation(float32(angle)), d.x+float32(px), d.y+float32(py), func() {
			d.text(gt, float32(px)-gt.w/2, float32(py), 0, ink)
		})
	}
}

// m0Speed 是四边环的基准带速。
const m0Speed = .12

// cinRingPoint 在圆角矩形上按周长距离取点与切线角度（度，顺时针；0 是向右）。
func cinRingPoint(cx, cy, w, h, r, dist float64) (x, y, angle float64) {
	if r <= 0 {
		r = .001
	}
	arc := math.Pi * r / 2
	topLen := max(0, w-2*r)
	sideLen := max(0, h-2*r)
	l, tp := cx-r, cy-r
	rgt, btm := l+w, tp+h
	deg := math.Pi / 180
	// 八段：上边、右上角、右边、右下角、下边、左下角、左边、左上角。
	lengths := [8]float64{topLen, arc, sideLen, arc, topLen, arc, sideLen, arc}
	total := topLen*2 + sideLen*2 + arc*4
	if total <= 0 {
		return cx, cy, 0
	}
	d := math.Mod(math.Mod(dist, total)+total, total)
	seg := 0
	for seg < 7 && d > lengths[seg] {
		d -= lengths[seg]
		seg++
	}
	u := 0.0
	if lengths[seg] > 0 {
		u = cinClamp(d/lengths[seg], 0, 1)
	}
	switch seg {
	case 0:
		return l + r + u*topLen, tp, 0
	case 1:
		a := (-90 + 90*u) * deg
		return rgt - r + r*math.Cos(a), tp + r + r*math.Sin(a), 90 * u
	case 2:
		return rgt, tp + r + u*sideLen, 90
	case 3:
		a := 90 * u * deg
		return rgt - r + r*math.Cos(a), btm - r + r*math.Sin(a), 90 + 90*u
	case 4:
		return rgt - r - u*topLen, btm, 180
	case 5:
		a := (90 + 90*u) * deg
		return l + r + r*math.Cos(a), btm - r + r*math.Sin(a), 180 + 90*u
	case 6:
		return l, btm - r - u*sideLen, 270
	default:
		a := (180 + 90*u) * deg
		return l + r + r*math.Cos(a), tp + r + r*math.Sin(a), 270 + 90*u
	}
}

// ---------------------------------------------------------------- 文字绘制

// text 画一段文字，left 是左缘、cy 是垂直中心，track 是额外的字距（px）。
func (d cinPaint) text(t cinText, left, cy, track float32, ink ui.Color) {
	if len(t.glyphs) == 0 || ink.A == 0 {
		return
	}
	glyphs := t.glyphs
	if track != 0 {
		glyphs = make([]ui.Glyph, len(t.glyphs))
		copy(glyphs, t.glyphs)
		for i := range glyphs {
			glyphs[i].X += track * float32(i)
		}
	}
	d.p.Glyphs(glyphs, d.x+left, d.y+cy+(t.ascent-t.descent)/2, ink)
}

// glowText 画同色辉光 + 实心字。原版是 CSS text-shadow 0 0 30px；MyGo 的文字阴影
// 是真模糊，正好对上——阴影用同色、模糊 30（按屏折算），铺在字下，实心字按 ink 的
// 透明度盖上去。旧写法把整字往外错位叠 16 遍，放大后是一圈硬锯齿，像描边。
func (d cinPaint) glowText(t *cinText, left, cy, alpha float32, ink ui.Color) {
	if t == nil || len(t.glyphs) == 0 || ink.A == 0 || alpha <= .01 || d.s.preview {
		return
	}
	// 原版把阴影浓度量化成档缓存；这里同样按档缓存排版，免得逐帧重新塑形。
	level := int(math.Round(float64(cinClamp(float64(alpha), 0, 1)) * cinGlowLevels))
	if level <= 0 {
		return
	}
	layout := t.glowLayout(float32(cinGlowRatio)*d.geo.unit, level, d.accent)
	_, h := layout.Size()
	d.p.TextLayout(layout, d.x+left, d.y+cy-h/2, ink)
}

// cinGlowLevels 是辉光浓度的档数，和原版一样够用就好。
const cinGlowLevels = 10

// cinGlow 是一段文字各（偏移, 模糊半径, 浓度）档的阴影排版，第一次用到才塑形。
type cinGlow struct {
	accent  ui.Color
	layouts map[[4]int]*ui.TextLayout
}

// glowLayoutFor 取单元这一档的阴影排版，并把新建的缓存挂回单元的 glow 上复用。
func glowLayoutFor(src string, font ui.Font, glow **cinGlow, blur float32, level int, accent ui.Color) *ui.TextLayout {
	return glowLayoutAtFor(src, font, glow, 0, 0, blur, level, accent)
}

// glowLayoutAtFor 同上，但阴影带偏移。
func glowLayoutAtFor(src string, font ui.Font, glow **cinGlow, dx, dy, blur float32, level int, color ui.Color) *ui.TextLayout {
	t := cinText{src: src, font: font, glow: *glow}
	layout := t.glowLayoutAt(dx, dy, blur, level, color)
	*glow = t.glow
	return layout
}

// glowLayout 取这一档的排版：Span 只带阴影，填充色绘制时再传，
// 淡入淡出才不会跟着阴影一起被量化。模糊半径按整数像素归档，避免逐帧重新塑形。
func (t *cinText) glowLayout(blur float32, level int, accent ui.Color) *ui.TextLayout {
	return t.glowLayoutAt(0, 0, blur, level, accent)
}

// glowLayoutAt 和 glowLayout 一样，但阴影可以带偏移，供掉落类的深投影用。
func (t *cinText) glowLayoutAt(dx, dy, blur float32, level int, accent ui.Color) *ui.TextLayout {
	if t.glow == nil {
		t.glow = &cinGlow{}
	}
	g := t.glow
	// 换封面会换主色，档位缓存得跟着重建。
	if g.accent != accent {
		*g = cinGlow{accent: accent}
	}
	if g.layouts == nil {
		g.layouts = map[[4]int]*ui.TextLayout{}
	}
	key := [4]int{int(math.Round(float64(dx))), int(math.Round(float64(dy))),
		int(math.Round(float64(blur))), level}
	if layout := g.layouts[key]; layout != nil {
		return layout
	}
	shadow := ui.NewTextShadow(float32(key[0]), float32(key[1]), float32(key[2]),
		accent.Alpha(float32(level)/cinGlowLevels))
	layout := ui.ShapeRichText([]ui.Span{{Text: t.src, Shadow: shadow}}, t.font, 0)
	g.layouts[key] = layout
	return layout
}

// lang 是舞台当前的排版语言。
func (d cinPaint) lang() string { return lyrics.Lang(d.s.lines) }
