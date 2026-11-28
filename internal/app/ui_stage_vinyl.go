package app

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// 黑胶唱片照 myMusic 的 vinyl-disc 1.7.10 移植（作者 pomepii，MIT）：唱片在右、歌词在左，
// 盘面、碟心、光泽、唱臂和逐字填色都按原模组的数值。碟心封面占直径 60%，盘面每 4dp 一道
// 一像素的细纹，背后一层封面色光晕和一片落地阴影。
//
// 移植取舍：MyGo 没有模糊，唱臂投影用向下偏 2.5% 直径的深色臂影近似，光晕与落地阴影按原模组
// 预先算好的径向色标画；碟心箭头（中心孔）原模组默认隐藏，这里也不画。
const (
	vinylLabelRatio  = .6
	vinylGrooveCount = 80 // 盘面细纹约 80 道，按唱片直径折算，避免小窗口糊成一片
	vinylHaloExtent  = 1.55
	vinylShadowDrop  = .081
	vinylShadowExt   = 1.45
	vinylRowsBefore  = 2
	vinylRowsAfter   = 4
	vinylRowEnter    = 320 * time.Millisecond
	vinylWordPop     = 180 * time.Millisecond
	vinylWordPopTo   = 1.06
	vinylMinDiscPct  = .12
)

// 光晕与落地阴影的色标：原模组 blur 之后的径向分布，按唱片半径归一。
var vinylHaloStops = []float32{0, .258, .387, .516, .645, .710, .774, .839, .903, 1}
var vinylHaloAlphas = []float32{.476, .401, .324, .223, .117, .0725, .039, .0176, .0065, 0}
var vinylShadowStops = []float32{0, .414, .552, .621, .690, .759, .828, .897, .966, 1}
var vinylShadowAlphas = []float32{1, .998, .92, .748, .4725, .21, .061, .011, .0012, 0}

// 光泽：conic-gradient(from 205deg)，46° 处 9% 白、214° 处 6% 白。
var vinylSheenStops = []ui.GradientStop{
	{Color: white.Alpha(0), At: 0},
	{Color: white.Alpha(0), At: 28.0 / 360},
	{Color: white.Alpha(.09), At: 46.0 / 360},
	{Color: white.Alpha(.02), At: 60.0 / 360},
	{Color: white.Alpha(0), At: 74.0 / 360},
	{Color: white.Alpha(0), At: 196.0 / 360},
	{Color: white.Alpha(.06), At: 214.0 / 360},
	{Color: white.Alpha(.01), At: 228.0 / 360},
	{Color: white.Alpha(0), At: 242.0 / 360},
	{Color: white.Alpha(0), At: 1},
}

// vinylDeck 是唱片和歌词区（都在舞台内）以及正文基准字号。
type vinylDeck struct {
	disc       ui.Rect
	text       ui.Rect
	titleBlock bool
	font       float32
	unit       float32
}

// stageVinylDeck：横屏唱片在右、歌词在左；窄窗唱片在上、歌词在下。唱片大小按原模组比例。
func stageVinylDeck(s stageFrame) vinylDeck {
	unit := max(.25, min(s.w, s.h)/720)
	minDisc := min(s.w, s.h) * vinylMinDiscPct
	if minDisc < 24 {
		minDisc = 24
	}
	if s.w > s.h {
		d := max(min(s.h*.62, s.w*.42), minDisc)
		d = min(d, s.h/(1+.125))
		pad := s.w * .15
		cy := max(s.h/2, d*.125+d/2)
		disc := ui.Rect{X: s.w - pad - d, Y: cy - d/2, W: d, H: d}
		gap := s.w * .03
		text := ui.Rect{X: pad, Y: 0, W: max(1, disc.X-gap-pad), H: s.h}
		return vinylDeck{disc: disc, text: text, titleBlock: true, font: vinylBaseFont(text, s), unit: unit}
	}
	d := max(min(s.w*.62, s.w*.8, s.h*.46), minDisc)
	d = min(d, min(s.w, s.h/(1+.125)))
	top := d*.125 + s.h*.02
	disc := ui.Rect{X: (s.w - d) / 2, Y: top, W: d, H: d}
	margin := s.w * .08
	textTop := min(s.h, disc.Y+disc.H+max(s.h*.04, d*.08))
	text := ui.Rect{X: margin, Y: textTop, W: max(1, s.w-2*margin), H: max(1, s.h-textTop)}
	return vinylDeck{disc: disc, text: text, font: vinylBaseFont(text, s), unit: unit}
}

// vinylBaseFont：正文基准字号跟舞台高走（约 4.9%），窄到一行放不下八个字时再缩；
// 缩略图卡片只有几十像素高，留 6px 的下限。
func vinylBaseFont(text ui.Rect, s stageFrame) float32 {
	return max(6, min(s.h*.049, text.W/8))
}

// stageListVinyl 画整个黑胶舞台：唱片 + 歌词列。
func (a *app) stageListVinyl(c *ui.Context, s stageFrame) {
	deck := stageVinylDeck(s)
	sung := s.accent
	if s.art != nil && s.art.tint.A != 0 {
		sung = s.art.tint
	}
	if sung.A == 0 {
		sung = ui.RGB(114, 130, 154)
	}
	drawStageVinyl(c, s, deck, sung)
	a.showStageVinylLyrics(c, s, deck, sung)
}

// ---------------------------------------------------------------- 唱片

// drawStageVinyl 画光晕、落地阴影、盘面、细纹、碟心、光泽和唱臂。
func drawStageVinyl(c *ui.Context, s stageFrame, deck vinylDeck, sung ui.Color) {
	disc := deck.disc
	d := disc.W
	r := d / 2
	cx, cy := disc.X+r, disc.Y+r
	core := ui.Box(c.Key("vinyl-disc")).Absolute().Top(0).Right(0).Bottom(0).Left(0).PassThrough()
	core.Draw(func(p *ui.Painter, o ui.Rect) {
		cx, cy := cx+o.X, cy+o.Y
		// 光晕：封面色（或主题色）的弥散光，半径 1.55 倍。
		hr := r * vinylHaloExtent
		halo := ui.Rect{X: cx - hr, Y: cy - hr, W: hr * 2, H: hr * 2}
		p.FillRadialGradient(halo, ui.RadialGradient{Radius: hr, Stops: vinylColorStops(sung, vinylHaloStops, vinylHaloAlphas, 1)}, 0)
		// 落地阴影：中心下移 8.1% 半径的黑色柔光。
		sr := r * vinylShadowExt
		sc := cy + r*vinylShadowDrop
		shadow := ui.Rect{X: cx - sr, Y: sc - sr, W: sr * 2, H: sr * 2}
		p.FillRadialGradient(shadow, ui.RadialGradient{Radius: sr, Stops: vinylColorStops(ui.RGB(0, 0, 0), vinylShadowStops, vinylShadowAlphas, .45)}, 0)
		// 盘面：farthest-corner 径向渐变。
		record := ui.Rect{X: cx - r, Y: cy - r, W: d, H: d}
		p.FillRadialGradient(record, ui.RadialGradient{Radius: r * 1.4142135, Stops: []ui.GradientStop{
			{Color: ui.Hex("#262626"), At: 0},
			{Color: ui.Hex("#1b1b1b"), At: .3},
			{Color: ui.Hex("#121212"), At: .7},
			{Color: ui.Hex("#0d0d0d"), At: 1},
		}}, r)
		// 细纹：每 4dp 一道 1dp 的白（4.5%），从碟心稍里面开始。
		spacing := max(2, d/vinylGrooveCount)
		line := max(1, spacing*.25)
		for radius := math.Floor(float64(r*.55)/float64(spacing))*float64(spacing) + float64(line)/2; radius+float64(line)/2 <= float64(r); radius += float64(spacing) {
			rr := float32(radius)
			p.Stroke(ui.Rect{X: cx - rr, Y: cy - rr, W: rr * 2, H: rr * 2}, white.Alpha(.045), rr, line)
		}
		labelR := r * vinylLabelRatio
		// 碟心外缘：贴边 1px 黑 65%，外面 2.5px 淡白 5%。
		p.Stroke(ui.Rect{X: cx - labelR, Y: cy - labelR, W: labelR * 2, H: labelR * 2}, white.Alpha(.05), labelR+1.75*deck.unit, max(1, 1.5*deck.unit))
		p.Stroke(ui.Rect{X: cx - labelR, Y: cy - labelR, W: labelR * 2, H: labelR * 2}, ui.RGBA(0, 0, 0, .65), labelR+.5*deck.unit, max(1, deck.unit))
		// 光泽与碟心一起转，播放时按“转一圈”匀速。
		angle := float32(0)
		if s.motion > 0 && !s.preview {
			turns := float32(s.pos%(20*time.Second)) / float32(20*time.Second)
			angle = turns * 360 * min(1, s.motion)
		}
		label := ui.Rect{X: cx - labelR, Y: cy - labelR, W: labelR * 2, H: labelR * 2}
		p.Transform(ui.Rotation(angle), cx, cy, func() {
			if s.art != nil && s.art.bmp != nil {
				p.Clip(label, labelR, func() { p.Image(s.art.bmp, label, ui.Cover) })
				return
			}
			// 没有封面：深棕细条纹（每 14° 换一色）+ 歌名、歌手。
			stops := make([]ui.GradientStop, 0, 52)
			for a, k := float32(0), 0; a < 360; a, k = a+14, k+1 {
				color := ui.Hex("#241f19")
				if k%2 == 1 {
					color = ui.Hex("#1e1a15")
				}
				stops = append(stops, ui.GradientStop{Color: color, At: a / 360}, ui.GradientStop{Color: color, At: min(1, (a+14)/360)})
			}
			p.FillConicGradient(label, ui.ConicGradient{Stops: stops, Center: ui.Point{X: .5, Y: .5}}, labelR)
			drawStageVinylLabelText(p, s, labelR, cx, cy)
		})
		p.FillConicGradient(record, ui.ConicGradient{Stops: vinylSheenStops, Center: ui.Point{X: .5, Y: .5}, Angle: 205}, r)
		if s.motion >= 0 {
			drawStageVinylArm(p, ui.Rect{X: cx - r, Y: cy - r, W: d, H: d}, s)
		}
	})
}

// vinylColorStops 把预计算的 alpha 序列变成渐变（乘上整体浓度）。
func vinylColorStops(ink ui.Color, at, alpha []float32, alphaScale float32) []ui.GradientStop {
	stops := make([]ui.GradientStop, len(at))
	for i := range at {
		stops[i] = ui.GradientStop{Color: ink.Alpha(alpha[i] * alphaScale), At: at[i]}
	}
	return stops
}

// drawStageVinylLabelText 是没有封面时的碟心：居中写歌名（标签宽 13%）和歌手（8.5%）。
// 它在碟心的旋转里画，所以跟着唱片一起转。
func drawStageVinylLabelText(p *ui.Painter, s stageFrame, labelR, cx, cy float32) {
	title := strings.TrimSpace(s.title)
	artist := strings.TrimSpace(s.artist)
	if title == "" && artist == "" {
		return
	}
	room := labelR * 2 * .76
	size := max(8, labelR*2*.13)
	if title == "" {
		title = "♪"
	}
	t := ui.ShapeText(title, ui.Font{Size: size, Weight: 600, Lang: lyrics.Lang(s.lines)}, room)
	tw, th := t.Size()
	var sub *ui.TextLayout
	var sw, sh float32
	gap := float32(0)
	if artist != "" {
		sub = ui.ShapeText(artist, ui.Font{Size: max(7, labelR*2*.085), Weight: 400, Lang: lyrics.Lang(s.lines)}, room)
		sw, sh = sub.Size()
		gap = labelR * 2 * .085 * .4
	}
	top := cy - (th+gap+sh)/2
	p.TextLayout(t, cx-tw/2, top, white.Alpha(.82))
	if sub != nil {
		p.TextLayout(sub, cx-sw/2, top+th+gap, white.Alpha(.57))
	}
}

// drawStageVinylArm 画唱臂：坐标是唱片 0–100 的方框（原模组 SVG），支点在 (90, -8)，
// 暂停时绕支点逆时针抬起 50°。
func drawStageVinylArm(p *ui.Painter, disc ui.Rect, s stageFrame) {
	u := disc.W / 100
	ox, oy := disc.X, disc.Y
	at := func(x, y float32) (float32, float32) { return ox + x*u, oy + y*u }
	px, py := at(90, -8)
	angle := float32(0)
	if !s.playing && s.motion > 0 {
		angle = -50 * min(1, s.motion)
	}
	// 臂影：没有模糊，用向下 2.5% 直径的深色臂杆近似。
	rod := func(dy float32, ink ui.Color) {
		x0, y0 := at(90, -8)
		x1, y1 := at(85.64, 12.07)
		x2, y2 := at(82.63, 16.83)
		x3, y3 := at(77, 21.2)
		p.Line(x0, y0+dy, x1, y1+dy, 1.9*u, ink)
		p.Line(x1, y1+dy, x2, y2+dy, 1.9*u, ink)
		p.Line(x2, y2+dy, x3, y3+dy, 1.9*u, ink)
	}
	p.Transform(ui.Rotation(angle), px, py, func() {
		rod(2.5*u, ui.RGBA(0, 0, 0, .4))
		rod(0, ui.Hex("#e8e8ec").Alpha(.94))
		// 唱头：接着臂杆的玻璃小胶囊，转到 142° 贴住杆尾。
		hx, hy := at(71.5, 18.7)
		head := ui.Rect{X: hx, Y: hy, W: 11 * u, H: 5 * u}
		cxp, cyp := at(77, 21.2)
		p.Transform(ui.Rotation(142), cxp, cyp, func() {
			p.Fill(head, ui.Hex("#f2f2f4").Alpha(.88), 2.5*u)
			p.Fill(ui.Rect{X: head.X, Y: head.Y, W: head.W, H: 2.2 * u}, white.Alpha(.35), 1.1*u)
			p.Fill(ui.Rect{X: head.X, Y: head.Y + 2.9*u, W: head.W, H: 2.1 * u}, ui.RGBA(0, 0, 0, .07), 1.05*u)
			bx, by := at(80.45, 20.25)
			p.Fill(ui.Rect{X: bx, Y: by, W: 1.9 * u, H: 1.9 * u}, ui.Hex("#3a3a3e"), .95*u)
		})
		// 支点：玻璃圆盘 + 深色圆心。
		p.Fill(ui.Rect{X: px - 4.3*u, Y: py - 4.3*u, W: 8.6 * u, H: 8.6 * u}, ui.Hex("#f6f6f8").Alpha(.9), 4.3*u)
		p.Stroke(ui.Rect{X: px - 4.3*u, Y: py - 4.3*u, W: 8.6 * u, H: 8.6 * u}, white.Alpha(.5), 4.3*u, .45*u)
		p.Fill(ui.Rect{X: px - 1.8*u, Y: py - 1.8*u, W: 3.6 * u, H: 3.6 * u}, ui.Hex("#2a2a2e"), 1.8*u)
	})
}

// ---------------------------------------------------------------- 歌词

// stageVinylRow 是一句排好后的样子（位置相对歌词区左上角）。
type stageVinylRow struct {
	index  int
	text   *ui.TextLayout
	sub    *ui.TextLayout
	words  []stageListWord
	units  int
	top    float32
	height float32
	active bool
}

// stageVinylCache 缓存整份排版；换歌、换尺寸、换语言才重排。
type stageVinylCache struct {
	key    uint64
	deck   vinylDeck
	title  *ui.TextLayout
	artist *ui.TextLayout
	shown  int
	rows   []stageVinylRow
}

func stageVinylKey(s stageFrame, deck vinylDeck, locale string) uint64 {
	h := fnv32(s.trackKey)
	h = h*31 + fnv32(locale)
	h = h*31 + uint32(s.w*4) + uint32(s.h*7)
	h = h*31 + fnv32(s.title)*3 + fnv32(s.artist)*5
	for i := range s.lines {
		l := &s.lines[i]
		h = h*31 + fnv32(l.Text)*7 + fnv32(l.Translation)*11 + uint32(l.At/1e6)*13
	}
	return uint64(h)<<32 | uint64(uint32(deck.font*64))
}

// vinylUsable 跳过空行和“歌名 - 歌手”这类元数据行。
func vinylUsable(s stageFrame) (bool, []bool) {
	meta := vinylMetaTexts(s.title, s.artist)
	usable := make([]bool, len(s.lines))
	for i := range s.lines {
		text := strings.TrimSpace(s.lines[i].Text)
		usable[i] = text != "" && !meta[text]
	}
	return len(meta) > 0, usable
}

func vinylMetaTexts(title, artist string) map[string]bool {
	t, a := strings.TrimSpace(title), strings.TrimSpace(artist)
	if t == "" || a == "" {
		return nil
	}
	out := map[string]bool{}
	for _, sep := range []string{" - ", "－", " – ", " — ", " -", "- ", "-", "·"} {
		out[strings.TrimSpace(t+sep+a)] = true
		out[strings.TrimSpace(a+sep+t)] = true
	}
	return out
}

// vinylShown 是这一刻该显示的句子：空行往回保持上一句。
func vinylShown(s stageFrame, usable []bool) int {
	index := s.current
	if index >= len(usable) {
		index = len(usable) - 1
	}
	shown := index
	for shown >= 0 && !usable[shown] {
		shown--
	}
	if shown < 0 {
		for i := range usable {
			if usable[i] {
				return i
			}
		}
		return -1
	}
	return shown
}

// vinylPickRows 选出当前句前 2 句、后 4 句。
func vinylPickRows(usable []bool, shown int) []int {
	var rows []int
	for i := shown - 1; i >= 0 && len(rows) < vinylRowsBefore; i-- {
		if usable[i] {
			rows = append([]int{i}, rows...)
		}
	}
	if shown >= 0 {
		rows = append(rows, shown)
	}
	count := 0
	for i := shown + 1; i < len(usable) && count < vinylRowsAfter; i++ {
		if usable[i] {
			rows = append(rows, i)
			count++
		}
	}
	return rows
}

// vinylRowAlpha：当前句 1，相邻 0.62，每远一句少 0.15，最低 0.16。
func vinylRowAlpha(distance int) float32 {
	if distance == 0 {
		return 1
	}
	return max(.16, .62-float32(distance-1)*.15)
}

// vinylLineWords 把逐词时间换算成“到这一词的 UTF-16 偏移”。
func vinylLineWords(line lyrics.Line) ([]stageListWord, int) {
	units := stageListTextUnits(line.Text)
	if !line.WordsSource {
		return nil, units
	}
	return stageListTimedWords(line, units), units
}

// stageVinylLayout 重排整份歌词（只在换歌、换尺寸时发生）。
func stageVinylLayout(s stageFrame, deck vinylDeck, locale string) *stageVinylCache {
	_, usable := vinylUsable(s)
	shown := vinylShown(s, usable)
	rows := vinylPickRows(usable, shown)
	cache := &stageVinylCache{key: stageVinylKey(s, deck, locale), deck: deck, shown: shown}
	lang := lyrics.Lang(s.lines)
	if lang == "" {
		lang = locale
	}
	area := deck.text
	if deck.titleBlock && (strings.TrimSpace(s.title) != "" || strings.TrimSpace(s.artist) != "") {
		cache.title = ui.ShapeText(strings.TrimSpace(s.title), ui.Font{Size: deck.font, Weight: 600, Lang: lang}, area.W)
		cache.artist = ui.ShapeText(strings.TrimSpace(s.artist), ui.Font{Size: deck.font * .8, Weight: 400, Lang: lang}, area.W)
	}
	inactive := ui.Font{Size: deck.font * .8, Weight: 600, Lang: lang, LineHeight: 1.5}
	active := ui.Font{Size: deck.font, Weight: 700, Lang: lang, LineHeight: 1.5}
	subFont := ui.Font{Size: deck.font * .62, Weight: 400, Lang: locale, LineHeight: 1.4}
	for _, i := range rows {
		line := &s.lines[i]
		font := inactive
		on := i == shown
		if on {
			font = active
		}
		text := ui.ShapeText(strings.TrimSpace(line.Text), font, area.W)
		_, height := text.Size()
		row := stageVinylRow{index: i, text: text, height: height, active: on}
		row.words, row.units = vinylLineWords(*line)
		if strings.TrimSpace(line.Translation) != "" {
			sub := ui.ShapeText(strings.TrimSpace(line.Translation), subFont, area.W)
			_, sh := sub.Size()
			row.sub = sub
			row.height += deck.font*.62*.18 + sh
		}
		cache.rows = append(cache.rows, row)
	}
	return cache
}

// showStageVinylLyrics 画歌词列：整块竖向居中，放不下时先省离当前句最远的行。
func (a *app) showStageVinylLyrics(c *ui.Context, s stageFrame, deck vinylDeck, sung ui.Color) {
	cache := &a.stageVinyl
	key := stageVinylKey(s, deck, c.Locale())
	if cache.key != key || cache.deck != deck {
		*cache = *stageVinylLayout(s, deck, c.Locale())
	}
	rows := cache.rows
	if len(rows) == 0 {
		return
	}
	area := deck.text
	gap := deck.font * .68
	headerH := float32(0)
	if cache.title != nil {
		_, th := cache.title.Size()
		_, ah := cache.artist.Size()
		headerH = th + deck.font*.8*.35 + ah + deck.font*1.2
	}
	// 放不下时先省离当前句最远的行（一样远先省后面的），只剩当前句还放不下就省歌名行。
	keep := make([]bool, len(rows))
	for i := range keep {
		keep[i] = true
	}
	active := -1
	for i := range rows {
		if rows[i].active {
			active = i
		}
	}
	total := func() float32 {
		sum := float32(0)
		if headerH > 0 {
			sum = headerH
		}
		count := 0
		for i := range rows {
			if keep[i] {
				sum += rows[i].height
				count++
			}
		}
		if count > 1 {
			sum += gap * float32(count-1)
		}
		return sum
	}
	order := make([]int, 0, len(rows))
	for i := range rows {
		if i != active {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(x, y int) bool {
		ix, iy := absStageListIndex(order[x]-active), absStageListIndex(order[y]-active)
		if ix != iy {
			return ix > iy
		}
		return order[x] > order[y]
	})
	for _, i := range order {
		if total() <= area.H {
			break
		}
		keep[i] = false
	}
	if total() > area.H {
		headerH = 0
	}
	block := headerH
	count := 0
	for i := range rows {
		if keep[i] {
			block += rows[i].height
			count++
		}
	}
	if count > 1 {
		block += gap * float32(count-1)
	}
	y := area.Y + max(0, (area.H-block)/2)
	if headerH > 0 {
		_, th := cache.title.Size()
		_, ah := cache.artist.Size()
		artistY := y + th + deck.font*.8*.35
		ui.Box(c.Key("vinyl-title")).Absolute().Left(area.X).Top(y).Width(area.W).Height(th).PassThrough().
			Draw(func(p *ui.Painter, r ui.Rect) { p.TextLayout(cache.title, r.X, r.Y, white.Alpha(.7*.85)) })
		ui.Box(c.Key("vinyl-artist")).Absolute().Left(area.X).Top(artistY).Width(area.W).Height(ah).PassThrough().
			Draw(func(p *ui.Painter, r ui.Rect) { p.TextLayout(cache.artist, r.X, r.Y, white.Alpha(.7*.55)) })
		y += headerH
	}
	for i := range rows {
		if !keep[i] {
			continue
		}
		row := &rows[i]
		row.top = y
		y += row.height + gap
	}
	for i := range rows {
		if !keep[i] {
			continue
		}
		row := &rows[i]
		alpha := vinylRowAlpha(absStageListIndex(row.index - cache.shown))
		// 换句入场：当前句从下方 0.35em 处淡入；首屏（当前句还是第一句且刚开始）不播。
		enter := float32(1)
		if row.active && s.motion > 0 {
			enter = vinylEaseOut(clamp01(float32((s.pos - cacheLineAt(s, row.index)).Seconds() / vinylRowEnter.Seconds())))
		}
		a.showStageVinylRow(c, s, deck, row, alpha, enter, sung)
	}
}

// cacheLineAt 是这一句的起始时刻；句首稍微晚一点起播入场，避免和整句刷新抢第一帧。
func cacheLineAt(s stageFrame, index int) time.Duration {
	if index < 0 || index >= len(s.lines) {
		return 0
	}
	return s.lines[index].At
}

// vinylEaseOut 是入场用的减速曲线。
func vinylEaseOut(k float32) float32 {
	u := 1 - clamp01(k)
	return 1 - u*u*u
}

// showStageVinylRow 画一句：正文、逐字填色、译文。
func (a *app) showStageVinylRow(c *ui.Context, s stageFrame, deck vinylDeck, row *stageVinylRow, alpha, enter float32, sung ui.Color) {
	area := deck.text
	rise := deck.font * .35 * (1 - enter)
	top := row.top - rise
	key := row.index
	var item ui.Element
	if !s.preview && row.index >= 0 {
		item = ui.ButtonBase(c.Key(key)).Label(s.lines[row.index].Text).Radius(deck.font * .3)
		if item.Hovered() {
			item.Background(white.Alpha(.05))
		}
		if item.Clicked() {
			a.pl.Seek(s.lines[row.index].At)
			c.AnimationFrame()
		}
	} else {
		item = ui.Box(c.Key(key)).PassThrough()
	}
	item.Absolute().Left(area.X).Top(top).Width(area.W).Height(row.height)
	subTop := float32(0)
	if row.sub != nil {
		_, th := row.text.Size()
		subTop = th + deck.font*.62*.18
	}
	item.Draw(func(p *ui.Painter, r ui.Rect) {
		ink := white.Alpha(alpha * enter)
		p.TextLayout(row.text, r.X, r.Y, ink)
		if row.sub != nil {
			p.TextLayout(row.sub, r.X, r.Y+subTop, white.Alpha(.7*alpha*enter))
		}
		if row.active && s.motion > 0 && len(row.words) > 0 && row.units > 0 {
			// 逐字填色：唱到的部分盖一层封面色，正在唱的词按 180ms 的节奏放大 6%。
			drawStageVinylKaraoke(p, row, r.X, r.Y, s.pos, sung.Alpha(alpha))
		}
	})
}

// drawStageVinylKaraoke 按词裁出已唱的部分；正在唱的词在自己的盒子里放大。
func drawStageVinylKaraoke(p *ui.Painter, row *stageVinylRow, x, y float32, pos time.Duration, sung ui.Color) {
	from := 0
	for _, word := range row.words {
		rects := row.text.SelectionRects(ui.TextInputRange{Start: from, End: word.through})
		from = word.through
		if len(rects) == 0 {
			continue
		}
		progress := vinylWordProgress(pos, word.start, word.end)
		if progress <= 0 {
			continue
		}
		grow := vinylWordScale(pos, word.start, word.end)
		for _, b := range rects {
			if b.W <= 0 || b.H <= 0 {
				continue
			}
			b.X, b.Y = b.X+x, b.Y+y
			paint := func() {
				lit := b.W * progress
				p.Clip(ui.Rect{X: b.X - 1, Y: b.Y - b.H*.25, W: lit + 1, H: b.H * 1.5}, 0, func() {
					p.TextLayout(row.text, x, y, sung)
				})
			}
			if grow > 1.001 {
				p.Transform(ui.Scaling(grow, grow), b.X+b.W/2, b.Y+b.H/2, paint)
				continue
			}
			paint()
		}
	}
}

// vinylWordProgress：词唱到哪儿（0..1）；结束点不晚于起点时过了就满。
func vinylWordProgress(pos, start, end time.Duration) float32 {
	if end > start {
		return clamp01(float32(pos-start) / float32(end-start))
	}
	if pos >= end {
		return 1
	}
	return 0
}

// vinylWordScale：唱起的 180ms 内放大到 1.06，唱完后 180ms 缩回。
func vinylWordScale(pos, start, end time.Duration) float32 {
	if end <= start || pos <= start {
		return 1
	}
	peak := vinylEaseOut(clamp01(float32((min(pos, end)-start) / vinylWordPop)))
	level := peak
	if pos > end {
		level = peak * (1 - vinylEaseOut(clamp01(float32((pos-end)/vinylWordPop))))
	}
	return 1 + (vinylWordPopTo-1)*level
}
