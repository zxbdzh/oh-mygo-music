package player

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

const stageTempoTestRate = 48000

func stageTempoTestBeat(rate, bpm, seconds int) []float32 {
	pcm := make([]float32, rate*seconds*2)
	for frame := 0; frame < rate*seconds; frame++ {
		if frame*bpm%(rate*60) < rate*bpm/100 {
			// 反相声道仍有能量，直接下混会把节拍抵消。
			pcm[frame*2], pcm[frame*2+1] = .8, -.8
		}
	}
	return pcm
}

func requireStageTempo(t *testing.T, bpm, confidence, want float64) {
	t.Helper()
	if math.Abs(bpm-want) > 2 || confidence < .5 || confidence > 1 {
		t.Fatalf("节奏估计 %.2f BPM（置信度 %.2f），预期 %.0f BPM", bpm, confidence, want)
	}
}

// 估计器能跨任意解码读取边界识别不同采样率及速度的节拍。
func TestStageTempoFindsSyntheticBeat(t *testing.T) {
	for _, tt := range []struct {
		name      string
		rate, bpm int
	}{{"slow", 44100, 52}, {"medium", 48000, 120}, {"fast", 44100, 142}, {"upper-limit", 48000, 200}} {
		t.Run(tt.name, func(t *testing.T) {
			var tempo tempoState
			tempo.setEnabled(true)
			pcm := stageTempoTestBeat(tt.rate, tt.bpm, 10)
			for start := 0; start < len(pcm); start += 514 {
				tempo.feed(int64(start/2), pcm[start:min(start+514, len(pcm))], tt.rate)
			}
			bpm, confidence := tempo.at(int64(tt.rate * 9))
			requireStageTempo(t, bpm, confidence, float64(tt.bpm))
		})
	}
}

// 合成 WAV 经实际解码流和测试输出测速；定位不会继续使用上一段的估计。
func TestStageTempoUsesDecodedWAV(t *testing.T) {
	const rate = stageTempoTestRate
	pcm := append(stageTempoTestBeat(rate, 120, 8), stageTempoTestBeat(rate, 150, 8)...)
	data := make([]byte, 0, len(pcm)*2)
	for _, sample := range pcm {
		data = binary.LittleEndian.AppendUint16(data, uint16(int16(sample*32767)))
	}
	s, err := openStream(downloaded(wavFile(1, 2, 16, rate, data)), 0)
	if err != nil {
		t.Fatal(err)
	}
	watch := &watched{stream: s, now: time.Now}
	out := &transitionTestOutput{decoder: watch}
	t.Cleanup(out.Close)
	p := &Player{stream: s, out: out, playing: true}
	p.SetStageTempoEnabled(true)
	out.Play()
	out.advance(rate * 5)
	bpm, confidence := p.StageTempo()
	requireStageTempo(t, bpm, confidence, 120)
	p.invalidateStageTempo()
	out.Seek(8 * time.Second)
	if bpm, _ := p.StageTempo(); bpm != 0 {
		t.Fatalf("定位沿用了旧片段节奏 %.2f BPM", bpm)
	}
	out.advance(rate * 5)
	bpm, confidence = p.StageTempo()
	requireStageTempo(t, bpm, confidence, 150)
}

// 相邻速度档的节拍应落在正确区间。
func TestStageTempoBands(t *testing.T) {
	for _, beat := range []int{60, 89, 91, 118, 122, 148, 152, 180} {
		var tempo tempoState
		tempo.setEnabled(true)
		tempo.feed(0, stageTempoTestBeat(stageTempoTestRate, beat, 12), stageTempoTestRate)
		bpm, confidence := tempo.at(stageTempoTestRate * 11)
		requireStageTempo(t, bpm, confidence, float64(beat))
		for _, boundary := range []float64{90, 120, 150} {
			if (bpm < boundary) != (float64(beat) < boundary) {
				t.Errorf("%d BPM 估计为 %.2f，跨过 %.0f 档位", beat, bpm, boundary)
			}
		}
	}
}

// 禁用采集会清除缓存，旧定位完成不能解除更新的定位屏蔽。
func TestStageTempoEnableAndOverlappingSeek(t *testing.T) {
	const rate = stageTempoTestRate
	var tempo tempoState
	pcm := stageTempoTestBeat(rate, 120, 8)
	tempo.feed(0, pcm, rate)
	if bpm, _ := tempo.at(rate * 7); bpm != 0 {
		t.Fatal("默认关闭时采集了节奏")
	}
	tempo.setEnabled(true)
	tempo.feed(0, pcm, rate)
	bpm, confidence := tempo.at(rate * 7)
	requireStageTempo(t, bpm, confidence, 120)
	tempo.invalidate()
	old := tempo.positionGeneration()
	tempo.invalidate()
	tempo.positioned(old)
	tempo.feed(0, pcm, rate)
	if bpm, _ := tempo.at(rate * 7); bpm != 0 {
		t.Fatal("旧定位完成后暴露了缓存")
	}
	tempo.positioned(tempo.positionGeneration())
	tempo.feed(0, pcm, rate)
	bpm, confidence = tempo.at(rate * 7)
	requireStageTempo(t, bpm, confidence, 120)
	tempo.setEnabled(false)
	if bpm, _ := tempo.at(rate * 7); bpm != 0 {
		t.Fatal("关闭采集后仍返回缓存")
	}
}

// 静音和无周期噪声没有可供舞台使用的节奏。
func TestStageTempoRejectsSilenceAndNoise(t *testing.T) {
	const rate = stageTempoTestRate
	for _, name := range []string{"silence", "noise"} {
		t.Run(name, func(t *testing.T) {
			var tempo tempoState
			tempo.setEnabled(true)
			pcm := make([]float32, rate*8*2)
			if name == "noise" {
				var state uint32 = 1
				for i := range pcm {
					state ^= state << 13
					state ^= state >> 17
					state ^= state << 5
					pcm[i] = (float32(state&0xffff)/32768 - 1) * .2
				}
			}
			tempo.feed(0, pcm, rate)
			if bpm, confidence := tempo.at(rate * 7); bpm != 0 || confidence != 0 {
				t.Fatalf("无周期信号被判为 %.2f BPM（置信度 %.2f）", bpm, confidence)
			}
		})
	}
}

// 向后定位即使落回已有估计的帧，也必须重新积累该片段的节奏。
func TestStageTempoResetsAfterDiscontinuity(t *testing.T) {
	const rate = stageTempoTestRate
	var tempo tempoState
	tempo.setEnabled(true)
	tempo.feed(0, stageTempoTestBeat(rate, 120, 10), rate)
	bpm, confidence := tempo.at(rate * 7)
	requireStageTempo(t, bpm, confidence, 120)
	tempo.feed(rate*7, make([]float32, rate/5*2), rate)
	if bpm, confidence := tempo.at(rate * 7); bpm != 0 || confidence != 0 {
		t.Fatalf("定位后残留 %.2f BPM（置信度 %.2f）", bpm, confidence)
	}
	tempo.feed(rate*7+rate/5, stageTempoTestBeat(rate, 150, 8), rate)
	bpm, confidence = tempo.at(rate * 14)
	requireStageTempo(t, bpm, confidence, 150)
}

// 解码进度领先播放时，查询只返回播放位置之前形成的估计，过早和过旧的位置均无结果。
func TestStageTempoFrameSnapshotsAreBounded(t *testing.T) {
	const rate = stageTempoTestRate
	var tempo tempoState
	tempo.setEnabled(true)
	tempo.feed(0, stageTempoTestBeat(rate, 120, 12), rate)
	tempo.feed(rate*12, stageTempoTestBeat(rate, 150, 16), rate)
	bpm, confidence := tempo.at(rate * 11)
	requireStageTempo(t, bpm, confidence, 120)
	bpm, confidence = tempo.at(rate * 27)
	requireStageTempo(t, bpm, confidence, 150)
	for _, frame := range []int64{rate * 3, rate * 29} {
		if bpm, confidence := tempo.at(frame); bpm != 0 || confidence != 0 {
			t.Fatalf("帧 %d 返回了不属于它的节拍 %.2f BPM（置信度 %.2f）", frame, bpm, confidence)
		}
	}
}
