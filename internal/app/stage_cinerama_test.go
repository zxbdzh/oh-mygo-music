package app

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// 描边填色只填已经扫过的一侧，空心一侧仍有跨过切口的独立辉光。
func TestCineramaFillKeepsOutlineHollow(t *testing.T) {
	paint := cinPaint{s: stageFrame{preview: true}, geo: cinGeometry{w: 320, h: 240, unit: 1}, accent: white}
	chunk := cinChunkArt{fontPx: 120, text: cinShape("HH", ui.Font{Size: 120, Weight: 900})}
	fill := float32(1)
	var vertical bool
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Size(320, 240).Background(black).Children(func() {
			chunk.chunk.fillVertical = vertical
			paint.showFillChunk(c, &chunk, 160, 120, 1, 1, fill)
		})
	}, 320, 240)
	countBright := func(img *image.RGBA, rect image.Rectangle) int {
		var n int
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				if img.RGBAAt(x, y).R >= 200 {
					n++
				}
			}
		}
		return n
	}
	for _, cfg := range []struct {
		vertical      bool
		filled, other image.Rectangle
	}{
		{false, image.Rect(0, 0, 156, 240), image.Rect(164, 0, 320, 240)},
		{true, image.Rect(0, 0, 320, 116), image.Rect(0, 124, 320, 240)},
	} {
		fill, vertical = 1, cfg.vertical
		tester.Frame()
		wantFill, wantOther := countBright(tester.Image(), cfg.filled), countBright(tester.Image(), cfg.other)
		fill = .5
		tester.Frame()
		gotFill, gotOther := countBright(tester.Image(), cfg.filled), countBright(tester.Image(), cfg.other)
		if gotFill == 0 || gotFill != wantFill || gotOther == 0 || gotOther >= wantOther/2 {
			t.Fatalf("纵向=%v：扫过一侧 %d/%d 像素，空心一侧 %d/%d 像素", vertical, gotFill, wantFill, gotOther, wantOther)
		}
	}
	out := tester.Image()
	paint.s.preview = false
	tester.Frame()
	in := tester.Image()
	for y := 124; y < 240; y++ {
		for x := 0; x < 320; x++ {
			if out.RGBAAt(x, y).R == 0 && in.RGBAAt(x, y).R > 10 {
				return
			}
		}
	}
	t.Fatal("空心一侧的字形外没有独立的辉光")
}

// 光晕降到最低档时，字身仍应继续淡出，不能跟着阴影突然消失。
func TestCineramaInkOutlastsGlow(t *testing.T) {
	paint := cinPaint{geo: cinGeometry{w: 240, h: 120, unit: 1}, accent: white}
	text := cinShape("Hold", ui.Font{Size: 64, Weight: 700})
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Size(240, 120).Background(black).Draw(func(p *ui.Painter, r ui.Rect) {
			paint.on(p, r).glowText(&text, 25, 60, .04, white.Alpha(.04))
		})
	}, 240, 120)
	img := tester.Image()
	for y := 20; y < 100; y++ {
		for x := 25; x < 215; x++ {
			if img.RGBAAt(x, y).R >= 5 {
				return
			}
		}
	}
	t.Fatal("关闭最低档阴影时把仍在淡出的字身一起隐藏了")
}

// 扫色和位移在退场前逐渐收住，词块仍在原时间窗内进出。
func TestCineramaChunkSettlesBeforeExit(t *testing.T) {
	chunk := cinChunk{start: 4, end: 5.2, fill: true, motion: cinStillMotion}
	if ok, _ := cinChunkLook(chunk, 3.99); ok {
		t.Fatal("词块提前出现")
	}
	if ok, _ := cinChunkLook(chunk, 5.2); ok {
		t.Fatal("词块越过原来的结束位置")
	}
	_, early := cinChunkLook(chunk, 4.3)
	_, middle := cinChunkLook(chunk, 4.6)
	_, late := cinChunkLook(chunk, 4.9)
	if early.fill <= .25 || middle.fill <= early.fill || late.fill >= 1 || late.fill-middle.fill >= middle.fill-early.fill {
		t.Fatalf("扫色应持续前进且后半段减速：%.3f、%.3f、%.3f", early.fill, middle.fill, late.fill)
	}

	chunk.fill = false
	chunk.motion.xFrom = 8
	_, early = cinChunkLook(chunk, 4.3)
	_, middle = cinChunkLook(chunk, 4.6)
	_, late = cinChunkLook(chunk, 4.9)
	if late.driftX < 0 || middle.driftX >= 4 || middle.driftX-late.driftX >= early.driftX-middle.driftX {
		t.Fatalf("横向位移应逐渐停稳且不越过中心：%.3f、%.3f、%.3f", early.driftX, middle.driftX, late.driftX)
	}
}

// 四边重复刚好走完一圈，按每句抽到的带速滚动，静态模式停住。
func TestCineramaRingFitsItsPath(t *testing.T) {
	paint := cinPaint{geo: cinGeometry{w: 960, h: 540, band: 62}}
	const font = 32
	const one = "晚风　"
	ring := paint.buildRing(one, font)
	wantLap := cinRingLap(960-62, 540-62, .4*62)
	if ring.copies < 2 || math.Abs(ring.unit*float64(ring.copies)-wantLap) > 1e-5 {
		t.Fatalf("重复 %d 份，每份 %.2f px，不能覆盖 %.2f px 的路径", ring.copies, ring.unit, wantLap)
	}
	base := cinShape(one, ui.Font{Size: font, Weight: 600})
	wantTrack := (ring.unit - float64(base.w)) / 3
	wantSecond := float64(base.glyphs[1].X+base.glyphs[1].Advance/2) + wantTrack
	if len(ring.glyphs) != 2 || math.Abs(ring.glyphs[1].center-wantSecond) > .01 {
		t.Fatal("去掉空白后的字形位置没有平摊整圈的剩余字距")
	}
	for _, speed := range []float64{.12, .2} {
		m := &cinMarquee{screensPerSec: speed}
		shift := paint.bandShift(m, .1, ring.unit)
		if math.Abs(shift*ring.unit-.1*speed*960) > 1e-5 {
			t.Fatalf("带速 %.2f 时，0.1 秒实际走了 %.2f px", speed, shift*ring.unit)
		}
	}
	paint.still = true
	if paint.bandShift(&cinMarquee{screensPerSec: .2}, .1, ring.unit) != 0 {
		t.Fatal("静态模式未停住")
	}
}

// 丝带淡边应透出底下已画的内容，而不是覆盖成舞台底色。
func TestCineramaRibbonMaskRevealsBackground(t *testing.T) {
	const w, h = 240, 180
	paint := cinPaint{geo: cinGeometry{w: w, h: h, unit: 1}, accent: ui.RGB(200, 80, 40), still: true}
	bg := ui.RGB(10, 90, 180)
	art := &cinRibbonArt{area: ui.Rect{W: w, H: h}, strips: []cinStripArt{{cx: w / 2, cy: h / 2, length: w, thickness: h, textUnit: w}}}
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Size(w, h).Background(bg).Children(func() {
			paint.showRibbon(c, art, &cinPlan{}, 0, cinEnvelope{opacity: 1, scale: 1})
		})
	}, w, h)
	img := tester.Image()
	if got := img.RGBAAt(w/2, h/2); got != (color.RGBA{R: 200, G: 80, B: 40, A: 255}) {
		t.Fatalf("遮罩不应洗掉带身中央：%v", got)
	}
	edge, corner := img.RGBAAt(0, h/2), img.RGBAAt(0, 0)
	if edge.B < 170 || corner.B < edge.B || corner.R > edge.R {
		t.Fatalf("遮罩应向四边渐隐且在角落相乘：边 %v，角 %v", edge, corner)
	}
}

// 相邻两句不抽到同一种版式，权重为 0 的版式永远不抽。
func TestCineramaPickStyleAvoidsRepeat(t *testing.T) {
	weights := cinWeights{hero: .4, small: .2, ribbon: .4}
	previous := cinHero
	hasPrevious := false
	for seed := uint32(0); seed < 400; seed++ {
		style := cinPickStyle(seed, previous, hasPrevious, weights, true, true)
		if hasPrevious && style == previous {
			t.Fatalf("seed %d 抽到了和上一句相同的版式 %d", seed, style)
		}
		previous, hasPrevious = style, true
	}
	for seed := uint32(0); seed < 200; seed++ {
		if style := cinPickStyle(seed, cinHero, true, cinWeights{hero: 0, small: 0, ribbon: 1}, true, true); style != cinRibbon {
			t.Fatalf("seed %d 抽到了权重为 0 的版式 %d", seed, style)
		}
	}
}

// 一句的窗口首尾相接、盖住整句时长，相邻句不重叠。
func TestCineramaPlanWindowsDoNotOverlap(t *testing.T) {
	lines := []lyrics.Line{
		{At: 0, End: 3 * time.Second, Text: "让晚风慢慢经过"},
		{At: 3 * time.Second, End: 7 * time.Second, Text: "星光落在路上"},
		{At: 7 * time.Second, End: 11 * time.Second, Text: "我们一起出发"},
	}
	prog := cinCompile(lines, "windows", "", cinDefaultWeights, true)
	if len(prog.plans) != len(lines) {
		t.Fatalf("得到 %d 句，想要 %d 句", len(prog.plans), len(lines))
	}
	for i, plan := range prog.plans {
		if plan.end <= plan.start {
			t.Fatalf("第 %d 句窗口是空的：%.2f..%.2f", i, plan.start, plan.end)
		}
		if i+1 < len(prog.plans) && plan.end > prog.plans[i+1].start+1e-6 {
			t.Fatalf("第 %d 句窗口 %.2f 压住了下一句 %.2f", i, plan.end, prog.plans[i+1].start)
		}
	}
}

// 放不下的单元会换行，盒子宽不超过限宽，所有单元都排到了。
func TestCineramaFlowWraps(t *testing.T) {
	widths := []float32{40, 40, 40, 40, 40}
	blank := []bool{false, false, false, false, false}
	pos, boxW, boxH := cinFlow(widths, blank, 20, 100)
	if boxW > 100.5 {
		t.Fatalf("盒子宽 %.1f 超出了限宽 100", boxW)
	}
	if boxH < 40-0.5 {
		t.Fatalf("五个 40 宽的单元在 100 宽里应排成两行，盒子高却只有 %.1f", boxH)
	}
	for i := 0; i < len(widths); i++ {
		if math.IsNaN(float64(pos[i*2])) || math.IsNaN(float64(pos[i*2+1])) {
			t.Fatalf("第 %d 个单元没有排上", i)
		}
	}
}

// 丝带的斜角都来自角度池，左右两侧轮流出现。
func TestCineramaRibbonAnglesFromPool(t *testing.T) {
	angles := cinRibbonAngles(6, 12345)
	if len(angles) != 6 {
		t.Fatalf("拿到 %d 条丝带的角度，想要 6 条", len(angles))
	}
	for _, a := range angles {
		found := false
		for _, want := range cinRibbonAnglesPool {
			if math.Abs(a-want) < 1e-9 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("角度 %.1f 不在角度池里", a)
		}
	}
	hasLeft, hasRight := false, false
	for _, a := range angles {
		if a < 0 {
			hasLeft = true
		} else {
			hasRight = true
		}
	}
	if !hasLeft || !hasRight {
		t.Fatalf("6 条丝带应当左右都有，实际是 %v", angles)
	}
}

// 字的生命周期：出现前和消失后都不画，演唱中是全亮。
func TestStardriftLookLifecycle(t *testing.T) {
	const appear, dur, hold, fade = .5, 1.2, .5, 1
	if _, ok := sdDriftLook(-.01, appear, dur, hold, fade, 1); ok {
		t.Fatal("字还没出现就在画")
	}
	if _, ok := sdDriftLook(appear+dur+hold+fade+.01, appear, dur, hold, fade, 1); ok {
		t.Fatal("字已经消失还在画")
	}
	look, ok := sdDriftLook(appear+dur*.5, appear, dur, hold, fade, 1)
	if !ok {
		t.Fatal("演唱中的字没有画")
	}
	if look.alpha != 1 || look.scale != 1 {
		t.Fatalf("演唱中的字应当全亮，实际 alpha=%.2f scale=%.2f", look.alpha, look.scale)
	}
	// 消失途中只该越来越淡。
	early, _ := sdDriftLook(appear+dur+hold+fade*.2, appear, dur, hold, fade, 1)
	late, _ := sdDriftLook(appear+dur+hold+fade*.8, appear, dur, hold, fade, 1)
	if late.alpha >= early.alpha {
		t.Fatalf("消失途中透明度没有下降：%.2f → %.2f", early.alpha, late.alpha)
	}
}

// 流星按固定排班：同一时刻永远同一颗，飞完之后到下一轮之前没有。
func TestStardriftMeteorSchedule(t *testing.T) {
	first, ok := sdMeteorAt(7, 1280, 720, 1, 1.5)
	if !ok {
		t.Fatal("排班内的时刻应当有流星")
	}
	again, ok := sdMeteorAt(7, 1280, 720, 1, 1.5)
	if !ok || again != first {
		t.Fatal("同一时刻的流星不稳定")
	}
	cross := float32(math.Hypot(1280, 720)+600) / 600
	period := cross * 1.4
	if _, ok := sdMeteorAt(7, 1280, 720, 1, cross+(period-cross)/2); ok {
		t.Fatal("穿屏之后、下一轮之前不该有流星")
	}
}
