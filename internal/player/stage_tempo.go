package player

import (
	"math"
	"sync"
)

const tempoWindows = 1200
const tempoSnapshots = 64

type tempoSnapshot struct {
	frame           int64
	bpm, confidence float64
}

// tempoState 跟随单个解码流，保留按播放帧查询的结果，避免预读把未来节奏提前展示。
type tempoState struct {
	mu                                 sync.RWMutex
	rate, window, used, count, written int
	next                               int64
	energy, previous                   float64
	envelope                           [tempoWindows]float64
	snapshots                          [tempoSnapshots]tempoSnapshot
	blocked                            bool
	enabled                            bool
	generation                         uint64
}

func (t *tempoState) resetLocked() {
	t.used, t.count, t.written = 0, 0, 0
	t.energy, t.previous = 0, 0
	clear(t.envelope[:])
	clear(t.snapshots[:])
}

// invalidate 在音频线程收到异步定位之前屏蔽旧结果。
func (t *tempoState) invalidate() {
	t.mu.Lock()
	t.generation++
	t.resetLocked()
	t.blocked = true
	t.mu.Unlock()
}

func (t *tempoState) positionGeneration() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.generation
}

func (t *tempoState) positioned(generation uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if generation == t.generation {
		t.resetLocked()
		t.next = -1
		t.blocked = false
	}
}

func (t *tempoState) setEnabled(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.enabled != enabled {
		t.resetLocked()
		t.next = -1
		t.enabled = enabled
	}
}

func (t *tempoState) feed(pos int64, pcm []float32, rate int) {
	if rate < 100 || len(pcm) < 2 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.blocked || !t.enabled {
		return
	}
	if t.rate != rate || t.next != pos {
		t.resetLocked()
		t.rate, t.window = rate, rate/100
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		l, r := float64(pcm[i]), float64(pcm[i+1])
		t.energy += (l*l + r*r) * .5
		t.used++
		if t.used == t.window {
			level := math.Sqrt(t.energy / float64(t.window))
			onset := math.Max(level-t.previous, 0)
			t.previous = level
			t.envelope[t.written%tempoWindows] = onset
			t.written++
			t.count = min(t.count+1, tempoWindows)
			t.used, t.energy = 0, 0
			if t.written%50 == 0 {
				bpm, confidence := t.analyze()
				frame := pos + int64(i/2+1)
				t.snapshots[(t.written/50-1)%tempoSnapshots] = tempoSnapshot{frame, bpm, confidence}
			}
		}
	}
	t.next = pos + int64(len(pcm)/2)
}

func (t *tempoState) analyze() (float64, float64) {
	if t.count < 400 {
		return 0, 0
	}
	var energy float64
	for i := 0; i < t.count; i++ {
		v := t.envelope[(t.written-1-i)%tempoWindows]
		energy += v * v
	}
	if energy < 1e-5 {
		return 0, 0
	}
	best, lag := 0.0, 0
	var scores [151]float64
	// 10ms 包络在 40–200 BPM 范围内扫描，置信度由归一化相关性决定。
	for shift := 30; shift <= 150; shift++ {
		var cross, a, b float64
		for i := 0; i < t.count-shift; i++ {
			// 合并相邻窗口，避免节拍落在窗口边界时被误判为半速。
			x := t.envelope[(t.written-1-i)%tempoWindows]
			y := t.envelope[(t.written-1-i-shift)%tempoWindows]
			if i+shift+1 < t.count {
				y += .5 * t.envelope[(t.written-2-i-shift)%tempoWindows]
			}
			if i+shift > 0 && i+shift < t.count {
				y += .5 * t.envelope[(t.written-i-shift)%tempoWindows]
			}
			cross += x * y
			a += x * x
			b += y * y
		}
		if a == 0 || b == 0 {
			continue
		}
		score := cross / math.Sqrt(a*b)
		scores[shift] = score
		if score > best+0.20 {
			best, lag = score, shift
		}
	}
	if best < .5 || lag == 0 {
		return 0, 0
	}
	refined := float64(lag)
	if lag > 30 && lag < 150 {
		left, center, right := scores[lag-1], scores[lag], scores[lag+1]
		if denominator := left - 2*center + right; denominator < 0 {
			refined += max(-.5, min(.5, .5*(left-right)/denominator))
		}
	}
	return 6000 / refined, min(best, 1)
}

func (t *tempoState) at(frame int64) (float64, float64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.blocked || frame < 0 || frame > t.next {
		return 0, 0
	}
	var best tempoSnapshot
	for _, snapshot := range t.snapshots {
		if snapshot.frame <= frame && snapshot.frame > best.frame {
			best = snapshot
		}
	}
	if best.frame == 0 || frame-best.frame > int64(t.rate) {
		return 0, 0
	}
	return best.bpm, best.confidence
}

// SetStageTempoEnabled 控制本地节奏分析，关闭时清除已有结果。
func (p *Player) SetStageTempoEnabled(enabled bool) {
	if p.stageTempoEnabled == enabled {
		return
	}
	p.stageTempoEnabled = enabled
	setStreamTempoEnabled(p.stream, enabled)
	if p.transitionNext != nil {
		setStreamTempoEnabled(p.transitionNext.stream, enabled)
	}
}

func setStreamTempoEnabled(s stream, enabled bool) {
	if s != nil {
		if _, spec := s.source(); spec != nil {
			spec.tempo.setEnabled(enabled)
		}
	}
}

func (p *Player) invalidateStageTempo() {
	p.stageTempoRevision++
	if p.stream != nil {
		if _, spec := p.stream.source(); spec != nil {
			spec.tempo.invalidate()
		}
	}
}

// StageTempo 返回已播放位置附近的本地节奏，定位和远程播放期间没有结果。
func (p *Player) StageTempo() (float64, float64) {
	if p.stream == nil || p.out == nil || p.seeking || p.remote != nil && p.remote.started {
		return 0, 0
	}
	_, spec := p.stream.source()
	if spec == nil {
		return 0, 0
	}
	frame := int64(p.Position().Seconds() * float64(p.stream.SampleRate()))
	return spec.tempo.at(frame)
}

// StageTempoRevision 在定位时变化，供调用方丢弃尚未应用的结果。
func (p *Player) StageTempoRevision() uint64 { return p.stageTempoRevision }

// StagePlaybackID 标识当前播放实例，交叉淡化交接和重新播放也会更新。
func (p *Player) StagePlaybackID() uint64 { return p.stagePlaybackID }
