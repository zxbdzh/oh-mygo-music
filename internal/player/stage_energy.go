package player

import (
	"math"
	"time"
)

// StageRingEnergy 读取指定播放位置的 52Hz 响度包络，暂停时也能保留该帧的响度。
func (p *Player) StageRingEnergy(pos time.Duration) (float32, bool) {
	if p.stream == nil || p.out == nil || p.seeking || p.remote != nil && p.remote.started {
		return 0, false
	}
	_, spec := p.stream.source()
	if spec == nil {
		return 0, false
	}
	return spec.ringEnergy(int64(pos.Seconds() * float64(p.stream.SampleRate())))
}

func (s *spectrum) ringEnergy(frame int64) (float32, bool) {
	if frame < 0 {
		return 0, false
	}
	const attack, release = .10, .40
	dt := float64(fftSize) / float64(s.rate)
	up := float32(-math.Expm1(-dt / attack))
	down := float32(-math.Expm1(-dt / release))
	chunk := frame / fftSize
	// 只用当前位置之前的连续频谱。四倍回落时间足以压低窗口起点的影响，且不依赖渲染帧率。
	start := max(0, chunk-min(int64(4*release/dt), int64(len(s.ring)-1)))
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := int64(len(s.ring))
	var energy float32
	ok := false
	for i := start; i <= chunk; i++ {
		m := &s.ring[i%count]
		if m.chunk != i {
			ok = false
			continue
		}
		var power, bass float32
		for b, level := range m.bands {
			power += level
			if b < Bands/3 {
				bass = max(bass, level)
			}
		}
		target := max(.06, .6*power/Bands+.4*bass)
		if !ok {
			energy, ok = target, true
		} else if target > energy {
			energy += (target - energy) * up
		} else {
			energy += (target - energy) * down
		}
	}
	if !ok {
		return 0, false
	}
	return energy, true
}
