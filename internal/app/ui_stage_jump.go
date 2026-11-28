package app

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// 跳一跳照 myMusic 的 visualizer-jump 1.2.0 移植（作者 tumuyan，AGPL-3.0）：
// 一句歌词切成几块，每块是一根等轴测方柱，字贴在柱子的顶面上；柱子一行一行折成 Z 字，
// 小人在柱顶之间跳，踩下去的柱子弹簧似的弹回来，落地踩出椭圆波纹和尘土；镜头跟着时间表走，
// 跳过的那几段弧线留下渐淡的点迹；译文浮在柱子后面，按俯角压扁、往右上侧倾。
//
// 移植取舍：MyGo 没有模糊和混合模式，落地阴影、译文光晕用多层错位和透明渐变近似；
// 歇脚柱用“上下椭圆 + 侧壁”画，不再逐段求弧。所有位置都由播放时刻直接算出，不读上一帧，
// 拖进度条和暂停看到的是同一帧。
const (
	jumpSqrt2          = 1.4142135
	jumpDeg            = math.Pi / 180
	jumpSpacing        = 1.2
	jumpPillarHeight   = 1.9
	jumpRestGap        = 1.75
	jumpRunnerScale    = 1.2
	jumpRunnerRatio    = 2.6
	jumpTextFill       = .78
	jumpFaceSpan       = 1.35
	jumpMinUnit        = .8
	jumpRestDia        = 1.9
	jumpSideStem       = 1.8
	jumpMaxChunks      = 5
	jumpMaxChunkChars  = 10
	jumpImpactSeconds  = .36
	jumpImpactDust     = 6
	jumpReferencePitch = 34
	jumpTrailAlpha     = .5
	jumpFaceMix        = .8
	jumpOtherRow       = .9
)

// jumpFaceOpacity 是原模组的柱面浓度旋钮：own·mix + (1−own)·mix³，mix=1 为实心。
func jumpFaceOpacity(own float32) float32 {
	t := float32(jumpFaceMix)
	return max(0, min(1, own*t+(1-own)*t*t*t))
}

// 柱面三档浓度：顶面最亮、正面稍暗、侧面中间。
var (
	jumpLitAlpha   = jumpFaceOpacity(.17)
	jumpShadeAlpha = jumpFaceOpacity(.08)
	jumpEdgeAlpha  = jumpFaceOpacity(.42)
)

type jumpFace int

const (
	jumpTop jumpFace = iota
	jumpFront
	jumpSide
	jumpAbove
)

// jumpProjection 是方位 45°、俯角 pitch 的正交投影，一格宽 1。
type jumpProjection struct{ pitch, sin, cos, lift float32 }

func newJumpProjection(pitch float32) jumpProjection {
	p := min(85, max(5, pitch))
	sin, cos := math.Sincos(float64(p) * jumpDeg)
	s, c := float32(sin), float32(cos)
	return jumpProjection{pitch: p, sin: s, cos: c, lift: c / jumpSqrt2}
}

func (p jumpProjection) x(wx, wy float32) float32 { return -.5*wx + .5*wy }
func (p jumpProjection) y(wx, wy, wz float32) float32 {
	return -.5*p.sin*wx - .5*p.sin*wy - p.lift*wz
}
func (p jumpProjection) axisX(alongX bool) float32 {
	if alongX {
		return -.5
	}
	return .5
}
func (p jumpProjection) axisY() float32 { return -.5 * p.sin }

// ---------------------------------------------------------------- 柱子的尺寸

type jumpShape struct{ length, depth, height float32 }

func jumpPillarHeightOf(face jumpFace, heightCells float32) float32 {
	h := max(.2, heightCells)
	if face == jumpSide {
		return min(2.6, max(.35, h*1.3))
	}
	return h
}

func jumpTextAspect(span, glyph float32) float32 {
	glyph = min(1.4, max(.4, glyph))
	return max(jumpMinUnit, span) / glyph
}

func jumpBlockShape(face jumpFace, span, height, glyph float32) jumpShape {
	aspect := jumpTextAspect(span, glyph)
	h := max(.2, height)
	if face == jumpSide {
		return jumpShape{jumpSideStem, jumpClamp(aspect/.4, .8, 12), h}
	}
	length := jumpClamp(max(jumpMinUnit, aspect)/.4, .6, 12)
	across := jumpClamp(h/.76, min(jumpFaceSpan, .8), 12)
	return jumpShape{length, across, h}
}

// jumpBoxGeometry 是一根方柱在屏幕上的三个面与贴面字盒（格，原点柱底中心）。
type jumpBoxGeometry struct {
	top, front, side       [8]float32
	labelW, labelH         float32
	label                  [6]float32
	minX, minY, maxX, maxY float32
}

func jumpBoxOf(p jumpProjection, alongX bool, shape jumpShape, face jumpFace, aspect float32) jumpBoxGeometry {
	length, depth := max(.1, shape.length), max(.1, shape.depth)
	height := max(0, shape.height)
	boxX := func(u, w float32) float32 {
		if alongX {
			return p.x(u, w)
		}
		return p.x(w, u)
	}
	boxY := func(u, w, z float32) float32 {
		if alongX {
			return p.y(u, w, z)
		}
		return p.y(w, u, z)
	}
	cx, cy := boxX(length/2, depth/2), boxY(length/2, depth/2, 0)
	quad := func(points ...[3]float32) [8]float32 {
		var out [8]float32
		for i, v := range points {
			out[i*2] = boxX(v[0], v[1]) - cx
			out[i*2+1] = boxY(v[0], v[1], v[2]) - cy
		}
		return out
	}
	top := quad([3]float32{0, 0, height}, [3]float32{length, 0, height}, [3]float32{length, depth, height}, [3]float32{0, depth, height})
	front := quad([3]float32{0, 0, height}, [3]float32{length, 0, height}, [3]float32{length, 0, 0}, [3]float32{0, 0, 0})
	side := quad([3]float32{0, 0, height}, [3]float32{0, depth, height}, [3]float32{0, depth, 0}, [3]float32{0, 0, 0})

	alongVx, alongVy := p.axisX(alongX), p.axisY()
	crossVx, crossVy := p.axisX(!alongX), p.axisY()
	downY := p.lift

	var label [6]float32
	var labelW, labelH float32
	if face == jumpAbove {
		// 悬浮：屏幕正立，底边往上留四分之一字高。
		room := jumpTypeBox(float32(math.Hypot(float64(length*alongVx), float64(length*alongVy))), depth, aspect)
		labelW, labelH = room[0], room[1]
		c := jumpCenterOf(top)
		label[0], label[3] = 1, 1
		label[4] = c[0] - labelW/2
		label[5] = c[1] - labelH*1.25
	} else {
		// 面上的二维坐标系：u 是横排方向，v 是“下”。
		var ux, uy, vx, vy, uSpan, vSpan float32
		var q [8]float32
		switch face {
		case jumpFront:
			ux, uy, vx, vy, uSpan, vSpan, q = alongVx, alongVy, 0, downY, length, height, front
		case jumpSide:
			ux, uy, vx, vy, uSpan, vSpan, q = -crossVx, -crossVy, 0, downY, depth, height, side
		default:
			ux, uy, vx, vy, uSpan, vSpan, q = alongVx, alongVy, -crossVx, -crossVy, length, depth, top
		}
		room := jumpTypeBox(uSpan, vSpan, aspect)
		labelW, labelH = room[0], room[1]
		sign := float32(1)
		if ux*vy-uy*vx < 0 {
			sign = -1
		}
		c := jumpCenterOf(q)
		label[0] = sign * ux
		label[1] = sign * uy
		label[2] = vx
		label[3] = vy
		label[4] = c[0] - (sign*ux*labelW/2 + vx*labelH/2)
		label[5] = c[1] - (sign*uy*labelW/2 + vy*labelH/2)
	}

	minX, minY := float32(math.MaxFloat32), float32(math.MaxFloat32)
	maxX, maxY := -float32(math.MaxFloat32), -float32(math.MaxFloat32)
	for _, q := range [][8]float32{top, front, side} {
		for i := range 4 {
			minX, maxX = min(minX, q[i*2]), max(maxX, q[i*2])
			minY, maxY = min(minY, q[i*2+1]), max(maxY, q[i*2+1])
		}
	}
	if face == jumpAbove {
		minX = min(minX, label[4])
		maxX = max(maxX, label[4]+labelW)
		minY = min(minY, label[5])
	}
	return jumpBoxGeometry{top: top, front: front, side: side, labelW: labelW, labelH: labelH, label: label,
		minX: minX, minY: minY, maxX: maxX, maxY: maxY}
}

// jumpTypeBox：字盒按长宽比顶满窗的 78%，出框整体缩回。
func jumpTypeBox(roomW, roomH, aspect float32) [2]float32 {
	a := max(.05, aspect)
	w, h := max(.05, roomW)*jumpTextFill, max(.05, roomH)*jumpTextFill
	var baseW, baseH float32
	if w/a <= h {
		baseW, baseH = w, w/a
	} else {
		baseW, baseH = h*a, h
	}
	fit := min(1, min(w/baseW, h/baseH))
	return [2]float32{baseW * fit, baseH * fit}
}

func jumpCenterOf(q [8]float32) [2]float32 {
	var x, y float32
	for i := range 4 {
		x += q[i*2]
		y += q[i*2+1]
	}
	return [2]float32{x / 4, y / 4}
}

// ---------------------------------------------------------------- 取景

type jumpFrameInput struct {
	face                                                             jumpFace
	heightCells, widestDepth, widestCross, spacing, runnerScale, arc float32
}

type jumpBudget struct{ below, above, crown, crownTop, roadSpread float32 }

func jumpBudgetOf(pitchDeg float32, in jumpFrameInput) jumpBudget {
	pitch := jumpClamp(pitchDeg, 5, 85)
	s, c := math.Sincos(float64(pitch) * jumpDeg)
	sf, cf := float32(s), float32(c)
	widestDepth, widestCross := max(.1, in.widestDepth), max(.1, in.widestCross)
	rise := sf / 2
	climb := (jumpMaxChunks + 2 + widestDepth) * rise
	lane := (jumpMaxChunks + 2 + max(.05, in.spacing)) * rise
	pillar := max(.2, in.heightCells) * cf / jumpSqrt2
	crown := float32(jumpTextFill * jumpFaceSpan)
	if in.face == jumpSide {
		crown = pillar + widestDepth*cf/jumpSqrt2
	}
	runner := jumpRunnerRatio * max(.1, in.runnerScale) * cf / jumpSqrt2
	arc := max(0, in.arc) * cf / jumpSqrt2
	crownTop := crown + max(jumpTextFill, runner+arc) + .35
	below := 2*climb + lane + crown + .35
	above := 2*climb + lane + crownTop + .35
	cross := (widestDepth + widestCross) / jumpSqrt2
	spread := max(max(3*(jumpMaxChunks+2)/jumpSqrt2, (jumpMaxChunks+2+2*widestDepth)/jumpSqrt2),
		(jumpMaxChunks+2+widestDepth)*rise+cross)
	return jumpBudget{below, above, crown, crownTop, spread}
}

func jumpFitCell(width, height float32, b jumpBudget) float32 {
	w, h := max(1, width), max(1, height)
	return min(min(h*.92/(b.below+b.above), w*.82/max(1, b.roadSpread)), min(h/2.8, w/3))
}

func jumpFrameOf(width, height, pitchDeg float32, in jumpFrameInput, floorPx float32) (cell, anchor float32) {
	budget := jumpBudgetOf(pitchDeg, in)
	reference := jumpBudgetOf(jumpReferencePitch, in)
	fit := min(jumpFitCell(width, height, budget), jumpFitCell(width, height, reference))
	need := budget.crown + budget.crownTop
	cell = min(max(floorPx, fit), max(1, height)*.95/need)
	cell = max(1, cell)
	ideal := budget.below * cell
	if (budget.below+budget.above)*cell <= height {
		return cell, ideal
	}
	share := height * budget.below / (budget.below + budget.above)
	return cell, jumpClamp(share, budget.crown*cell, max(budget.crown*cell, height-budget.crownTop*cell))
}

// ---------------------------------------------------------------- 切块

type jumpChunk struct {
	text    string
	display string
	chars   int
}

func jumpTrimSpaces(text string) string { return strings.TrimSpace(text) }

func jumpPunctuationOnly(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

func jumpAllWide(text string) bool {
	for _, r := range text {
		if !jumpIsWide(r) {
			return false
		}
	}
	return true
}

func jumpIsWide(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		r >= 0x2E80 && r <= 0xA4CF || r >= 0xF900 && r <= 0xFAFF || r >= 0xFE30 && r <= 0xFE4F ||
		r >= 0xFF00 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6
}

func jumpCharWidth(r rune) float32 {
	if unicode.IsSpace(r) {
		return 0
	}
	if jumpIsWide(r) {
		return 1
	}
	return .55
}

func jumpWidthOf(text string) float32 {
	var w float32
	for _, r := range text {
		w += jumpCharWidth(r)
	}
	return w
}

func jumpIsLatinJoint(r rune) bool {
	return strings.ContainsRune("-‐‑–—'’", r)
}

func jumpIsLatinWord(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

// jumpRuneRunesSegments 是没有逐字时间时的分词：拉丁词整段、中日韩逐字、空白单独。
func jumpRuneSegments(text string) []string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			flush()
			out = append(out, string(r))
		case jumpIsWide(r):
			flush()
			out = append(out, string(r))
		default:
			word.WriteRune(r)
		}
	}
	flush()
	return out
}

func jumpSegments(text string, words []lyrics.Word) []string {
	if len(words) > 0 {
		var joined strings.Builder
		for _, w := range words {
			joined.WriteString(w.Text)
		}
		if joined.String() == text {
			out := make([]string, 0, len(words))
			for _, w := range words {
				out = append(out, w.Text)
			}
			return out
		}
	}
	return jumpRuneSegments(text)
}

// jumpSplitChunks 把分词切成一块块：相邻中日韩词合并（不超过 maxChunkChars），超宽再切，标点黏在前一块上。
func jumpSplitChunks(segments []string, maxChunkChars int) []jumpChunk {
	maxChars := float32(max(1, maxChunkChars))
	var merged []string
	for _, text := range segments {
		if text == "" {
			continue
		}
		trimmed := jumpTrimSpaces(text)
		if jumpPunctuationOnly(text) || trimmed == "" {
			if len(merged) > 0 {
				merged[len(merged)-1] += text
			} else {
				merged = append(merged, text)
			}
			continue
		}
		lastTrimmed := ""
		if len(merged) > 0 {
			lastTrimmed = jumpTrimSpaces(merged[len(merged)-1])
		}
		if lastTrimmed != "" && jumpAllWide(lastTrimmed) && jumpAllWide(trimmed) &&
			jumpWidthOf(merged[len(merged)-1])+jumpWidthOf(trimmed) <= maxChars {
			merged[len(merged)-1] += text
			continue
		}
		merged = append(merged, text)
	}
	var chunks []string
	push := func(text string) {
		if text == "" {
			return
		}
		if len(chunks) > 0 && jumpPunctuationOnly(text) {
			chunks[len(chunks)-1] += text
			return
		}
		chunks = append(chunks, text)
	}
	for _, text := range merged {
		trimmed := jumpTrimSpaces(text)
		if jumpPunctuationOnly(text) || trimmed == "" {
			push(text)
			continue
		}
		pieces := []string{trimmed}
		if jumpWidthOf(trimmed) > maxChars {
			pieces = jumpSplitText(trimmed, maxChunkChars)
		}
		at := strings.Index(text, trimmed)
		leading, trailing := text[:at], text[at+len(trimmed):]
		for i, piece := range pieces {
			slice := piece
			if i == 0 {
				slice = leading + slice
			}
			if i == len(pieces)-1 {
				slice += trailing
			}
			push(slice)
		}
	}
	out := make([]jumpChunk, len(chunks))
	for i, text := range chunks {
		out[i] = jumpChunk{text: text, display: jumpTrimSpaces(text), chars: utf8.RuneCountInString(text)}
	}
	return out
}

func jumpCutByWidth(text string, maxChars int) []string {
	limit := float32(max(1, maxChars))
	var pieces []string
	var current strings.Builder
	var currentW float32
	flush := func() {
		if current.Len() > 0 {
			pieces = append(pieces, current.String())
			current.Reset()
			currentW = 0
		}
	}
	var run int
	for _, r := range text {
		if jumpIsLatinJoint(r) && current.Len() > 0 {
			current.WriteRune(r)
			flush()
			continue
		}
		width := jumpCharWidth(r)
		nextRun := 0
		if jumpIsLatinWord(r) {
			nextRun = run + 1
		}
		if current.Len() > 0 && currentW+width-float32(nextRun)*width > limit {
			flush()
		}
		current.WriteRune(r)
		currentW += width
		run = nextRun
	}
	flush()
	return pieces
}

func jumpSplitText(text string, maxChars int) []string {
	var words []string
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if unicode.IsSpace(r) {
			words = append(words, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	var out []string
	for _, word := range words {
		var joint strings.Builder
		var jointed []string
		for _, r := range word {
			joint.WriteRune(r)
			if jumpIsLatinJoint(r) {
				jointed = append(jointed, joint.String())
				joint.Reset()
			}
		}
		if joint.Len() > 0 {
			jointed = append(jointed, joint.String())
		}
		out = append(out, jumpCutByWidth(strings.Join(jointed, ""), maxChars)...)
	}
	return out
}

func jumpChunksFor(segments []string, chunkChars, charCount int) []jumpChunk {
	capn := max(2, jumpMaxChunks)
	if charCount > 0 {
		capn = min(capn, max(2, charCount))
	}
	per := max(1, chunkChars)
	chunks := jumpSplitChunks(segments, per)
	for len(chunks) > capn && per < jumpMaxChunkChars {
		per++
		wider := jumpSplitChunks(segments, per)
		if len(wider) >= len(chunks) {
			break
		}
		chunks = wider
	}
	return chunks
}

// ---------------------------------------------------------------- 时间

type jumpLineTimes struct{ start, end, enter, voicedEnd float64 }

func jumpLineTimesOf(line *lyrics.Line) jumpLineTimes {
	start := line.At.Seconds()
	end := line.End.Seconds()
	enter, voiced := math.NaN(), math.NaN()
	if len(line.Words) > 0 {
		enter = line.Words[0].Start.Seconds()
		last := line.Words[len(line.Words)-1]
		if !(len(line.Words) == 1 && math.Abs(last.End.Seconds()-end) < .001) {
			voiced = last.End.Seconds()
		}
	}
	return jumpLineTimes{start, end, enter, voiced}
}

// jumpCharTimings 是这一句每个字的起止（秒），长 2n。
func jumpCharTimings(text string, startS, endS float64, words []lyrics.Word) []float64 {
	count := utf8.RuneCountInString(text)
	end := endS
	if !(endS > startS) {
		end = startS + max(.5, float64(count)*.12)
	}
	out := make([]float64, count*2)
	if len(words) > 0 && jumpFillFromWords(text, startS, end, words, out) {
		return out
	}
	step := max(.02, (end-startS)/float64(max(1, count)))
	for i := 0; i < count; i++ {
		out[i*2] = startS + step*float64(i)
		out[i*2+1] = startS + step*float64(i+1)
	}
	return out
}

func jumpFillFromWords(text string, start, end float64, words []lyrics.Word, out []float64) bool {
	var chars []rune
	var starts, ends []float64
	for _, w := range words {
		wordStart := w.Start.Seconds()
		wordEnd := w.End.Seconds()
		if !(wordEnd > wordStart) {
			wordEnd = end
		}
		runes := []rune(w.Text)
		if len(runes) == 0 {
			continue
		}
		step := max(.02, (wordEnd-wordStart)/float64(len(runes)))
		for k, r := range runes {
			chars = append(chars, r)
			starts = append(starts, wordStart+step*float64(k))
			ends = append(ends, wordStart+step*float64(k+1))
		}
	}
	if len(chars) == 0 || (len(words) == 1 && len(chars) > 1) {
		return false
	}
	count := len(out) / 2
	var joined strings.Builder
	for _, w := range words {
		joined.WriteString(w.Text)
	}
	if len(chars) == count && joined.String() == text {
		copy(out, interleave(starts, ends))
		return true
	}
	// 按非空白字对齐，空白取前一个字的收尾。
	w, c := 0, 0
	last := start
	for _, r := range text {
		if unicode.IsSpace(r) {
			out[c*2], out[c*2+1] = last, last
		} else {
			for w < len(chars) && unicode.IsSpace(chars[w]) {
				w++
			}
			if w >= len(chars) || chars[w] != r {
				return false
			}
			out[c*2], out[c*2+1] = starts[w], ends[w]
			last = ends[w]
			w++
		}
		c++
	}
	return true
}

func interleave(a, b []float64) []float64 {
	out := make([]float64, 0, len(a)*2)
	for i := range a {
		out = append(out, a[i], b[i])
	}
	return out
}

type jumpWindow struct{ writeStart, writeEnd, afterStart, afterEnd float64 }

func jumpWindowsOf(lines []jumpLineTimes) []jumpWindow {
	enter := func(l jumpLineTimes) float64 {
		if !math.IsNaN(l.enter) {
			return l.enter
		}
		return l.start
	}
	out := make([]jumpWindow, len(lines))
	for i, line := range lines {
		writeStart := enter(line)
		nextEnter := math.NaN()
		if i+1 < len(lines) {
			nextEnter = enter(lines[i+1])
		}
		renderEnd := line.end
		reserve := .12
		if !math.IsNaN(nextEnter) {
			reserve = min(.28, max(.12, (nextEnter-writeStart)*.15))
		}
		writeEnd := writeStart
		if !math.IsNaN(nextEnter) {
			writeEnd = max(writeStart, min(renderEnd, nextEnter-reserve))
		} else {
			writeEnd = max(writeStart, renderEnd)
		}
		afterEnd := writeEnd
		if !math.IsNaN(nextEnter) {
			afterEnd = nextEnter
		} else {
			afterEnd = max(writeEnd, renderEnd)
		}
		motion := jumpMotionWindow(line.start, writeEnd, afterEnd, line.voicedEnd, renderEnd)
		window := jumpWindow{writeStart: writeStart, writeEnd: writeEnd, afterStart: writeEnd, afterEnd: afterEnd}
		if motion != nil {
			window.afterStart, window.afterEnd = motion[0], motion[1]
		}
		out[i] = window
	}
	return out
}

func jumpMotionWindow(start, end, nextStart, voicedEnd, motionEnd float64) []float64 {
	voiced := voicedEnd
	if math.IsNaN(voiced) {
		voiced = end
	}
	target := nextStart
	if math.IsNaN(target) {
		target = end
	}
	if !(target > start) {
		return nil
	}
	span := target - start
	motionRoom := span
	if !math.IsNaN(motionEnd) {
		motionRoom = max(0, motionEnd-start)
	}
	fallback := start + max(0, min(span, min(motionRoom, span*.85)))
	estimate := math.IsNaN(voiced) || (!math.IsNaN(motionEnd) && voiced > motionEnd+1e-6)
	sung := fallback
	if !estimate {
		sung = min(max(voiced, start), target)
	}
	afterStart := min(sung, target-.16)
	return []float64{max(start, afterStart), target}
}

func jumpShiftFly(flySeconds float64) float64 { return jumpClamp64(flySeconds*1.5, .14, 1.5) }

func jumpRestPillar(release, nextStart, flySeconds float64) []float64 {
	if math.IsNaN(release) || math.IsNaN(nextStart) {
		return nil
	}
	target := max(release, nextStart)
	if !(target > release) {
		return nil
	}
	fly := jumpShiftFly(flySeconds)
	if !(target-release > max(max(0, jumpRestGap), fly*2)) {
		return nil
	}
	return []float64{release + fly, target - fly}
}

func jumpShiftLanding(release, nextStart, flySeconds float64) float64 {
	if math.IsNaN(release) || math.IsNaN(nextStart) || !(nextStart > release) {
		return math.NaN()
	}
	target := max(release, nextStart)
	if jumpRestPillar(release, nextStart, flySeconds) != nil {
		return target
	}
	return min(release+jumpShiftFly(flySeconds), target)
}

type jumpHopTime struct {
	from, to                  int
	start, liftTime, landTime float64
}
type jumpSpan struct{ begin, end float64 }

type jumpTimeline struct {
	start, end   float64
	landings     []float64
	hops         []jumpHopTime
	pressTimes   []float64
	releaseTimes []float64
	cameraTimes  []float64
	gap          float64
	lastRelease  float64
	tailSpan     jumpSpan
	cameraTail   *jumpSpan
	restIndex    int
	gapShift     *jumpSpan
	shiftLand    float64
	shiftHold    *jumpSpan
}

func jumpTimelineOf(startTime, endTime float64, count int, flySeconds float64, landTimes []float64,
	lastRelease float64, rest []float64, entryLand, nextStart float64) jumpTimeline {
	fly := jumpClamp64(flySeconds, .05, 3)
	shiftFly := jumpShiftFly(flySeconds)
	start := startTime
	if math.IsNaN(start) {
		start = 0
	}
	end := endTime
	if math.IsNaN(end) || !(end > start) {
		end = start + 2
	}
	total := max(0, count)
	span := end - start
	steps := span / float64(max(1, total))
	minGap := jumpClamp64(steps*.35, .06, .3)
	restIndex := -1
	if rest != nil && !math.IsNaN(rest[0]) && total > 1 {
		restIndex = total - 1
	}
	restLift := math.NaN()
	if restIndex >= 0 {
		restLift = rest[1]
	}
	landings := make([]float64, total)
	previous := start
	for i := 0; i < total; i++ {
		due := start + steps*float64(i)
		if i < len(landTimes) && !math.IsNaN(landTimes[i]) {
			due = landTimes[i]
		}
		landing := due
		switch {
		case i == 0:
			landing = min(start, due)
		case i == restIndex:
		default:
			landing = max(due, min(previous+minGap, end))
		}
		landings[i] = max(previous, landing)
		previous = landings[i]
	}
	release := lastRelease
	if math.IsNaN(release) {
		release = end
	}
	lastLanding := start
	if total > 0 {
		lastLanding = landings[total-1]
	}
	tailEnd := release
	if !math.IsNaN(nextStart) {
		tailEnd = max(release, nextStart)
	}
	tailRelease := tailEnd > lastLanding+1e-6
	shiftLanding := lastLanding
	switch {
	case restIndex >= 0:
		shiftLanding = tailEnd
	case tailRelease:
		shiftLanding = min(release+shiftFly, tailEnd)
	}
	hops := make([]jumpHopTime, 0, max(0, total-1))
	for i := 0; i < total-1; i++ {
		hopStart, hopEnd := landings[i], landings[i+1]
		share := jumpClamp64(fly/max(.05, hopEnd-hopStart), .15, .95)
		air := (hopEnd - hopStart) * share
		lift := hopEnd - air
		if i+1 == restIndex {
			lift = release
		}
		hops = append(hops, jumpHopTime{from: i, to: i + 1, start: hopStart, liftTime: lift, landTime: hopEnd})
	}
	leave := make([]float64, total)
	for i := range leave {
		switch {
		case i == restIndex && !math.IsNaN(restLift):
			leave[i] = restLift
		case i < len(hops):
			leave[i] = hops[i].liftTime
		default:
			leave[i] = release
		}
	}
	var camera []float64
	for i := 0; i < total; i++ {
		landing := landings[i]
		switch {
		case i == 0:
			camera = append(camera, landing)
		case i == restIndex:
			camera = append(camera, landing+min(max(0, leave[i]-landing)/2, .35))
		default:
			camera = append(camera, landing+max(0, leave[i]-landing)/2)
		}
	}
	cameraStop := start
	if len(camera) > 0 {
		cameraStop = camera[len(camera)-1]
	}
	if tailRelease && restIndex < 0 {
		camera = append(camera, tailEnd)
	}
	for i := 1; i < len(camera); i++ {
		if camera[i] <= camera[i-1] {
			camera[i] = camera[i-1] + 1e-3
		}
	}
	tailBegin := cameraStop
	if restIndex >= 0 && !math.IsNaN(restLift) {
		tailBegin = max(cameraStop+1e-3, restLift-shiftFly)
	}
	var cameraTail *jumpSpan
	if tailRelease && !math.IsNaN(nextStart) && shiftLanding > tailBegin {
		cameraTail = &jumpSpan{tailBegin, shiftLanding}
	}
	press := make([]float64, total)
	for i := range press {
		if i == 0 {
			if !math.IsNaN(entryLand) {
				press[i] = min(entryLand, start)
			} else {
				press[i] = start
			}
		} else {
			press[i] = hops[i-1].landTime
		}
	}
	gap := steps
	if total > 1 && landings[total-1] > landings[0] {
		gap = (landings[total-1] - landings[0]) / float64(total-1)
	}
	tl := jumpTimeline{
		start: start, end: end, landings: landings, hops: hops, pressTimes: press, releaseTimes: leave,
		cameraTimes: camera, gap: gap, lastRelease: release,
		tailSpan: jumpSpan{lastLanding, lastLanding}, restIndex: -1, shiftLand: math.NaN(),
	}
	if tailRelease {
		tl.tailSpan.end = tailEnd
	}
	if !math.IsNaN(restLift) {
		tl.restIndex = restIndex
	}
	if tailRelease {
		switch {
		case restIndex >= 0 && !math.IsNaN(restLift):
			tl.gapShift = &jumpSpan{restLift, tailEnd}
		default:
			tl.gapShift = &jumpSpan{release, shiftLanding}
		}
	}
	tl.shiftLand = math.NaN()
	if tailRelease {
		tl.shiftLand = shiftLanding
	}
	if tailRelease && restIndex < 0 {
		tl.shiftHold = &jumpSpan{shiftLanding, tailEnd}
	}
	tl.cameraTail = cameraTail
	return tl
}

// ---------------------------------------------------------------- 一条路

type jumpUnit struct {
	text     string
	shape    jumpShape
	rest     bool
	sx, sy   float32
	tx, ty   float32
	box      *jumpBoxGeometry
	cylinder *jumpCylinder
}

type jumpCylinder struct{ rx, ry, cy float32 }

type jumpRow struct {
	lineIndex    int
	units        []jumpUnit
	window       jumpWindow
	timeline     jumpTimeline
	alongX       bool
	direction    int
	cornerX      float32
	cornerY      float32
	revealStart  float64
	revealBudget float64
	hasWords     bool
}

type jumpConfig struct {
	face       jumpFace
	pitch      float32
	chunkChars int
	flySeconds float32
	arc        float32
}

type jumpCourse struct {
	rows       []jumpRow
	windows    []jumpWindow
	rowOfLine  []int
	projection jumpProjection
	frameInput jumpFrameInput
	config     jumpConfig
	maxUnits   int
}

type jumpDraftUnit struct {
	text  string
	span  float32
	start float64
	rest  bool
}

type jumpDraftRow struct {
	lineIndex int
	units     []jumpDraftUnit
	times     jumpLineTimes
	hasWords  bool
}

func jumpBuildCourse(lines []lyrics.Line, config jumpConfig, measure func(string) float32) *jumpCourse {
	face := config.face
	height := jumpPillarHeightOf(face, jumpPillarHeight)
	fly := float64(config.flySeconds)
	var drafts []jumpDraftRow
	for i := range lines {
		line := &lines[i]
		text := line.Text
		if strings.TrimSpace(text) == "" {
			continue
		}
		segments := jumpSegments(text, line.Words)
		chunks := jumpChunksFor(segments, config.chunkChars, utf8.RuneCountInString(text))
		timings := jumpCharTimings(text, line.At.Seconds(), line.End.Seconds(), line.Words)
		var units []jumpDraftUnit
		cursor := 0
		for _, chunk := range chunks {
			from := cursor
			cursor += chunk.chars
			if chunk.display == "" {
				continue
			}
			first := from
			for _, r := range chunk.text {
				if !unicode.IsSpace(r) {
					break
				}
				first++
			}
			at := max(0, min(first, len(timings)/2-1))
			start := line.At.Seconds()
			if len(timings) > 0 {
				start = timings[at*2]
			}
			units = append(units, jumpDraftUnit{text: chunk.display, span: max(.05, measure(chunk.display)), start: start})
		}
		if len(units) == 0 {
			continue
		}
		drafts = append(drafts, jumpDraftRow{lineIndex: i, units: units, times: jumpLineTimesOf(line), hasWords: len(line.Words) > 1})
	}
	times := make([]jumpLineTimes, len(drafts))
	for i := range drafts {
		times[i] = drafts[i].times
	}
	windows := jumpWindowsOf(times)
	projection := newJumpProjection(config.pitch)
	timelines := make([]jumpTimeline, len(drafts))
	unitLists := make([][]jumpDraftUnit, len(drafts))
	for index, draft := range drafts {
		window := windows[index]
		var nextStart, prevAfter float64 = math.NaN(), math.NaN()
		if index+1 < len(windows) {
			nextStart = windows[index+1].writeStart
		}
		if index > 0 {
			prevAfter = windows[index-1].afterStart
		}
		entryLand := jumpShiftLanding(prevAfter, window.writeStart, fly)
		release := window.afterStart
		rest := jumpRestPillar(release, nextStart, fly)
		units := draft.units
		if rest != nil {
			units = append(append([]jumpDraftUnit(nil), units...), jumpDraftUnit{span: jumpRestDia, start: rest[0], rest: true})
		}
		landTimes := make([]float64, len(units))
		for i := range units {
			landTimes[i] = units[i].start
		}
		unitLists[index] = units
		timelines[index] = jumpTimelineOf(window.writeStart, window.writeEnd, len(units), fly, landTimes, release, rest, entryLand, nextStart)
	}

	gap := float32(jumpSpacing)
	var x, y float32
	lead := gap / 2
	widestDepth := jumpBlockShape(face, jumpMinUnit, height, 1).depth
	widestCross := float32(jumpFaceSpan)
	maxUnits := 1
	course := &jumpCourse{rowOfLine: make([]int, len(lines)), config: config}
	for i := range course.rowOfLine {
		course.rowOfLine[i] = -1
	}
	for index, draft := range drafts {
		alongX := index%2 == 1
		units := unitLists[index]
		shapes := make([]jumpShape, len(units))
		for k, unit := range units {
			if unit.rest {
				shapes[k] = jumpShape{jumpRestDia, jumpRestDia, height}
			} else {
				shapes[k] = jumpBlockShape(face, unit.span, height, 1)
			}
		}
		cursor := lead
		placed := make([]jumpUnit, 0, len(units))
		for k, unit := range units {
			shape := shapes[k]
			middle := cursor + shape.length/2
			cursor += shape.length + gap
			wx, wy := x, y
			if alongX {
				wx = x + middle
			} else {
				wy = y + middle
			}
			sx := projection.x(wx, wy)
			sy := projection.y(wx, wy, 0)
			ty := projection.y(wx, wy, shape.height)
			if unit.rest {
				rx := shape.length * jumpSqrt2 / 4
				placed = append(placed, jumpUnit{shape: shape, rest: true, sx: sx, sy: sy, tx: sx, ty: ty,
					cylinder: &jumpCylinder{rx: rx, ry: rx * projection.sin, cy: -projection.lift * shape.height}})
			} else {
				box := jumpBoxOf(projection, alongX, shape, face, jumpTextAspect(unit.span, 1))
				placed = append(placed, jumpUnit{text: unit.text, shape: shape, sx: sx, sy: sy, tx: sx, ty: ty, box: &box})
			}
			widestDepth = max(widestDepth, shape.depth)
			widestCross = max(widestCross, shape.depth)
		}
		length := cursor - gap + gap/2
		cornerWx, cornerWy := x, y
		if alongX {
			cornerWx = x + length
		} else {
			cornerWy = y + length
		}
		revealStart := math.NaN()
		revealBudget := 0.0
		if index > 1 {
			revealStart = windows[index-2].afterEnd
			revealBudget = max(0, windows[index-1].writeEnd-revealStart) * .7
		}
		row := jumpRow{lineIndex: draft.lineIndex, units: placed, window: windows[index], timeline: timelines[index],
			alongX: alongX, direction: 1, cornerX: projection.x(cornerWx, cornerWy),
			cornerY: projection.y(cornerWx, cornerWy, height), revealStart: revealStart, revealBudget: revealBudget,
			hasWords: draft.hasWords}
		if alongX {
			row.direction = -1
		}
		course.rows = append(course.rows, row)
		if draft.lineIndex >= 0 && draft.lineIndex < len(course.rowOfLine) {
			course.rowOfLine[draft.lineIndex] = index
		}
		maxUnits = max(maxUnits, len(placed))
		if alongX {
			x += length
		} else {
			y += length
		}
		lead = 0
		for _, s := range shapes {
			lead = max(lead, s.depth)
		}
		lead = lead/2 + gap
	}
	course.windows = windows
	course.projection = projection
	course.frameInput = jumpFrameInput{face: face, heightCells: height, widestDepth: widestDepth,
		widestCross: widestCross, spacing: jumpSpacing, runnerScale: jumpRunnerScale, arc: config.arc}
	course.maxUnits = maxUnits
	return course
}

// ---------------------------------------------------------------- 一帧

const (
	jumpPrev = 0
	jumpCur  = 1
	jumpNext = 2
)

type jumpPose struct {
	rows        [3]int
	sink        [3][]float32
	reveal      [3][]float32
	focusX      float32
	focusY      float32
	lookDir     float32
	runner      bool
	feetX       float32
	feetY       float32
	groundX     float32
	groundY     float32
	squash      float32
	trailCount  int
	trailHop    []int
	trailFade   []float32
	impactCount int
	impactX     []float32
	impactY     []float32
	impactU     []float32
	impactScale []float32
	impactFade  []float32
	impactDust  []bool
}

func newJumpPose(capacity int) *jumpPose {
	capacity = max(1, capacity)
	pose := &jumpPose{trailHop: make([]int, capacity), trailFade: make([]float32, capacity),
		impactX: make([]float32, capacity*2+4), impactY: make([]float32, capacity*2+4),
		impactU: make([]float32, capacity*2+4), impactScale: make([]float32, capacity*2+4),
		impactFade: make([]float32, capacity*2+4), impactDust: make([]bool, capacity*2+4)}
	for i := range 3 {
		pose.sink[i] = make([]float32, capacity)
		pose.reveal[i] = make([]float32, capacity)
	}
	return pose
}

func (p *jumpPose) reset() {
	p.rows = [3]int{-1, -1, -1}
	p.runner = false
	p.squash = 1
	p.trailCount = 0
	p.impactCount = 0
}

func (p *jumpPose) addImpact(time, landTime float64, x, y, scale, fade float32, dust bool) {
	if math.IsNaN(landTime) || p.impactCount >= len(p.impactX) {
		return
	}
	u := (time - landTime) / jumpImpactSeconds
	if u < 0 || u > 1 {
		return
	}
	i := p.impactCount
	p.impactCount++
	p.impactX[i], p.impactY[i], p.impactU[i] = x, y, float32(u)
	p.impactScale[i], p.impactFade[i], p.impactDust[i] = scale, fade, dust
}

func jumpRowReveal(time, start, budget float64, order, count int) float32 {
	if math.IsNaN(start) {
		return 1
	}
	rise, stagger := .4, .07
	total := rise + stagger*float64(max(1, count)-1)
	if budget > 0 && total > budget {
		scale := budget / total
		stagger *= scale
		rise = max(.12, rise*scale)
	}
	begin := start + float64(max(0, order))*stagger
	if time <= begin {
		return 0
	}
	t := (time - begin) / rise
	if t >= 1 {
		return 1
	}
	return float32(1 - math.Pow(1-t, 3))
}

func jumpTrailFade(time, landTime, gap float64) float32 {
	if !(time > landTime) {
		return 1
	}
	span := jumpClamp64(gap*4, .8, 6)
	age := jumpClamp64((time-landTime)/span, 0, 1)
	return float32(.22 + .78*(1-age))
}

func jumpBoardRow(windows []jumpWindow, time float64) int {
	if len(windows) == 0 {
		return -1
	}
	row := 0
	for i := 1; i < len(windows); i++ {
		if time >= windows[i-1].afterEnd {
			row = i
		}
	}
	return row
}

func jumpHopSquash(progress float32) float32 {
	t := clamp01(progress)
	charge := float32(math.Exp(-math.Pow(float64((t-.1)/.08), 2))) * .08
	impact := float32(math.Exp(-math.Pow(float64((t-.9)/.07), 2))) * .1
	stretch := float32(math.Sin(math.Pi*float64(clamp01((t-.16)/.72)))) * .05
	return jumpClamp(1-charge-impact+stretch, .88, 1.07)
}

func jumpPillarSpring(time, press, release float64, strength float32) float32 {
	if strength <= 0 || math.IsNaN(press) || time < press {
		return 0
	}
	depth := .45 * strength
	if !math.IsNaN(release) && release <= press {
		return 0
	}
	if math.IsNaN(release) || time < release {
		t := jumpClamp64((time-press)/.09, 0, 1)
		return depth * float32(1-math.Pow(1-t, 3))
	}
	tau := time - release
	if tau > 1.4 {
		return 0
	}
	decay := math.Exp(-7 * tau)
	if decay < .004 {
		return 0
	}
	return depth * float32(decay*math.Cos(2*math.Pi*tau/.3))
}

func jumpBurstRadius(u float32) float32 {
	return .62 + .38*(1-float32(math.Pow(1-float64(clamp01(u)), 3)))
}
func jumpBurstFade(u float32) float32 { return float32(math.Pow(1-float64(clamp01(u)), 1.35)) }

func jumpBurstDust(u float32, index int, out *[2]float32) {
	t := clamp01(u)
	spread := 1 - float32(math.Pow(1-float64(t), 3))
	angle := 2*math.Pi*float64(index)/jumpImpactDust + .42
	reach := spread * (.62 + .38*float32(index%3)/2)
	out[0] = float32(math.Cos(angle)) * reach
	out[1] = float32(math.Sin(angle)) * .5 * reach
}

type jumpRunnerState struct {
	from, to int
	progress float64
	airborne bool
}

func jumpRunnerStateOf(tl *jumpTimeline, time float64, state *jumpRunnerState) {
	last := len(tl.landings) - 1
	if last < 0 {
		*state = jumpRunnerState{0, 0, 1, false}
		return
	}
	if time < tl.start {
		*state = jumpRunnerState{0, 0, 0, false}
		return
	}
	if tl.gapShift != nil && tl.gapShift.end > tl.gapShift.begin && time > tl.gapShift.begin && time < tl.gapShift.end {
		*state = jumpRunnerState{last, last, (time - tl.gapShift.begin) / (tl.gapShift.end - tl.gapShift.begin), true}
		return
	}
	for _, hop := range tl.hops {
		if time < hop.liftTime {
			*state = jumpRunnerState{hop.from, hop.from, 1, false}
			return
		}
		if time < hop.landTime {
			*state = jumpRunnerState{hop.from, hop.to, (time - hop.liftTime) / max(1e-6, hop.landTime-hop.liftTime), true}
			return
		}
	}
	*state = jumpRunnerState{last, last, 1, false}
}

func jumpPoseOf(course *jumpCourse, time float64, pose *jumpPose) {
	pose.reset()
	rows := course.rows
	ri := jumpBoardRow(course.windows, time)
	if ri < 0 {
		return
	}
	p := course.projection
	fly := float64(course.config.flySeconds)
	row := rows[ri]
	last := len(row.units) - 1

	holdBegin, holdEnd := math.NaN(), math.NaN()
	if row.timeline.shiftHold != nil {
		holdBegin, holdEnd = row.timeline.shiftHold.begin, row.timeline.shiftHold.end
	}
	waiting := holdEnd > holdBegin && time >= holdBegin && time < holdEnd
	openingAt := row.window.writeStart
	debut := ri == 0
	lead := jumpClamp64(fly, .2, .9)
	hasEntrance := debut && openingAt >= lead
	sinkless := debut && !hasEntrance

	if ri > 0 {
		pose.rows[jumpPrev] = ri - 1
	}
	pose.rows[jumpCur] = ri
	if ri+1 < len(rows) {
		pose.rows[jumpNext] = ri + 1
	}
	for role := 0; role < 3; role++ {
		r := pose.rows[role]
		if r < 0 {
			continue
		}
		target := rows[r]
		for k := range target.units {
			pose.reveal[role][k] = jumpRowReveal(time, target.revealStart, target.revealBudget, k, len(target.units))
			if sinkless && role == jumpCur && k == 0 {
				pose.sink[role][k] = 0
			} else {
				pose.sink[role][k] = jumpPillarSpring(time, target.timeline.pressTimes[k], target.timeline.releaseTimes[k], 1) * p.lift
			}
		}
	}

	shiftBegin, shiftEnd := row.timeline.tailSpan.begin, row.timeline.tailSpan.end
	if row.timeline.gapShift != nil && row.timeline.gapShift.end > row.timeline.gapShift.begin {
		shiftBegin, shiftEnd = row.timeline.gapShift.begin, row.timeline.gapShift.end
	}
	shiftAirborne := shiftEnd > shiftBegin && time > shiftBegin && time < shiftEnd
	var state jumpRunnerState
	jumpRunnerStateOf(&row.timeline, time, &state)
	canCross := pose.rows[jumpNext] >= 0
	entranceAirborne := hasEntrance && time > openingAt-lead && time < openingAt

	toRole := jumpCur
	fromIdx, toIdx, progress := state.from, state.to, state.progress
	hopping := false
	switch {
	case shiftAirborne && canCross:
		fromIdx, toRole, toIdx, hopping = last, jumpNext, 0, true
		progress = phaseOf(shiftBegin, shiftEnd, time)
	case waiting && canCross:
		fromIdx, toRole, toIdx, hopping = last, jumpNext, 0, true
		progress = 1
	case entranceAirborne:
		fromIdx, toIdx = 0, 0
		progress = phaseOf(openingAt-lead, openingAt, time)
	default:
		fromIdx = max(0, min(state.from, last))
		toIdx = max(0, min(state.to, last))
	}
	airborne := shiftAirborne || entranceAirborne || state.airborne
	toUnits := row.units
	if toRole == jumpNext {
		toUnits = rows[pose.rows[jumpNext]].units
	}

	// 镜头
	var camera jumpCameraState
	jumpCameraPathOf(&row.timeline, time, &camera)
	tailBegin, tailEnd := math.NaN(), math.NaN()
	if row.timeline.cameraTail != nil {
		tailBegin, tailEnd = row.timeline.cameraTail.begin, row.timeline.cameraTail.end
	}
	tailValid := pose.rows[jumpNext] >= 0 && tailEnd > tailBegin
	tailing := tailValid && time >= tailBegin && time < row.timeline.tailSpan.end
	if (hopping || tailing) && pose.rows[jumpNext] >= 0 {
		nextRow := rows[pose.rows[jumpNext]]
		a := row.units[last]
		b := nextRow.units[0]
		phase := 1.0
		if tailValid {
			phase = phaseOf(tailBegin, tailEnd, time)
		}
		first := float32(math.Hypot(float64(row.cornerX-a.tx), float64(row.cornerY-a.ty)))
		second := float32(math.Hypot(float64(b.tx-row.cornerX), float64(b.ty-row.cornerY)))
		share := .5
		if first+second > 1e-6 {
			share = float64(first / (first + second))
		}
		var weights [3]float32
		jumpPathWeights(phase, share, &weights)
		sum := max(1e-6, weights[0]+weights[1]+weights[2])
		pose.focusX = (a.tx*weights[0] + row.cornerX*weights[1] + b.tx*weights[2]) / sum
		pose.focusY = (a.ty*weights[0] + row.cornerY*weights[1] + b.ty*weights[2]) / sum
		pose.lookDir = float32(row.direction) + float32(nextRow.direction-row.direction)*float32(phase)
	} else {
		i := max(0, min(camera.index, last))
		j := max(0, min(camera.index+1, last))
		k := float32(jumpClamp64(camera.progress, 0, 1))
		pose.focusX = row.units[i].tx + (row.units[j].tx-row.units[i].tx)*k
		pose.focusY = row.units[i].ty + (row.units[j].ty-row.units[i].ty)*k
		pose.lookDir = float32(row.direction)
	}

	from := row.units[fromIdx]
	to := toUnits[toIdx]
	t := float32(jumpClamp64(progress, 0, 1))
	sinkFrom := pose.sink[jumpCur][fromIdx]
	sinkTo := pose.sink[toRole][toIdx]
	sink := sinkFrom + (sinkTo-sinkFrom)*t
	pose.runner = true
	pose.groundX = from.tx + (to.tx-from.tx)*t
	pose.groundY = from.ty + (to.ty-from.ty)*t + sink
	pose.feetX = pose.groundX
	pose.feetY = pose.groundY - jumpArcLift(course)*float32(math.Sin(math.Pi*float64(t)))
	if airborne {
		pose.squash = jumpHopSquash(t)
	} else {
		pose.squash = 1
	}

	for index, hop := range row.timeline.hops {
		if hop.to > last || time < hop.landTime || pose.trailCount >= len(pose.trailHop) {
			continue
		}
		pose.trailHop[pose.trailCount] = index
		pose.trailFade[pose.trailCount] = jumpTrailFade(time, hop.landTime, row.timeline.gap)
		pose.trailCount++
	}

	curSink := pose.sink[jumpCur]
	for _, hop := range row.timeline.hops {
		if hop.to <= last {
			u := row.units[hop.to]
			pose.addImpact(time, hop.landTime, u.tx, u.ty+curSink[hop.to], 1, 1, true)
		}
		if hop.from <= last && hop.liftTime-hop.start >= jumpImpactSeconds {
			u := row.units[hop.from]
			pose.addImpact(time, hop.liftTime, u.tx, u.ty+curSink[hop.from], .78, .7, false)
		}
	}
	if waiting && pose.rows[jumpNext] >= 0 {
		u := rows[pose.rows[jumpNext]].units[0]
		pose.addImpact(time, holdBegin, u.tx, u.ty+pose.sink[jumpNext][0], 1, 1, true)
	}
	crossedAt := openingAt
	if ri > 0 {
		if land := rows[ri-1].timeline.shiftLand; !math.IsNaN(land) {
			crossedAt = land
		}
	}
	if !sinkless && (ri > 0 || hasEntrance) {
		pose.addImpact(time, crossedAt, row.units[0].tx, row.units[0].ty+curSink[0], 1, 1, true)
	}
	if last >= 0 {
		if shiftBegin-row.timeline.landings[last] >= jumpImpactSeconds {
			pose.addImpact(time, shiftBegin, row.units[last].tx, row.units[last].ty+curSink[last], .78, .7, false)
		}
	}
}

func jumpArcLift(course *jumpCourse) float32 { return course.config.arc * course.projection.lift }
func jumpRunnerCells(course *jumpCourse) float32 {
	return course.projection.lift * jumpRunnerRatio * jumpRunnerScale
}

func phaseOf(begin, end, time float64) float64 {
	switch {
	case !(end > begin) || time <= begin:
		return 0
	case time >= end:
		return 1
	}
	return (time - begin) / (end - begin)
}

type jumpCameraState struct {
	index    int
	progress float64
}

func jumpCameraPathOf(tl *jumpTimeline, time float64, out *jumpCameraState) {
	stops := tl.cameraTimes
	out.index, out.progress = 0, 0
	if len(stops) <= 1 || time <= stops[0] {
		return
	}
	for i := 0; i < len(stops)-1; i++ {
		end := stops[i+1]
		if time >= end {
			continue
		}
		out.index = i
		out.progress = (time - stops[i]) / max(1e-6, end-stops[i])
		return
	}
	out.index = max(0, len(stops)-2)
	out.progress = 1
}

func jumpPathWeights(phase, share float64, out *[3]float32) {
	t := jumpClamp64(phase, 0, 1)
	corner := .5
	if !math.IsNaN(share) {
		corner = jumpClamp64(share, 0, 1)
	}
	if t <= corner {
		local := 0.0
		if corner > 0 {
			local = t / corner
		}
		out[0], out[1], out[2] = float32(1-local), float32(local), 0
		return
	}
	local := 1.0
	if corner < 1 {
		local = (t - corner) / (1 - corner)
	}
	out[0], out[1], out[2] = 0, float32(1-local), float32(local)
}

// ---------------------------------------------------------------- 画

type jumpCache struct {
	key    uint64
	course *jumpCourse
}

func (a *app) stageSceneJump(c *ui.Context, s stageFrame) {
	if s.w < 2 || s.h < 2 {
		return
	}
	config := jumpConfig{face: jumpTop, pitch: 34, chunkChars: 2, flySeconds: .45, arc: 1.2}
	key := jumpKey(s, config)
	if a.stageJump.course == nil || a.stageJump.key != key {
		// 原模组按 128px 字号量宽、除以 128 当作“以字号为单位”的宽度。
		measure := func(text string) float32 {
			w, _ := c.MeasureText(0, ui.Span{Text: text, Size: 128, Weight: 700})
			return w / 128
		}
		a.stageJump = jumpCache{key: key, course: jumpBuildCourse(s.lines, config, measure)}
	}
	course := a.stageJump.course
	if len(course.rows) == 0 {
		stageSceneEmpty(c, s)
		return
	}
	if s.preview || s.motion == 0 {
		drawJumpStill(c, s, course)
		if !s.preview {
			drawJumpTranslation(c, s, course)
		}
		return
	}
	safe := ui.Rect{X: 0, Y: 0, W: s.w, H: s.h}
	floor := max(10, min(s.w, s.h)*.05)
	cell, anchor := jumpFrameOf(safe.W, safe.H, course.config.pitch, course.frameInput, floor)
	pose := newJumpPose(course.maxUnits)
	time := s.pos.Seconds()
	jumpPoseOf(course, time, pose)
	if pose.rows[jumpCur] < 0 {
		return
	}
	ox := safe.X + safe.W/2 - pose.lookDir*.5*cell - pose.focusX*cell
	oy := safe.Y + safe.H - anchor - pose.focusY*cell
	stageSceneCanvas(c.Key("jump-scene")).Draw(func(p *ui.Painter, r ui.Rect) {
		drawJumpScene(p, c, s, course, pose, cell, ox+r.X, oy+r.Y)
	})
	drawJumpTranslation(c, s, course)
}

func jumpKey(s stageFrame, config jumpConfig) uint64 {
	h := fnv32(s.trackKey)
	h = h*31 + fnv32(s.localeKey())
	for i := range s.lines {
		l := &s.lines[i]
		h = h*31 + fnv32(l.Text)*7 + uint32(l.At/1e6)*13
		for _, w := range l.Words {
			h = h*31 + fnv32(w.Text)*17 + uint32(w.Start/1e6)*19
		}
	}
	return uint64(h)<<32 | uint64(config.chunkChars)<<8 | uint64(uint32(config.pitch))
}

// localeKey 只用来让不同语言下的量字结果各自缓存。
func (s stageFrame) localeKey() string { return lyrics.Lang(s.lines) }

// drawJumpStill 是减少动态效果的静帧：只摆当前句，柱子全部立好，小人站在唱到的那一块上。
func drawJumpStill(c *ui.Context, s stageFrame, course *jumpCourse) {
	rowIndex := -1
	if s.current >= 0 && s.current < len(course.rowOfLine) {
		rowIndex = course.rowOfLine[s.current]
	}
	if rowIndex < 0 {
		rowIndex = jumpBoardRow(course.windows, s.pos.Seconds())
	}
	if rowIndex < 0 || rowIndex >= len(course.rows) {
		return
	}
	row := course.rows[rowIndex]
	// 静帧也按播放位置挑柱子，拖进度条时能看到小人换位置。
	at := 0
	for k, unit := range row.units {
		if unit.rest {
			continue
		}
		if row.timeline.landings[k] <= s.pos.Seconds()+1e-9 {
			at = k
		}
	}
	stand := row.units[at]
	runner := jumpRunnerCells(course)
	// 缩略图里把整条路缩进一张小卡片，比只放当前一行更容易认出这个样式。
	fitRows := []jumpRow{row}
	if s.preview {
		fitRows = course.rows
	}
	minX, minY := float32(math.MaxFloat32), float32(math.MaxFloat32)
	maxX, maxY := -float32(math.MaxFloat32), -float32(math.MaxFloat32)
	for _, fitRow := range fitRows {
		for _, unit := range fitRow.units {
			if unit.box != nil {
				minX = min(minX, unit.sx+unit.box.minX)
				maxX = max(maxX, unit.sx+unit.box.maxX)
				minY = min(minY, unit.sy+unit.box.minY)
				maxY = max(maxY, unit.sy+unit.box.maxY)
			} else if unit.cylinder != nil {
				minX = min(minX, unit.sx-unit.cylinder.rx)
				maxX = max(maxX, unit.sx+unit.cylinder.rx)
				minY = min(minY, unit.ty-unit.cylinder.ry)
				maxY = max(maxY, unit.sy+unit.cylinder.ry)
			}
		}
	}
	minY = min(minY, stand.ty-runner)
	minX = min(minX, stand.tx-runner*.45)
	maxX = max(maxX, stand.tx+runner*.45)
	floor := max(10, min(s.w, s.h)*.05)
	cell, _ := jumpFrameOf(s.w, s.h, course.config.pitch, course.frameInput, floor)
	bw, bh := max(.5, maxX-minX), max(.5, maxY-minY)
	cell = max(1, min(cell, min(s.w*.88/bw, s.h*.84/bh)))
	ox := s.w/2 - (minX+maxX)/2*cell
	oy := s.h/2 - (minY+maxY)/2*cell
	pose := newJumpPose(course.maxUnits)
	// 静帧把前后两行也立起来，缩略图里能看到整条折线路。
	if rowIndex > 0 {
		pose.rows[jumpPrev] = rowIndex - 1
	}
	pose.rows[jumpCur] = rowIndex
	if rowIndex+1 < len(course.rows) {
		pose.rows[jumpNext] = rowIndex + 1
	}
	for role := 0; role < 3; role++ {
		if r := pose.rows[role]; r >= 0 {
			for k := range course.rows[r].units {
				pose.reveal[role][k] = 1
			}
		}
	}
	pose.runner = true
	pose.feetX, pose.feetY = stand.tx, stand.ty
	pose.groundX, pose.groundY = stand.tx, stand.ty
	// 静帧不跳，但按播放位置在两根柱子之间过渡，拖进度条能看到小人往前走。
	if at+1 < len(row.units) && !row.units[at+1].rest {
		gap := row.timeline.landings[at+1] - row.timeline.landings[at]
		t := clamp01(float32((s.pos.Seconds() - row.timeline.landings[at]) / max(1e-6, gap)))
		next := row.units[at+1]
		pose.groundX = stand.tx + (next.tx-stand.tx)*t
		pose.groundY = stand.ty + (next.ty-stand.ty)*t
		pose.feetX = pose.groundX
		pose.feetY = pose.groundY - jumpArcLift(course)*.35*float32(math.Sin(math.Pi*float64(t)))
	}
	pose.focusX, pose.focusY = stand.tx, stand.ty
	pose.lookDir = float32(row.direction)
	stageSceneCanvas(c.Key("jump-still")).Draw(func(p *ui.Painter, r ui.Rect) {
		drawJumpScene(p, c, s, course, pose, cell, ox+r.X, oy+r.Y)
	})
}

func drawJumpScene(p *ui.Painter, c *ui.Context, s stageFrame, course *jumpCourse, pose *jumpPose, cell, ox, oy float32) {
	row := course.rows[pose.rows[jumpCur]]
	drawJumpTrail(p, s, course, row, pose, cell, ox, oy)

	// 柱子按柱底的屏幕高度从远到近画。
	type entry struct {
		key  float32
		role int
		k    int
	}
	var list []entry
	for role := 0; role < 3; role++ {
		r := pose.rows[role]
		if r < 0 {
			continue
		}
		for k, unit := range course.rows[r].units {
			list = append(list, entry{unit.sy, role, k})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].key < list[j].key })
	for _, e := range list {
		reveal := pose.reveal[e.role][e.k]
		if reveal <= .003 {
			continue
		}
		target := course.rows[pose.rows[e.role]]
		unit := target.units[e.k]
		fade := reveal
		if e.role != jumpCur {
			fade *= jumpOtherRow
		}
		x := ox + unit.sx*cell
		y := oy + unit.sy*cell + (1-reveal)*.9*cell + pose.sink[e.role][e.k]*cell
		drawJumpPillar(p, c, s, unit, target.alongX, x, y, cell, fade)
	}

	if !pose.runner {
		return
	}
	stature := max(6, jumpRunnerCells(course)*cell)
	feetX, feetY := ox+pose.feetX*cell, oy+pose.feetY*cell
	groundX, groundY := ox+pose.groundX*cell, oy+pose.groundY*cell
	air := clamp01((groundY - feetY) / max(1, stature*.55))
	shrink := 1 - .35*air
	rx := stature * .42 * shrink
	ry := stature * .42 * max(.16, course.projection.sin) * .8 * shrink
	// 影子：贴在柱顶上，人越高越淡越小。
	p.FillRadialGradient(ui.Rect{X: groundX - rx, Y: groundY - ry, W: rx * 2, H: ry * 2},
		ui.RadialGradient{Radius: max(rx, ry), Stops: []ui.GradientStop{
			{Color: ui.RGBA(6, 10, 22, .62*.38*(1-.5*air)), At: 0},
			{Color: ui.Transparent, At: .7},
		}}, ry)

	drawJumpImpacts(p, s, course, pose, cell, ox, oy)

	// 小人：胶囊身子 + 头，左上高光、下面压暗；起跳蹲、落地压。
	// 曲线路径在 p.scale 的容差下会退化，所以不套 Transform，直接用屏幕坐标画。
	accent := s.accent
	wide := 1 + (1-pose.squash)*.5
	sx := stature * wide
	sy := stature * pose.squash
	topR, botR := .26*sy, .14*sy
	y0, y1 := feetY-.68*sy, feetY
	p.FillPath(jumpQuadPath(feetX-.14*sx, y0+topR, .28*sx, y1-botR-y0-topR), accent)
	p.FillPath(jumpEllipsePath(feetX, y0+topR, .26*sx, topR), accent)
	p.FillPath(jumpEllipsePath(feetX, y1-botR, .14*sx, botR), accent)
	p.FillPath(jumpEllipsePath(feetX-.09*sx, y0+.19*sy, .11*sx, .09*sy), white.Alpha(.38))
	p.FillPath(jumpEllipsePath(feetX, y1-.09*sy, .23*sx, .08*sy), ui.RGBA(6, 10, 22, .18))
	headY := feetY - .83*sy
	p.FillPath(jumpEllipsePath(feetX, headY, .17*sx, .17*sy), accent)
	p.FillPath(jumpEllipsePath(feetX-.05*sx, headY-.05*sy, .07*sx, .06*sy), white.Alpha(.4))
}

func jumpQuadPath(x, y, w, h float32) *ui.Path {
	return new(ui.Path).MoveTo(x, y).LineTo(x+w, y).LineTo(x+w, y+h).LineTo(x, y+h).Close()
}

func jumpEllipsePath(cx, cy, rx, ry float32) *ui.Path {
	k := float32(.5523)
	return new(ui.Path).MoveTo(cx+rx, cy).
		CubeTo(cx+rx, cy+ry*k, cx+rx*k, cy+ry, cx, cy+ry).
		CubeTo(cx-rx*k, cy+ry, cx-rx, cy+ry*k, cx-rx, cy).
		CubeTo(cx-rx, cy-ry*k, cx-rx*k, cy-ry, cx, cy-ry).
		CubeTo(cx+rx*k, cy-ry, cx+rx, cy-ry*k, cx+rx, cy).Close()
}

func drawJumpPillar(p *ui.Painter, c *ui.Context, s stageFrame, unit jumpUnit, alongX bool, x, y, cell, fade float32) {
	lit := jumpLitAlpha * fade
	shade := jumpShadeAlpha * fade
	edgeAlpha := jumpEdgeAlpha * fade
	// 原模组：柱面是背景与正文的中间色再掺 15% 主题色，棱线是正文色与主题色各半。
	face := ui.Hex("#8a90a0").Mix(s.accent, .15)
	edge := white.Mix(s.accent, .5)
	fillQuad := func(q [8]float32, alpha float32) {
		path := new(ui.Path).MoveTo(x+q[0]*cell, y+q[1]*cell)
		path.LineTo(x+q[2]*cell, y+q[3]*cell).LineTo(x+q[4]*cell, y+q[5]*cell).LineTo(x+q[6]*cell, y+q[7]*cell).Close()
		p.FillPath(path, face.Alpha(alpha))
		p.StrokePath(path, 1.2, edge.Alpha(edgeAlpha))
	}
	if unit.box != nil {
		box := unit.box
		frontAlpha, sideAlpha := shade, lit
		if alongX {
			frontAlpha, sideAlpha = lit, shade
		}
		fillQuad(box.side, sideAlpha)
		fillQuad(box.front, frontAlpha)
		fillQuad(box.top, lit)
		drawJumpLabel(p, c, s, unit.text, *box, x, y, cell, fade)
		return
	}
	if unit.cylinder != nil {
		cyl := unit.cylinder
		rx, ry := cyl.rx*cell, cyl.ry*cell
		cy := y + cyl.cy*cell
		// 柱身：上下椭圆之间的一块，左右两半分色。
		p.Fill(ui.Rect{X: x - rx, Y: cy, W: rx, H: y - cy}, face.Alpha(shade), 0)
		p.Fill(ui.Rect{X: x, Y: cy, W: rx, H: y - cy}, face.Alpha(lit), 0)
		p.Fill(ui.Rect{X: x - rx, Y: y - ry, W: rx * 2, H: ry * 2}, face.Alpha(shade), ry)
		p.Fill(ui.Rect{X: x - rx, Y: cy - ry, W: rx * 2, H: ry * 2}, face.Alpha(lit), ry)
		p.Stroke(ui.Rect{X: x - rx, Y: cy - ry, W: rx * 2, H: ry * 2}, edge.Alpha(edgeAlpha), ry, 1.2)
	}
}

// drawJumpLabel 把字盒经仿射矩阵贴到柱面上。
func drawJumpLabel(p *ui.Painter, c *ui.Context, s stageFrame, text string, box jumpBoxGeometry, x, y, cell, alpha float32) {
	height := box.labelH * cell
	if text == "" || height < 1 || alpha <= .003 {
		return
	}
	layout := ui.ShapeText(text, ui.Font{Size: height, Weight: 600, Lang: lyrics.Lang(s.lines)}, 0)
	lw, lh := layout.Size()
	m := box.label
	t := jumpAffine(m[0], m[1], m[2], m[3], x+m[4]*cell, y+m[5]*cell)
	p.Transform(t, 0, 0, func() {
		p.TextLayout(layout, (box.labelW*cell-lw)/2, (box.labelH*cell-lh)/2, white.Alpha(.9*alpha))
	})
}

// jumpAffine 把 2×2 仿射拆成 旋转 → 缩放 → 斜切，配上平移，交给 MyGo 的 Transform。
func jumpAffine(ux, uy, vx, vy, ex, ey float32) ui.Transform {
	phi := math.Atan2(float64(uy), float64(ux))
	sx := math.Hypot(float64(ux), float64(uy))
	det := float64(ux*vy - uy*vx)
	sy := det / sx
	k := (math.Cos(phi)*float64(vx) + math.Sin(phi)*float64(vy)) / sx
	// Then 是「先做前者」：先把字盒斜切成平行四边形，再缩放、旋转，最后平移到位。
	return ui.Skewing(float32(math.Atan(k)*180/math.Pi), 0).
		Then(ui.Scaling(float32(sx), float32(sy))).
		Then(ui.Rotation(float32(phi * 180 / math.Pi))).
		Then(ui.Translation(ex, ey))
}

func drawJumpTrail(p *ui.Painter, s stageFrame, course *jumpCourse, row jumpRow, pose *jumpPose, cell, ox, oy float32) {
	arc := jumpArcLift(course) * cell
	base := float32(jumpTrailAlpha) * .8
	dot := float32(1.2)
	for n := 0; n < pose.trailCount; n++ {
		hop := row.timeline.hops[pose.trailHop[n]]
		from, to := row.units[hop.from], row.units[hop.to]
		fade := pose.trailFade[n]
		fx, fy := ox+from.tx*cell, oy+from.ty*cell
		tx, ty := ox+to.tx*cell, oy+to.ty*cell
		for step := 1; step <= 8; step++ {
			t := float32(step) / 8
			px := fx + (tx-fx)*t
			py := fy + (ty-fy)*t - arc*float32(math.Sin(math.Pi*float64(t)))
			p.Fill(ui.Rect{X: px - dot, Y: py - dot, W: dot * 2, H: dot * 2}, s.accent.Alpha(base*.45*fade*(.3+.7*t)), dot)
		}
		rx, ry := float32(5), float32(2.5)
		p.Stroke(ui.Rect{X: tx - rx, Y: ty - ry, W: rx * 2, H: ry * 2}, s.accent.Alpha(base*.3*fade), ry, 1)
	}
}

func drawJumpImpacts(p *ui.Painter, s stageFrame, course *jumpCourse, pose *jumpPose, cell, ox, oy float32) {
	squash := course.projection.sin
	baseRadius := max(10, cell*.8)
	opacity := float32(.35 + .65*jumpTrailAlpha)
	var dust [2]float32
	dustCount := jumpImpactDust
	for i := 0; i < pose.impactCount; i++ {
		u := pose.impactU[i]
		x, y := ox+pose.impactX[i]*cell, oy+pose.impactY[i]*cell
		ringRadius := baseRadius * pose.impactScale[i]
		radius := ringRadius * jumpBurstRadius(u)
		fade := jumpBurstFade(u)
		alpha := clamp01(opacity * fade * pose.impactFade[i])
		p.Stroke(ui.Rect{X: x - radius, Y: y - radius*squash, W: radius * 2, H: radius * 2 * squash},
			s.accent.Alpha(alpha), radius*squash, 2)
		if !pose.impactDust[i] {
			continue
		}
		dot := max(1.6, ringRadius*.09) / 2
		for d := 0; d < dustCount; d++ {
			jumpBurstDust(u, d*jumpImpactDust/dustCount, &dust)
			p.Fill(ui.Rect{X: x + dust[0]*ringRadius - dot, Y: y + dust[1]*ringRadius*squash - dot, W: dot * 2, H: dot * 2},
				s.accent.Alpha(clamp01(fade*.7*pose.impactFade[i])), dot)
		}
	}
}

// drawJumpTranslation 画顶部译文：按俯角压扁、往右上侧倾，靠上的行再收一点。
func drawJumpTranslation(c *ui.Context, s stageFrame, course *jumpCourse) {
	if s.current < 0 || s.current >= len(s.lines) || strings.TrimSpace(s.lines[s.current].Translation) == "" {
		return
	}
	text := strings.TrimSpace(s.lines[s.current].Translation)
	room := s.w * .84
	target := max(8, min(s.h*.12, 26))
	size := target
	capacity := room / max(1, size)
	rows := jumpWrapRows(text, capacity)
	if len(rows) > 3 {
		rows = rows[:3]
	}
	if len(rows) == 0 {
		return
	}
	layouts := make([]*ui.TextLayout, len(rows))
	var widest float32
	for i, row := range rows {
		layouts[i] = ui.ShapeText(row, ui.Font{Size: size, Weight: 500, Lang: c.Locale()}, 0)
		w, _ := layouts[i].Size()
		widest = max(widest, w)
	}
	scaleX := max(.86, min(1, room/max(1, widest)))
	lineHeight := size * 1.26
	amount := jumpTiltAmount(float32(course.config.pitch))
	// 俯角越大，译文越往下压、越往右上侧倾；每行先斜切、再压扁、最后平移。
	lean := .18 * amount
	flatten := 1 - .1*amount
	stageSceneCanvas(c.Key("jump-translation")).Draw(func(p *ui.Painter, r ui.Rect) {
		cx := r.X + s.w/2
		top := r.Y + 6
		for i, layout := range layouts {
			x := -widest / 2
			t := ui.Skewing(float32(-lean*180/math.Pi), 0).
				Then(ui.Scaling(scaleX, flatten)).
				Then(ui.Translation(cx, top+float32(i)*lineHeight))
			p.Transform(t, 0, 0, func() {
				// 光晕：MyGo 没有模糊，用几层错位的同色字近似。
				for _, off := range [][2]float32{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
					p.TextLayout(layout, x+off[0], off[1], ui.Hex("#aab4fa").Alpha(.2))
				}
				p.TextLayout(layout, x, 0, white.Alpha(.8))
			})
		}
	})
}

func jumpWrapRows(text string, capacity float32) []string {
	limit := max(1, capacity)
	var tokens []string
	var token strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) {
			if token.Len() > 0 {
				tokens = append(tokens, token.String())
				token.Reset()
			}
			tokens = append(tokens, " ")
			continue
		}
		last, _ := utf8.DecodeLastRuneInString(token.String())
		if token.Len() > 0 && (jumpIsWide(r) || jumpIsLatinJoint(last)) {
			tokens = append(tokens, token.String())
			token.Reset()
		}
		token.WriteRune(r)
	}
	if token.Len() > 0 {
		tokens = append(tokens, token.String())
	}
	var rows []string
	var row strings.Builder
	var width float32
	for _, piece := range tokens {
		if piece == " " {
			if row.Len() > 0 {
				row.WriteByte(' ')
			}
			continue
		}
		w := jumpWidthOf(piece)
		if row.Len() > 0 && width+w > limit {
			rows = append(rows, strings.TrimSpace(row.String()))
			row.Reset()
			width = 0
		}
		row.WriteString(piece)
		width += w
	}
	if strings.TrimSpace(row.String()) != "" {
		rows = append(rows, strings.TrimSpace(row.String()))
	}
	return rows
}

func jumpTiltAmount(pitchDeg float32) float32 {
	p := jumpClamp(pitchDeg, 1, 89)
	reach := 1 - float32(math.Sin(float64(p)*jumpDeg))
	base := max(1e-6, 1-float32(math.Sin(jumpReferencePitch*jumpDeg)))
	return jumpClamp(reach/base, 0, 2.5)
}

func jumpClamp(v, lo, hi float32) float32   { return min(hi, max(lo, v)) }
func jumpClamp64(v, lo, hi float64) float64 { return min(hi, max(lo, v)) }
