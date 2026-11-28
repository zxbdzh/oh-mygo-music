package app

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"mygo-music/internal/lyrics"
)

// 巨幕照 myMusic 的 cinerama 移植（原模组 cinerama 1.0.0，作者 tumuyan，AGPL-3.0）：
// 每句按歌曲种子抽一种版式，相邻两句不重复——大字报（一句切成至多 5 块，一块一块闪出）、
// 小字报（整句按切分方式拆成单元依次点亮）、斜切丝带（6 条强调色斜带横穿全屏）；大字报和小字报再配跑马灯带。
// 本文件只做编译期与求值的纯逻辑（不碰画布），画法在 ui_stage_cinerama_draw.go。
const (
	cinTailSec         = .6   // 句末之后的视觉尾巴
	cinGapSec          = .6   // 下一句开口离这句收尾这么近算紧邻：不淡出，由下一句转场接走
	cinRevealFloor     = .3   // 出字窗口至少占行窗口的三成
	cinUnitEnter       = .18  // 小字报一个单元的入场
	cinHeroEnter       = .108 // 大字报一块的淡入
	cinHeroExit        = .072 // 大字报一块的淡出
	cinHeroLineHeight  = 1.02
	cinHeroTrackEm     = .01
	cinOutlineEm       = .014
	cinMotionRefSec    = 1.2
	cinMaxChunks       = 5
	cinMaxWordsPerChunk = 6
	cinChunkChance     = .55
	cinItalicChance    = .35
	cinFillCap         = 2.0 / 15.0
	cinTitleChance     = 0 // 舞台拿不到歌名，取材落空时原版也退回原文
	cinSpeedMin        = .12
	cinSpeedSpan       = .12
	cinSpeedSpanSec    = 10.0
	cinTightenRatio    = .15
	cinEdgeGlowStrength = .3
	cinAlphaFloor      = .55
	cinRibbonAngleLimit = 60.0
	cinRibbonShallow   = 26.0
	cinRibbonMinVisible = 3.0
	cinRibbonMin       = 3.4 * 1.8
	cinRibbonMax       = 4.6 * 1.8
	cinRibbonFontRatio = .66
	cinRibbonMargin    = .06
	cinRibbonMinOverflow = 8.0
	cinRibbonTextSpeed = .16
	cinTravelTail      = 1.0
	cinDriftVel        = 4.0
	cinDriftClamp      = 70.0
	cinDriftMargin     = 3.0
	cinDriftMax        = 15.0
)

// 四种版式权重（面板默认：大字报 40%、小字报 20%、斜切丝带 40%）。
var cinDefaultWeights = cinWeights{hero: .4, small: .2, ribbon: .4}

// ---------------------------------------------------------------- 随机

// cinMix 是原版 mixCineramaSeed：(seed ^ salt) × 2654435761，取低 32 位。
func cinMix(seed uint32, salt int) uint32 { return (seed ^ uint32(salt)) * 2654435761 }

// cinHash01 是原版 cineramaHash01：同一个种子、序号、盐永远给同一个 [0, 1) 的数。
func cinHash01(seed uint32, index, salt int) float64 {
	return float64(cinMix(seed+uint32(index+1)*97, salt)) / 4294967296
}

func cinClamp(v, lo, hi float64) float64 { return min(hi, max(lo, v)) }

func cinSmoothstep(v float64) float64 {
	t := cinClamp(v, 0, 1)
	return t * t * (3 - 2*t)
}

// cinFrac 取小数部分，负数也落在 [0, 1)。
func cinFrac(v float64) float64 { return math.Mod(math.Mod(v, 1)+1, 1) }

// cinRound 照 JS 的 Math.round：.5 往大进。
func cinRound(v float64) float64 { return math.Floor(v + .5) }

// cinChoose 按字符串种子挑起点，往后找第一个和 previous 不同的；池里只有一项时照给。
func cinChoose[T comparable](choices []T, seed string, previous T, hasPrevious bool) T {
	if len(choices) == 0 {
		var zero T
		return zero
	}
	if len(choices) == 1 || !hasPrevious {
		return choices[int(fnv32(seed)%uint32(len(choices)))]
	}
	start := int(fnv32(seed) % uint32(len(choices)))
	for offset := range choices {
		if c := choices[(start+offset)%len(choices)]; c != previous {
			return c
		}
	}
	return choices[start]
}

// cinDrawWeighted 按权重掷一次；总权重不大于 0 时 ok=false。
func cinDrawWeighted[T comparable](entries []struct {
	kind   T
	weight float64
}, seed uint32, salt int) (T, bool) {
	var zero T
	total := 0.0
	for _, e := range entries {
		total += e.weight
	}
	if total <= 0 {
		return zero, false
	}
	roll := cinHash01(seed, 0, salt) * total
	for _, e := range entries {
		roll -= e.weight
		if roll < 0 {
			return e.kind, true
		}
	}
	return entries[len(entries)-1].kind, true
}

// cinShuffle 是 Fisher–Yates，第 i 步的落点按 hash01(seed, i, salt)。
func cinShuffle[T any](list []T, seed uint32, salt int) []T {
	out := append([]T(nil), list...)
	for i := len(out) - 1; i >= 1; i-- {
		j := int(cinClamp(math.Floor(cinHash01(seed, i, salt)*float64(i+1)), 0, float64(i)))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ---------------------------------------------------------------- 版式与权重

type cinStyle int

const (
	cinHero cinStyle = iota
	cinSmall
	cinRibbon
)

type cinWeights struct{ hero, small, ribbon float64 }

func (w cinWeights) of(s cinStyle) float64 {
	switch s {
	case cinHero:
		return w.hero
	case cinRibbon:
		return w.ribbon
	default:
		return w.small
	}
}

// cinPickStyle 是原版 pickCineramaStyleKind：池子是“这一句排得出、权重大于 0”的版式，
// 盐 349 掷一次；和上一句相同且池里不止一种时，去掉上一句那种、用盐 353 重掷。
func cinPickStyle(seed uint32, previous cinStyle, hasPrevious bool, w cinWeights, heroOk, ribbonOk bool) cinStyle {
	type entry = struct {
		kind   cinStyle
		weight float64
	}
	var pool []entry
	if heroOk && w.hero > 0 {
		pool = append(pool, entry{cinHero, w.hero})
	}
	if w.small > 0 {
		pool = append(pool, entry{cinSmall, w.small})
	}
	if ribbonOk && w.ribbon > 0 {
		pool = append(pool, entry{cinRibbon, w.ribbon})
	}
	if len(pool) == 0 {
		return cinSmall
	}
	picked, ok := cinDrawWeighted(pool, seed, 349)
	if !ok {
		return cinSmall
	}
	if !hasPrevious || picked != previous || len(pool) == 1 {
		return picked
	}
	var rest []entry
	for _, e := range pool {
		if e.kind != previous {
			rest = append(rest, e)
		}
	}
	if again, ok := cinDrawWeighted(rest, seed, 353); ok {
		return again
	}
	return picked
}

// 小字报的落点表：左右 / 上下留白、字号倍率、字距、行高、文字盒最大宽度、竖直与水平落点。group 是权重分组。
type cinLayout struct {
	group                            int
	padX, padY, fontScale, trackEm   float32
	lineHeight, maxWidthRatio        float32
	alignY, alignX                   int
}

var cinLayouts = []cinLayout{
	{0, .07, .08, 1, .02, 1.08, .88, 0, 0},   // 居中
	{1, .08, .10, .9, .03, 1.12, .84, -1, 0}, // 上三分
	{1, .08, .10, .9, .03, 1.3, .84, 1, 0},   // 下三分
	{1, .09, .08, .86, .015, 1.16, .72, 0, -1}, // 左侧带
	{1, .09, .08, .86, .015, 1.16, .72, 0, 1},  // 右侧带
	{2, .06, .16, 1.12, .06, 1.1, .92, 1, 0},   // 宽银幕
}

var cinLayoutGroupWeights = []float64{1, 1, .5}

// cinPickLayout：居中组 1、偏置组 1、宽银幕组 0.5，盐 401 掷一次，和上一句相同就去掉它重掷。
func cinPickLayout(seed uint32, previous int) int {
	type entry = struct {
		kind   int
		weight float64
	}
	pool := make([]entry, len(cinLayouts))
	for i, l := range cinLayouts {
		pool[i] = entry{i, cinLayoutGroupWeights[l.group]}
	}
	picked, ok := cinDrawWeighted(pool, seed, 401)
	if !ok {
		return 0
	}
	if previous < 0 || picked != previous {
		return picked
	}
	var rest []entry
	for _, e := range pool {
		if e.kind != previous {
			rest = append(rest, e)
		}
	}
	if again, ok := cinDrawWeighted(rest, seed, 401); ok {
		return again
	}
	return picked
}

// ---------------------------------------------------------------- 切分与分词

type cinSplit int

const (
	cinWhole cinSplit = iota
	cinWordSplit
	cinClause
	cinGrapheme
	cinHalf
)

const cinMaxGraphemeUnits = 32

func cinCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func cinClauseRune(r rune) bool {
	return strings.ContainsRune("，,。、；;：:！!？?…—~\n", r)
}

// cinSegments 近似 ICU 的按词切分：汉字逐字、拉丁字母数字连成一段，空白与标点各自成段。
func cinSegments(text string) []string {
	runes := []rune(text)
	var out []string
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case unicode.IsSpace(r):
			j := i
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			out, i = append(out, string(runes[i:j])), j
		case cinCJK(r):
			out, i = append(out, string(r)), i+1
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '\'' || r == '’':
			j := i
			for j < len(runes) && !cinCJK(runes[j]) && (unicode.IsLetter(runes[j]) || unicode.IsNumber(runes[j]) || runes[j] == '\'' || runes[j] == '’') {
				j++
			}
			out, i = append(out, string(runes[i:j])), j
		default:
			out, i = append(out, string(r)), i+1
		}
	}
	return out
}

func cinWordLike(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// cinWords 照原版 segmentCineramaWords：词照留，空白和标点并进前一个词（句首的标点自成一段）。
func cinWords(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var words []string
	for _, part := range cinSegments(text) {
		if part == "" {
			continue
		}
		if cinWordLike(part) {
			words = append(words, part)
			continue
		}
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			if len(words) > 0 {
				words[len(words)-1] += part
			}
			continue
		}
		if len(words) > 0 {
			words[len(words)-1] += trimmed
		} else {
			words = append(words, trimmed)
		}
	}
	var out []string
	for _, w := range words {
		if strings.TrimSpace(w) != "" {
			out = append(out, w)
		}
	}
	return out
}

// cinLineWords 取这句的分词：歌词自带逐词时间时用它的词边界（中文本就是词，不是字），
// 拼回去与原文对不上（空格、重复段等）时退回 cinSegments 的切法。
func cinLineWords(text string, src []lyrics.Word) []string {
	if len(src) == 0 {
		return cinWords(text)
	}
	var parts []string
	for _, w := range src {
		parts = append(parts, w.Text)
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if squash(strings.Join(parts, "")) != squash(text) {
		return cinWords(text)
	}
	var out []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		if cinWordLike(part) {
			out = append(out, part)
			continue
		}
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			if len(out) > 0 {
				out[len(out)-1] += part
			}
			continue
		}
		if len(out) > 0 {
			out[len(out)-1] += trimmed
		} else {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return cinWords(text)
	}
	return out
}

func cinSupportsSplit(kind cinSplit, text string) bool {
	n := len([]rune(text))
	switch kind {
	case cinWhole:
		return true
	case cinWordSplit:
		return len(cinSegments(text)) > 1
	case cinClause:
		return strings.IndexFunc(text, cinClauseRune) >= 0
	case cinGrapheme:
		return n >= 2 && n <= cinMaxGraphemeUnits
	case cinHalf:
		return n >= 6
	}
	return false
}

func cinPickSplit(text, seed string, previous cinSplit, hasPrevious bool) cinSplit {
	var supported []cinSplit
	for _, k := range []cinSplit{cinWhole, cinWordSplit, cinClause, cinGrapheme, cinHalf} {
		if cinSupportsSplit(k, text) {
			supported = append(supported, k)
		}
	}
	return cinChoose(supported, seed, previous, hasPrevious)
}

// cinSplitBalanced 在目标位置左右 6 个字以内找空白或标点断开，分成 count 段。
func cinSplitBalanced(text string, count int) []string {
	runes := []rune(text)
	if count < 2 {
		return []string{text}
	}
	isBreak := func(i int) bool {
		return i >= 0 && i < len(runes) && (unicode.IsSpace(runes[i]) || cinClauseRune(runes[i]))
	}
	var cuts []int
	for step := 1; step < count; step++ {
		target := int(cinRound(float64(len(runes)) * float64(step) / float64(count)))
		best := -1
		for offset := 0; offset <= 6; offset++ {
			if isBreak(target+offset) {
				best = target + offset
				break
			}
			if isBreak(target-offset) {
				best = target - offset
				break
			}
		}
		cut := target
		if best > 0 {
			cut = best + 1
		}
		last := 0
		if len(cuts) > 0 {
			last = cuts[len(cuts)-1]
		}
		if cut > last && cut < len(runes) {
			cuts = append(cuts, cut)
		}
	}
	var parts []string
	cursor := 0
	for _, cut := range cuts {
		parts = append(parts, string(runes[cursor:cut]))
		cursor = cut
	}
	parts = append(parts, string(runes[cursor:]))
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cinSplitClauses 按断句标点切开，标点自成一段。
func cinSplitClauses(text string) []string {
	var parts []string
	var b strings.Builder
	for _, r := range text {
		if cinClauseRune(r) {
			if b.Len() > 0 {
				parts = append(parts, b.String())
				b.Reset()
			}
			parts = append(parts, string(r))
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}

func cinParts(text string, kind cinSplit) []string {
	switch kind {
	case cinWordSplit:
		return cinSegments(text)
	case cinClause:
		return cinSplitClauses(text)
	case cinGrapheme:
		out := make([]string, 0, len([]rune(text)))
		for _, r := range text {
			out = append(out, string(r))
		}
		return out
	case cinHalf:
		if len([]rune(text)) > 16 {
			return cinSplitBalanced(text, 3)
		}
		return cinSplitBalanced(text, 2)
	}
	return []string{text}
}

// ---------------------------------------------------------------- 时间线

type cinSpan struct{ start, end float64 }

type cinWord struct {
	text       string
	start, end float64
}

type cinUnit struct {
	text       string
	start, end float64
}

// cinReveal：出字窗口从句首到最后一个词唱完（没有逐字时到作者写的句末），
// 至少占行窗口的三成，且不超过行窗口。
func cinReveal(start, windowEnd float64, lastWordEnd float64, hasWords bool, authoredEnd float64) cinSpan {
	span := max(.001, windowEnd-start)
	sung := authoredEnd
	if hasWords {
		sung = lastWordEnd
	}
	end := min(windowEnd, max(sung, start+span*cinRevealFloor))
	return cinSpan{start, max(end, start+.001)}
}

func cinEven(count int, start, end float64) []cinSpan {
	unit := max(0, end-start) / float64(max(1, count))
	out := make([]cinSpan, count)
	for i := range out {
		out[i] = cinSpan{start + unit*float64(i), start + unit*float64(i+1)}
		if i == count-1 {
			out[i].end = end
		}
	}
	return out
}

func cinFindSequence(source, target []string, from int) int {
	if len(target) == 0 {
		return from
	}
	for i := from; i <= len(source)-len(target); i++ {
		ok := true
		for j := range target {
			if source[i+j] != target[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// cinTimeline：每个字什么时候出。逐字时间按词在整句里对位，词的时长均分给它的字；
// 没对上的字落在前一个词开口处，零长的字挪到前一个字唱完的时刻，整条单调不减、夹在行窗口里。
func cinTimeline(graphemes []string, words []cinWord, windowStart, windowEnd float64, reveal cinSpan) []cinSpan {
	if len(graphemes) == 0 {
		return nil
	}
	if len(words) == 0 {
		return cinEven(len(graphemes), reveal.start, reveal.end)
	}
	clampTime := func(v float64) float64 { return min(windowEnd, max(windowStart, v)) }
	timeline := make([]cinSpan, len(graphemes))
	filled := make([]bool, len(graphemes))
	cursor := 0
	lastEnd := reveal.start
	for _, word := range words {
		wordGraphemes := []rune(word.text)
		if len(wordGraphemes) == 0 {
			continue
		}
		wg := make([]string, len(wordGraphemes))
		for i, r := range wordGraphemes {
			wg[i] = string(r)
		}
		wordStart, wordEnd := clampTime(word.start), clampTime(word.end)
		matched := cinFindSequence(graphemes, wg, cursor)
		start := cursor
		if matched >= 0 {
			start = matched
		}
		end := min(start+len(wg), len(graphemes))
		for i := cursor; i < start; i++ {
			timeline[i], filled[i] = cinSpan{wordStart, wordStart}, true
		}
		timings := cinEven(len(wg), wordStart, wordEnd)
		for i := start; i < end; i++ {
			timeline[i], filled[i] = timings[i-start], true
		}
		lastEnd = max(lastEnd, wordEnd)
		cursor = max(cursor, end)
	}
	previousEnd, previousStart := reveal.start, windowStart
	out := make([]cinSpan, len(graphemes))
	for i := range graphemes {
		entry := cinSpan{lastEnd, lastEnd}
		if filled[i] {
			entry = timeline[i]
		}
		if entry.end > entry.start {
			previousEnd = entry.end
		} else {
			at := max(reveal.start, previousEnd)
			entry = cinSpan{at, at}
		}
		start := min(windowEnd, max(entry.start, previousStart))
		previousStart = start
		out[i] = cinSpan{start, min(windowEnd, max(entry.end, start))}
	}
	return out
}

// cinUnits：按切分方式分段，每段的窗口取它覆盖的字的最早开口和最晚唱完。
func cinUnits(text string, parts, graphemes []string, timeline []cinSpan, reveal cinSpan) []cinUnit {
	if text == "" {
		return nil
	}
	even := func() []cinUnit {
		span := max(.001, reveal.end-reveal.start)
		out := make([]cinUnit, len(parts))
		for i, part := range parts {
			out[i] = cinUnit{part, reveal.start + span*float64(i)/float64(len(parts)), reveal.start + span*float64(i+1)/float64(len(parts))}
		}
		return out
	}
	if strings.Join(parts, "") != text || len(timeline) != len(graphemes) {
		return even()
	}
	var units []cinUnit
	cursor := 0
	for _, part := range parts {
		size := len([]rune(part))
		to := min(cursor+size, len(timeline))
		if to <= cursor {
			cursor += size
			continue
		}
		start, end := math.Inf(1), math.Inf(-1)
		for i := cursor; i < to; i++ {
			start = math.Min(start, timeline[i].start)
			end = math.Max(end, timeline[i].end)
		}
		cursor += size
		units = append(units, cinUnit{part, start, max(end, start+.001)})
	}
	if len(units) == 0 {
		return even()
	}
	return units
}

// cinSegmentSpans：大字报每块覆盖的字的最早开口与最晚唱完；段前的空白跳过。
func cinSegmentSpans(parts, graphemes []string, timeline []cinSpan) []cinSpan {
	if len(parts) == 0 || len(graphemes) == 0 || len(timeline) == 0 {
		return nil
	}
	spans := make([]cinSpan, 0, len(parts))
	cursor := 0
	last := min(len(graphemes), len(timeline)) - 1
	for _, part := range parts {
		start, end := math.Inf(1), math.Inf(-1)
		for _, char := range part {
			for cursor < len(graphemes) && graphemes[cursor] != string(char) && strings.TrimSpace(graphemes[cursor]) == "" {
				cursor++
			}
			timing := timeline[min(cursor, last)]
			start = math.Min(start, timing.start)
			end = math.Max(end, timing.end)
			cursor++
		}
		if math.IsInf(start, 1) || math.IsInf(end, -1) {
			at := timeline[min(cursor, last)].start
			spans = append(spans, cinSpan{at, at})
		} else {
			spans = append(spans, cinSpan{start, max(end, start)})
		}
	}
	return spans
}

// ---------------------------------------------------------------- 跑马灯带

const (
	cinBands = iota // 上下两条
	cinFrame        // 四边一圈
)

const (
	cinSrcTitle = iota
	cinSrcLine
	cinSrcTranslation
	cinSrcLineTranslation
)

type cinMarquee struct {
	kind          int
	source        int
	orbit         int
	screensPerSec float64
	top, bottom   string
	mirror        bool
	ring          string
}

func cinBandText(source int, line, translation, title string) string {
	l, tr := strings.TrimSpace(line), strings.TrimSpace(translation)
	switch source {
	case cinSrcTitle:
		if title != "" {
			return strings.TrimSpace(title)
		}
		return l
	case cinSrcTranslation:
		if tr != "" {
			return tr
		}
		return l
	case cinSrcLineTranslation:
		var parts []string
		if l != "" {
			parts = append(parts, l)
		}
		if tr != "" {
			parts = append(parts, tr)
		}
		return strings.Join(parts, "　")
	}
	return l
}

// cinPickSource：四分之一跑歌名（盐 71），其余在原文 /（有翻译时）翻译 / 原文+翻译 里按盐 73 挑。
func cinPickSource(seed uint32, salt int, hasTranslation bool) int {
	if cinHash01(seed, salt, 71) < cinTitleChance {
		return cinSrcTitle
	}
	pool := []int{cinSrcLine}
	if hasTranslation {
		pool = []int{cinSrcLine, cinSrcTranslation, cinSrcLineTranslation}
	}
	return pool[int(math.Floor(cinHash01(seed, salt, 73)*float64(len(pool))))%len(pool)]
}

// cinMarqueeFor：丝带句没有带；“自动”按种子 55% 双带、45% 四边。
func cinMarqueeFor(style cinStyle, seed uint32, line, translation, title string) *cinMarquee {
	if style == cinRibbon {
		return nil
	}
	kind := cinBands
	if cinHash01(seed, 21, 151) < .55 {
		kind = cinBands
	} else {
		kind = cinFrame
	}
	salt := 7
	if kind == cinFrame {
		salt = 8
	}
	source := cinPickSource(seed, salt, strings.TrimSpace(translation) != "")
	orbit := 1
	if cinHash01(seed, 9, 83) < .5 {
		orbit = -1
	}
	speed := cinSpeedMin + cinHash01(seed, 10, 89)*cinSpeedSpan
	text := func(s int) string { return cinBandText(s, line, translation, title) }
	if kind == cinFrame {
		ring := text(source)
		return &cinMarquee{kind, source, orbit, speed, ring, ring, true, ring}
	}
	topSource := source
	if source == cinSrcLineTranslation {
		topSource = cinSrcLine
	}
	top := text(topSource)
	bottom := text(cinSrcTranslation)
	if source == cinSrcTitle {
		bottom = text(cinSrcTitle)
	}
	if bottom == "" {
		bottom = top
	}
	return &cinMarquee{kind, source, orbit, speed, top, bottom, bottom == top, top}
}

// cinBandShift：跑马灯的相位（0..1，一份字走过去为一圈），每秒走 screensPerSec × speed 个屏宽。
func cinBandShift(t, screensPerSec, speed, screenWidth, unitPx float64) float64 {
	gain := 1.0
	if speed > 0 {
		gain = speed
	}
	rate := gain / max(1e-3, screensPerSec*cinSpeedSpanSec)
	if screensPerSec > 0 && unitPx > 0 && screenWidth > 0 {
		rate = screensPerSec * gain * screenWidth / unitPx
	}
	return cinFrac(t * rate)
}

// cinBandSlot：一条带上第一份字的位移（以“份”为单位）。
func cinBandSlot(shift float64, orbit int, bottom, mirror bool) float64 {
	m := shift - 1
	if orbit > 0 {
		m = -shift
	}
	switch {
	case !bottom:
		return m
	case mirror:
		return m + 1
	default:
		return -1 - m
	}
}

// cinRingCopies：四边一圈排几份字、每个字再加多少字距，让整数份正好绕满一圈。
func cinRingCopies(lap, unit float64, glyphs int) (int, float64) {
	if !(lap > 0) || !(unit > 0) || glyphs <= 0 {
		return 1, 0
	}
	copies := max(1, int(cinRound(lap/unit)))
	spacing := (lap/float64(copies) - unit) / float64(glyphs)
	limit := -cinTightenRatio * (unit / float64(glyphs))
	if spacing < limit {
		copies = max(1, copies-1)
		spacing = (lap/float64(copies) - unit) / float64(glyphs)
	}
	return copies, spacing
}

// cinRingLap 是圆角矩形一圈的周长。
func cinRingLap(width, height, radius float64) float64 {
	return 2*max(0, width-2*radius) + 2*max(0, height-2*radius) + 2*math.Pi*radius
}

func cinRingOffset(shift float64, orbit int, unit float64) float64 {
	return cinFrac(float64(orbit)*shift) * unit
}

// ---------------------------------------------------------------- 转场

func cinAdjacent(end float64, nextStart float64, hasNext bool) bool {
	return hasNext && nextStart-end <= cinGapSec
}

func cinTransitionSec(start, nextStart float64) float64 {
	return cinClamp(max(.001, nextStart-start)*.22, .14, .28)
}

type cinEnvelope struct{ opacity, scale, bands float64 }

func cinDissolve(presence float64) (opacity, scale float64) {
	p := cinClamp(presence, 0, 1)
	away := 1 - p
	tail := 1 - cinClamp((away-.85)/.15, 0, 1)
	return p * tail, 1 - away*.03
}

type cinFrameState struct {
	current, outgoing int
	now, out          cinEnvelope
}

func cinPlanAt(starts []float64, t float64) int {
	lo, hi, found := 0, len(starts)-1, -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if starts[mid] <= t {
			found, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	return found
}

// cinEvaluate 算这一帧画哪一句、要不要叠上一句的退场层。全部按时间算，seek 稳定。
func cinEvaluate(program *cinProgram, t float64, still bool, between bool) cinFrameState {
	var f cinFrameState
	f.current, f.outgoing = -1, -1
	f.now, f.out = cinEnvelope{1, 1, 1}, cinEnvelope{1, 1, 1}
	index := cinPlanAt(program.starts, t)
	f.current = index
	if index < 0 {
		return f
	}
	start, end := program.starts[index], program.ends[index]
	hasNext := index+1 < len(program.starts)
	next := 0.0
	if hasNext {
		next = program.starts[index+1]
	}
	adjacent := cinAdjacent(end, next, hasNext)
	if still {
		gone := !adjacent && (t >= end || between)
		if gone {
			f.now = cinEnvelope{0, 1, 0}
		}
		return f
	}
	span := max(.001, end-start)
	enter := min(.28, span*.4)
	exit := 0.0
	if !adjacent {
		exit = min(.22, span*.35)
	}
	exitProgress := -1.0
	switch {
	case t < start+enter:
		o, s := cinDissolve(cinSmoothstep((t - start) / enter))
		f.now = cinEnvelope{o, s, 1}
	case exit > 0 && t > end-exit:
		exitProgress = min(1, (t-(end-exit))/exit)
		o, s := cinDissolve(1 - cinSmoothstep(exitProgress))
		f.now = cinEnvelope{o, s, 1}
	default:
		f.now = cinEnvelope{1, 1, 1}
	}
	bands := 1.0
	if exitProgress >= 0 {
		bands = 1 - cinSmoothstep(exitProgress*2)
	}
	if index > 0 && cinAdjacent(program.ends[index-1], start, true) {
		duration := cinTransitionSec(program.starts[index-1], start)
		elapsed := t - start
		if elapsed >= 0 && elapsed < duration {
			p := elapsed / duration
			o, s := cinDissolve(1 - cinSmoothstep(p))
			f.outgoing = index - 1
			f.out = cinEnvelope{o, s, 1 - cinSmoothstep(p*2)}
			bands = cinSmoothstep(p*2 - 1)
		}
	}
	f.now.bands = bands
	return f
}

// cinUnitProgress：单元出现进度，开口后 0.18 秒（或单元时长的一半）内 smoothstep 到 1。
func cinUnitProgress(start, end, t, enter float64) float64 {
	span := max(.001, end-start)
	return cinSmoothstep((t - start) / min(enter, span*.5))
}

// ---------------------------------------------------------------- 大字报

type cinMotion struct {
	scaleFrom, scaleTo float64
	xFrom, yFrom       float64
	trackFrom, trackTo float64
}

func (m cinMotion) tracks() bool { return m.trackFrom != 0 || m.trackTo != 0 }

var cinStillMotion = cinMotion{1, 1, 0, 0, 0, 0}

var cinMotionPool = []struct {
	weight float64
	motion cinMotion
}{
	{4, cinStillMotion},
	{2, cinMotion{.93, 1.5, -1.5, 0, 0, 0}},
	{2, cinMotion{1.5, .93, 1.5, 0, 0, 0}},
	{2, cinMotion{1, 1, -2.2, 0, 0, 0}},
	{1, cinMotion{1, 1, 0, -2.6, 0, 0}},
	{2, cinMotion{1, 1.3, 0, 0, 0, .15}},
}

func cinPickMotion(seed uint32, index int) cinMotion {
	total := 0.0
	for _, e := range cinMotionPool {
		total += e.weight
	}
	roll := cinHash01(seed, index, 149) * total
	for _, e := range cinMotionPool {
		roll -= e.weight
		if roll < 0 {
			return e.motion
		}
	}
	return cinMotionPool[0].motion
}

func cinCharWidthEm(r rune) float64 {
	switch {
	case r >= 0x3000 && r <= 0x9FFF, r >= 0xFF00 && r <= 0xFFEF, r >= 0x3040 && r <= 0x30FF:
		return 1
	case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return .62
	case r >= 'a' && r <= 'z':
		return .55
	case unicode.IsSpace(r):
		return .28
	}
	return .4
}

func cinChunkWidthEm(text string) float64 {
	sum := 0.0
	for _, r := range text {
		sum += cinCharWidthEm(r) + cinHeroTrackEm
	}
	return sum
}

// cinHeroMetrics 是大字报的尺寸约束（px）：字号上限、下限、“放得下”的门槛，以及可用宽度。
type cinHeroMetrics struct {
	maxFont, minFont, fitFont, usableWidth float64
}

func (m cinHeroMetrics) raw(text string) float64 {
	return min(m.maxFont, m.usableWidth/max(.4, cinChunkWidthEm(text)))
}

func (m cinHeroMetrics) font(text string) float64 {
	return cinClamp(m.raw(text), m.minFont, m.maxFont)
}

func (m cinHeroMetrics) fits(text string) bool { return m.font(text) >= m.fitFont }

func cinMetricsOf(screenW, screenH, contentW, contentH, sideInset float64) cinHeroMetrics {
	minFont := screenH * .112
	maxFont := max(minFont, min(screenH*.512, contentH/cinHeroLineHeight))
	usable := max(screenW*.2, min(screenW*.736-sideInset, contentW))
	return cinHeroMetrics{maxFont, minFont, screenH * .256, usable}
}

type cinChunk struct {
	text         string
	start, end   float64
	italic       bool
	font         float64
	fill         bool
	fillVertical bool
	motion       cinMotion
}

// cinSplitLongWord：一个词整块放不下时逐字劈开，每段尽量长。
func cinSplitLongWord(word string, fits func(string) bool) []string {
	var pieces []string
	current := ""
	for _, r := range word {
		next := current + string(r)
		if current != "" && !fits(next) {
			pieces = append(pieces, current)
			current = string(r)
			continue
		}
		current = next
	}
	if current != "" {
		pieces = append(pieces, current)
	}
	return pieces
}

// cinPackChunks：从一个词起，放得下且掷点小于 chance 就再并一个，最多 6 个词一块。
func cinPackChunks(words []string, chance float64, seed uint32, fits func(string) bool) []string {
	var chunks []string
	cursor, chunkIndex := 0, 0
	for cursor < len(words) {
		take := 1
		for take < cinMaxWordsPerChunk && cursor+take < len(words) &&
			fits(strings.Join(words[cursor:cursor+take+1], "")) &&
			cinHash01(seed, chunkIndex, 300+take) < chance {
			take++
		}
		chunks = append(chunks, strings.Join(words[cursor:cursor+take], ""))
		cursor += take
		chunkIndex++
	}
	return chunks
}

// cinMergeChunks：多于 5 块时并最窄的相邻两块（优先并完还放得下的），并不动就停。
func cinMergeChunks(chunks []string, metrics cinHeroMetrics) []string {
	packed := append([]string(nil), chunks...)
	for len(packed) > cinMaxChunks {
		bestIndex, narrowIndex := -1, -1
		bestWidth, narrowWidth := math.Inf(1), math.Inf(1)
		for i := 0; i < len(packed)-1; i++ {
			merged := packed[i] + packed[i+1]
			width := cinChunkWidthEm(merged)
			if width < narrowWidth && metrics.raw(merged) >= metrics.minFont {
				narrowWidth, narrowIndex = width, i
			}
			if !metrics.fits(merged) || width >= bestWidth {
				continue
			}
			bestWidth, bestIndex = width, i
		}
		at := bestIndex
		if at < 0 {
			at = narrowIndex
		}
		if at < 0 {
			break
		}
		next := append([]string{}, packed[:at]...)
		next = append(next, packed[at]+packed[at+1])
		packed = append(next, packed[at+2:]...)
	}
	return packed
}

// cinFillChance：这一块几个可见字 → 描边填色的概率，1 字 0、5 字起封顶 2/15。
func cinFillChance(visible int) float64 {
	return cinFillCap * cinSmoothstep(float64(visible-1)/4)
}

func cinVisibleCount(text string) int {
	n := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			n++
		}
	}
	return n
}

// cinHeroChunks 照原版 buildHeroChunks：按词分块（放不下的词逐字劈），按词时序定每块的闪出窗口，
// 再给每块抽斜体、字号微调（相邻不重复）、描边填色、持续运动。
func cinHeroChunks(plan *cinPlan, metrics cinHeroMetrics) []cinChunk {
	seed := plan.seed
	text := plan.line.Text
	var words []string
	for _, w := range plan.words {
		if metrics.fits(w) {
			words = append(words, w)
		} else {
			words = append(words, cinSplitLongWord(w, metrics.fits)...)
		}
	}
	if len(words) == 0 {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			trimmed = "　"
		}
		words = append(words, trimmed)
	}
	packedRaw := cinMergeChunks(cinPackChunks(words, cinChunkChance, seed, metrics.fits), metrics)
	packed := make([]string, len(packedRaw))
	for i, p := range packedRaw {
		packed[i] = strings.TrimRight(p, " \t")
	}

	reveal := plan.heroReveal
	spans := cinSegmentSpans(packed, plan.graphemes, plan.heroTimeline)
	ordered := spans != nil
	for i := 1; ordered && i < len(spans); i++ {
		if spans[i].start <= spans[i-1].start+1e-4 {
			ordered = false
		}
	}
	var windows []cinSpan
	if ordered {
		starts := make([]float64, len(spans))
		for i, s := range spans {
			starts[i] = cinClamp(s.start, reveal.start, reveal.end)
		}
		windows = make([]cinSpan, len(packed))
		for i := range packed {
			end := reveal.end
			if i+1 < len(packed) {
				end = starts[i+1]
			}
			windows[i] = cinSpan{starts[i], max(starts[i]+.001, end)}
		}
	} else {
		step := max(.001, reveal.end-reveal.start) / float64(len(packed))
		windows = make([]cinSpan, len(packed))
		for i := range packed {
			windows[i] = cinSpan{reveal.start + step*float64(i), reveal.start + step*float64(i+1)}
		}
	}

	fitFont := metrics.maxFont
	for _, chunk := range packed {
		fitFont = min(fitFont, metrics.font(chunk))
	}
	lineFont := cinClamp(fitFont, metrics.minFont, metrics.maxFont)
	scales := []float64{.95, 1, 1.05}
	var previousScale float64
	hasScale := false
	previousAxis := false
	hasAxis := false
	out := make([]cinChunk, len(packed))
	for i, chunk := range packed {
		factor := cinChoose(scales, fmt.Sprintf("%d:chunk-scale:%d", seed, i), previousScale, hasScale)
		previousScale, hasScale = factor, true
		vertical := cinChoose([]bool{false, true}, fmt.Sprintf("%d:fill-axis:%d", seed, i), previousAxis, hasAxis)
		previousAxis, hasAxis = vertical, true
		fill := cinHash01(seed, i, 191) < cinFillChance(cinVisibleCount(chunk))
		motion := cinStillMotion
		if !fill {
			motion = cinPickMotion(seed, i)
		}
		out[i] = cinChunk{
			text: chunk, start: windows[i].start, end: windows[i].end,
			italic: cinHash01(seed, i, 113) < cinItalicChance,
			font:   cinClamp(lineFont*factor, metrics.minFont, metrics.font(chunk)),
			fill:   fill, fillVertical: vertical, motion: motion,
		}
	}
	return out
}

// cinChunkLook 是一块这一帧的样子。不在窗口里时 ok=false。
func cinChunkLook(c cinChunk, t float64) (ok bool, look cinChunkState) {
	if t < c.start || t > c.end {
		return false, look
	}
	fadeIn := cinUnitProgress(c.start, c.start+cinHeroEnter*2, t, cinHeroEnter)
	fadeOut := cinUnitProgress(c.end-cinHeroExit*2, c.end, t, cinHeroExit)
	visible := fadeIn * (1 - fadeOut)
	if visible <= .001 {
		return false, look
	}
	span := max(.001, c.end-c.start)
	u := cinClamp((t-c.start)/span, 0, 1)
	m := c.motion
	amount := min(1, span/cinMotionRefSec)
	glide := 1 + ((1-u)*(m.scaleFrom-1)+u*(m.scaleTo-1))*amount
	look.opacity = visible
	look.driftX = m.xFrom * (1 - u) * amount
	look.driftY = m.yFrom * (1 - u) * amount
	look.scale = (0.92 + .08*fadeIn) * glide
	look.trackEm = ((1-u)*m.trackFrom + u*m.trackTo) * amount
	look.fill = u
	return true, look
}

type cinChunkState struct {
	opacity, driftX, driftY, scale, trackEm, fill float64
}

// ---------------------------------------------------------------- 斜切丝带

type cinStrip struct {
	text                        string
	angle, topPct, thicknessPct float64
	driftDir                    int
	driftVelScale               float64
	shadeAngle, shadeStart      float64
	shadeSpan, shadeStrength    float64
}

func (s cinStrip) fontPct() float64 { return s.thicknessPct * cinRibbonFontRatio }

func (s cinStrip) centerFrac() float64 {
	return cinClamp(s.topPct/100+s.thicknessPct/200, 0, 1)
}

var cinRibbonAnglesPool = []float64{-54, -42, -32, -23, -13, 12, 21, 31, 41, 53}

// cinRibbonAngles：角度池洗牌后左右两侧轮流取，|角度| < 26° 的平角最多 count/3 条。
func cinRibbonAngles(count int, seed uint32) []float64 {
	pool := cinShuffle(cinRibbonAnglesPool, seed, 211)
	var sides [2][]float64
	for _, a := range pool {
		if a < 0 {
			sides[0] = append(sides[0], a)
		} else if a > 0 {
			sides[1] = append(sides[1], a)
		}
	}
	shallowCap := max(1, count/3)
	shallowUsed := 0
	side := 0
	if cinHash01(seed, 0, 281) >= .5 {
		side = 1
	}
	var picked []float64
	for len(picked) < count {
		list := sides[side]
		for len(list) > 0 {
			angle := list[0]
			list = list[1:]
			shallow := math.Abs(angle) < cinRibbonShallow
			if shallow && shallowUsed >= shallowCap {
				continue
			}
			if shallow {
				shallowUsed++
			}
			picked = append(picked, angle)
			break
		}
		sides[side] = list
		side = 1 - side
		if len(sides[0]) == 0 && len(sides[1]) == 0 {
			break
		}
	}
	out := make([]float64, len(picked))
	for i, a := range picked {
		out[i] = cinClamp(a, -cinRibbonAngleLimit, cinRibbonAngleLimit)
	}
	return out
}

// cinRibbonTops：从屏顶外 6% 到屏底外 4% 均匀铺开，洗牌后再各自上下抖 ±10%。
func cinRibbonTops(count int, seed uint32) []float64 {
	base := make([]float64, count)
	for i := range base {
		base[i] = -6 + 104*float64(i)/float64(max(1, count-1))
	}
	shuffled := cinShuffle(base, seed, 223)
	for i := range shuffled {
		shuffled[i] += (cinHash01(seed, i, 227) - .5) * 20
	}
	return shuffled
}

// cinRibbonStrips：[count] 条（3–9），文字在原文和翻译之间轮流；整句和翻译都空时没有丝带。
func cinRibbonStrips(seed uint32, count int, line, translation string) []cinStrip {
	var texts []string
	for _, t := range []string{strings.TrimSpace(line), strings.TrimSpace(translation)} {
		if t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	n := min(9, max(3, count))
	angles := cinRibbonAngles(n, seed)
	tops := cinRibbonTops(n, seed)
	out := make([]cinStrip, n)
	for i := range out {
		thickness := cinRibbonMin + cinHash01(seed, i, 241)*(cinRibbonMax-cinRibbonMin)
		dir := 1
		if cinHash01(seed, i, 271) < .5 {
			dir = -1
		}
		out[i] = cinStrip{
			text:         texts[i%len(texts)],
			angle:        angles[i%len(angles)],
			topPct:       cinClamp(tops[i], -(thickness-cinRibbonMinVisible), 100-cinRibbonMinVisible),
			thicknessPct: thickness,
			driftDir:     dir,
			driftVelScale: .8 + cinHash01(seed, i, 277)*.4,
			shadeAngle:   100 + cinHash01(seed, i, 251)*160,
			shadeStart:   cinHash01(seed, i, 257) * 20,
			shadeSpan:    46 + cinHash01(seed, i, 263)*26,
			shadeStrength: .2 + cinHash01(seed, i, 269)*.22,
		}
	}
	return out
}

// cinRibbonCoverage：斜放的丝带要多长才能横穿整屏。
func cinRibbonCoverage(width, height, angle, centerFrac float64) float64 {
	rad := math.Abs(angle) * math.Pi / 180
	c, s := math.Abs(math.Cos(rad)), math.Abs(math.Sin(rad))
	center := cinClamp(centerFrac, 0, 1)
	return width*c + 2*max(center, 1-center)*height*s
}

// cinRibbonLength：丝带盒子的长度，两端各至少伸出屏宽 8%，还要够漂移那一段。
func cinRibbonLength(width, height float64, strip cinStrip, driftClampPx float64) float64 {
	rad := math.Abs(strip.angle) * math.Pi / 180
	need := cinRibbonCoverage(width, height, strip.angle, strip.centerFrac())*(1+cinRibbonMargin) +
		2*math.Abs(driftClampPx)*math.Abs(math.Sin(rad))
	overflow := max(cinRibbonMinOverflow, (need/width-1)/2*100)
	return width * (1 + 2*overflow/100)
}

// cinRibbonDriftClamp：丝带上下漂移的上限（px）：每条丝带中线离屏边还剩多远换算成沿漂移方向能走多少，取最小；
// 走完这段要的时间比“句长 + 1 秒”短时按比例放宽（最多 12 倍、不超过屏高 15%）。再按实际屏高缩放。
func cinRibbonDriftClamp(strips []cinStrip, spanSec, height float64) float64 {
	if len(strips) == 0 {
		return cinDriftClamp * height / 1000
	}
	const refH = 1000.0
	margin := refH * cinDriftMargin / 100
	limit := math.Inf(1)
	for _, strip := range strips {
		sinAbs := math.Abs(math.Sin(strip.angle * math.Pi / 180))
		edge := min(strip.centerFrac(), 1-strip.centerFrac()) * refH
		if sinAbs < 1e-6 || edge <= margin {
			limit = min(limit, cinDriftClamp)
		} else {
			limit = min(limit, (edge-margin)/sinAbs)
		}
	}
	minReach := spanSec + cinTravelTail
	shortest := math.Inf(1)
	for _, strip := range strips {
		sinAbs := math.Abs(math.Sin(strip.angle * math.Pi / 180))
		vel := cinDriftVel * strip.driftVelScale
		if vel > 0 && sinAbs >= 1e-6 {
			shortest = min(shortest, limit*sinAbs/vel)
		}
	}
	if !math.IsInf(shortest, 1) && shortest < minReach {
		ceiling := refH * cinDriftMax / 100
		limit = min(limit*(minReach/shortest), min(limit*12, ceiling))
	}
	return limit * height / refH
}

func cinRibbonDrift(strip cinStrip, elapsed, height, clampPx float64) float64 {
	vel := cinDriftVel * strip.driftVelScale * height / 1000
	return cinClamp(float64(strip.driftDir)*vel*max(0, elapsed), -clampPx, clampPx)
}

// cinRibbonScroll：丝带上的字往左走了多少（px），每秒 0.16 个屏宽，走满“句长 + 1 秒”就停。
func cinRibbonScroll(elapsed, width, spanSec float64) float64 {
	speed := cinRibbonTextSpeed * width
	return min(max(0, elapsed)*speed, speed*(spanSec+cinTravelTail))
}

// cinShadeStops 是丝带斜向光泽的六个色标（0..1），按 CSS 的规矩单调化。
func cinShadeStops(strip cinStrip) []float64 {
	s, span := strip.shadeStart, strip.shadeSpan
	raw := []float64{s, s + span*.5, s + span, s + span + 12, min(96, s+span*1.5+12), 100}
	out := make([]float64, len(raw))
	last := 0.0
	for i, v := range raw {
		last = max(last, v)
		out[i] = cinClamp(last, 0, 100) / 100
	}
	return out
}

// ---------------------------------------------------------------- 辉光

// cinErfc 是互补误差函数（Abramowitz–Stegun 7.1.26，误差 < 1.5e-7）。
func cinErfc(x float64) float64 {
	if x < 0 {
		return 2 - cinErfc(-x)
	}
	t := 1 / (1 + .3275911*x)
	poly := t * (.254829592 + t*(-.284496736+t*(1.421413741+t*(-1.453152027+t*1.061405429))))
	return poly * math.Exp(-x*x)
}

// cinShadowCoverage 是 CSS 盒阴影在边外 / 边内 distance 处的覆盖率：边上 0.5，往外按高斯尾巴衰减。
func cinShadowCoverage(distance, blur float64) float64 {
	sigma := blur / 2
	if sigma <= 1e-6 {
		if distance <= 0 {
			return 1
		}
		return 0
	}
	return .5 * cinErfc(distance/(sigma*math.Sqrt2))
}

// 渐变取样的位置（靠边处密一点）。
var cinGlowStops = []float32{0, .06, .14, .25, .4, .6, .8, 1}

// cinEdgeGlow 是跑马灯带边线的辉光：屏面一侧是亮芯 + 中间层，带子一侧是往外溢的一层。
type cinEdgeGlow struct {
	extent float32
	screen []float32
	band   []float32
}

func cinEdgeGlowOf(band float64) cinEdgeGlow {
	response := cinAlphaFloor + (1-cinAlphaFloor)*cinEdgeGlowStrength
	core := band * .22 * cinEdgeGlowStrength
	mid := band * .5 * cinEdgeGlowStrength
	wide := band * .5 * cinEdgeGlowStrength
	extent := 1.5 * max(core, max(mid, wide))
	screen := make([]float32, len(cinGlowStops))
	outward := make([]float32, len(cinGlowStops))
	for i, stop := range cinGlowStops {
		d := float64(stop) * extent
		a := response * cinShadowCoverage(d, core)
		b := .42 * response * cinShadowCoverage(d, mid)
		screen[i] = float32(1 - (1-a)*(1-b))
		outward[i] = float32(.22 * response * cinShadowCoverage(d, wide))
	}
	return cinEdgeGlow{float32(extent), screen, outward}
}

// cinShadowStops 是一层盒阴影在 0..extent 上按取样点算出的透明度。
func cinShadowStops(alpha, blur, offset, extent float64) []float32 {
	out := make([]float32, len(cinGlowStops))
	for i, stop := range cinGlowStops {
		out[i] = float32(alpha * cinShadowCoverage(float64(stop)*extent-offset, blur))
	}
	return out
}

// ---------------------------------------------------------------- 小字报排版

// cinFlow 把单元像 inline-block 一样流式换行、整块居中，返回每个单元左上角（x、y 交替）。
func cinFlow(widths []float32, blank []bool, rowHeight, maxWidth float32) (pos []float32, boxW, boxH float32) {
	pos = make([]float32, len(widths)*2)
	for i := range pos {
		pos[i] = float32(math.NaN())
	}
	var rowStart []int
	var rowWidth []float32
	x, ink := float32(0), float32(0)
	first := -1
	closeRow := func() {
		rowStart = append(rowStart, first)
		rowWidth = append(rowWidth, ink)
		x, ink, first = 0, 0, -1
	}
	for i, w := range widths {
		if first >= 0 && x+w > maxWidth+.5 {
			closeRow()
			if blank[i] {
				continue
			}
		}
		if first < 0 && blank[i] {
			continue
		}
		if first < 0 {
			first = i
		}
		pos[i*2] = x
		pos[i*2+1] = float32(len(rowStart)) * rowHeight
		x += w
		if !blank[i] {
			ink = x
		}
	}
	if first >= 0 {
		closeRow()
	}
	for _, w := range rowWidth {
		boxW = max(boxW, w)
	}
	boxH = float32(len(rowStart)) * rowHeight
	for r := range rowStart {
		shift := (boxW - rowWidth[r]) / 2
		to := len(widths)
		if r+1 < len(rowStart) {
			to = rowStart[r+1]
		}
		for i := rowStart[r]; i < to; i++ {
			if !math.IsNaN(float64(pos[i*2])) {
				pos[i*2] += shift
			}
		}
	}
	return pos, boxW, boxH
}

// ---------------------------------------------------------------- 编译

type cinPlan struct {
	index        int
	line         *lyrics.Line
	start, end   float64
	seed         uint32
	split        cinSplit
	layout       int
	style        cinStyle
	words        []string
	units        []cinUnit
	marquee      *cinMarquee
	graphemes    []string
	heroTimeline []cinSpan
	heroReveal   cinSpan
}

type cinProgram struct {
	plans  []cinPlan
	starts []float64
	ends   []float64
}

// cinCompile 照原版 compileCineramaProgram：每句派生句种子，抽切分（相邻不重复）、排版、样式（权重 + 相邻不重复），
// 再定跑马灯带。行窗口取“句末 + 0.6 秒”，夹到下一句开口。
func cinCompile(lines []lyrics.Line, seedStr, title string, weights cinWeights, marqueeOn bool) *cinProgram {
	program := &cinProgram{}
	previousSplit, previousLayout, previousStyle := cinWhole, -1, cinSmall
	hasSplit, hasStyle := false, false
	for index := range lines {
		line := &lines[index]
		start := line.At.Seconds()
		authoredEnd := colorStageEnd(line).Seconds()
		rawEnd := max(start+.001, authoredEnd+cinTailSec)
		nextStart := math.Inf(1)
		if index+1 < len(lines) {
			nextStart = lines[index+1].At.Seconds()
		}
		end := max(start+.001, min(rawEnd, nextStart))
		text := line.Text
		lineSeed := fmt.Sprintf("%s:%d:%s", seedStr, index, text)
		split := cinPickSplit(text, lineSeed+":split", previousSplit, hasSplit)
		previousSplit, hasSplit = split, true
		layout := cinPickLayout(fnv32(lineSeed+":layout"), previousLayout)
		previousLayout = layout

		graphemes := make([]string, 0, len([]rune(text)))
		for _, r := range text {
			graphemes = append(graphemes, string(r))
		}
		words := make([]cinWord, 0, len(line.Words))
		for _, w := range line.Words {
			words = append(words, cinWord{w.Text, w.Start.Seconds(), w.End.Seconds()})
		}
		lastWordEnd := 0.0
		hasWords := len(words) > 0
		if hasWords {
			lastWordEnd = words[len(words)-1].end
		}
		rawReveal := cinReveal(start, rawEnd, lastWordEnd, hasWords, authoredEnd)
		rawTimeline := cinTimeline(graphemes, words, start, rawEnd, rawReveal)
		units := cinUnits(text, cinParts(text, split), graphemes, rawTimeline, rawReveal)
		heroReveal := cinReveal(start, end, lastWordEnd, hasWords, authoredEnd)
		heroTimeline := rawTimeline
		if end != rawEnd {
			heroTimeline = cinTimeline(graphemes, words, start, end, heroReveal)
		}

		planSeed := fnv32(lineSeed)
		style := cinPickStyle(planSeed, previousStyle, hasStyle, weights, len(graphemes) > 0, len(units) > 0 || strings.TrimSpace(text) == "")
		previousStyle, hasStyle = style, true
		var marquee *cinMarquee
		if marqueeOn {
			marquee = cinMarqueeFor(style, planSeed, text, line.Translation, title)
		}
		reveal := rawReveal
		if end != rawEnd {
			reveal = heroReveal
		}
		program.plans = append(program.plans, cinPlan{
			index: index, line: line, start: start, end: end, seed: planSeed,
			split: split, layout: layout, style: style, units: units, marquee: marquee, words: cinLineWords(text, line.Words),
			graphemes: graphemes, heroTimeline: heroTimeline, heroReveal: reveal,
		})
		program.starts = append(program.starts, start)
		program.ends = append(program.ends, end)
	}
	return program
}
