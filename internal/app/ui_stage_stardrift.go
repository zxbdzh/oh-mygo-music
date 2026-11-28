package app

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// 星潮·银河照 myMusic 的 stardrift 移植（原模组 stardrift 1.38.0，作者 w0w1w2，MIT）：
// 整页是一片不断更替的星海，中央一团很淡的星云，流星不时斜穿画面；当前句的每个字出现在随机位置，
// 先是一颗小光点，放大成字、颜色从强调色变成文字色，正在唱的字罩一圈柔光，唱完停留后变回强调色缩小消失。
//
// 移植取舍：星星按“星位 × 轮次”算出位置和节奏（不存历史），每句落点用该句自己的随机序列，
// 于是同一播放位置永远同一帧；流星按固定排班算位置，也不用逐帧积分。
const (
	sdZoomS       = .5
	sdBaseFont    = 64
	sdMeanStarLife = 3.2
	sdStillT      = 12.5
	sdStarColors  = 3
	sdAlphaLevels = 10
	sdPulseMax    = 1.2
	sdRingScale   = 1.45
	sdRingInner   = .05
	sdPlaceTries  = 12
	sdMaxTrailGap = 3
)

// sdRandom 是一串稳定的伪随机数：同一个种子永远给同一串。
type sdRandom struct {
	seed uint32
	n    uint32
}

func sdNewRandom(seed uint32) *sdRandom { return &sdRandom{seed: seed} }

func (r *sdRandom) next() float32 {
	r.n++
	h := r.seed ^ (r.n * 2654435761)
	h ^= h >> 16
	h *= 0x7feb352d
	h ^= h >> 15
	h *= 0x846ca68b
	h ^= h >> 16
	return float32(h>>8) / float32(1<<24)
}

// sdHash01 是不带状态的取数：同一个 (种子, 盐) 永远同一个 [0, 1)。
func sdHash01(seed uint32, salt int) float32 {
	h := seed ^ uint32(salt)*0x9e3779b9
	h ^= h >> 16
	h *= 0x7feb352d
	h ^= h >> 15
	h *= 0x846ca68b
	h ^= h >> 16
	return float32(h>>8) / float32(1<<24)
}

// sdStarCount 是稳定时屏幕上有多少颗星（原版按星的寿命与补星速率估算）。
func sdStarCount(density float32) int {
	perSecond := min(.5, max(4, density)/180) * 60
	steady := min(density*1.6, perSecond*sdMeanStarLife)
	alive := max(max(10, density*.6), steady)
	return max(1, int(math.Round(float64(alive))))
}

// sdStarEnvelope 是星星一生里的亮度包络：前 15% 淡入，后 15% 淡出。
func sdStarEnvelope(life float32) float32 {
	switch {
	case life < 0 || life >= 1:
		return 0
	case life < .15:
		return life / .15
	case life > .85:
		return (1 - life) / .15
	}
	return 1
}

type sdStar struct {
	x, y, size, alpha, tint float32
	level                   int
}

// sdStars 算出 t 秒时所有星的位置、大小、亮度和色调；纯函数，不存历史。
func sdStars(seed uint32, count int, w, h, t float32) []sdStar {
	out := make([]sdStar, 0, count)
	for i := 0; i < count; i++ {
		lifeSeed, offset := sdHash01(seed, i*3+1), sdHash01(seed, i*3+2)
		lifetime := 1.6 + lifeSeed*3.2
		// 每个星位按自己的寿命无限循环：t 加上自己的相位，落在第几轮、一轮的哪里都是算出来的。
		pos := (t + offset*lifetime) / lifetime
		cycle := int(math.Floor(float64(pos)))
		env := sdStarEnvelope(pos - float32(cycle))
		if env <= 0 {
			continue
		}
		r := sdNewRandom(seed ^ uint32(i)*0x61c88647 ^ uint32(cycle)*0x2545f491)
		var x, y float32
		if r.next() < .4 {
			// 四成生在左上到右下的斜向银河带里
			u := r.next()
			x = w*(.15+.7*u) + (r.next()-.5)*w*.3
			y = h*(.25+.5*u) + (r.next()-.5)*h*.26
		} else {
			x, y = r.next()*w, r.next()*h
		}
		size := (.6 + float32(math.Pow(float64(r.next()), 1.8))*2.4)
		speed := .25 + r.next()
		phase := r.next() * 6.2831853
		tint := r.next()
		breathe := .5 + .5*float32(math.Sin(float64(speed*t*1.2+phase)))
		alpha := min(1, (.15+.85*breathe)*env)
		level := int(math.Round(float64(alpha * sdAlphaLevels)))
		if level <= 0 {
			continue
		}
		out = append(out, sdStar{
			x: x, y: y, size: size * (.6 + .8*env) * (.85 + .3*breathe),
			alpha: alpha, tint: tint, level: level,
		})
	}
	return out
}

// sdSparkle 往 path 里加一颗四角星（8 个顶点，尖角 size、凹角 0.45 倍，第一个尖朝上）。
func sdSparkle(path *ui.Path, x, y, size float32) {
	inner := size * .45
	for i := 0; i < 8; i++ {
		r := size
		if i%2 == 1 {
			r = inner
		}
		a := math.Pi/4*float64(i) - math.Pi/2
		px, py := x+r*float32(math.Cos(a)), y+r*float32(math.Sin(a))
		if i == 0 {
			path.MoveTo(px, py)
		} else {
			path.LineTo(px, py)
		}
	}
	path.Close()
}

// ---------------------------------------------------------------- 流星

type sdMeteor struct {
	x, y, vx, vy, length, width, tint float32
}

// sdMeteorAt 是这一帧的流星：按固定排班算位置，屏外时没有。数量固定 1 颗，周期比穿屏时间稍长。
func sdMeteorAt(seed uint32, w, h, unit, t float32) (sdMeteor, bool) {
	cross := (float32(math.Hypot(float64(w), float64(h))) + 600*unit) / (600 * unit)
	period := cross * 1.4
	n := int(math.Floor(float64(t / period)))
	local := t - float32(n)*period
	if local > cross {
		return sdMeteor{}, false
	}
	r := sdNewRandom(seed ^ uint32(n)*0x3e7e0a11)
	var x, y float32
	switch int(r.next() * 4) {
	case 0:
		x, y = r.next()*(w+240*unit)-120*unit, -40*unit-r.next()*90*unit
	case 1:
		x, y = w+40*unit+r.next()*90*unit, r.next()*h
	case 2:
		x, y = r.next()*(w+240*unit)-120*unit, h+40*unit+r.next()*90*unit
	default:
		x, y = -40*unit-r.next()*90*unit, r.next()*h
	}
	angle := float32(math.Atan2(float64(h/2-y), float64(w/2-x))) + (r.next()-.5)*.9
	speed := (420 + r.next()*360) * unit
	return sdMeteor{
		x: x, y: y,
		vx: float32(math.Cos(float64(angle))) * speed, vy: float32(math.Sin(float64(angle))) * speed,
		length: (110 + r.next()*90) * unit, width: (1.6 + r.next()*1.4) * unit,
		tint: r.next(),
	}, true
}

// ---------------------------------------------------------------- 字的生命周期

type sdLook struct {
	scale, alpha, primary, ring, glow float32
}

// sdDriftLook 是字的生命周期：光点放大成字 → 唱和停留 → 变回强调色缩小消失。
func sdDriftLook(local, appear, dur, hold, fade, pulse float32) (sdLook, bool) {
	holdEnd := appear + dur + hold
	var out sdLook
	if local < 0 || local > holdEnd+fade {
		return out, false
	}
	easeOut := func(k float32) float32 {
		u := 1 - k
		return 1 - u*u*u
	}
	switch {
	case local < appear:
		k := clamp01(local / appear)
		if k <= 0 {
			return out, false
		}
		e := easeOut(k)
		out = sdLook{.12 + .88*e, k, e, .12 + .88*e, 24 * k * pulse}
	case local < holdEnd:
		ring := float32(1)
		if local >= appear+dur {
			ring = 1 - clamp01((local-appear-dur)/max(.2, appear))
		}
		out = sdLook{1, min(1, .8+.2*pulse), 1, ring, 18 * pulse}
	default:
		k := 1 - clamp01((local-holdEnd)/fade)
		e := k * k * k
		out = sdLook{.12 + .88*e, k, e, 1 - clamp01((local-appear-dur)/max(.2, appear)), 18 * k * pulse}
	}
	return out, true
}

// ---------------------------------------------------------------- 落点

// sdPlace 给 count 个字在 area 里挑随机落点，彼此（以及与 avoid）尽量相距 minDist。
func sdPlace(count int, area ui.Rect, minDist float32, r *sdRandom, avoid []ui.Point) []ui.Point {
	placed := make([]ui.Point, 0, count)
	nearest := func(p ui.Point, others []ui.Point) float32 {
		d := float32(math.MaxFloat32)
		for _, o := range others {
			d = min(d, float32(math.Hypot(float64(o.X-p.X), float64(o.Y-p.Y))))
		}
		return d
	}
	for i := 0; i < count; i++ {
		best, bestDist := ui.Point{}, float32(-1)
		for k := 0; k < sdPlaceTries; k++ {
			p := ui.Point{X: area.X + r.next()*area.W, Y: area.Y + r.next()*area.H}
			d := min(nearest(p, placed), nearest(p, avoid))
			if d > bestDist {
				best, bestDist = p, d
			}
			if d >= minDist {
				break
			}
		}
		placed = append(placed, best)
	}
	return placed
}

// ---------------------------------------------------------------- 排版

type sdRune struct {
	char       string
	start, end float32
	x, y       float32
	punct      bool
	latin      bool
}

// sdTimings 是这一句的可见字与起止秒数；行尾字错位三秒以上时把它挪到前一字后 0.1 秒。
func sdTimings(line *lyrics.Line) []sdRune {
	text := strings.TrimSpace(line.Text)
	runes := []rune(text)
	starts, ends := colorStageTimings(line, text, len(runes))
	out := make([]sdRune, 0, len(runes))
	for i, r := range runes {
		if r == ' ' || r == '\t' || r == '　' {
			continue
		}
		out = append(out, sdRune{
			char: string(r),
			start: float32(starts[i].Seconds()), end: float32(ends[i].Seconds()),
			punct: sdPunct(r), latin: sdLatin(r),
		})
	}
	if n := len(out); n >= 2 {
		last, prev := &out[n-1], &out[n-2]
		gap := last.start - prev.end
		span := float32(colorStageEnd(line).Seconds() - line.At.Seconds())
		if gap > sdMaxTrailGap && gap > .3*max(.001, span) {
			shift := gap - .1
			last.start -= shift
			last.end -= shift
		}
	}
	return out
}

// sdPunct 只认真正的句读标点，免得把符号型字符一起吞掉。
func sdPunct(r rune) bool {
	return strings.ContainsRune("，。、！？；：“”‘’（）【】《》〈〉「」『』…—–·・‥,。!?;:()[]{}\"'-~`@#$%^&*+=_/\\|<>", r)
}

// sdLatin 只管 ASCII 字母和数字（原模组“英文不亮圈”）。
func sdLatin(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

// sdLineSeed 让同一句的字每次落在同样的地方。
func sdLineSeed(seed uint32, index int) uint32 {
	return seed ^ uint32(index)*0x40a7b892 ^ 0x57a2d41f
}

// ---------------------------------------------------------------- 画

type sdPaint struct {
	p      *ui.Painter
	s      stageFrame
	x, y   float32
	text   ui.Color
	accent ui.Color
	alt    ui.Color
	font   float32
	unit   float32
	area   ui.Rect
	w, h   float32
	pulse  float32
	shapes map[rune]cinText
}

// stageSceneStardrift 画星潮·银河。
func stageSceneStardrift(c *ui.Context, s stageFrame) {
	if s.w < 2 || s.h < 2 {
		return
	}
	canvas := stageSceneCanvas(c.Key("sd-screen"))
	shapes := *ui.Local(canvas, "glyphs", func() map[rune]cinText { return map[rune]cinText{} })
	fontPx := min(76, s.w*.066, s.h*.18)
	textUnit := fontPx / sdBaseFont
	w, h := s.w, s.h
	// 安全区左右各收 8%、上下各收 14%，底部再让出宿主副行。
	half := fontPx * .6
	mx := max(w*.08, half)
	my := max(h*.14, half)
	subtitle := float32(0)
	if s.current >= 0 && s.current < len(s.lines) && strings.TrimSpace(s.lines[s.current].Translation) != "" {
		subtitle = fontPx * 1.2
	}
	area := ui.Rect{X: mx, Y: my, W: max(1, w-2*mx), H: max(1, h-2*my-max(my, subtitle+half))}
	if area.W <= 0 || area.H <= 0 {
		area = ui.Rect{X: w * .1, Y: h * .2, W: max(1, w * .8), H: max(1, h * .6)}
	}
	still := s.motion == 0
	pulse := float32(1)
	if s.hasLevels && !still {
		sum := float32(0)
		for _, v := range s.levels {
			sum += clamp01(v)
		}
		pulse = .8 + .4*min(1, sum/float32(len(s.levels)))
	}
	alt := s.accent.Mix(white, .45)
	canvas.Draw(func(p *ui.Painter, r ui.Rect) {
		d := sdPaint{p: p, s: s, x: r.X, y: r.Y, text: white, accent: s.accent, alt: alt,
			font: fontPx, unit: textUnit, area: area, w: w, h: h, pulse: pulse, shapes: shapes}
		if still {
			d.draw(float32(sdStillT), true)
			return
		}
		d.draw(float32(s.pos.Seconds()), false)
	})
	if len(s.lines) == 0 {
		stageSceneEmpty(c, s)
		return
	}
	stageSceneTranslation(c, s, s.h*.89)
}

func (d sdPaint) draw(t float32, still bool) {
	d.nebula()
	d.stars(t)
	if still {
		if m, ok := sdMeteorAt(fnvtrack(d.s.trackKey), d.w, d.h, 1, sdStillT*.35); ok {
			d.meteor(m)
		}
	} else if m, ok := sdMeteorAt(fnvtrack(d.s.trackKey), d.w, d.h, 1, t); ok {
		d.meteor(m)
	}
	if d.s.current < 0 || d.s.current >= len(d.s.lines) {
		return
	}
	// 上一句还没走完的字继续走完，当前句的字挑新落点避开它们。
	avoid := []ui.Point(nil)
	if d.s.current > 0 {
		avoid = d.glyphSpots(&d.s.lines[d.s.current-1], d.s.current-1)
		d.lyrics(d.s.current-1, t, still, nil)
	}
	d.lyrics(d.s.current, t, still, avoid)
}

func fnvtrack(key string) uint32 { return fnv32(key) }

func (d sdPaint) nebula() {
	// 舞台没有宿主背景，先铺一层深空底色（原版让位给宿主的模糊封面）。
	d.p.Fill(ui.Rect{X: d.x, Y: d.y, W: d.w, H: d.h}, ui.Hex("#050818").Mix(d.accent, .08), 0)
	radius := max(d.w, d.h) * .55
	d.p.FillRadialGradient(ui.Rect{X: d.x, Y: d.y, W: d.w, H: d.h}, ui.RadialGradient{
		Center: ui.Point{X: .5, Y: .5}, Radius: radius,
		Stops: []ui.GradientStop{
			{Color: d.text.Alpha(.05), At: 0},
			{Color: d.accent.Alpha(.035), At: .5},
			{Color: ui.Transparent, At: 1},
		},
	}, 0)
}

func (d sdPaint) stars(t float32) {
	count := sdStarCount(150)
	if d.s.preview {
		count = min(count, 60)
	}
	stars := sdStars(fnvtrack(d.s.trackKey), count, d.w, d.h, t)
	var paths [sdStarColors * sdAlphaLevels]ui.Path
	var used [sdStarColors * sdAlphaLevels]bool
	for _, st := range stars {
		color := 0
		if st.tint >= .85 {
			color = 2
		} else if st.tint >= .55 {
			color = 1
		}
		level := min(sdAlphaLevels, max(1, st.level))
		slot := color*sdAlphaLevels + level - 1
		sdSparkle(&paths[slot], d.x+st.x, d.y+st.y, max(.4, st.size))
		used[slot] = true
	}
	palette := [sdStarColors]ui.Color{d.text, d.accent, d.alt}
	for i := range paths {
		if !used[i] {
			continue
		}
		alpha := float32(i%sdAlphaLevels+1) / sdAlphaLevels * d.pulse / sdPulseMax
		d.p.FillPath(&paths[i], palette[i/sdAlphaLevels].Alpha(clamp01(alpha)))
	}
}

func (d sdPaint) meteor(m sdMeteor) {
	base := d.text
	if m.tint >= .85 {
		base = d.alt
	} else if m.tint >= .5 {
		base = d.accent
	}
	speed := max(.001, float32(math.Hypot(float64(m.vx), float64(m.vy))))
	tailX := m.x - m.vx/speed*m.length
	tailY := m.y - m.vy/speed*m.length
	const segments = 7
	for i := 0; i < segments; i++ {
		a, b := float32(i)/segments, float32(i+1)/segments
		w := m.width * (1 - .7*a)
		d.p.Line(d.x+tailX+(m.x-tailX)*a, d.y+tailY+(m.y-tailY)*a,
			d.x+tailX+(m.x-tailX)*b, d.y+tailY+(m.y-tailY)*b, w, base.Alpha(.9*b))
	}
	var head ui.Path
	sdSparkle(&head, d.x+m.x, d.y+m.y, m.width*2.6)
	d.p.FillPath(&head, base)
}

// glyphSpots 是这一句每个字在安全区里的落点（纯函数）。
func (d sdPaint) glyphSpots(line *lyrics.Line, index int) []ui.Point {
	runes := sdTimings(line)
	if len(runes) == 0 {
		return nil
	}
	minDist := max(90*d.unit, d.font*1.15)
	return sdPlace(len(runes), d.area, minDist, sdNewRandom(sdLineSeed(fnvtrack(d.s.trackKey), index)), nil)
}

// lyrics 画一句：每个字按自己的时间走完一生；avoid 里是还亮着的旧字，落点尽量避开。
func (d sdPaint) lyrics(index int, t float32, still bool, avoid []ui.Point) {
	if index < 0 || index >= len(d.s.lines) {
		return
	}
	line := &d.s.lines[index]
	runes := sdTimings(line)
	if len(runes) == 0 {
		return
	}
	minDist := max(90*d.unit, d.font*1.15)
	spots := sdPlace(len(runes), d.area, minDist, sdNewRandom(sdLineSeed(fnvtrack(d.s.trackKey), index)), avoid)
	appear := float32(min(max(.2, sdZoomS), .5))
	fade := appear * 2
	hold := float32(.5)
	for i := range runes {
		g := &runes[i]
		if g.punct {
			continue
		}
		look := sdLook{scale: 1, alpha: 1, primary: 1, ring: 1, glow: 18}
		if !still {
			var ok bool
			look, ok = sdDriftLook(t-g.start, appear, max(.05, g.end-g.start), hold, fade, d.pulse)
			if !ok {
				continue
			}
		}
		d.glyph(g, spots[i].X, spots[i].Y, look)
	}
}

// glyph 画一个字：光罩、辉光、以及字本身。
func (d sdPaint) glyph(g *sdRune, x, y float32, look sdLook) {
	shape := d.shape(g.char)
	if shape.w <= 0 {
		return
	}
	ringAlpha := look.ring
	if ringAlpha > .02 && !g.latin {
		radius := d.font * sdRingScale
		d.p.FillRadialGradient(ui.Rect{X: d.x + x - radius, Y: d.y + y - radius, W: radius * 2, H: radius * 2}, ui.RadialGradient{
			Center: ui.Point{X: .5, Y: .5}, Radius: radius,
			Stops: []ui.GradientStop{
				{Color: d.accent.Alpha(.12 * ringAlpha), At: 0},
				{At: sdRingInner + (1-sdRingInner)*.35, Color: d.accent.Alpha(.08 * ringAlpha)},
				{At: sdRingInner + (1-sdRingInner)*.7, Color: d.accent.Alpha(.035 * ringAlpha)},
				{At: 1, Color: ui.Transparent},
			},
		}, 0)
	}
	ink := d.accent.Mix(d.text, clamp01(look.primary))
	alpha := clamp01(look.alpha)
	if look.glow > .2 && !d.s.preview {
		// MyGo 没有模糊，用两圈错位的同色字近似原版的文字阴影：内圈亮、外圈淡，合起来边缘更顺。
		radius := look.glow * d.unit * .35
		for ring, scale := range []float32{.55, 1} {
			const steps = 8
			for i := 0; i < steps; i++ {
				a := float64(i)*2*math.Pi/steps + float64(ring)*math.Pi/steps
				dx, dy := radius*scale*float32(math.Cos(a)), radius*scale*float32(math.Sin(a))
				d.drawShape(shape, x+dx, y+dy, float32(look.scale), ink.Alpha(alpha*.11))
			}
		}
	}
	d.drawShape(shape, x, y, float32(look.scale), ink.Alpha(alpha))
}

func (d sdPaint) drawShape(t cinText, x, y, scale float32, ink ui.Color) {
	if scale <= .01 {
		return
	}
	if scale == 1 {
		d.p.Glyphs(t.glyphs, d.x+x-t.w/2, d.y+y+(t.ascent-t.descent)/2, ink)
		return
	}
	d.p.Transform(ui.Scaling(scale, scale), d.x+x, d.y+y, func() {
		d.p.Glyphs(t.glyphs, d.x+x-t.w/2, d.y+y+(t.ascent-t.descent)/2, ink)
	})
}

func (d sdPaint) shape(char string) cinText {
	r := []rune(char)
	if len(r) == 0 {
		return cinText{}
	}
	if t, ok := d.shapes[r[0]]; ok {
		return t
	}
	t := cinShape(char, ui.Font{Size: d.font, Weight: 500, Lang: lyrics.Lang(d.s.lines)})
	d.shapes[r[0]] = t
	return t
}
