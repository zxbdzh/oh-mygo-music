package player

import (
	"io"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
)

type transitionChunk struct {
	samples []float32
	err     error
}

// 每曲独立预解码；尾轨下载停滞时丢弃其未就绪尾音，不能阻塞已经开播的新曲。
type transitionBuffer struct {
	decoder     audio.Decoder
	chunks      chan transitionChunk
	done        chan struct{}
	stop        func()
	mu          sync.Mutex
	current     []float32
	failure     error
	closed      bool
	once        sync.Once
	worker      sync.WaitGroup
	length      int64
	started     bool
	capacity    int
	produced    int64
	consumed    int64
	actualEnd   int64
	discard     int
	sourceSkip  int64
	firstActive int64
	lastActive  int64
	energy      float64
	windowCount int
}

func newTransitionBuffer(decoder audio.Decoder) *transitionBuffer {
	// 十二秒淡化还需要提前看到正常 EOF；轻量预读只测音量，不运行节拍或模型分析。
	buffer := &transitionBuffer{decoder: decoder, length: decoder.Len(), capacity: (16*48000 + 1023) / 1024, actualEnd: -1, firstActive: -1}
	buffer.chunks = make(chan transitionChunk, buffer.capacity)
	buffer.done = make(chan struct{})
	buffer.stop = sync.OnceFunc(func() { close(buffer.done) })
	return buffer
}

func (buffer *transitionBuffer) start() {
	buffer.chunks = make(chan transitionChunk, buffer.capacity)
	buffer.done = make(chan struct{})
	chunks, done := buffer.chunks, buffer.done
	// 定位与关闭可能先后取消同一轮；失败退出也必须保留可安全重复清理的状态。
	buffer.stop = sync.OnceFunc(func() { close(done) })
	buffer.worker.Add(1)
	buffer.started = true
	go func() {
		defer buffer.worker.Done()
		for {
			samples := make([]float32, 2048)
			count, failure := buffer.decoder.Read(samples)
			if count == 0 && failure == nil {
				failure = io.ErrNoProgress
			}
			select {
			case chunks <- transitionChunk{samples: samples[:count], err: failure}:
				buffer.mu.Lock()
				for offset := 0; offset+1 < count; offset += 2 {
					left, right := float64(samples[offset]), float64(samples[offset+1])
					buffer.energy += left*left + right*right
					buffer.windowCount += 2
					buffer.produced++
					if buffer.windowCount == 960 {
						if math.Sqrt(buffer.energy/960) >= .0031622776601683794 {
							if buffer.firstActive < 0 {
								buffer.firstActive = buffer.produced - 480
							}
							buffer.lastActive = buffer.produced
						}
						buffer.energy, buffer.windowCount = 0, 0
					}
				}
				if failure == io.EOF && buffer.windowCount > 0 && math.Sqrt(buffer.energy/float64(buffer.windowCount)) >= .0031622776601683794 {
					if buffer.firstActive < 0 {
						buffer.firstActive = buffer.produced - int64(buffer.windowCount/2)
					}
					buffer.lastActive = buffer.produced
				}
				if failure == io.EOF {
					buffer.actualEnd = buffer.produced
				}
				buffer.mu.Unlock()
			case <-done:
				return
			}
			if failure != nil {
				return
			}
		}
	}()
}
func (buffer *transitionBuffer) SampleRate() int { return 48000 }
func (buffer *transitionBuffer) Channels() int   { return 2 }
func (buffer *transitionBuffer) Len() int64      { return buffer.length }
func (buffer *transitionBuffer) Read(dst []float32) (int, error) {
	buffer.mu.Lock()
	if !buffer.started && !buffer.closed {
		buffer.start()
	}
	// 淡化入口只跳过已确认的片头静音，不改动歌曲中间的停顿。
	for buffer.discard > 0 && !buffer.closed {
		if len(buffer.current) == 0 {
			chunks, done := buffer.chunks, buffer.done
			buffer.mu.Unlock()
			var chunk transitionChunk
			select {
			case chunk = <-chunks:
			case <-done:
				return 0, io.ErrClosedPipe
			}
			buffer.mu.Lock()
			buffer.current, buffer.failure = chunk.samples, chunk.err
		}
		count := min(buffer.discard, len(buffer.current))
		buffer.current = buffer.current[count:]
		buffer.discard -= count
		if count == 0 && buffer.failure != nil {
			failure := buffer.failure
			buffer.mu.Unlock()
			return 0, failure
		}
	}
	for len(buffer.current) == 0 && buffer.failure == nil && !buffer.closed {
		chunks, done := buffer.chunks, buffer.done
		buffer.mu.Unlock()
		var chunk transitionChunk
		select {
		case chunk = <-chunks:
		case <-done:
			return 0, io.ErrClosedPipe
		}
		buffer.mu.Lock()
		buffer.current, buffer.failure = chunk.samples, chunk.err
	}
	defer buffer.mu.Unlock()
	if buffer.closed {
		return 0, io.ErrClosedPipe
	}
	count := copy(dst, buffer.current)
	buffer.current = buffer.current[count:]
	buffer.consumed += int64(count / 2)
	if len(buffer.current) == 0 {
		return count, buffer.failure
	}
	return count, nil
}

func (buffer *transitionBuffer) sourceFrame(frame int64) int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return frame + buffer.sourceSkip
}

func (buffer *transitionBuffer) lengths() (int64, int64) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	source := buffer.length
	if buffer.actualEnd >= 0 {
		source = buffer.actualEnd
	}
	output := source - buffer.sourceSkip
	if source < 0 {
		output = source
	}
	return source, output
}

func (buffer *transitionBuffer) crossfadeEnd(length int64) int64 {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	last := buffer.lastActive
	// 只有正常 EOF 证明后面全是尾静音，才能把淡化提前；曲中停顿不参与裁剪。
	if last > 0 && buffer.actualEnd-last >= int64(trimSilenceMinimumRun)*48000/int64(time.Second) {
		return last - buffer.sourceSkip
	}
	return length
}

func (buffer *transitionBuffer) skipLeadingSilence() {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	first := buffer.firstActive
	if buffer.consumed == 0 && buffer.sourceSkip == 0 && first >= int64(trimSilenceMinimumRun)*48000/int64(time.Second) {
		buffer.sourceSkip = first
		buffer.discard = int(first * 2)
	}
}
func (buffer *transitionBuffer) ready() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if !buffer.started && !buffer.closed {
		buffer.start()
	}
	return len(buffer.current) > 0 || len(buffer.chunks) > 0 || buffer.failure != nil
}
func (buffer *transitionBuffer) SetPosition(frame int64) error {
	buffer.mu.Lock()
	if buffer.closed {
		buffer.mu.Unlock()
		return io.ErrClosedPipe
	}
	if !buffer.started {
		buffer.mu.Unlock()
		return buffer.decoder.SetPosition(frame)
	}
	buffer.stop()
	buffer.mu.Unlock()
	buffer.worker.Wait()
	if failure := buffer.decoder.SetPosition(frame); failure != nil {
		return failure
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.current = nil
	buffer.failure = nil
	buffer.produced, buffer.consumed, buffer.actualEnd = 0, 0, -1
	buffer.sourceSkip = 0
	buffer.discard = 0
	buffer.firstActive, buffer.lastActive = -1, 0
	buffer.energy, buffer.windowCount = 0, 0
	if buffer.closed {
		return io.ErrClosedPipe
	}
	buffer.start()
	return nil
}
func (buffer *transitionBuffer) Close() error {
	buffer.once.Do(func() {
		buffer.mu.Lock()
		buffer.closed = true
		buffer.stop()
		buffer.mu.Unlock()
		if closer, ok := buffer.decoder.(io.Closer); ok {
			closer.Close()
		}
	})
	return nil
}
