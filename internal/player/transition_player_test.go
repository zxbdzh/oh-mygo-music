package player

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/egoist/mygo/audio"

	"mygo-music/internal/music"
)

// 本机 FLAC 的暂停与恢复必须按设置时长改变设备音量，不能只延迟暂停却保持原音量。
func TestTransitionPlayerConfiguredFadesReachOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		path, err := filepath.Abs("testdata/seek.flac")
		if err != nil {
			t.Fatal(err)
		}
		source.file = path
		current.SetVolume(.8)
		current.Transitions = func() TransitionSettings {
			settings := DefaultTransitionSettings()
			settings.PauseFadeOut = 850 * time.Millisecond
			settings.PlayFadeIn = time.Second
			return settings
		}
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}}, 0)
		for step := 0; step < 10; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		out, ok := current.out.(*transitionTestOutput)
		if !ok {
			t.Fatal("FLAC 没有建立输出")
		}
		time.Sleep(1100 * time.Millisecond)
		synctest.Wait()
		pumpTransitionPosts(posted)
		steady := out.Volume()
		current.TogglePlay()
		time.Sleep(425 * time.Millisecond)
		synctest.Wait()
		pumpTransitionPosts(posted)
		pauseMiddle := out.Volume()
		if !out.Playing() || math.Abs(pauseMiddle/steady-.5) > .03 {
			t.Fatalf("暂停半程没有半音量渐出: steady=%v middle=%v playing=%v", steady, pauseMiddle, out.Playing())
		}
		time.Sleep(440 * time.Millisecond)
		synctest.Wait()
		pumpTransitionPosts(posted)
		if out.Playing() || out.Volume() != 0 {
			t.Fatalf("暂停结束未静音: volume=%v playing=%v", out.Volume(), out.Playing())
		}
		current.TogglePlay()
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		pumpTransitionPosts(posted)
		resumeMiddle := out.Volume()
		if !out.Playing() || math.Abs(resumeMiddle/steady-.5) > .03 {
			t.Fatalf("恢复半程没有半音量渐入: steady=%v middle=%v playing=%v", steady, resumeMiddle, out.Playing())
		}
		time.Sleep(520 * time.Millisecond)
		synctest.Wait()
		pumpTransitionPosts(posted)
		if math.Abs(out.Volume()-steady) > .0001 {
			t.Fatalf("恢复未达到原音量: steady=%v actual=%v", steady, out.Volume())
		}
		t.Logf("输出增益: 正常 %.3f → 暂停425ms %.3f → 暂停865ms 0 → 恢复500ms %.3f → 恢复1020ms %.3f", steady, pauseMiddle, resumeMiddle, out.Volume())
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

type transitionSource struct {
	nothing
	file     string
	mu       sync.Mutex
	played   []string
	fail     string
	gateID   string
	gate     chan struct{}
	requests map[string]int
}




type transitionTestOutput struct {
	decoder  audio.Decoder
	frame    atomic.Int64
	decoded  int64
	captured []float32
	playing  bool
	closed   bool
	volume   atomic.Uint64
	onEnd    func()
}


// 交叉淡化只裁掉完整歌曲首尾静音，并保留歌曲中间停顿。
func TestCrossfadePlayerTrimsOnlySongEdges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		const rate = 48000
		const totalFrames = rate * 4
		data := make([]byte, 44+totalFrames*4)
		copy(data, "RIFF")
		binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
		copy(data[8:], "WAVEfmt ")
		binary.LittleEndian.PutUint32(data[16:], 16)
		binary.LittleEndian.PutUint16(data[20:], 1)
		binary.LittleEndian.PutUint16(data[22:], 2)
		binary.LittleEndian.PutUint32(data[24:], rate)
		binary.LittleEndian.PutUint32(data[28:], rate*4)
		binary.LittleEndian.PutUint16(data[32:], 4)
		binary.LittleEndian.PutUint16(data[34:], 16)
		copy(data[36:], "data")
		binary.LittleEndian.PutUint32(data[40:], uint32(totalFrames*4))
		for frame := rate / 2; frame < rate*7/2; frame++ {
			if frame >= rate*3/2 && frame < rate*5/2 {
				continue
			}
			offset := 44 + frame*4
			binary.LittleEndian.PutUint16(data[offset:], uint16(int16(9830)))
			binary.LittleEndian.PutUint16(data[offset+2:], uint16(int16(9830)))
		}
		path := filepath.Join(t.TempDir(), "song-edges.wav")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		source.file = path
		current.SetVolume(.8)
		current.Transitions = func() TransitionSettings {
			settings := DefaultTransitionSettings()
			settings.Crossfade = true
			settings.PlaybackFade = false
			return settings
		}
		current.PlayQueue([]music.Track{{ID: "trimmed", Source: "test", Duration: 4000}}, 0)
		for step := 0; step < 12; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		if position := current.Position(); position < 490*time.Millisecond || position > 510*time.Millisecond {
			t.Fatalf("没有跳过 500ms 片头静音: position=%v", position)
		}
		if duration := current.Duration(); duration < 3490*time.Millisecond || duration > 3510*time.Millisecond {
			t.Fatalf("尾静音未从可听时长去掉: duration=%v", duration)
		}
		out := current.out.(*transitionTestOutput)
		for step := 0; step < 205; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		middle := out.captured[96_000:192_000]
		for _, sample := range middle {
			if sample != 0 {
				t.Fatal("歌曲中间的一秒停顿被跳过")
			}
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

func (output *transitionTestOutput) Play()  { output.playing = true }
func (output *transitionTestOutput) Pause() { output.playing = false }
func (output *transitionTestOutput) Close() {
	output.closed = true
	output.playing = false
	if closer, ok := output.decoder.(io.Closer); ok {
		closer.Close()
	}
}
func (output *transitionTestOutput) Seek(position time.Duration) {
	frame := int64(position) * 48000 / int64(time.Second)
	output.frame.Store(frame)
	output.decoder.SetPosition(frame)
}
func (output *transitionTestOutput) Position() time.Duration {
	return time.Duration(output.frame.Load()) * time.Second / 48000
}
func (output *transitionTestOutput) Duration() time.Duration { return 0 }
func (output *transitionTestOutput) Playing() bool           { return output.playing }
func (output *transitionTestOutput) OnEnd(callback func()) func() {
	output.onEnd = callback
	return func() { output.onEnd = nil }
}
func (output *transitionTestOutput) SetVolume(volume float64) *audio.Player {
	output.volume.Store(math.Float64bits(volume))
	return nil
}

func (output *transitionTestOutput) Volume() float64 {
	return math.Float64frombits(output.volume.Load())
}
func (output *transitionTestOutput) advance(frames int) {
	if !output.playing || output.closed {
		return
	}
	if decoder, ok := output.decoder.(*transitionOutput); ok {
		decoder.mu.Lock()
		waiting := !decoder.allowNext && decoder.current != nil && decoder.current.err == io.EOF && decoder.current.offset == len(decoder.current.buffer)
		decoder.mu.Unlock()
		if waiting {
			// 原生混音器不会等待暂停中的解码线程；测试端也把这次消费留到恢复。
			return
		}
	}
	buffer := make([]float32, frames*2)
	count, failure := output.decoder.Read(buffer)
	output.captured = append(output.captured, buffer[:count]...)
	output.decoded = output.frame.Add(int64(count / 2))
	if failure != nil {
		output.playing = false
		if output.onEnd != nil {
			output.onEnd()
		}
	}
}

// 音频已提前混出 B 但用户尚在听 A 时暂停，不得把 B 写入历史或成为当前曲。
func TestTransitionPlayerPauseBeforeBufferedHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 250; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		out := current.out.(*transitionTestOutput)
		count := readTransitionSamples(t, out.decoder, make([]float32, 48000*2))
		out.decoded = out.frame.Load() + int64(count/2)
		current.TogglePlay()
		for step := 0; step < 8; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		if current.Current().ID != "a" || current.Playing() {
			t.Fatalf("缓冲中的下一曲提前接手: %+v", current.Current())
		}
		if records := source.records(); len(records) != 1 || records[0] != "a" {
			t.Fatalf("未听到的新曲被记账: %v", records)
		}
		current.TogglePlay()
		for step := 0; step < 10; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "a" || current.Position() < 2*time.Second {
			t.Fatalf("恢复丢失原曲位置: %+v at %v", current.Current(), current.Position())
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 设置变更发生在曲尾缓冲已完成 A→B 之后时，正在播放的交叉继续沿用开始时参数。
func TestTransitionPlayerRefreshKeepsBufferedHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 20; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		out := current.out.(*transitionTestOutput)
		count := readTransitionSamples(t, out.decoder, make([]float32, (3*48000+1)*2))
		out.decoded = int64(count / 2)
		before := current.transition
		current.RefreshTransitions()
		if current.out != out || current.transition != before || current.transitionNext == nil {
			t.Fatal("刷新设置打断了已经进入输出缓冲的 A→B")
		}
		// 游标指向下一帧；越过边界一帧才表示 B 的首帧已经输出。
		out.frame.Store(3*48000 + 1)
		current.commitTransitions()
		if current.Current().ID != "b" {
			t.Fatalf("缓冲播放完成后未交接: current=%+v", current.Current())
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 从中间恢复必须在输出创建前定位，不能把全局时间从零开始误报为歌曲从头播放。
func TestTransitionPlayerReloadPreservesAudiblePosition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}}, 0)
		for step := 0; step < 150; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		before := current.Position()
		duration := current.Duration()
		current.Reload()
		for step := 0; step < 8; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		advanceTransitionPlayer(current, posted, 480)
		if actual := current.Position(); actual < before || actual > before+100*time.Millisecond {
			t.Fatalf("恢复位置 %v，原位置 %v", actual, before)
		}
		if actual := current.Duration(); actual != duration {
			t.Fatalf("恢复后总时长变为 %v，原时长 %v", actual, duration)
		}
		synctest.Wait()
		if records := source.records(); len(records) != 1 {
			t.Fatalf("定位恢复重复记录: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 原生输出会在构造时预读；中途恢复的首批样本不能因尚未定位而被判为空流。
func TestTransitionPlayerResumeReadyBeforeOutputConstruction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}}, 0)
		for step := 0; step < 120; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		firstRead := 0
		current.newOutput = func(decoder audio.Decoder) audioOutput {
			firstRead, _ = decoder.Read(make([]float32, 64))
			return &transitionTestOutput{decoder: decoder}
		}
		current.Reload()
		for step := 0; step < 8; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		if firstRead == 0 {
			t.Fatal("输出预读把尚未定位的恢复流当作 EOF")
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

func (source *transitionSource) Stream(id, _ string) (*music.Stream, error) {
	source.mu.Lock()
	if source.requests == nil {
		source.requests = map[string]int{}
	}
	source.requests[id]++
	gateID, gate := source.gateID, source.gate
	source.mu.Unlock()
	if id == gateID && gate != nil {
		<-gate
	}
	if id == source.fail {
		return nil, errors.New("unavailable")
	}
	return &music.Stream{URL: music.FileURL(source.file)}, nil
}

// B 已在预加载失败，C 曲尾才就绪时普通回退不得重新请求 B。
func TestTransitionPlayerFallbackPreservesFailedEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		source.fail = "b"
		source.gateID = "c"
		source.gate = make(chan struct{})
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}, {ID: "c", Source: "test"}}, 0)
		for step := 0; step < 440; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		close(source.gate)
		for step := 0; step < 15; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "c" {
			t.Fatalf("普通回退未接 C: %+v", current.Current())
		}
		source.mu.Lock()
		asked := source.requests["b"]
		source.mu.Unlock()
		if asked != 1 {
			t.Fatalf("曲尾重新请求失败 B: %d", asked)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 新曲首帧后故障恢复沿用播放实例；旧尾曲不回滚，恢复预算不能再领取。
func TestTransitionPlayerFailureAfterHandoffKeepsRecordAndBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 350 && current.Current().ID != "b"; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "b" {
			t.Fatal("新曲未交接")
		}
		current.interruptedAt(io.ErrUnexpectedEOF, &recoveryTiming{started: time.Now(), policy: playbackPolicy{candidate: time.Second, stall: time.Second, recovery: 2 * time.Second, failureLimit: 3}})
		for step := 0; step < 20; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		synctest.Wait()
		if current.Current().ID != "b" || !current.playback.recoveryUsed {
			t.Fatalf("恢复回滚或预算返还: %+v", current.Current())
		}
		if records := source.records(); len(records) != 2 || records[1] != "b" {
			t.Fatalf("恢复重复写记录: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}
func (source *transitionSource) Played(track music.Track) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.played = append(source.played, track.ID)
}
func (source *transitionSource) records() []string {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]string(nil), source.played...)
}

func transitionPlayerFixture(t *testing.T) (*Player, chan func(), *transitionSource) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "song.wav")
	frames := 48000 * 4
	data := make([]byte, 44+frames*4)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 2)
	binary.LittleEndian.PutUint32(data[24:], 48000)
	binary.LittleEndian.PutUint32(data[28:], 192000)
	binary.LittleEndian.PutUint16(data[32:], 4)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(frames*4))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	source := &transitionSource{file: path}
	posted := make(chan func(), 1024)
	current := New(func(fn func()) { posted <- fn })
	current.newOutput = func(decoder audio.Decoder) audioOutput { return &transitionTestOutput{decoder: decoder} }
	current.Quiet = current.post
	current.SetVolume(0)
	current.Source = func(string) music.Source { return source }
	current.Transitions = func() TransitionSettings {
		settings := DefaultTransitionSettings()
		settings.Crossfade = true
		settings.CrossfadeDuration = time.Second
		settings.PlaybackFade = false
		return settings
	}
	t.Cleanup(current.stop)
	return current, posted, source
}

func pumpTransitionPosts(posted chan func()) {
	for {
		select {
		case callback := <-posted:
			callback()
		default:
			return
		}
	}
}

func readTransitionSamples(t *testing.T, decoder audio.Decoder, samples []float32) int {
	t.Helper()
	count := 0
	// 解码允许短读；需要预填指定区间的测试应像设备一样继续读取，才能真正跨过交接点。
	for count < len(samples) {
		read, err := decoder.Read(samples[count:])
		count += read
		if err != nil || read == 0 {
			t.Fatalf("过渡缓冲未填满: count=%d want=%d error=%v", count, len(samples), err)
		}
	}
	return count
}

func advanceTransitionPlayer(current *Player, posted chan func(), frames int) {
	if current.out != nil {
		current.out.(*transitionTestOutput).advance(frames)
	}
	time.Sleep(10 * time.Millisecond)
	synctest.Wait()
	pumpTransitionPosts(posted)
}

// 交接前插队取消旧准备，交接后插队保留当前交叉，两个位置的历史各只出现一次。
func TestTransitionPlayerPlayNextAcrossHandoff(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				current, posted, source := transitionPlayerFixture(t)
				current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
				for step := 0; step < 8; step++ {
					synctest.Wait()
					pumpTransitionPosts(posted)
				}
				if after {
					for step := 0; step < 350 && current.Current().ID != "b"; step++ {
						advanceTransitionPlayer(current, posted, 480)
					}
				}
				current.PlayNext(music.Track{ID: "c", Source: "test"})
				want := "a"
				if after {
					want = "b"
				}
				if current.Current().ID != want {
					t.Fatalf("插队改变当前曲: %+v", current.Current())
				}
				for step := 0; step < 500 && current.Current().ID != "c"; step++ {
					advanceTransitionPlayer(current, posted, 480)
				}
				if current.Current().ID != "c" {
					t.Fatalf("插队未生效: %+v", current.Current())
				}
				synctest.Wait()
				records := source.records()
				wantCount := 2
				if after {
					wantCount = 3
				}
				if len(records) != wantCount {
					t.Fatalf("插队历史: %v", records)
				}
				current.stop()
				time.Sleep(200 * time.Millisecond)
				pumpTransitionPosts(posted)
			})
		})
	}
}

// 同次播放的重载恢复不能重记历史；结束后的单曲循环则属于新一遍。
func TestTransitionPlayerReloadKeepsSingleRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}}, 0)
		for step := 0; step < 10; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		current.Reload()
		for step := 0; step < 10; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		synctest.Wait()
		if records := source.records(); len(records) != 1 {
			t.Fatalf("恢复重复记账: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 播完后定位再续播仍须从选定位置开始，不能因残留完成标记又跳回曲首。
func TestTransitionPlayerSeekAfterEndResumesChosenPosition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}}, 0)
		for step := 0; step < 440; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Playing() {
			t.Fatal("歌曲尚未播完")
		}
		current.Seek(time.Second)
		for step := 0; step < 10; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		current.TogglePlay()
		for step := 0; step < 10; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		if position := current.Position(); position < time.Second || position > 1200*time.Millisecond {
			t.Fatalf("未从选定位置续播: %v", position)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 单曲循环每次实际开播是新的一遍，暂停恢复仍属于同一遍。
func TestTransitionPlayerRepeatOneRecordsEachPlayback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.CycleRepeat()
		current.CycleRepeat()
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}}, 0)
		for step := 0; step < 470; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		synctest.Wait()
		records := source.records()
		if len(records) != 2 || records[0] != "a" || records[1] != "a" {
			t.Fatalf("循环开播历史不正确: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 交叉淡化提前准备输出帧，交接复用同一个设备输出且不重取来源。
func TestTransitionPlayerPreparesBeforeBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.Transitions = func() TransitionSettings {
			settings := DefaultTransitionSettings()
			settings.PlaybackFade = false
			return settings
		}
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 20; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		original := current.out
		for step := 0; step < 410 && current.Current().ID != "b"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "b" || current.out != original {
			t.Fatalf("交接重新创建了设备输出: current=%+v same=%v", current.Current(), current.out == original)
		}
		source.mu.Lock()
		requested := source.requests["b"]
		source.mu.Unlock()
		if requested != 1 {
			t.Fatalf("待接曲被重复请求: %d", requested)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 交叉淡化会在上一首结束前交接下一首，并继续使用同一个设备输出。
func TestTransitionPlayerCrossfadeHandsOffBeforeSongEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.Transitions = func() TransitionSettings {
			settings := DefaultTransitionSettings()
			settings.Crossfade = true
			settings.CrossfadeDuration = time.Second
			settings.PlaybackFade = false
			return settings
		}
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 20; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		output := current.out
		for step := 0; step < 350 && current.Current().ID != "b"; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "b" || current.out != output {
			t.Fatalf("交叉淡化没有在旧曲结束前接管新曲: current=%+v sameOutput=%v", current.Current(), current.out == output)
		}
		if output.Position() >= 4*time.Second {
			t.Fatalf("新曲交接发生在旧曲结束之后: position=%v", output.Position())
		}
		synctest.Wait()
		if records := source.records(); len(records) != 2 || records[0] != "a" || records[1] != "b" {
			t.Fatalf("交叉淡化开播记录异常: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 暂停淡出跨过旧曲末尾时，EOF 不能覆盖暂停意图而自动启动下一首。
func TestTransitionPlayerPauseAtBoundaryStaysPaused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.Transitions = func() TransitionSettings {
			settings := DefaultTransitionSettings()
			settings.CrossfadeDuration = 0
			return settings
		}
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 396; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		current.TogglePlay()
		for step := 0; step < 25; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Playing() || current.Current().ID != "a" {
			t.Fatalf("曲尾自动覆盖暂停: current=%+v playing=%v", current.Current(), current.Playing())
		}
		original := current.out
		current.TogglePlay()
		for step := 0; step < 25; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		source.mu.Lock()
		requested := source.requests["b"]
		source.mu.Unlock()
		if current.Current().ID != "b" || current.out != original || requested != 1 {
			t.Fatalf("曲尾续播丢弃已准备 B: current=%+v sameOutput=%v requests=%d", current.Current(), current.out == original, requested)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 交叉中的暂停冻结输出位置，恢复后从同一位置继续且不会再次交接。
func TestTransitionPlayerPauseFreezesBothSongs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 350 && current.Current().ID != "b"; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Current().ID != "b" {
			t.Fatal("交叉未开始")
		}
		current.TogglePlay()
		synctest.Wait()
		pumpTransitionPosts(posted)
		position := current.Position()
		for step := 0; step < 20; step++ {
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Position() != position {
			t.Fatal("暂停后位置仍推进")
		}
		current.TogglePlay()
		advanceTransitionPlayer(current, posted, 480)
		if current.Position() <= position {
			t.Fatal("恢复后没有继续")
		}
		synctest.Wait()
		if records := source.records(); len(records) != 2 {
			t.Fatalf("恢复重复记账: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// B 加载失败仍让 A 播到交接 C，历史只记录真正输出过的 A 和 C。
func TestTransitionPlayerSkipsFailedPreparationWithoutReplacingCurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, source := transitionPlayerFixture(t)
		source.fail = "b"
		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}, {ID: "c", Source: "test"}}, 0)
		for step := 0; step < 20; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			if current.out != nil {
				current.out.(*transitionTestOutput).advance(480)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if current.Current().ID != "a" {
			t.Fatalf("预加载提前替换当前曲: %+v", current.Current())
		}
		for step := 0; step < 400 && current.Current().ID != "c"; step++ {
			current.out.(*transitionTestOutput).advance(480)
			time.Sleep(10 * time.Millisecond)
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		if current.Current().ID != "c" {
			t.Fatalf("后继未交接: current=%+v err=%s output=%+v pending=%+v pcm=%+v", current.Current(), current.Err(), current.out, current.transitionNext, current.transition)
		}
		synctest.Wait()
		records := source.records()
		if len(records) != 2 || records[0] != "a" || records[1] != "c" {
			t.Fatalf("开播历史不正确: %v", records)
		}
		current.TogglePlay()
		current.TogglePlay()
		synctest.Wait()
		pumpTransitionPosts(posted)
		if records = source.records(); len(records) != 2 {
			t.Fatalf("暂停续播重复记账: %v", records)
		}
		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}
