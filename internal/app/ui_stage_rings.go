package app

import (
	"hash/fnv"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// 52Hz 照 myMusic 的 Rings52HzLayout / Rings52HzVisualizer 移植：
// 整句居中，每个字按句子的种子错位、旋转、缩放；唱到的字点亮并鼓起 12%；
// 每 0.72 秒从一个外扩的字身轮廓发出一圈年轮，年轮在歌曲时间上往外走、到屏幕边缘前淡没。

const (
	ringsLineHeight = 1.34  // 行距倍数（原版 LINE_HEIGHT）
	ringsGap        = 0.55  // 年轮离字身的间距（原版 RING_GAP）
	ringsCorner     = 0.14  // 圆角（原版 RING_CORNER）
	ringsBeat       = 0.72  // 没有真实拍点时的拍间隔（原版 STATIC_BEAT_S）
	ringsLightMin   = 0.12  // 点亮用时下限（秒）
	ringsLightMax   = 0.45  // 点亮用时上限（秒）
	ringsSwell      = 0.12  // 点亮时鼓起幅度
	ringsDim        = 0.3   // 没唱到的字的透明度
	ringsLineFade   = 0.45  // 整句淡入淡出（秒）
	ringsOpacity    = 0.8   // 年轮浓度
	ringsWideHalf   = 0.42  // 宽字的字身半高（按字号）
	ringsLatinHalf  = 0.38  // 拉丁字的字身半高
	ringsGlowRadius = 0.39  // 点亮字的阴影半径（按字号，原版 Shadow 的 blurRadius）
)

type ringsGlyph struct {
	glyphs     []ui.Glyph
	text       string  // 单个字，辉光排版要用
	x, y       float32 // 字心
	rotation   float32 // 角度
	scale      float32
	width      float32 // 原始步进宽（不含词间距）
	wide       bool
	row, word  int // 宽字的 word 是 -1，和原版一致
	start, end time.Duration
	glow       *cinGlow // 点亮后每帧复用同一份阴影排版
}

type ringsRect struct {
	cx, cy, hw, hh float32
	row           int
}

type ringsLayout struct {
	line   *lyrics.Line
	glyphs []ringsGlyph
	fine   []ringsRect // 宽字一字一个，拉丁字母按词合成一个
	coarse []ringsRect // 每行合并到不超过 6 个，远处用
	size   float32
	font   ui.Font // 塑形字体，辉光排版复用
	// 辉光排版按行盒居中；字身用同一组度量对齐，避免亮字和暗字错半像素。
	ascent, descent float32
	at, end         time.Duration
}

// ringsWide 是原模组的 WIDE_CHAR：CJK、假名、谚文、全角标点。
func ringsWide(r rune) bool {
	return r >= 0x1100 && r <= 0x11FF || r >= 0x2E80 && r <= 0x9FFF || r >= 0xAC00 && r <= 0xD7AF ||
		r >= 0xF900 && r <= 0xFAFF || r >= 0xFE30 && r <= 0xFE4F || r >= 0xFF00 && r <= 0xFFEF
}

// buildRingsLayout 排一句：放不下 3 行就按 0.88 缩小重排，最多 6 次（原版 FIT_ATTEMPTS）。
func buildRingsLayout(line *lyrics.Line, em, w, h float32, lang string) *ringsLayout {
	out := &ringsLayout{line: line, at: line.At, end: colorStageEnd(line)}
	text := strings.TrimSpace(line.Text)
	if text == "" {
		return out
	}
	chars := []rune(text)
	maxWidth := w * .78
	size := em
	var rows [][]int
	for attempt := 0; attempt < 6; attempt++ {
		font := ui.Font{Size: size, Weight: 700, Lang: lang, LineHeight: ringsLineHeight}
		widths := make([]float32, len(chars))
		for i, r := range chars {
			if g := ui.Shape(string(r), font); len(g) > 0 {
				widths[i] = g[len(g)-1].X + g[len(g)-1].Advance
			}
		}
		rows = ringsWrap(chars, widths, maxWidth)
		if len(rows) <= 3 {
			break
		}
		size *= .88
	}
	out.size = size
	font := ui.Font{Size: size, Weight: 700, Lang: lang, LineHeight: ringsLineHeight}
	m := font.Metrics()
	out.font, out.ascent, out.descent = font, m.Ascent, m.Descent
	starts, ends := colorStageTimings(line, text, len(chars))

	hash := fnv.New32a()
	hash.Write([]byte(text))
	sequence := 0
	between := func(a, b float32) float32 {
		sequence++
		return a + (b-a)*lineRandom(float64(hash.Sum32()), float64(sequence))
	}
	step := size * ringsLineHeight
	top := h*.47 - step*float32(len(rows)-1)/2
	wordID := -1
	for ri, row := range rows {
		// 先算这一行的步进：相邻宽字多留 0.06 字高，拉丁词内只留 0.02。
		advance := make([]float32, len(row))
		raw := make([]float32, len(row))
		for i, ci := range row {
			g := ui.Shape(string(chars[ci]), font)
			var width float32
			if len(g) > 0 {
				width = g[len(g)-1].X + g[len(g)-1].Advance
			}
			raw[i] = width
			gap := float32(0)
			if i+1 < len(row) {
				nextWide := ringsWide(chars[row[i+1]])
				curWide := ringsWide(chars[ci])
				if curWide || nextWide {
					gap = size * .06
				} else {
					gap = size * .02
				}
			}
			advance[i] = width + gap
		}
		var sum float32
		for _, a := range advance {
			sum += a
		}
		cursor := w/2 - sum/2 + between(-.04, .04)*size
		rowY := top + step*float32(ri)
		// 原版每个拉丁词共用一组 (纵向错位, 旋转, 缩放)；宽字和空格没有词。
		var wordOffset [3]float32
		hasWord := false
		for i, ci := range row {
			ch := chars[ci]
			wide := ringsWide(ch)
			if unicode.IsSpace(ch) || wide {
				hasWord = false
				wordOffset = [3]float32{}
			}
			if !unicode.IsSpace(ch) {
				if !wide && !hasWord {
					wordOffset = [3]float32{between(-.1, .1), between(-.05, .05), between(.94, 1.06)}
					hasWord = true
					wordID++
				}
				jitter := float32(.2)
				if wide {
					jitter = 1
				}
				// 字心按原始步进算，不扣那 0.02 字高；词间距只影响落笔位置。
				x := cursor + raw[i]/2 + between(-.035, .035)*size*jitter
				y := rowY + (wordOffset[0]+between(-.11, .11)*jitter)*size
				rotation := wordOffset[1] + between(-.075, .075)*jitter
				wordScale := float32(1)
				if hasWord {
					wordScale = wordOffset[2]
				}
				scale := wordScale * (1 + between(-.07*jitter, .07*jitter))
				word := wordID
				if wide {
					word = -1
				}
				out.glyphs = append(out.glyphs, ringsGlyph{
					glyphs: ui.Shape(string(ch), font), text: string(ch), x: x, y: y,
					rotation: rotation * 180 / math.Pi, scale: scale, width: raw[i],
					wide: wide, row: ri, word: word,
					start: starts[ci], end: ends[ci],
				})
			}
			cursor += advance[i]
		}
	}
	out.fine = ringsRects(out)
	// 每行合成一个外接矩形：MyGo 画不了并集轮廓，按行合并后一圈才像完整的一圈。
	out.coarse = ringsMerge(out.fine, 1)
	return out
}

// ringsWrap 按宽字断词、超出宽度就换行，返回每个字在 chars 里的下标。
func ringsWrap(chars []rune, widths []float32, maxWidth float32) [][]int {
	var tokens [][]int
	var current []int
	var currentWidth float32
	flush := func() {
		if len(current) > 0 {
			tokens = append(tokens, current)
			current, currentWidth = nil, 0
		}
	}
	for i, r := range chars {
		if ringsWide(r) {
			flush()
			tokens = append(tokens, []int{i})
			continue
		}
		current = append(current, i)
		currentWidth += widths[i]
		if unicode.IsSpace(r) {
			flush()
		}
	}
	flush()
	var rows [][]int
	var row []int
	var rowWidth float32
	for _, t := range tokens {
		var tokenWidth float32
		for _, i := range t {
			tokenWidth += widths[i]
		}
		if len(row) > 0 && rowWidth+tokenWidth > maxWidth {
			rows = append(rows, row)
			trimmed := t
			if len(trimmed) > 0 && unicode.IsSpace(chars[trimmed[0]]) {
				trimmed = trimmed[1:]
			}
			row, rowWidth = append([]int(nil), trimmed...), tokenWidth
			continue
		}
		row = append(row, t...)
		rowWidth += tokenWidth
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, nil)
	}
	return rows
}

// ringsRects 把字变成年轮源：宽字一字一个，拉丁字母按词合成一个外接矩形。
func ringsRects(l *ringsLayout) []ringsRect {
	var out []ringsRect
	for i := 0; i < len(l.glyphs); {
		g := l.glyphs[i]
		if g.wide || g.word < 0 {
			out = append(out, ringsRect{cx: g.x, cy: g.y, hw: g.width * .46 * g.scale, hh: l.size * ringsWideHalf * g.scale, row: g.row})
			i++
			continue
		}
		left, right := g.x, g.x
		topY, bottom := g.y, g.y
		j := i
		for ; j < len(l.glyphs) && l.glyphs[j].word == g.word && !l.glyphs[j].wide; j++ {
			h := l.glyphs[j]
			left = min(left, h.x-h.width*.46*h.scale)
			right = max(right, h.x+h.width*.46*h.scale)
			topY = min(topY, h.y-l.size*ringsLatinHalf*h.scale)
			bottom = max(bottom, h.y+l.size*ringsLatinHalf*h.scale)
		}
		out = append(out, ringsRect{cx: (left + right) / 2, cy: (topY + bottom) / 2, hw: (right - left) / 2, hh: (bottom - topY) / 2, row: g.row})
		i = j
	}
	return out
}

// ringsMerge 把同一行相邻的矩形并到不超过 cap 个（原版 mergeRingRects）。
func ringsMerge(rects []ringsRect, cap int) []ringsRect {
	if len(rects) <= cap {
		return rects
	}
	byRow := map[int][]ringsRect{}
	var rows []int
	for _, r := range rects {
		if _, ok := byRow[r.row]; !ok {
			rows = append(rows, r.row)
		}
		byRow[r.row] = append(byRow[r.row], r)
	}
	group := 2
	for group < len(rects) {
		total := 0
		for _, row := range rows {
			total += (len(byRow[row]) + group - 1) / group
		}
		if total <= cap {
			break
		}
		group++
	}
	var out []ringsRect
	for _, row := range rows {
		list := byRow[row]
		for i := 0; i < len(list); i += group {
			end := min(len(list), i+group)
			left, right := list[i].cx-list[i].hw, list[i].cx+list[i].hw
			top, bottom := list[i].cy-list[i].hh, list[i].cy+list[i].hh
			for _, r := range list[i+1 : end] {
				left, right = min(left, r.cx-r.hw), max(right, r.cx+r.hw)
				top, bottom = min(top, r.cy-r.hh), max(bottom, r.cy+r.hh)
			}
			out = append(out, ringsRect{cx: (left + right) / 2, cy: (top + bottom) / 2, hw: (right - left) / 2, hh: (bottom - top) / 2, row: row})
		}
	}
	return out
}

// ringsLightProgress 是字的点亮进度：用时取字长，夹在 0.12–0.45 秒。
func ringsLightProgress(pos time.Duration, g ringsGlyph) float32 {
	duration := float32((g.end - g.start).Seconds())
	duration = min(ringsLightMax, max(ringsLightMin, duration))
	return clamp01(float32((pos - g.start).Seconds()) / duration)
}

// drawRingsGlyphs 画一句：没唱到的是 30% 白，唱到的按进度叠亮并鼓起。
func (d colorStagePaint) drawRingsGlyphs(l *ringsLayout, alpha float32) {
	if l == nil || alpha <= 0 {
		return
	}
	for i := range l.glyphs {
		g := &l.glyphs[i]
		if len(g.glyphs) == 0 {
			continue
		}
		progress := float32(1)
		if d.s.motion != 0 {
			progress = ringsLightProgress(d.s.pos, *g)
		}
		lit := easeOutCubic(progress)
		scale := g.scale
		if d.s.motion != 0 && progress > 0 && progress < 1 {
			scale *= 1 + ringsSwell*float32(math.Sin(math.Pi*float64(progress)))
		}
		transform := func(draw func()) {
			d.p.Transform(ui.Rotation(g.rotation).Then(ui.Scaling(scale, scale)), g.x, g.y, draw)
		}
		// 暗字按行盒居中画；亮字连同阴影走同一份排版，两种画法共用同一组度量才对得齐。
		transform(func() {
			d.p.Glyphs(g.glyphs, g.x-g.width/2, g.y+(l.ascent-l.descent)/2, white.Alpha(ringsDim*alpha))
		})
		if lit <= 0 {
			continue
		}
		level := int(math.Round(float64(clamp01(lit*alpha)) * cinGlowLevels))
		if level <= 0 {
			continue
		}
		// 原版唱到的字一直挂着同色阴影（半径 0.39 字高），不只点亮途中。
		blur := 2 * (.57735*l.size*ringsGlowRadius + .5)
		layout := glowLayoutFor(g.text, l.font, &g.glow, blur, level, d.s.accent)
		_, h := layout.Size()
		transform(func() {
			d.p.TextLayout(layout, g.x-g.width/2, g.y-h/2, white.Alpha(lit*alpha))
		})
	}
}

// drawRings 按歌曲时间画年轮：每 0.72 秒一圈，从字身外扩 0.55 个字高开始往外走。
func (d colorStagePaint) drawRings(l *ringsLayout, prev *ringsLayout) {
	s := d.s
	if l == nil {
		return
	}
	fade := float32(1)
	if s.motion != 0 {
		fade = clamp01(float32((s.pos - l.at).Seconds()) / ringsLineFade)
	}
	// 上一句淡出，别在换句时整行突然消失。
	if prev != nil && s.motion != 0 {
		out := 1 - clamp01(float32((s.pos-prev.end).Seconds())/ringsLineFade)
		d.drawRingsGlyphs(prev, out*(1-fade))
	}

	halfDiagonal := float32(math.Hypot(float64(s.w), float64(s.h))) / 2
	speed := halfDiagonal / 10
	energy := float32(0.5)
	if s.hasLevels && len(s.levels) > 0 {
		var bass float32
		for i := 0; i < len(s.levels)/3; i++ {
			bass = max(bass, clamp01(s.levels[i]))
		}
		energy = .5 + .5*bass
	}
	// 年轮只取当前句开始之后、最近 6 拍的那些：更早的属于上一句，形状已经换人了。
	now := max(0, s.pos.Seconds())
	if s.motion == 0 {
		// 减少动效时整页静止：年轮停在换句那一刻，不随播放时间往外走。
		now = max(0, l.at.Seconds())
	}
	births := int(now / ringsBeat)
	oldest := max(0, int(float64(l.at.Seconds())/ringsBeat))
	for k := births; k >= 0 && k > births-6 && k >= oldest; k-- {
		birth := float64(k) * ringsBeat
		radius := float32((now - birth) * float64(speed))
		if radius > halfDiagonal {
			break
		}
		// MyGo 没有路径并集，按行合并的矩形看起来才像一圈完整轮廓；逐字矩形会叠成一堆药丸。
		elements := l.coarse
		strength := (0.25 + 0.75*energy) * powRings(1-radius/halfDiagonal, 1.8)
		alpha := (1 - float32(math.Exp(-1.6*float64(strength)))) * ringsOpacity
		if alpha < .004 {
			continue
		}
		ink := s.accent
		if ink.A == 0 {
			ink = ui.RGB(101, 186, 255)
		}
		ink = ink.Mix(white, clamp01(radius/halfDiagonal*1.6))
		width := max(1, 2*(0.5+1.9*powRings(energy, 1.5)))
		inflate := ringsGap*l.size + radius
		for _, r := range elements {
			rect := ui.Rect{X: r.cx - r.hw - inflate, Y: r.cy - r.hh - inflate, W: (r.hw + inflate) * 2, H: (r.hh + inflate) * 2}
			corner := min(ringsCorner*l.size+inflate, min(rect.W, rect.H)/2)
			d.p.Stroke(rect, ink.Alpha(alpha), corner, width)
		}
	}
	// 字压在年轮上面，和 myMusic 一样年轮只当背景。
	d.drawRingsGlyphs(l, fade)
}

func powRings(v, p float32) float32 {
	if v <= 0 {
		return 0
	}
	return float32(math.Pow(float64(v), float64(p)))
}
