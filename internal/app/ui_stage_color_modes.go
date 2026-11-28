package app

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

func renderStageColorMode(c *ui.Context, s stageFrame) {
	if s.w <= 0 || s.h <= 0 {
		return
	}
	canvas := ui.Box(c.Key("color-mode")).Absolute().Top(0).Right(0).Bottom(0).Left(0).PassThrough()
	cache := ui.Local(canvas, "layouts", func() colorStageCache { return colorStageCache{} })
	lang := lyrics.Lang(s.lines)
	if lang == "" {
		lang = c.Locale()
	}
	if cache.w != s.w || cache.h != s.h || cache.style != s.style || cache.lang != lang {
		*cache = colorStageCache{w: s.w, h: s.h, style: s.style, lang: lang}
	}
	em := min(76, s.w*0.066, s.h*0.18)
	current := min(s.current, len(s.lines)-1)
	var line *lyrics.Line
	var layout *colorStageLayout
	if current >= 0 {
		line = &s.lines[current]
		layout = cache.layout(line, em)
	}
	// 动感波光在句间、空行保持上一句（原模组的 WaveHold），不要闪成一片空背景。
	if s.style == "ripple" && line != nil && strings.TrimSpace(line.Text) == "" {
		for i := current - 1; i >= 0; i-- {
			if strings.TrimSpace(s.lines[i].Text) != "" {
				line, layout = &s.lines[i], cache.layout(&s.lines[i], em)
				break
			}
		}
	}
	var old [2]*colorStageLayout
	var oldEnd [2]time.Duration
	// 虹光和 52Hz 也需要上一句做短暂淡出，否则换句时整行突然消失，看起来像闪切。
	if s.style == "miside" || s.style == "52hz" {
		for i := range old {
			if n := current - i - 1; n >= 0 {
				previous := &s.lines[n]
				end := min(colorStageEnd(previous), s.lines[n+1].At)
				if s.pos-end < 5*time.Second || s.motion == 0 {
					old[i], oldEnd[i] = cache.layout(previous, em), end
				}
			}
		}
	}
	var misideL *misideLayout
	var misideOld [2]*misideLayout
	var misideEnds [2]time.Duration
	if s.style == "miside" {
		if line != nil {
			misideL = cache.misideLayoutFor(line, em)
		}
		for i, v := range old {
			if v != nil {
				// 掉落的计时属于这一句自己的唱完时间，不跟下一句的开头取短。
				misideOld[i], misideEnds[i] = cache.misideLayoutFor(v.line, em), colorStageEnd(v.line)
			}
		}
	}
	if s.accent.A == 0 {
		s.accent = ui.RGB(101, 186, 255)
	}
	canvas.Draw(func(p *ui.Painter, r ui.Rect) {
		paint := colorStagePaint{p: p, s: s, x: r.X, y: r.Y, em: em, light: s.preview || !p.Accelerated()}
		switch s.style {
		case "rainbow":
			paint.aurora(layout)
		case "52hz":
			var ringsL, prevRings *ringsLayout
			if line != nil {
				ringsL = cache.ringsLayoutFor(line, em)
			}
			if old[0] != nil {
				prevRings = cache.ringsLayoutFor(old[0].line, em)
			}
			paint.drawRings(ringsL, prevRings)
		case "miside":
			paint.miside(misideL, misideOld, misideEnds)
		case "ripple":
			paint.ripple(layout)
		}
	})
	if layout == nil || len(layout.pieces) == 0 {
		ui.Column(c.Key("color-stage-intro")).Absolute().Top(0).Bottom(0).Left(0).Right(0).Center().PassThrough().Children(func() {
			mark := "·  ·  ·"
			if len(s.lines) == 0 {
				mark = "♪"
			}
			ui.Text(c, mark).FontSize(em * .76).TextColor(white.Alpha(.58))
		})
	} else if line.Translation != "" && !s.preview && s.style != "ripple" {
		ui.Column(c.Key("color-stage-translation")).Absolute().Left(s.w * .1).Right(s.w * .1).Bottom(s.h * .045).
			AlignItems(ui.Center).PassThrough().Children(func() {
			ui.Text(c, line.Translation).Lang(c.Locale()).FontSize(min(18, em*.37)).TextColor(white.Alpha(.65)).TextAlign(ui.Center)
		})
	}
}

// rainbowAtmosphere 已移除：myMusic 的虹光没有星点和色带，只留当前句本身。

type colorStagePiece struct {
	glyphs      []ui.Glyph
	text        string  // 单元的原文，辉光排版要用
	x, y, width float32 // 字形原点、基线及可见单元在该行上的宽度
	start, end  time.Duration
	glow        *cinGlow // 唱和时的阴影排版，逐帧复用
}

type colorStageLayout struct {
	line           *lyrics.Line
	pieces         []colorStagePiece
	font, width, y float32
	textFont       ui.Font // 塑形字体，辉光排版和基线对齐复用
}

type colorStageCache struct {
	w, h        float32
	style, lang string
	next        int
	layouts     [4]*colorStageLayout
	rings       [4]*ringsLayout
	ringsNext   int
	miside      [4]*misideLayout
	misideNext  int
}

// misideLayoutFor 和 ringsLayoutFor 一样只缓存最近几句。
func (c *colorStageCache) misideLayoutFor(line *lyrics.Line, em float32) *misideLayout {
	for _, v := range c.miside {
		if v != nil && v.line == line {
			return v
		}
	}
	out := buildMisideLayout(line, em, c.w, c.h, c.lang)
	c.miside[c.misideNext] = out
	c.misideNext = (c.misideNext + 1) % len(c.miside)
	return out
}

// ringsLayoutFor 只缓存最近几句：换歌、换尺寸或换样式时整个缓存重来，不会拿旧字形去画。
func (c *colorStageCache) ringsLayoutFor(line *lyrics.Line, em float32) *ringsLayout {
	for _, v := range c.rings {
		if v != nil && v.line == line {
			return v
		}
	}
	out := buildRingsLayout(line, em, c.w, c.h, c.lang)
	c.rings[c.ringsNext] = out
	c.ringsNext = (c.ringsNext + 1) % len(c.rings)
	return out
}

// 只缓存当前容器的几句排版，预览、换尺寸和换歌词不会共用旧字形或坐标。
func (c *colorStageCache) layout(line *lyrics.Line, em float32) *colorStageLayout {
	for _, v := range c.layouts {
		if v != nil && v.line == line {
			return v
		}
	}
	out := &colorStageLayout{line: line}
	c.layouts[c.next] = out
	c.next = (c.next + 1) % len(c.layouts)
	text := strings.TrimSpace(line.Text)
	if text == "" {
		return out
	}

	font := ui.Font{Size: em, Weight: 650, Lang: c.lang, LineHeight: 1.17}
	maxWidth, center := c.w*.83, c.h*.47
	switch c.style {
	case "rainbow":
		// 虹光照 Aurora：Medium 字重、1.35 行高、左右各留 8%。
		font.Weight, font.LineHeight, maxWidth = 500, 1.35, c.w*.84
	case "52hz":
		font.Size, font.Weight, maxWidth = em*.82, 740, c.w*.72
	case "miside":
		font.Weight, center = 830, c.h*.42
	case "ripple":
		// 动感波光：Bold、1.3 行高、左右各留 7%。
		font.Weight, font.LineHeight, maxWidth = 700, 1.3, c.w*.86
	}
	shape := ui.ShapeText(text, font, maxWidth)
	rows := shape.Lines()
	if len(rows) > 3 {
		font.Size *= 3 / float32(len(rows))
		shape = ui.ShapeText(text, font, maxWidth)
		rows = shape.Lines()
	}
	_, height := shape.Size()
	scale := min(1, c.h*.47/max(height, 1))
	for _, row := range rows {
		scale = min(scale, maxWidth/max(row.Rect.W, 1))
	}
	if scale < .99 {
		font.Size *= scale
		shape = ui.ShapeText(text, font, maxWidth)
		rows = shape.Lines()
		_, height = shape.Size()
	}
	out.font, out.y, out.textFont = font.Size, center, font

	runes := []rune(text)
	starts, ends := colorStageTimings(line, text, len(runes))
	utf16Offsets := make([]int, len(runes)+1)
	for i, r := range runes {
		utf16Offsets[i+1] = utf16Offsets[i] + 1
		if r > 0xffff {
			utf16Offsets[i+1]++
		}
	}
	groupWords := c.style == "52hz" || c.style == "ripple"
	for _, row := range rows {
		start := sort.SearchInts(utf16Offsets, row.Start)
		end := sort.SearchInts(utf16Offsets, row.End)
		for end > start && unicode.IsSpace(runes[end-1]) {
			end--
		}
		glyphs := ui.Shape(string(runes[start:end]), font)
		if len(glyphs) == 0 {
			continue
		}
		width := glyphs[len(glyphs)-1].X + glyphs[len(glyphs)-1].Advance
		out.width = max(out.width, width)
		ids := make([]int, end-start)
		for i := range ids {
			ids[i] = i
			if i > 0 && groupWords && colorStageJoins(runes[start+i-1]) && colorStageJoins(runes[start+i]) {
				ids[i] = ids[i-1]
			}
		}
		// 整行塑形后再按字簇切绘制范围，保留双向次序、连接字和组合附标。
		for lo := 0; lo < len(glyphs); {
			g := glyphs[lo]
			hi, first, last := lo+1, g.Cluster, g.Cluster+g.Runes-1
			for hi < len(glyphs) && ids[glyphs[hi].Cluster] == ids[g.Cluster] {
				first = min(first, glyphs[hi].Cluster)
				last = max(last, glyphs[hi].Cluster+glyphs[hi].Runes-1)
				hi++
			}
			if !unicode.IsSpace(runes[start+first]) {
				w := max(font.Size*.13, glyphs[hi-1].X+glyphs[hi-1].Advance-g.X)
				out.pieces = append(out.pieces, colorStagePiece{glyphs: glyphs[lo:hi],
					text: string(runes[start+first : start+last+1]),
					x:    (c.w - width) / 2, y: center - height/2 + row.Baseline, width: w,
					start: starts[start+first], end: ends[min(end-1, start+last)]})
			}
			lo = hi
		}
	}
	return out
}

func colorStageJoins(r rune) bool {
	return (unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '\'' || r == '’') &&
		!unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func colorStageEnd(line *lyrics.Line) time.Duration {
	if line.End > line.At {
		return line.End
	}
	if n := len(line.Words); n > 0 && line.Words[n-1].End > line.At {
		return line.Words[n-1].End
	}
	return line.At + 2*time.Second
}

func colorStageTimings(line *lyrics.Line, text string, n int) ([]time.Duration, []time.Duration) {
	starts, ends := make([]time.Duration, n), make([]time.Duration, n)
	span := colorStageEnd(line) - line.At
	for i := range n {
		starts[i], ends[i] = line.At+span*time.Duration(i)/time.Duration(n), line.At+span*time.Duration(i+1)/time.Duration(n)
	}
	// 有时间戳的片段在原句内顺序匹配，保留音源没给时间的空格和标点。
	cursor, at := 0, 0
	for _, word := range line.Words {
		part := strings.TrimSpace(word.Text)
		if part == "" || word.End < word.Start {
			continue
		}
		found := strings.Index(text[cursor:], part)
		if found < 0 {
			continue
		}
		at += utf8.RuneCountInString(text[cursor : cursor+found])
		count := utf8.RuneCountInString(part)
		for i := range count {
			starts[at+i] = word.Start + (word.End-word.Start)*time.Duration(i)/time.Duration(count)
			ends[at+i] = word.Start + (word.End-word.Start)*time.Duration(i+1)/time.Duration(count)
		}
		at += count
		cursor += found + len(part)
	}
	return starts, ends
}

type colorStagePaint struct {
	p        *ui.Painter
	s        stageFrame
	x, y, em float32
	light    bool
}

func (d colorStagePaint) glyph(g colorStagePiece, dx, dy float32, ink ui.Color) {
	d.p.Glyphs(g.glyphs, d.x+g.x+dx, d.y+g.y+dy, ink)
}

func (d colorStagePaint) glow(x, y, radius float32, ink ui.Color, alpha float32) {
	// Radius 必须写死成半径：默认值只到方框的角，沿中线到框边时还没淡完，会留下一条直边。
	d.p.FillRadialGradient(ui.Rect{X: x - radius, Y: y - radius, W: radius * 2, H: radius * 2}, ui.RadialGradient{
		Center: ui.Point{X: .5, Y: .5}, Radius: radius,
		Stops: []ui.GradientStop{{Color: ink.Alpha(alpha)}, {At: 1, Color: ink.Alpha(0)}},
	}, 0)
}

// stageHSL 转出一个 ui.Color，够画虹光的色相环用。
func stageHSL(h, s, l float32) ui.Color {
	h = float32(math.Mod(float64(h), 360))
	if h < 0 {
		h += 360
	}
	c := (1 - absStageFloat(2*l-1)) * s
	x := c * (1 - absStageFloat(float32(math.Mod(float64(h/60), 2))-1))
	m := l - c/2
	var r, g, b float32
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return ui.RGBA(uint8(clamp01(r+m)*255), uint8(clamp01(g+m)*255), uint8(clamp01(b+m)*255), 1)
}

func absStageFloat(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// 虹光照 myMusic Aurora：只画当前句、居中；每个字按起唱秒数取色相（起点秒数对 4 取余 × 70°，
// 唱的过程中再转 40°），亮度 58%→72%，正在唱的那个字带同色光晕并上抬 2/64 字高；还没唱到的是 28% 白。
func (d colorStagePaint) aurora(l *colorStageLayout) {
	if l == nil || len(l.pieces) == 0 {
		return
	}
	// 整句 0.45 秒淡入，唱完后再 0.45 秒淡出。
	fade := clamp01(float32((d.s.pos - l.line.At).Seconds()) / .45)
	if end := colorStageEnd(l.line); d.s.pos > end {
		fade *= 1 - clamp01(float32((d.s.pos-end).Seconds())/.45)
	}
	if fade <= 0 {
		return
	}
	if d.s.motion == 0 {
		fade = 1
	}
	scale := l.font / 64
	for i := range l.pieces {
		g := &l.pieces[i]
		t := float32(0)
		if d.s.motion != 0 {
			t = clamp01(float32(d.s.pos-g.start) / float32(max(1, g.end-g.start)))
		} else {
			// 减少动效时整句直接亮到唱完的样子。
			t = 1
		}
		if t <= 0 {
			d.glyph(*g, 0, 0, white.Alpha(.28*fade))
			continue
		}
		hue := float32(math.Mod(float64(g.start.Seconds()*70)+float64(t*40), 360))
		ink := stageHSL(hue, .9, .58+t*.14)
		lift := float32(0)
		if t < 1 {
			lift = -2 * scale
			// 原版唱和时挂半径 8+14t 的同色阴影；MyGo 的排版阴影就是真模糊，按同一半径换算。
			level := int(math.Round(float64(clamp01(.65*fade)) * cinGlowLevels))
			if level > 0 {
				// 模糊半径随 t 连续变化，按 6px 归档，避免每个字攒出几十份阴影排版。
				blur := 6 * float32(math.Round(float64(2*(.57735*(8+14*t)*scale+.5))/6))
				layout := glowLayoutFor(g.text, l.textFont, &g.glow, blur, level, stageHSL(hue, .9, .65))
				asc := l.textFont.Metrics().Ascent
				// 单元排版从 0 起排，要加上它在整行里的字形偏移。
				d.p.TextLayout(layout, d.x+g.x+g.glyphs[0].X, d.y+g.y+lift-asc, ink.Alpha(fade))
				continue
			}
		}
		d.glyph(*g, 0, lift, ink.Alpha(fade))
	}
}

// 下落与回弹（旧的时间求值版本）已换成 ui_stage_miside.go 里照 myMusic 移植的实现。

// 动感波光已换成 ui_stage_ripple.go 里照 myMusic 移植的实现。

func colorStageSin(t float64) float32 { return float32(math.Sin(t)) }
func colorStageCos(t float64) float32 { return float32(math.Cos(t)) }
