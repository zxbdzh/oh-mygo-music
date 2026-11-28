package app

import (
	"math"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"

	"mygo-music/internal/lyrics"
)

const (
	ringsHistory     = 10 * time.Second
	ringsPithPeriod  = 11 * time.Second
	ringsMaxElements = 64
	ringsMaxSlots    = 4
	ringsMaxBands    = 24
)

// 髓心是一条闭合多边形；歌词年轮的形状由圆角字身框组成。
type ringsShape struct {
	fine, coarse []ringsRect
	polygon      [][4]float32
	gap, corner  float32
}

type ringsSource struct {
	shape      ringsShape
	start, end time.Duration
	open       bool
}

func ringsLineAt(lines []lyrics.Line, pos time.Duration) int {
	i := lyrics.Index(lines, pos)
	if i >= 0 && pos < colorStageEnd(&lines[i]) && strings.TrimSpace(lines[i].Text) != "" {
		return i
	}
	return -1
}

// 从媒体时间重建最近四个年轮源，拖回同一位置时旧句的轮廓也能复原。
func (d colorStagePaint) ringsSources(cache *colorStageCache) ([]ringsSource, *ringsLayout, *ringsLayout) {
	s := d.s
	i := lyrics.Index(s.lines, s.pos)
	active := ringsLineAt(s.lines, s.pos)
	var current, previous *ringsLayout
	if active >= 0 {
		current = cache.ringsLayoutFor(&s.lines[active], d.em)
	}
	old := i
	if i == active {
		old--
	}
	for old >= 0 && strings.TrimSpace(s.lines[old].Text) == "" {
		old--
	}
	if old >= 0 && s.motion != 0 {
		end := colorStageEnd(&s.lines[old])
		if old+1 < len(s.lines) {
			end = min(end, s.lines[old+1].At)
		}
		if s.pos-end < time.Duration(ringsLineFade*float64(time.Second)) {
			previous = cache.ringsLayoutFor(&s.lines[old], d.em)
		}
	}
	if s.motion == 0 && current != nil {
		return []ringsSource{{shape: current.shape, open: true}}, current, nil
	}

	var sources []ringsSource
	appendPith := func(from, to time.Duration, open bool) {
		for to > from && len(sources) < ringsMaxSlots {
			// 以右端点前一刻取段号，避免跨段时把同一段加两次。
			period := int((to - 1) / ringsPithPeriod)
			start := max(from, time.Duration(period)*ringsPithPeriod)
			shape := ringsPith(s, period)
			sources = append(sources, ringsSource{shape: shape, start: start, end: to, open: open})
			if s.motion == 0 {
				return
			}
			to, open = start, false
		}
	}
	now := max(0, s.pos)
	before := max(0, now-ringsHistory)
	next := int64(math.MaxInt64)
	if i+1 < len(s.lines) {
		next = int64(s.lines[i+1].At)
	}
	if s.motion == 0 {
		if time.Duration(next)-now > 4*time.Second {
			// 静态谱只用当前髓心（同一句或同一髓心段内不随进度移动）。
			shape := ringsPith(s, int(now/ringsPithPeriod))
			sources = append(sources, ringsSource{shape: shape, open: true})
		}
		return sources, current, nil
	}

	for ; i >= 0 && len(sources) < ringsMaxSlots; i-- {
		line := &s.lines[i]
		if strings.TrimSpace(line.Text) == "" {
			continue
		}
		limit := time.Duration(next)
		if i+1 < len(s.lines) {
			limit = s.lines[i+1].At
		}
		end := min(colorStageEnd(line), limit)
		// 离下一句不足四秒的间隙不长髓心，旧句发出的圈仍保留。
		pithEnd := min(now, limit-4*time.Second)
		if pithEnd > max(end, before) {
			appendPith(max(end, before), pithEnd, pithEnd == now)
		}
		if end <= before || len(sources) == ringsMaxSlots {
			return sources, current, previous
		}
		l := cache.ringsLayoutFor(line, d.em)
		sources = append(sources, ringsSource{shape: l.shape, start: line.At, end: end, open: now < end})
	}
	if len(sources) < ringsMaxSlots {
		end := now
		if len(s.lines) > 0 {
			end = min(now, s.lines[0].At-4*time.Second)
		}
		appendPith(before, end, end == now)
	}
	return sources, current, previous
}

func ringsPith(s stageFrame, period int) ringsShape {
	random := ringsSeed(s.trackKey) ^ ringsRandom(uint32(period+1)*0x9e3779b1)
	between := func(lo, hi float32) float32 { return lo + (hi-lo)*random.next() }
	cx, cy := s.w*(.5+between(-.14, .14)), s.h*(.47+between(-.14, .14))
	radius := min(s.w, s.h) * between(.035, .07)
	stretch, tilt := between(.75, 1.35), between(0, math.Pi)
	freq := [...]float64{2, 3, 4, 5, 7}
	var amp, phase [len(freq)]float64
	for i, k := range freq {
		amp[i], phase[i] = float64(between(0, .32))/math.Sqrt(k), float64(between(0, 2*math.Pi))
	}
	const count = 24
	var points [count]ui.Point
	si, co := math.Sincos(float64(tilt))
	for i := range points {
		theta := float64(i) * 2 * math.Pi / count
		r := 1.0
		for j, k := range freq {
			r += amp[j] * math.Sin(k*theta+phase[j])
		}
		sn, cs := math.Sincos(theta)
		x, y := cs*float64(stretch), sn
		scale := max(.35, r) * float64(radius)
		points[i] = ui.Point{X: cx + float32((x*co-y*si)*scale), Y: cy + float32((x*si+y*co)*scale)}
	}
	out := ringsShape{polygon: make([][4]float32, count)}
	for i, a := range points {
		b := points[(i+1)%count]
		out.polygon[i] = [4]float32{a.X, a.Y, b.X, b.Y}
	}
	return out
}

// 片元按最小有符号距离取并集：框交叠的位置没有内边。对外扩距离加固定的噪声，
// 同一处每一圈都会朝同方向偏，让木纹在动画里保持连续。
const ringsFragment = `package main

var Elements [64]vec4
var Bands [24]vec4
var Slots [4]vec4
var Bounds [4]vec4
var Limits vec4
var SlotCount int
var Fade float
var Near, Far vec3

func jitter(p vec2) float {
	return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453)
}
func noise(p vec2) float {
	i := floor(p)
	f := fract(p)
	u := f*f*(3-2*f)
	return mix(mix(jitter(i), jitter(i+vec2(1,0)), u.x), mix(jitter(i+vec2(0,1)), jitter(i+vec2(1,1)), u.x), u.y)
}
func boxDist(p vec2, b vec4, corner float) float {
	r := min(corner, min(b.z,b.w))
	k := abs(p-b.xy)-b.zw+r
	return length(max(k, vec2(0)))+min(max(k.x,k.y),0)-r
}
func Fragment(dst vec4, p vec2) vec4 {
	firstElem := 0
	firstBand := 0
	nearest := 20000.0
	total := 0.0
	grain := 2.0
	for j := 0; j < 4; j++ {
		if j >= SlotCount { break }
		s := Slots[j]
		endElem := int(s.x)
		endBand := int(s.y)
		box := Bounds[j]
		if boxDist(p,box,0) <= Limits[j] {
			distance := 20000.0
			inside := false
			for i := 0; i < 64; i++ {
				idx := firstElem+i
				if idx >= endElem { break }
				e := Elements[idx]
				if s.w >= 0 {
					distance = min(distance, boxDist(p,e,s.w))
				} else {
					a := e.xy
					v := e.zw-a
					q := p-a
					distance = min(distance, length(q-v*clamp(dot(q,v)/max(dot(v,v),0.0001),0,1)))
					if (a.y > p.y) != (e.w > p.y) {
						if p.x < a.x + v.x*(p.y-a.y)/v.y { inside = !inside }
					}
				}
			}
			raw := distance-s.z
			if raw > 0 && !inside {
				if grain > 1 {
					g := p/260
					grain = (noise(g)*0.7+noise(g*2.3+7.1)*0.3-0.5)*0.22
				}
				d := raw + grain*min(raw,600)
				f := pow(clamp(1-d/Fade,0,1),1.8)*smoothstep(0,24,raw)
				if f > 0 {
					nearest = min(nearest,d)
					last := 0.0
					for i := 0; i < 24; i++ {
						idx := firstBand+i
						if idx >= endBand { break }
						b := Bands[idx]
						v := (1-smoothstep(b.y,b.y+1.2,abs(d-b.x)))*b.z
						if d >= b.x && d < last { v += b.w }
						total += f*v
						last = b.x
					}
				}
			}
		}
		firstElem = endElem
		firstBand = endBand
	}
	a := (1-exp(-total*1.6))*0.8
	return vec4(mix(Near,Far,clamp(nearest/Fade*1.6,0,1))*a,a)
}`

var ringsShader = sync.OnceValue(func() *shader.Shader { return shader.MustCompile(ringsFragment) })

type ringsUniforms struct {
	elements, bands, slots, bounds, limits, count, fade, near, far shader.Uniform
}

var ringsShaderUniforms = sync.OnceValue(func() ringsUniforms {
	u := ringsShader().Uniform
	return ringsUniforms{u("Elements"), u("Bands"), u("Slots"), u("Bounds"), u("Limits"), u("SlotCount"), u("Fade"), u("Near"), u("Far")}
})

func ringsStaticEnergy(birth time.Duration) float32 {
	sample := int64(math.Floor(birth.Seconds()*60)) & 1023
	return .2 + .45*(.5+.5*float32(math.Sin(float64(sample)*.13)))*(.5+.5*float32(math.Sin(float64(sample)*.031)))
}

func ringsWindow(birth, start, end time.Duration) float32 {
	return float32(cinSmoothstep((birth-start+20*time.Millisecond).Seconds()/.27) *
		cinSmoothstep((end+20*time.Millisecond-birth).Seconds()/.27))
}

func (d colorStagePaint) paintRings(cache *colorStageCache, sources []ringsSource) {
	if len(sources) == 0 {
		return
	}
	fade := float32(math.Hypot(float64(d.s.w), float64(d.s.h))) / 2
	now := d.s.pos
	still := d.s.motion == 0
	if still {
		now = 12 * time.Second
	}
	energy := float32(.5)
	if !still && d.s.ringEnergy > 0 {
		energy = d.s.ringEnergy
	}

	var elems [ringsMaxElements * 4]float32
	var bands [ringsMaxBands * 4]float32
	var slots, bounds [ringsMaxSlots * 4]float32
	var limits [ringsMaxSlots]float32
	var elemCount, bandCount, slotCount int
	// 先留够所有源的粗轮廓，再给新源升级细轮廓，避免换句时旧轮廓直接消失。
	extra := ringsMaxElements
	for _, src := range sources {
		extra -= len(src.shape.coarse) + len(src.shape.polygon)/2
	}
	beat := time.Duration(ringsBeat * float64(time.Second))
	for _, src := range sources {
		first := bandCount
		start, end := max(src.start, now-ringsHistory), src.end
		if still || src.open {
			end = now + time.Second
		}
		if still {
			start = now - ringsHistory
		}
		birth := (start / beat) * beat
		if still {
			birth = now - (ringsHistory/beat)*beat
		}
		// shortcut: 仍按固定间隔发圈，所有在场圈共用当前响度包络；接入逐拍记录后再按各自出生响度绘制。
		for ; birth <= min(end, now) && bandCount < ringsMaxBands-1; birth += beat {
			if birth < start {
				continue
			}
			weight := float32(1)
			e := energy
			if still {
				e = ringsStaticEnergy(birth)
			} else {
				weight = ringsWindow(birth, src.start, end)
			}
			if weight <= .001 {
				continue
			}
			r := float32((now-birth).Seconds()/10) * fade
			i := bandCount * 4
			bands[i], bands[i+1] = r, .5+1.9*float32(math.Pow(float64(e), 1.5))
			bands[i+2], bands[i+3] = (.25+.75*e)*weight, e*e*.05*weight
			bandCount++
		}
		if first == bandCount {
			continue
		}
		if (still || src.open) && bandCount < ringsMaxBands {
			bands[bandCount*4+3] = energy * energy * .05
			bandCount++
		}

		shape := src.shape
		rects, stride := shape.coarse, 2
		delta := len(shape.fine) - len(shape.coarse) + len(shape.polygon)/2
		if delta <= extra {
			extra -= delta
			rects, stride = shape.fine, 1
		}
		left, top, right, bottom := float32(math.MaxFloat32), float32(math.MaxFloat32), -float32(math.MaxFloat32), -float32(math.MaxFloat32)
		for _, r := range rects {
			copy(elems[elemCount*4:], []float32{r.cx, r.cy, r.hw, r.hh})
			elemCount++
			left, top = min(left, r.cx-r.hw), min(top, r.cy-r.hh)
			right, bottom = max(right, r.cx+r.hw), max(bottom, r.cy+r.hh)
		}
		corner := shape.corner
		if len(shape.polygon) != 0 {
			corner = -1
			for i := 0; i < len(shape.polygon); i += stride {
				a, b := shape.polygon[i], shape.polygon[i+stride-1]
				copy(elems[elemCount*4:], []float32{a[0], a[1], b[2], b[3]})
				elemCount++
				left, top = min(left, a[0], b[2]), min(top, a[1], b[3])
				right, bottom = max(right, a[0], b[2]), max(bottom, a[1], b[3])
			}
		}
		i := slotCount * 4
		slots[i], slots[i+1], slots[i+2], slots[i+3] = float32(elemCount), float32(bandCount), shape.gap, corner
		bounds[i], bounds[i+1], bounds[i+2], bounds[i+3] = (left+right)/2, (top+bottom)/2, (right-left)/2, (bottom-top)/2
		maxRadius := bands[first*4] + bands[first*4+1] + 1.2
		limits[slotCount] = shape.gap + maxRadius/.89
		slotCount++
	}
	if slotCount == 0 {
		return
	}
	fx, u := ringsShader(), ringsShaderUniforms()
	if cache.ringsValues == nil {
		cache.ringsValues = fx.NewValues()
	}
	v := cache.ringsValues.Floats(u.elements, elems[:elemCount*4]...).Floats(u.bands, bands[:bandCount*4]...).
		Floats(u.slots, slots[:slotCount*4]...).Floats(u.bounds, bounds[:slotCount*4]...).Floats(u.limits, limits[:]...).
		Int(u.count, int32(slotCount)).Float(u.fade, fade).Color(u.near, d.s.accent).Color(u.far, white)
	fx.Draw(d.p, ui.Rect{X: d.x, Y: d.y, W: d.s.w, H: d.s.h}, shader.Options{Values: v})
}
