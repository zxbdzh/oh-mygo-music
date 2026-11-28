package app

import (
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

// MiSide 掉落照 myMusic 的 MisideDropVisualizer / MisideDropPhysics 移植（原模组 miside-drop 2.5.3，MIT）：
// 当前句在中央逐字打出，白字带 7 层递减灰阶的挤出和一层深投影，读成实体厚度；整句唱完后每个字随机上抛、
// 横向散开、三轴翻滚，按重力下落，在地面线（82%）回弹、停住、保留平面旋转，停留 2.5 秒后 1.5 秒淡出，
// 地上最多留两句。数值照原模组的 px 写，按“正文基准字号 ÷ 56”折成屏幕像素。
//
// 移植取舍：原模组的粒子靠逐帧积分，这里改成按“掉落时长”直接求值的解析解（阻尼位移、逐次回弹、
// 俯仰偏航的三角波反射），同一播放位置永远画出同一帧，倒拖、暂停、改尺寸都不会残留上一次的状态。
// 原模组逐字模式让唱到的字单独掉、换句时半空的字急坠；这里只做默认的整句掉落，每句唱完统一落。
const (
	misideBaseFont    = 56    // 原模组基准字号（px），所有 px 数值都按它折算
	misideGroundRatio = .82   // 地面线在安全区高度的位置
	misideStagger     = .014  // 整句一起落时字与字错开的秒数
	misideFadeSec     = 1.5   // 落地停留后淡出的秒数
	misideSettleHold  = 2.5   // 落地后停多久才开始淡出
	misideTiltLimit   = 16    // 俯仰、偏航的来回幅度（度）
	misideGravity     = 1400  // 重力（原模组 px/s²）
	misideElasticity  = .34   // 回弹系数
	misideBounceMin   = 50    // 触地速度低于它就直接停住
	misideDamp        = .9069 // 横向阻尼：原模组每帧 vx *= 0.985（60 帧）折成每秒的指数衰减
	misideLayers      = 7     // 挤出层数
)

// misideChar 是一个字：单独塑形好的字形，加上它在舞台上的字框和基线。
type misideChar struct {
	glyphs   []ui.Glyph
	text     string   // 单个字，深投影排版要用
	font     ui.Font  // 塑形字体，和字面同一份
	deep     *cinGlow // 最底层挤出的深投影缓存
	x, y     float32  // 字框左上角
	w, h     float32
	baseline float32
	start    time.Duration
}

type misideLayout struct {
	line  *lyrics.Line
	chars []misideChar
	size  float32 // 排好版后的正文字号
	end   time.Duration
}

// buildMisideLayout 把一句排进宽 84%、最多 3 行、居中；每个字取自己的字框，空白不出粒子。
func buildMisideLayout(line *lyrics.Line, em, w, h float32, lang string) *misideLayout {
	out := &misideLayout{line: line, end: colorStageEnd(line)}
	text := strings.TrimSpace(line.Text)
	if text == "" {
		return out
	}
	maxWidth := w * .84
	size := em
	font := ui.Font{Size: size, Weight: 700, Lang: lang, LineHeight: 1.35}
	layout := ui.ShapeText(text, font, maxWidth)
	rows := layout.Lines()
	// 原模组最多 3 行，超出就把整句缩小重排。
	for len(rows) > 3 && size > 8 {
		size *= .9
		font.Size = size
		layout = ui.ShapeText(text, font, maxWidth)
		rows = layout.Lines()
	}
	out.size = size
	lw, lh := layout.Size()
	left, top := w/2-lw/2, h*.5-lh/2

	runes := []rune(text)
	units := make([]int, len(runes)+1) // 每个字在整句里的 UTF-16 下标，SelectionRects 用它取字框
	for i, r := range runes {
		units[i+1] = units[i] + 1
		if r > 0xffff {
			units[i+1]++
		}
	}
	starts, _ := colorStageTimings(line, text, len(runes))
	for i, r := range runes {
		if unicode.IsSpace(r) {
			continue
		}
		rects := layout.SelectionRects(ui.TextInputRange{Start: units[i], End: units[i+1]})
		if len(rects) == 0 || rects[0].W <= 0 || rects[0].H <= 0 {
			continue
		}
		glyphs := ui.Shape(string(r), font)
		if len(glyphs) == 0 {
			continue
		}
		// 找出这个字所在的行，取它的基线，让单独塑形的字回到整句排版里的高度。
		baseline := rows[0].Baseline
		for _, row := range rows {
			if units[i] >= row.Start && units[i] < row.End {
				baseline = row.Baseline
				break
			}
		}
		box := rects[0]
		out.chars = append(out.chars, misideChar{
			glyphs: glyphs, text: string(r), font: font,
			x: left + box.X, y: top + box.Y, w: box.W, h: box.H,
			baseline: top + baseline, start: starts[i],
		})
	}
	return out
}

// misideBody 是一个字在掉落 age 秒后的姿态；落定后只剩淡出。
type misideBody struct {
	x, y       float32
	rx, ry, rz float32
	alpha      float32
}

// misideFall 按原模组的积分规则求 age 秒时的位置和姿态：重力加速、横向指数阻尼、
// 触地速度大于 50px/s 就按回弹系数反弹（横向和三个角速度都衰减），否则停住、俯仰偏航归零、保留平面旋转。
func misideFall(age, x0, y0, floor, vx, vy, vrx, vry, vrz, g, rest, unit float32) misideBody {
	b := misideBody{alpha: 1}
	if age <= 0 {
		b.x, b.y = x0, y0
		return b
	}
	x, y, t := x0, y0, age
	angleX, angleY, angleZ := float32(0), float32(0), float32(0)
	settledAt := float32(-1)
	for i := 0; i < 24 && t > 0; i++ {
		// y + vy·t + ½g·t² = floor 的正根：这一程落地要多久
		disc := vy*vy + 2*g*(floor-y)
		if disc < 0 {
			break
		}
		hit := (-vy + float32(math.Sqrt(float64(disc)))) / g
		span := min(t, hit)
		damp := (1 - float32(math.Exp(-misideDamp*float64(span)))) / misideDamp
		x += vx * damp
		y += vy*span + .5*g*span*span
		vy += g * span
		vx *= float32(math.Exp(-misideDamp * float64(span)))
		angleX += vrx * span
		angleY += vry * span
		angleZ += vrz * span
		t -= span
		if span < hit {
			break
		}
		// 触地：速度够大就反弹，否则落定
		y = floor
		if abs32(vy) <= misideBounceMin*unit {
			settledAt = age - t
			break
		}
		vy = -vy * rest
		vx *= .7
		vrx *= .4
		vry *= .4
		vrz *= .4
	}
	b.x, b.y, b.rz = x, y, angleZ
	if settledAt >= 0 {
		// 落地平躺，厚度正对观众；保留平面旋转，像随手散落。
		held := age - settledAt
		if held > misideSettleHold {
			b.alpha = 1 - clamp01((held-misideSettleHold)/misideFadeSec)
		}
	} else {
		b.rx = misideReflect(angleX, misideTiltLimit)
		b.ry = misideReflect(angleY, misideTiltLimit)
	}
	return b
}

// misideReflect 把累计转角折成 ±limit 之间的三角波（原模组在边界处反转角速度）。
func misideReflect(v, limit float32) float32 {
	period := 4 * limit
	p := float32(math.Mod(float64(v+limit), float64(period)))
	if p < 0 {
		p += period
	}
	if p <= 2*limit {
		return p - limit
	}
	return 3*limit - p
}

// miside 是这一帧的整幅画面：地面线、上两句的落字、当前句（打字段或刚落下的字）。
func (d colorStagePaint) miside(l *misideLayout, old [2]*misideLayout, ends [2]time.Duration) {
	ground := d.s.h * misideGroundRatio
	d.p.FillGradient(ui.Rect{X: d.x + d.s.w*.08, Y: d.y + ground, W: d.s.w * .84, H: max(1, d.em*.02)}, ui.LinearGradient{
		Angle: 0, Stops: []ui.GradientStop{
			{Color: white.Alpha(0)}, {At: .18, Color: white.Alpha(.14)}, {At: .82, Color: white.Alpha(.14)}, {At: 1, Color: white.Alpha(0)},
		},
	}, 0)

	if d.s.motion == 0 {
		// 减少动效：当前句整句显示，地面上是离线模拟到落定的上两句（上一句亮、上上句淡）。
		for i := len(old) - 1; i >= 0; i-- {
			if old[i] != nil {
				alpha := float32(1)
				if i > 0 {
					alpha = .4
				}
				d.misideRows(old[i], 6, ground, alpha, true)
			}
		}
		if l != nil {
			unit := l.size / misideBaseFont
			for i := range l.chars {
				d.misideExtruded(&l.chars[i], unit, 1)
			}
		}
		return
	}

	for i := len(old) - 1; i >= 0; i-- {
		if old[i] != nil {
			d.misideRows(old[i], float32((d.s.pos - ends[i]).Seconds()), ground, 1, false)
		}
	}
	if l == nil {
		return
	}
	if d.s.pos < l.end {
		unit := l.size / misideBaseFont
		for i := range l.chars {
			if ch := &l.chars[i]; d.s.pos >= ch.start {
				d.misideExtruded(ch, unit, 1)
			}
		}
		return
	}
	d.misideRows(l, float32((d.s.pos - l.end).Seconds()), ground, 1, false)
}

// misideRows 画一整句掉落中的字，字与字从左到右错开 14ms 交给重力；still 是减少动效时的“已落定”静帧。
func (d colorStagePaint) misideRows(l *misideLayout, age, ground, alpha float32, still bool) {
	unit := l.size / misideBaseFont
	seed := float64(l.line.At/time.Millisecond) / 1000
	for i := range l.chars {
		ch := &l.chars[i]
		a := age - float32(i)*misideStagger
		if a <= 0 {
			// 还没轮到它掉：停在打字的位置
			d.misideExtruded(ch, unit, alpha)
			continue
		}
		d.misideDrop(ch, a, ground, unit, seed+float64(i)*13.7, alpha, still)
	}
}

// misideDrop 画一个掉落中的字：按原模组的取数顺序生成初速度、角速度、重力和回弹。
func (d colorStagePaint) misideDrop(ch *misideChar, age, ground, unit float32, seed float64, alpha float32, still bool) {
	random := func(k float64) float32 { return lineRandom(seed, k) }
	spread := float32(.3) // 整句一起落时横向散开收到三成，否则像爆开
	vx := (random(1) - .5) * 170 * spread * unit
	vy := (-60 - random(2)*80) * unit
	vrx := (random(3) - .5) * 56 * spread
	vry := (random(4) - .5) * 56 * spread
	vrz := (random(5) - .5) * 300 * spread
	g := misideGravity * (.88 + random(6)*.24) * unit
	rest := misideElasticity * (.85 + random(7)*.25)
	b := misideFall(age, ch.x, ch.y, ground-ch.h, vx, vy, vrx, vry, vrz, g, rest, unit)
	if still {
		// 减少动效：离线模拟结束后强制摆在地上，不参与淡出。
		b.y, b.rx, b.ry, b.alpha = ground-ch.h, 0, 0, 1
	}
	if a := b.alpha * alpha; a > 0 {
		// 旋转绕字框中心：字画在平移后的位置，原点也一起搬过去，不然会绕原位打转。
		dx, dy := b.x-ch.x, b.y-ch.y
		cx, cy := d.x+ch.x+dx+ch.w/2, d.y+ch.y+dy+ch.h/2
		// 原模组的父元素没有透视，rotateX/rotateY 是正交投影：直接交给 MyGo 的 3D 变换，顺序照 CSS 从右往左作用。
		d.p.Transform(ui.Rotation(b.rz).Then(ui.RotationY(b.ry)).Then(ui.RotationX(b.rx)), cx, cy, func() {
			d.misideExtrudedAt(ch, dx, dy, unit, a)
		})
	}
}

// misideExtruded 画一个字的实体厚度：7 层从深到浅的灰阶挤出（跟着字面颜色走），再压上白字面，
// 最深一层带深投影（文字阴影，模糊 24 个字高单位）。
func (d colorStagePaint) misideExtruded(ch *misideChar, unit, alpha float32) {
	d.misideExtrudedAt(ch, 0, 0, unit, alpha)
}

func (d colorStagePaint) misideExtrudedAt(ch *misideChar, dx, dy, unit, alpha float32) {
	if alpha <= 0 {
		return
	}
	x, base := ch.x+dx, ch.baseline+dy
	// 原版最底下那层挤出带 88% 黑的深投影（偏移 3/5、模糊 24 个字高单位）；
	// 旧写法用两层错位黑影近似，放大后没有真正的柔边。
	asc := ch.font.Metrics().Ascent
	level := int(math.Round(float64(clamp01(alpha)) * cinGlowLevels))
	for i := misideLayers - 1; i >= 0; i-- {
		gray := 168 + (64-168)*i/(misideLayers-1)
		off := float32(i+1) * unit
		ink := ui.RGB(uint8(gray), uint8(gray), uint8(min(255, gray+5))).Alpha(alpha)
		if i == misideLayers-1 && level > 0 {
			layout := glowLayoutAtFor(ch.text, ch.font, &ch.deep, 3*unit, 5*unit, 24*unit,
				level, ui.RGB(0, 0, 0).Alpha(.88))
			d.p.TextLayout(layout, x+off, base+off-asc, ink)
			continue
		}
		d.p.Glyphs(ch.glyphs, x+off, base+off, ink)
	}
	d.p.Glyphs(ch.glyphs, x, base, white.Alpha(alpha))
}
