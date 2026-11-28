package app

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

const (
	stageListHold   = 3 * time.Second
	stageListIntro  = time.Second
	stageListBreak  = 5 * time.Second
	stageListTravel = 320 * time.Millisecond
)

type stageListState struct {
	layout stageListLayout
	scroll ui.ScrollState
	shown  float32
	manual time.Time
	glide  struct {
		at       time.Time
		from, to float32
	}
	pressedRow   int // 即使拖出可见区，也保留按下的那一行直到松手
	dragDistance float32
}

type stageListLayout struct {
	key    stageListLayoutKey
	rows   []stageListRow
	font   float32
	end    float32
	width  float32
	anchor float32
	gap    float32
}

type stageListLayoutKey struct {
	style, track, locale string
	first                *lyrics.Line
	count                int
	width, height        float32
	direction            ui.Direction
}

type stageListRow struct {
	index                    int // -1 表示没有跳播行为的间奏或空状态
	at, end                  time.Duration
	top, height, subtitleTop float32
	text, subtitle           *ui.TextLayout
	words                    []stageListWord
	flow                     []stageListFlowWord // rush 的逐词排版：词间留词距，放不下就折行
	units                    int                 // MyGo 的文本范围以 UTF-16 为单位
}

// stageListFlowWord 是一个词在句子里的位置，仿 myMusic Rush 的 SyllableLine 排法。
type stageListFlowWord struct {
	layout     *ui.TextLayout
	x, y, w, h float32
	start, end time.Duration
}

type stageListWord struct {
	start, end time.Duration
	through    int
}

type stageListLook struct {
	box, disc ui.Rect
	font      float32
	anchor    float32
	align     ui.Align
}

func (a *app) renderStageListMode(c *ui.Context, s stageFrame) {
	if s.w <= 0 || s.h <= 0 {
		return
	}
	// 经典列表照 myMusic LyricsList：左对齐大字，当前句停在 42% 高度处。
	look := stageListLook{font: max(13, min(56, s.w*0.058, s.h*0.16)), anchor: 0.42, align: ui.Start}
	width := min(s.w*0.9, 880)
	look.box = ui.Rect{X: (s.w - width) / 2, W: width, H: s.h}
	switch s.style {
	case "rush":
		drawStageListRushBackground(c, s)
		// 照 myMusic Rush：字号约窗口宽的 7%，当前句停在三分之一处，最多 720dp 宽、靠左。
		look.font, look.anchor, look.align = max(14, min(64, s.w*0.070, s.h*0.16)), 1.0/3.0, ui.Start
		look.box.W = min(s.w*0.84, 720)
		look.box.X = max(s.w*0.075, (s.w-look.box.W)*0.34)
	case "vinyl":
		// 黑胶唱片是一整块固定版式（唱片 + 前 2/后 4 句），不走可滚动的列表视口。
		a.stageListVinyl(c, s)
		return
	}

	key := stageListLayoutKey{style: s.style, track: s.trackKey, locale: c.Locale(), count: len(s.lines),
		width: look.box.W, height: look.box.H, direction: c.Direction()}
	if len(s.lines) > 0 {
		key.first = &s.lines[0]
	}
	viewport := ui.Box(c.Key("list-viewport")).Absolute().Left(look.box.X).Top(look.box.Y).
		Width(look.box.W).Height(look.box.H).Clip().PassThrough()
	viewport.Children(func() {
		if s.preview {
			layout := makeStageListLayout(s, look, key)
			active := layout.active(s)
			showStageListRows(c, a, nil, s, &layout, active, layout.target(active), false)
			return
		}
		state := &a.stageList
		fresh := state.layout.key != key || state.layout.font != look.font
		if fresh {
			state.layout = makeStageListLayout(s, look, key)
			state.manual, state.glide.at = time.Time{}, time.Time{}
			state.pressedRow, state.dragDistance = -1, 0
		}
		layout := &state.layout
		active := layout.active(s)
		offset, manual := state.follow(c, s, layout.target(active), max(0, layout.end-layout.key.height), fresh)
		scroll := ui.Scroll(c.Key("list-scroll")).Absolute().Top(0).Left(0).Bottom(0).Right(0).
			Scrollbars(true).TrackScroll(&state.scroll)
		scroll.Children(func() {
			ui.Box(c.Key("list-paper")).Width(look.box.W).Height(layout.end).Shrink(0).Children(func() {
				showStageListRows(c, a, state, s, layout, active, offset, manual)
			})
		})
	})
}

func (st *stageListState) follow(c *ui.Context, s stageFrame, target, limit float32, fresh bool) (float32, bool) {
	now := c.Now()
	offset := min(limit, max(0, st.scroll.Y))
	// TrackScroll 接住滚轮与滚动条。只认外部改动，避免把自己的跟随过渡误当成用户滚动。
	if !fresh && math.Abs(float64(offset-st.shown)) > 0.5 {
		st.manual = now.Add(stageListHold)
	}
	manual := now.Before(st.manual) || st.pressedRow >= 0 && st.dragDistance >= 6
	switch {
	case manual:
		st.glide.at = time.Time{}
		if now.Before(st.manual) {
			c.After(st.manual.Sub(now)) // 暂停时也要能回到当前句
		}
	case fresh || s.motion <= 0 || math.Abs(float64(offset-target)) > 2*float64(st.layout.key.height):
		offset, st.glide.to, st.glide.at = target, target, time.Time{}
	default:
		if st.glide.to != target || st.glide.at.IsZero() && offset != target {
			st.glide.at, st.glide.from, st.glide.to = now, offset, target
		}
		if !st.glide.at.IsZero() {
			t := float32(now.Sub(st.glide.at)) / float32(stageListTravel)
			offset = lerp(st.glide.from, target, easeOutCubic(t))
			if t < 1 {
				c.AnimationFrame()
			} else {
				st.glide.at = time.Time{}
			}
		}
	}
	st.scroll.Y, st.shown = offset, offset
	return offset, manual
}

func makeStageListLayout(s stageFrame, look stageListLook, key stageListLayoutKey) stageListLayout {
	// 行距和行高对齐 myMusic：Rush 用 32/28 的行高、行间再让半行；经典列表留 0.78 个字高。
	gap, lineHeight := look.font*0.32, float32(1.19)
	switch s.style {
	case "rush":
		gap, lineHeight = look.font*(32.0/28.0/2), 32.0/28.0
	case "classic":
		gap, lineHeight = look.font*0.78, 1.3
	}
	layout := stageListLayout{key: key, font: look.font, width: max(1, key.width-min(key.width*0.12, 28)),
		anchor: key.height * look.anchor, gap: gap}
	lang := lyrics.Lang(s.lines)
	if lang == "" {
		lang = key.locale
	}
	shape := ui.TextShape{Width: layout.width, Align: look.align, Direction: key.direction}
	font := ui.Font{Size: look.font, Weight: 780, Lang: lang, LineHeight: lineHeight}
	subtitle := ui.Font{Size: look.font * 0.56, Weight: 560, Lang: key.locale, LineHeight: 1.22}
	if s.style == "classic" {
		subtitle = ui.Font{Size: look.font * 0.62, Weight: 500, Lang: key.locale, LineHeight: 0.62 * 1.25}
	}
	if s.style == "rush" {
		subtitle = ui.Font{Size: look.font * 0.75, Weight: 500, Lang: key.locale, LineHeight: 0.75 * 32.0 / 28.0}
	}
	nextTop := layout.anchor
	add := func(row stageListRow) {
		row.top = nextTop
		nextTop += row.height + layout.gap
		layout.rows = append(layout.rows, row)
	}
	addBreak := func(from, to time.Duration) {
		// 间奏三点的行高：上下各 12/28 字高留白 + 8/28 的点。
		add(stageListRow{index: -1, at: from, end: to, height: look.font * (32.0 / 28.0)})
	}
	previous := -1
	for i := range s.lines {
		line := &s.lines[i]
		if strings.TrimSpace(line.Text) == "" && strings.TrimSpace(line.Translation) == "" {
			continue
		}
		if s.style == "rush" {
			switch {
			case previous < 0 && line.At >= stageListIntro:
				addBreak(0, line.At)
			case previous >= 0:
				end := s.lines[previous].End
				if end > s.lines[previous].At && line.At-end >= stageListBreak {
					addBreak(end, line.At)
				}
			}
		}
		text := ui.ShapeRichTextWith([]ui.Span{{Text: line.Text}}, font, shape)
		_, height := text.Size()
		row := stageListRow{index: i, at: line.At, end: line.End, text: text, height: height, units: stageListTextUnits(line.Text)}
		if line.WordsSource {
			row.words = stageListTimedWords(*line, row.units)
		}
		if s.style == "rush" {
			// Rush 逐词排版：词距 8/28 个字高，按列表宽度折行，行高由折行结果决定。
			row.flow, row.height = makeStageListFlow(*line, font, layout.width, look.font*(8.0/28.0), shape)
		}
		if strings.TrimSpace(line.Translation) != "" {
			subGap := look.font * 0.15
			if s.style == "rush" {
				subGap = look.font * (4.0 / 28.0)
			}
			row.subtitleTop = row.height + subGap
			row.subtitle = ui.ShapeRichTextWith([]ui.Span{{Text: line.Translation}}, subtitle, shape)
			_, h := row.subtitle.Size()
			row.height = row.subtitleTop + h
		}
		add(row)
		previous = i
	}
	if len(layout.rows) == 0 {
		if s.style == "rush" {
			addBreak(0, 0)
		} else {
			text := ui.ShapeRichTextWith([]ui.Span{{Text: T("No lyrics. Enjoy the music.")}}, subtitle, shape)
			_, h := text.Size()
			add(stageListRow{index: -1, text: text, height: h})
		}
	}
	// 末行也能停在锚点，手动浏览时还能完整看完换行和译文。
	last := layout.rows[len(layout.rows)-1]
	layout.end = max(nextTop-layout.gap, last.top+last.height/2-layout.anchor+key.height)
	return layout
}

func (l *stageListLayout) active(s stageFrame) int {
	i := sort.Search(len(l.rows), func(i int) bool { return l.rows[i].at > s.pos }) - 1
	if i < 0 {
		return -1
	}
	row := &l.rows[i]
	// 标记或空行可能被过滤。焦点留在歌词附近，不能跳回开头。
	if row.index >= 0 && s.current >= row.index || s.style == "rush" && row.text == nil {
		return i
	}
	return -1
}

func (l *stageListLayout) target(active int) float32 {
	if active < 0 {
		return 0
	}
	// 当前句的中心对到锚点（Rush 是 1/3，经典列表 0.42），和 myMusic 的 anchorError 一致。
	row := l.rows[active]
	return row.top + row.height/2 - l.anchor
}

func showStageListRows(c *ui.Context, a *app, st *stageListState, s stageFrame, l *stageListLayout, active int, offset float32, manual bool) {
	first := sort.Search(len(l.rows), func(i int) bool { return l.rows[i].top+l.rows[i].height+l.gap >= offset })
	last := sort.Search(len(l.rows), func(i int) bool { return l.rows[i].top-l.gap > offset+l.key.height })
	for i := first; i < last; i++ {
		a.showStageListRow(c.Key(i), st, s, l, i, active, offset, manual)
	}
	if st != nil && st.pressedRow >= 0 && (st.pressedRow < first || st.pressedRow >= last) {
		i := st.pressedRow
		a.showStageListRow(c.Key(i), st, s, l, i, active, offset, manual)
	}
}

func (a *app) showStageListRow(c *ui.Context, st *stageListState, s stageFrame, l *stageListLayout, i, active int, offset float32, manual bool) {
	row := &l.rows[i]
	on := i == active
	// 上下两端渐隐：进来的行从 0 渐亮，离开的行渐暗，别在视口边缘硬切。
	band := l.font * 1.6
	edge := min(1, max(0, (row.top+row.height-offset)/band), max(0, (offset+l.key.height-row.top)/band))
	alpha := float32(0.48)
	if !manual && !on {
		distance := absStageListIndex(i - max(0, active))
		// 经典列表照 myMusic：相邻句 60%，再远 38%；Rush 另按过去/未来分层。
		alpha = max(0.17, 0.55-float32(distance)*0.10)
		if s.style == "classic" {
			alpha = 0.38
			if distance == 1 {
				alpha = 0.6
			}
		}
	}
	if on {
		alpha = 1
	}
	alpha *= edge

	top := row.top - l.gap/2
	var item ui.Element
	if st == nil || row.index < 0 {
		if st == nil {
			top -= offset
		}
		item = ui.Box(c).PassThrough()
	} else {
		line := s.lines[row.index]
		label := line.Text
		if strings.TrimSpace(label) == "" {
			label = line.Translation
		}
		item = ui.ButtonBase(c).Label(label).Radius(l.gap)
		if item.Hovered() {
			alpha = max(alpha, 0.74)
			item.Background(white.Alpha(0.06))
		}
		dragged := st.readDrag(c, item, i)
		if item.Clicked() && !dragged {
			a.pl.Seek(row.at)
			st.manual = time.Time{}
			c.AnimationFrame()
		}
	}
	item.Absolute().Left(0).Top(top).Width(l.key.width).Height(row.height + l.gap)
	pad := (l.key.width - l.width) / 2
	if row.text == nil {
		item.Draw(func(p *ui.Painter, r ui.Rect) {
			dots := alpha
			if i > active {
				dots = alpha * 0.3
			}
			drawStageListDots(p, r, pad, l.font, *row, s, dots)
		})
		return
	}
	ink := white
	// Rush：当前句 1.0、前后句 0.8（myMusic 的 RUSH_OTHER_SCALE），不再另外做逐行缩放。
	if s.style == "rush" {
		scale, origin := float32(0.8), float32(0)
		if l.key.direction == ui.RightToLeft {
			origin = 1
		}
		if on || manual || active < 0 {
			scale = 1
		}
		item.TransformOrigin(origin, 0.5).Scale(scale, scale)
		item.Draw(func(p *ui.Painter, r ui.Rect) {
			x, y := r.X+pad, r.Y+l.gap/2
			if row.subtitle != nil {
				p.TextLayout(row.subtitle, x, y+row.subtitleTop, ink.Alpha(alpha*0.7))
			}
			// 过去的句子整句亮；还没唱的只有 20% 的底层；当前句底层 50%，唱到的词另叠一层。
			tone := float32(0.2)
			switch {
			case i < active:
				tone = 1
			case on:
				tone = 0.5
			}
			for _, w := range row.flow {
				p.TextLayout(w.layout, x+w.x, y+w.y, ink.Alpha(alpha*tone))
			}
		})
		if on && s.motion > 0 {
			feather := l.font * (16.0 / 28.0)
			item.Children(func() {
				for wi, w := range row.flow {
					progress := stageListFlowProgress(w, s.pos)
					if progress <= 0 {
						continue
					}
					drawStageListFlowWord(c, wi, w, pad, l.gap/2, ink.Alpha(alpha), progress, feather)
				}
			})
		}
		return
	}
	base := ink.Alpha(alpha)
	// 经典列表照 myMusic：当前句放大到 1.22，其余行保持原字号，只降透明度。
	if s.style == "classic" {
		scale := float32(1)
		if on {
			scale = 1.22
		}
		origin := float32(0.5)
		if l.key.direction == ui.RightToLeft {
			origin = 1
		} else {
			origin = 0
		}
		item.TransformOrigin(origin, 0.5).Scale(scale, scale)
	}
	sweep := on && s.motion > 0 && len(row.words) > 0 && row.units > 0
	if sweep {
		// 未唱的部分留 45%，和 myMusic 的卡拉 OK 底层一致。
		base = ink.Alpha(alpha * 0.45)
	}
	item.Draw(func(p *ui.Painter, r ui.Rect) {
		x, y := r.X+pad, r.Y+l.gap/2
		p.TextLayout(row.text, x, y, base)
		p.TextLayout(row.subtitle, x, y+row.subtitleTop, ink.Alpha(alpha*0.65))
	})
	if sweep {
		item.Children(func() {
			drawStageListSweep(c, row, pad, l.gap/2, l.font, s.pos, ink.Alpha(alpha))
		})
	}
}

// stageListFlowProgress：这个词此刻唱到哪儿，0..1；整句没有逐字时间时一到句首就整句亮。
func stageListFlowProgress(w stageListFlowWord, pos time.Duration) float32 {
	if w.end <= w.start {
		if pos >= w.start {
			return 1
		}
		return 0
	}
	return clamp01(float32(pos-w.start) / float32(w.end-w.start))
}

// drawStageListFlowWord 在词的盒子里画羽化扫光：左侧已唱区域实心，右侧用 16/28 个字高的渐变收边。
func drawStageListFlowWord(c *ui.Context, key int, w stageListFlowWord, x, y float32, ink ui.Color, progress, feather float32) {
	box := ui.Box(c.Key(key)).Absolute().Left(x+w.x).Top(y+w.y).Width(w.w).Height(w.h).Clip()
	if progress >= 1 {
		box.PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
			p.TextLayout(w.layout, r.X, r.Y, ink)
		})
		return
	}
	lit := w.w*progress + feather
	lo, hi := clamp01((lit-feather)/max(1, w.w)), clamp01(lit/max(1, w.w))
	box.PassThrough().MaskLinearGradient(ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
		{Color: white, At: 0},
		{Color: white, At: lo},
		{Color: ui.Transparent, At: hi},
		{Color: ui.Transparent, At: 1},
	}}).Draw(func(p *ui.Painter, r ui.Rect) {
		p.TextLayout(w.layout, r.X, r.Y, ink)
	})
}

// makeStageListFlow 把一句拆成词排进列表宽度：词间留词距，放不下就折行，返回每一行的实际宽度和总高。
func makeStageListFlow(line lyrics.Line, font ui.Font, width, gap float32, shape ui.TextShape) ([]stageListFlowWord, float32) {
	type token struct {
		text       string
		start, end time.Duration
	}
	var tokens []token
	if line.WordsSource {
		for _, w := range line.Words {
			if text := strings.TrimSpace(w.Text); text != "" {
				tokens = append(tokens, token{text, w.Start, w.End})
			}
		}
	}
	if len(tokens) == 0 {
		// 没有逐字时间：整句当一个词，到句首整句亮。
		tokens = []token{{strings.TrimSpace(line.Text), line.At, line.At}}
	}
	shape.Width = width
	out := make([]stageListFlowWord, 0, len(tokens))
	var x, y, rowHeight float32
	for _, t := range tokens {
		layout := ui.ShapeRichTextWith([]ui.Span{{Text: t.text}}, font, shape)
		w, h := layout.Size()
		if x > 0 && x+gap+w > width {
			y += rowHeight
			x, rowHeight = 0, 0
		}
		if x > 0 {
			x += gap
		}
		out = append(out, stageListFlowWord{layout: layout, x: x, y: y, w: w, h: h, start: t.start, end: t.end})
		x += w
		rowHeight = max(rowHeight, h)
	}
	return out, y + rowHeight
}

func absStageListIndex(n int) int { return max(n, -n) }

func (st *stageListState) readDrag(c *ui.Context, item ui.Element, row int) bool {
	_, dy, held := item.Dragged()
	if held && st.pressedRow < 0 {
		st.pressedRow, st.dragDistance = row, 0
	}
	if st.pressedRow != row {
		return false
	}
	if held {
		st.dragDistance += float32(math.Abs(float64(dy)))
		if st.dragDistance >= 6 && dy != 0 {
			st.manual = c.Now().Add(stageListHold)
			st.scroll.Y = min(st.scroll.MaxY, max(0, st.scroll.Y-dy))
			st.shown = st.scroll.Y
			c.AnimationFrame()
		}
		return st.dragDistance >= 6
	}
	dragged := st.dragDistance >= 6
	if dragged {
		st.manual = c.Now().Add(stageListHold)
		c.After(stageListHold)
	}
	st.pressedRow, st.dragDistance = -1, 0
	return dragged
}

func stageListTextUnits(text string) (n int) {
	for _, r := range text {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func stageListTimedWords(line lyrics.Line, units int) []stageListWord {
	var result []stageListWord
	text, offset := line.Text, 0
	// 在原文上匹配时间段以保留空格和标点。匹配失败时按整句估时，避免扫错字。
	for _, word := range line.Words {
		part := strings.TrimSpace(word.Text)
		if part == "" {
			continue
		}
		at := strings.Index(text, part)
		if at < 0 || word.End <= word.Start || len(result) > 0 && word.Start < result[len(result)-1].start {
			return nil
		}
		through := at + len(part)
		offset += stageListTextUnits(text[:through])
		result = append(result, stageListWord{start: word.Start, end: word.End, through: offset})
		text = text[through:]
	}
	if len(result) > 0 {
		result[len(result)-1].through = units
	}
	return result
}

func stageListHighlight(row *stageListRow, pos time.Duration) (from, to int, progress float32) {
	if len(row.words) == 0 {
		// 行时间歌词只沿整句移动，不用合成的词时间伪装成原始打点。
		span := row.end - row.at
		if span <= 0 {
			return row.units, row.units, 1
		}
		return 0, row.units, clamp01(float32(pos-row.at) / float32(span))
	}
	for _, word := range row.words {
		if pos < word.end {
			return from, word.through, clamp01(float32(pos-word.start) / float32(word.end-word.start))
		}
		from = word.through
	}
	return from, from, 1
}

func drawStageListSweep(c *ui.Context, row *stageListRow, x, y, font float32, pos time.Duration, ink ui.Color) {
	from, to, progress := stageListHighlight(row, pos)
	if from == row.units || progress >= 1 && to == row.units {
		ui.Box(c.Key("fully-lit")).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
			p.TextLayout(row.text, r.X+x, r.Y+y, ink)
		})
		return
	}
	paint := func(key *ui.Context, b ui.Rect, gradient *ui.LinearGradient) {
		box := ui.Box(key).Absolute().Left(x + b.X).Top(y + b.Y).Width(b.W).Height(b.H).Clip().PassThrough()
		if gradient != nil {
			box.MaskLinearGradient(*gradient)
		}
		box.Draw(func(p *ui.Painter, r ui.Rect) { p.TextLayout(row.text, r.X-b.X, r.Y-b.Y, ink) })
	}
	type segmentKey struct {
		sung bool
		i    int
	}
	if from > 0 {
		for i, b := range row.text.SelectionRects(ui.TextInputRange{End: from}) {
			if b.W > 0 && b.H > 0 {
				paint(c.Key(segmentKey{sung: true, i: i}), b, nil)
			}
		}
	}
	if progress <= 0 || from == to {
		return
	}
	parts := row.text.SelectionRects(ui.TextInputRange{Start: from, End: to})
	var width float32
	for _, b := range parts {
		width += b.W
	}
	remaining := width * progress
	lines := row.text.Lines()
	for i, b := range parts {
		// 将羽化限制在正在唱的字边缘。软渲染只需这一小块渐变遮罩；宽度照 myMusic 取 16/28 字高。
		feather := min(b.W, font*(16.0/28.0))
		lit := min(b.W, remaining)
		remaining -= lit
		if lit <= 0 || b.H <= 0 {
			continue
		}
		var gradient *ui.LinearGradient
		if lit < b.W {
			angle := float32(90)
			rtl := false
			for _, line := range lines {
				if b.Y >= line.Rect.Y && b.Y < line.Rect.Y+line.Rect.H {
					rtl = line.RTL
					break
				}
			}
			end := min(b.W, lit+feather/2)
			if rtl {
				angle = 270
				b.X += b.W - end
			}
			gradient = &ui.LinearGradient{From: white, To: ui.Transparent, Angle: angle, Start: max(0, (lit-feather/2)/end), End: 1}
			b.W = end
		}
		paint(c.Key(segmentKey{i: i}), b, gradient)
	}
}

func drawStageListDots(p *ui.Painter, r ui.Rect, pad, font float32, row stageListRow, s stageFrame, alpha float32) {
	progress := float32(0)
	if row.end > row.at {
		progress = clamp01(float32(s.pos-row.at) / float32(row.end-row.at))
	}
	// 照 myMusic RushDots：8dp 的点、16dp 间距、12dp 四周留白，都是按 28sp 定的。
	unit := font / 28
	dot, spacing, edge := max(2, 8*unit), 16*unit, 12*unit
	cy := r.Y + r.H/2
	for i := range 3 {
		// 第 i 个点比前一个晚 15% 起跳，按 1.4 倍追上去。
		step := clamp01((progress - float32(i)*0.15) * 1.4)
		radius := dot / 2 * (1 + 0.8*step)
		tone := 0.3 + 0.7*step
		cx := r.X + pad + edge + dot/2 + float32(i)*(dot+spacing)
		p.Fill(ui.Rect{X: cx - radius, Y: cy - radius, W: radius * 2, H: radius * 2}, white.Alpha(tone*alpha), radius)
	}
}

func drawStageListRushBackground(c *ui.Context, s stageFrame) {
	accent := s.accent
	if accent.A == 0 {
		accent = ui.RGB(96, 119, 162)
	}
	black := ui.RGB(12, 13, 20)
	if s.rushBackground == "" || s.rushBackground == "cover" {
		// 正式舞台已有按用户设置渲染的封面背景。静态卡片则自己填入示例封面。
		if !s.preview {
			return
		}
		ui.Box(c.Key("rush-cover")).Absolute().Top(0).Right(0).Bottom(0).Left(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(r, black.Mix(accent, 0.24), 0)
			if s.art != nil {
				if s.art.backdrop != nil {
					p.Image(s.art.backdrop, r, ui.Cover)
				} else if s.art.bmp != nil {
					p.Image(s.art.bmp, r, ui.Cover)
				}
			}
			p.FillGradient(r, ui.LinearGradient{From: black.Alpha(0.86), To: black.Alpha(0.53), Angle: 82}, 0)
		})
		return
	}
	base := black.Mix(accent, 0.13)
	phase := 0.0
	if s.motion > 0 {
		phase = s.pos.Seconds() * 0.45
	}
	// 暂停、缺音频和减少动效时不使用上一帧遗留的电平。
	var bass, mid float32
	if s.hasLevels && s.motion > 0 && (s.playing || s.preview) {
		for i, level := range s.levels {
			if i < len(s.levels)/3 {
				bass = max(bass, clamp01(level))
			} else {
				mid = max(mid, clamp01(level))
			}
		}
	}
	ui.Box(c.Key("rush-background")).Absolute().Top(0).Right(0).Bottom(0).Left(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, base, 0)
		switch s.rushBackground {
		case "flow":
			drawStageListRushGlow(p, r, accent, 0.72+0.19*float32(math.Sin(phase)), 0.42, 0.54)
			drawStageListRushGlow(p, r, accent.Mix(ui.RGB(116, 76, 169), 0.52), 0.29, 0.9+0.13*float32(math.Cos(phase*0.8)), 0.24)
		case "rhythm":
			switch s.rushRhythm {
			case "gradient":
				drawStageListRushGlow(p, r, accent, 0.68, 0.78, 0.24+0.35*bass)
			case "stripes", "curve":
				for i := range 9 {
					x := r.X + r.W*(0.26+0.13*float32(i))
					p.Line(x, r.Y+r.H, x+r.W*(0.19+0.10*bass), r.Y, max(1, r.W*0.004), accent.Alpha(0.12+0.14*mid))
				}
			default:
				for i := range 3 {
					var points [32][2]float32
					for j := range points {
						t := float32(j) / float32(len(points)-1)
						wave := float32(math.Sin(float64(t)*math.Pi*2 + phase*float64(bass) + float64(i)*0.73))
						points[j] = [2]float32{r.X + r.W*t, r.Y + r.H*(0.78+float32(i)*0.063+wave*(0.025+0.13*bass))}
					}
					p.StrokePolyline(points[:], max(1, r.H*0.005), accent.Alpha(0.24+0.25*mid))
				}
			}
		}
	})
}

func drawStageListRushGlow(p *ui.Painter, r ui.Rect, color ui.Color, x, y, opacity float32) {
	p.FillRadialGradient(r, ui.RadialGradient{Center: ui.Point{X: x, Y: y}, Radius: max(r.W*0.62, r.H*0.76),
		Stops: []ui.GradientStop{{Color: color.Alpha(opacity), At: 0}, {Color: color.Alpha(0), At: 1}}}, 0)
}

