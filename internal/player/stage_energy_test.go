package player

import (
	"testing"
	"time"
)

// 52Hz 快起慢收，且暂停和回查同一个已解码位置不会改变包络。
func TestStageRingEnergy(t *testing.T) {
	const rate = 48000
	s := newSpectrum(rate)
	for i := int64(0); i <= 34; i++ {
		if i == 33 {
			continue // 模拟定位后只有新落点的数据，不能跨过空洞带入旧音量。
		}
		var m measured
		m.chunk = i
		if i >= 24 && i < 29 || i == 32 {
			for b := range m.bands {
				m.bands[b] = 1
			}
		}
		s.ring[i] = m
	}
	p := Player{stream: &wavStream{format: wavFormat{rate: rate}, spec: s}, out: &transitionTestOutput{}, playing: true}
	at := func(chunk int64) (float32, bool) {
		pos := time.Duration(chunk*fftSize+1) * time.Second / rate
		return p.StageRingEnergy(pos)
	}
	read := func(chunk int64) float32 {
		t.Helper()
		v, ok := at(chunk)
		if !ok {
			t.Fatalf("第 %d 块已测量，取不到响度", chunk)
		}
		return v
	}
	quiet, first, peak, after := read(23), read(24), read(28), read(29)
	if !(quiet < first && first < peak && after < peak && after-quiet > .85*(peak-quiet)) {
		t.Fatalf("包络没跟上强拍或回落过快: %v %v %v %v", quiet, first, peak, after)
	}
	if first-quiet <= peak-after {
		t.Fatal("上升幅度没有大于回落幅度")
	}
	p.playing = false
	if read(28) != peak {
		t.Fatal("暂停并回查已解码位置时响度改变")
	}
	if _, ok := at(33); ok || read(34) != quiet {
		t.Fatal("频谱有空洞时仍使用之前的响度")
	}
	p.seeking = true
	if _, ok := at(28); ok {
		t.Fatal("定位尚未完成时读取了旧频谱")
	}
}
