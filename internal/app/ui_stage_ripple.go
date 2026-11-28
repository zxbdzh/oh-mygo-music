package app

import (
	"math"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// 动感波光照 myMusic 的 WaveLightVisualizer 移植（原模组 wave-light 1.5.1，MIT）：
// 一个主色派生整套配色；底色是压暗的主色铺成的椭圆径向渐变（135%×100%，中心在 44% 高处）；
// 左右屏边各一组同心声波（核心光晕 + 9 圈此起彼伏的同心圈 + 3 道外扩声波 + 3 段缓慢转动的亮弧），
// 两组之间一团慢慢游走的淡光；当前句居中，按“字或词”从中间往两边浮现，背后是这句里分量最重的词的超大水印。
//
// 移植取舍：MyGo 没有加色混合和实时模糊，波光用普通透明叠在很暗的底色上近似（两者在暗底上几乎一样），
// 入场与柔焦只做透明度和位移，不做模糊；原模组的圈带辉光透明度不到 1%，本来也看不见，省掉。
const (
	waveRings      = 9   // 每组的同心圈数
	wavePulses     = 3   // 外扩声波的条数
	waveArcs       = 3   // 转动亮弧的段数
	waveStillT     = 12  // 减少动效时停住的相位：原模组静态预览画的是第 12 秒
	waveTokenEnter = 520 // 一个字的入场用时（毫秒）
	waveTokenDelay = 80  // 离中间每远一个单位，晚这么多毫秒开始
	waveKeywordIn  = 550 // 水印入场用时
	waveSubtitleIn = 500 // 副行入场用时
	waveHeroTie    = .94 // 分数在这个比例以内的词之间稳定地挑
)

type wavePalette struct {
	wave, tint, keyword   ui.Color
	bgMid, bgEdge, bgDeep ui.Color
	drift                 ui.Color
}

// stageHue 取颜色的 HSL，色相在 0..1，和原模组的 rgbToHsl 一样。
func stageHue(c ui.Color) (h, s, l float32) {
	r, g, b := float32(c.R)/255, float32(c.G)/255, float32(c.B)/255
	hi, lo := max(r, max(g, b)), min(r, min(g, b))
	l = (hi + lo) / 2
	if hi == lo {
		return 0, 0, l
	}
	d := hi - lo
	if l > .5 {
		s = d / (2 - hi - lo)
	} else {
		s = d / (hi + lo)
	}
	switch hi {
	case r:
		h = ((g-b)/d + 6*float32(btoi(g < b))) / 6
	case g:
		h = ((b-r)/d + 2) / 6
	default:
		h = ((r-g)/d + 4) / 6
	}
	return h, s, l
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

// stageScaleRGB 把颜色各通道乘 k（原模组 scaleRgb，用来压暗）。
func stageScaleRGB(c ui.Color, k float32) ui.Color {
	return ui.RGBA(uint8(clamp01(float32(c.R)/255*k)*255), uint8(clamp01(float32(c.G)/255*k)*255), uint8(clamp01(float32(c.B)/255*k)*255), 1)
}

// waveColor 是声波色：主色的色相，饱和度 ×1.15+0.06 夹到 0.45–0.9，明度 0.58。
func waveColor(base ui.Color) ui.Color {
	h, s, _ := stageHue(base)
	return stageHSL(h*360, min(.9, max(.45, s*1.15+.06)), .58)
}

// ripplePaletteOf 照原模组的 computePalette：歌词是同色相的亮淡色，水印是主色压到四成，
// 底色压到 24% / 7% / 4%；中间那团游走的淡光取同一个主色偏一点色相。
func ripplePaletteOf(base ui.Color) wavePalette {
	h, s, l := stageHue(base)
	// 封面主色先归一化：饱和度 ×1.2+0.08 夹到 0.42–0.85，明度夹到 0.46–0.62。
	base = stageHSL(h*360, min(.85, max(.42, s*1.2+.08)), min(.62, max(.46, l)))
	return wavePalette{
		wave:    waveColor(base),
		tint:    stageHSL(h*360, min(.45, s*.35+.06), .92),
		keyword: stageScaleRGB(base, .4),
		bgMid:   stageScaleRGB(base, .24),
		bgEdge:  stageScaleRGB(base, .07),
		bgDeep:  stageScaleRGB(base, .04),
		drift:   waveColor(stageHSL(h*360+45, min(.85, max(.42, s*1.2+.08)), min(.62, max(.46, l)))),
	}
}

// ripple 画动感波光的一整帧：底色、两组声波、水印、正文和副行。
func (d colorStagePaint) ripple(l *colorStageLayout) {
	if d.s.w <= 0 || d.s.h <= 0 {
		return
	}
	// 原模组取封面主色；没有封面时退回主题色。舞台的 accent 已经掺过白，直接用会偏淡。
	base := d.s.accent
	if d.s.art != nil && d.s.art.tint.A != 0 {
		base = d.s.art.tint
	}
	pal := ripplePaletteOf(base)
	t := float64(waveStillT)
	if d.s.motion != 0 {
		t = max(0, d.s.pos.Seconds())
	}
	d.rippleWash(pal)
	d.rippleWaves(t, pal)
	if l == nil {
		return
	}
	elapsed := float32((d.s.pos - l.line.At).Milliseconds())
	if d.s.motion == 0 {
		elapsed = 1e6
	}
	d.rippleWatermark(l, pal, elapsed)
	d.rippleTokens(l, pal, elapsed)
	d.rippleSubtitle(l, pal, elapsed)
}

// rippleWash 是底色：主色压暗后的椭圆径向渐变，横向拉宽成 135%。
func (d colorStagePaint) rippleWash(pal wavePalette) {
	w, h := d.s.w, d.s.h
	stretch := max(1, w*1.35/h)
	cx := d.x + w*.5
	d.p.Transform(ui.Scaling(stretch, 1), cx, d.y, func() {
		d.p.FillRadialGradient(ui.Rect{X: cx - w/(2*stretch), Y: d.y, W: w / stretch, H: h}, ui.RadialGradient{
			Center: ui.Point{X: .5, Y: .44}, Radius: h,
			Stops: []ui.GradientStop{{Color: pal.bgMid}, {At: .58, Color: pal.bgEdge}, {At: 1, Color: pal.bgDeep}},
		}, 0)
	})
}

// rippleWaves 是左右两组声波和中间那团游走的淡光。
func (d colorStagePaint) rippleWaves(t float64, pal wavePalette) {
	w, h := d.s.w, d.s.h
	baseR := min(w, h) * .62
	cy := h * (.45 + .02*waveSin(t*.09))
	cxL := w * (.015 + .012*waveSin(t*.06+1.2))
	cxR := w * (.985 + .012*waveSin(t*.055+3.9))
	d.rippleCluster(cxL, cy, baseR, t, 1, 1, pal.wave)
	d.rippleCluster(cxR, cy*.98, baseR*1.04, t*1.06, 2, .92, pal.wave)
	dx := w * (.5 + .18*waveSin(t*.08+.7))
	dy := h * (.42 + .08*waveCos(t*.06+2.1))
	d.glow(d.x+dx, d.y+dy, h*.4, pal.drift, .05)
}

// rippleCluster 是一组声波：核心光晕、9 圈呼吸的同心圈、3 道外扩声波、3 段转动的亮弧。
func (d colorStagePaint) rippleCluster(cx, cy, baseR float32, t float64, seed int, alphaScale float32, ink ui.Color) {
	h := d.s.h
	coreR := max(1, baseR*.9)
	d.glow(d.x+cx, d.y+cy, coreR, ink, .15*alphaScale)
	for i := 1; i <= waveRings; i++ {
		radius := waveRingRadius(i, baseR, t, seed)
		d.rippleRing(cx, cy, radius, ink, waveRingAlpha(i, t, seed, alphaScale), max(.8, waveRingWidth(i, seed, h)))
	}
	for k := 0; k < wavePulses; k++ {
		phase := wavePulsePhase(k, t, seed)
		alpha := alphaScale * .17 * pow32(1-phase, 1.6)
		if alpha <= .004 {
			continue
		}
		radius := baseR * (.1 + 1.04*phase)
		d.rippleRing(cx, cy, radius, ink, alpha, max(.8, h*(.0018+.0035*(1-phase))))
	}
	for k := 0; k < waveArcs; k++ {
		salt := k + seed*31
		r1, r2 := waveRand(salt, 4), waveRand(salt, 6)
		radius := baseR * (.28 + .72*r1) * (1 + .02*waveSin(t*.2+float64(k)))
		start := waveArcStart(k, t, seed)
		span := .9 + 1.5*r2
		alpha := max(.01, alphaScale*(.09+.08*waveRand(salt, 5))*(.7+.3*waveSin(t*.3+float64(k)*2.2)))
		d.rippleArc(cx, cy, radius, start, span, max(1, h*(.0018+.0035*r1)), ink, alpha)
	}
}

// rippleRing 是一圈声波：竖向渐变描边，上下两端淡出、中间最亮，像水面反光。
func (d colorStagePaint) rippleRing(cx, cy, radius float32, ink ui.Color, alpha, width float32) {
	if alpha <= .004 || radius <= 0 {
		return
	}
	var path ui.Path
	path.Circle(d.x+cx, d.y+cy, radius)
	d.p.StrokePathGradient(&path, width, ui.LinearGradient{
		Angle: 180, Stops: []ui.GradientStop{
			{Color: ink.Alpha(0)}, {At: .3, Color: ink.Alpha(alpha * .6)}, {At: .5, Color: ink.Alpha(alpha)},
			{At: .7, Color: ink.Alpha(alpha * .6)}, {At: 1, Color: ink.Alpha(0)},
		},
	})
}

// rippleArc 是一段缓慢正反转的亮弧，用短线段拼出来，好让上下两端按高度淡出。
func (d colorStagePaint) rippleArc(cx, cy, radius, start, span, width float32, ink ui.Color, alpha float32) {
	const segments = 18
	prevX, prevY := float32(0), float32(0)
	for i := 0; i <= segments; i++ {
		angle := float64(start + span*float32(i)/segments)
		x := cx + radius*float32(math.Cos(angle))
		y := cy + radius*float32(math.Sin(angle))
		if i > 0 {
			// 竖向渐变：弧的上下两端淡出
			u := clamp01((y - (cy - radius)) / max(1, 2*radius))
			fade := float32(math.Sin(float64(u) * math.Pi))
			d.p.Line(d.x+prevX, d.y+prevY, d.x+x, d.y+y, width, ink.Alpha(alpha*fade))
		}
		prevX, prevY = x, y
	}
}

// rippleWatermark 是正文背后的大字水印：越长字越小，入场时由大变小、由淡变清。
func (d colorStagePaint) rippleWatermark(l *colorStageLayout, pal wavePalette, elapsed float32) {
	word := rippleKeyword(l.line, strings.TrimSpace(l.line.Text))
	if word == "" {
		return
	}
	w, h := d.s.w, d.s.h
	runes := []rune(word)
	size := max(28, min(h*.42, w*.8/float32(max(1, len(runes))))) * .8
	font := ui.Font{Size: size, Weight: 800}
	// 宽度给足舞台：给 0 时排版的 Size 就是 0，取不到水印的真实宽高。
	shape := ui.ShapeText(word, font, w)
	sw, sh := shape.Size()
	if sw > w*.96 {
		size *= w * .96 / sw
		font.Size = size
		shape = ui.ShapeText(word, font, w)
		sw, sh = shape.Size()
	}
	p := rippleEase(clamp01(elapsed / waveKeywordIn))
	scale := 1.06 - .06*p
	x, y := d.x+w/2, d.y+h*.5
	// 没有模糊时原模组也会把水印压到 55%，免得一个清楚的大字抢正文。
	ink := pal.keyword.Alpha(.55 * p)
	d.p.Transform(ui.Scaling(scale, scale), x, y, func() {
		d.p.TextLayout(shape, x-sw/2, y-sh/2, ink)
	})
}

// rippleTokens 画正文：按“字或词”从中间往两边浮现，外侧的从自己那一侧滑进来。
func (d colorStagePaint) rippleTokens(l *colorStageLayout, pal wavePalette, elapsed float32) {
	n := len(l.pieces)
	if n == 0 {
		return
	}
	mid := float32(n-1) / 2
	motion := d.s.motion
	// 原版正文一直挂着主色阴影：σ = max(10px, 屏高 3%)/2，偏移向下 1px。
	blur := max(10, d.s.h*.03)
	asc := l.textFont.Metrics().Ascent
	for i := range l.pieces {
		g := &l.pieces[i]
		dist := absStageFloat(float32(i) - mid)
		side := float32(1)
		if float32(i) < mid {
			side = -1
		}
		p := rippleEase(clamp01((elapsed - (100 + dist*waveTokenDelay)) / waveTokenEnter))
		dx := side * min(dist, 4) * .7 * l.font * (1 - p) * motion
		if p <= 0 {
			continue
		}
		level := int(math.Round(float64(clamp01(.5*p)) * cinGlowLevels))
		if level > 0 && len(g.glyphs) > 0 {
			layout := glowLayoutFor(g.text, l.textFont, &g.glow, blur, level, pal.keyword)
			d.p.TextLayout(layout, d.x+g.x+dx+g.glyphs[0].X, d.y+g.y-asc, pal.tint.Alpha(.97*p))
			continue
		}
		d.glyph(*g, dx, 0, pal.tint.Alpha(.97*p))
	}
}

// rippleSubtitle 是副行（翻译或罗马音）：排在水印下沿和底部之间，淡入。
func (d colorStagePaint) rippleSubtitle(l *colorStageLayout, pal wavePalette, elapsed float32) {
	text := strings.TrimSpace(l.line.Translation)
	if text == "" || d.s.preview {
		return
	}
	w, h := d.s.w, d.s.h
	size := max(11, l.font*.4)
	font := ui.Font{Size: size, Weight: 400, LineHeight: 1.5}
	shape := ui.ShapeText(text, font, w*.7)
	_, sh := shape.Size()
	// 水印下沿再往下四分之一个字高，不压正文、不出舞台。
	centerY := h*.75 + l.font/4
	centerY = max(centerY, h*.5+l.font*1.1+sh/2)
	centerY = min(centerY, h-sh/2-4)
	p := rippleEase(clamp01(elapsed / waveSubtitleIn))
	_, sw := shape.Size()
	d.p.TextLayout(shape, d.x+w/2-sw/2, d.y+centerY-sh/2, pal.tint.Alpha(.68*p))
}

// 原模组的按序号伪随机（每帧一样，画面稳定）：frac(sin((i+1)·127.1 + salt·311.7)·43758.5453)。
func waveRand(i, salt int) float32 {
	v := math.Sin(float64(i+1)*127.1+float64(salt)*311.7) * 43758.5453
	return float32(v - math.Floor(v))
}

func waveRingRadius(i int, baseR float32, t float64, seed int) float32 {
	r1 := waveRand(i+seed*13, 1)
	swell := 1 + .15*waveSin(t*(.65+.15*float64(r1))-float64(i)*.62+float64(seed)*2.1)
	return baseR * (.16 + .92*float32(i)/waveRings) * swell
}

func waveRingAlpha(i int, t float64, seed int, alphaScale float32) float32 {
	r2 := waveRand(i+seed*13, 2)
	breathe := .6 + .4*waveSin(t*(.45+.1*float64(r2))-float64(i)*.55+float64(seed))
	return max(.008, alphaScale*(.03+.095*(1-float32(i)/waveRings))*breathe)
}

func waveRingWidth(i, seed int, height float32) float32 {
	return max(.8, height*(.0012+.005*waveRand(i+seed*13, 2)))
}

func wavePulsePhase(k int, t float64, seed int) float32 {
	v := t*(.1+.018*float64(waveRand(seed, 7))) + float64(k)/3 + float64(seed)*.21
	return float32(v - math.Floor(v))
}

func waveArcStart(k int, t float64, seed int) float32 {
	r1 := waveRand(k+seed*31, 4)
	dir := float32(1)
	if k%2 == 1 {
		dir = -1
	}
	return float32(t*(.05+.07*float64(r1)))*dir + float32(k)*2.4 + float32(seed)*1.7
}

func waveSin(t float64) float32 { return float32(math.Sin(t)) }
func waveCos(t float64) float32 { return float32(math.Cos(t)) }

func pow32(v, p float32) float32 {
	if v <= 0 {
		return 0
	}
	return float32(math.Pow(float64(v), float64(p)))
}

// rippleEase 是原模组入场用的 cubic-bezier(0.2, 0.8, 0.3, 1)：二分求参数，再取纵坐标。
func rippleEase(x float32) float32 {
	x = clamp01(x)
	lo, hi := float32(0), float32(1)
	for i := 0; i < 12; i++ {
		mid := (lo + hi) / 2
		if rippleBezier(.2, .3, mid) < x {
			lo = mid
		} else {
			hi = mid
		}
	}
	return rippleBezier(.8, 1, (lo+hi)/2)
}

func rippleBezier(a, b, t float32) float32 {
	u := 1 - t
	return 3*u*u*t*a + 3*u*t*t*b + t*t*t
}

// rippleKeyword 挑这句里分量最重的词：字数占 14 分、唱到它的时长占 18 分；
// 分数在最高分 94% 以内的几个之间用整句哈希稳定地挑，同一句每次都一样。
// 单字撑不起大字水印，向分量更重的邻词借字凑够两个。署名行（作词：……）取冒号后面的名字。
func rippleKeyword(line *lyrics.Line, text string) string {
	if text == "" {
		return ""
	}
	if tail, ok := splitCredit(text); ok && tail != "" {
		if len([]rune(tail)) <= 8 {
			return tail
		}
		if w, ok := ripplePickHero(line, tail); ok {
			return w
		}
		if r := []rune(tail); len(r) > 8 {
			return string(r[:8])
		}
		return tail
	}
	cleaned := rippleClean(text)
	if cleaned == "" {
		return ""
	}
	if w, ok := ripplePickHero(line, text); ok {
		return w
	}
	if r := []rune(cleaned); len(r) > 8 {
		return string(r[:8])
	}
	return cleaned
}

// splitCredit 认出“作词：方文山”这类署名行，返回冒号后的名字；认不认得出交给 lyrics.IsCredit。
func splitCredit(text string) (string, bool) {
	if !lyrics.IsCredit(text) {
		return "", false
	}
	// 中文冒号是三个字节，按字节切会把名字切坏。
	at, size := strings.LastIndex(text, "："), len("：")
	if colon := strings.LastIndex(text, ":"); colon > at {
		at, size = colon, 1
	}
	if at < 0 {
		return "", false
	}
	return strings.TrimFunc(text[at+size:], isMark), true
}

// rippleClean 去掉空白和标点，最多留 12 个字。
func rippleClean(text string) string {
	var b strings.Builder
	n := 0
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		if n++; n > 12 {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ripplePickHero 按字数与唱到的时长给词打分，挑最重的一个；够不着两个字的再借邻词。
func ripplePickHero(line *lyrics.Line, text string) (string, bool) {
	type word struct {
		text  string
		chars int
		score float32
	}
	var words []word
	total := 0
	for _, w := range line.Words {
		part := strings.TrimSpace(w.Text)
		if part == "" || !strings.Contains(text, part) {
			continue
		}
		n := 0
		for _, r := range part {
			if !unicode.IsSpace(r) {
				n++
			}
		}
		if n == 0 {
			continue
		}
		words = append(words, word{part, n, 0})
		total += n
	}
	if len(words) == 0 {
		return "", false
	}
	span := float32(colorStageEnd(line)-line.At) / float32(1e9)
	span = min(16, max(.4, span))
	for i := range words {
		sung := min(2.5, span*float32(words[i].chars)/float32(max(1, total)))
		words[i].score = float32(min(words[i].chars, 8))*14 + sung*18
	}
	best := 0
	for i := range words {
		if words[i].score > words[best].score {
			best = i
		}
	}
	var near []int
	for i := range words {
		if words[i].score >= words[best].score*waveHeroTie {
			near = append(near, i)
		}
	}
	pick := best
	if len(near) > 1 {
		hash := fnv32(text)
		pick = near[int(hash%uint32(len(near)))]
	}
	// 单字撑不起大字水印：向分量更重的邻词借字，凑够两个。
	lo, hi := pick, pick
	visible := func() int {
		n := 0
		for i := lo; i <= hi; i++ {
			n += words[i].chars
		}
		return n
	}
	for visible() < 2 && (lo > 0 || hi < len(words)-1) {
		prev, next := -1, -1
		if lo > 0 {
			prev = lo - 1
		}
		if hi < len(words)-1 {
			next = hi + 1
		}
		switch {
		case next < 0 || prev >= 0 && words[prev].score >= words[next].score:
			lo = prev
		default:
			hi = next
		}
	}
	var b strings.Builder
	for i := lo; i <= hi; i++ {
		b.WriteString(words[i].text)
	}
	return rippleClean(b.String()), true
}

func fnv32(text string) uint32 {
	h := uint32(2166136261)
	for _, r := range text {
		h = (h ^ uint32(r)) * 16777619
	}
	return h
}
