package app

import (
	"math"
	"testing"
	"time"

	"mygo-music/internal/lyrics"
)

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
