package player

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// starvedAfter is how long the playing stream may wait for its download before
// the player says it's buffering: decoding takes milliseconds, and it's asked
// for more only as there's room for it, so a call this long waits on bytes
// that haven't come in, after a seek past them, or from a network slower
// than the song.
const starvedAfter = 250 * time.Millisecond

// watched is the stream that plays, timing what it's asked.
type watched struct {
	stream
	now func() time.Time
	// ended hears that a call that took starvedAfter or longer returned,
	// for what said it's buffering to say so no more.
	ended       func()
	since       atomic.Int64 // when the call on its way began, in Unix nanoseconds, or 0
	reading     atomic.Int64
	positioning atomic.Bool
	// 解码错误不能被无原因的 OnEnd 回调抹成正常结束。
	failureMu  sync.Mutex
	failureErr error
	failed     chan error
}

func (w *watched) Read(dst []float32) (int, error) {
	w.reading.Store(time.Now().UnixNano())
	defer w.reading.Store(0)
	defer w.begin()()
	n, err := w.stream.Read(dst)
	if err != nil && !errors.Is(err, io.EOF) {
		w.failureMu.Lock()
		w.failureErr = err
		w.failureMu.Unlock()
		if w.failed != nil {
			select {
			case w.failed <- err:
			default:
			}
		}
	}
	return n, err
}

func (w *watched) SetPosition(frame int64) error {
	w.positioning.Store(true)
	defer w.positioning.Store(false)
	defer w.begin()()
	err := w.stream.SetPosition(frame)
	if err != nil {
		// 音频层会把定位失败变成无原因结束，必须在这里保留原始错误。
		w.failureMu.Lock()
		w.failureErr = err
		w.failureMu.Unlock()
		if w.failed != nil {
			select {
			case w.failed <- err:
			default:
			}
		}
	}
	return err
}

func (w *watched) begin() func() {
	start := w.now()
	w.since.Store(start.UnixNano())
	return func() {
		w.since.Store(0)
		if w.ended != nil && w.now().Sub(start) >= starvedAfter {
			w.ended()
		}
	}
}

// starved reports whether the call on its way has waited starvedAfter.
func (w *watched) starved() bool {
	since := w.since.Load()
	return since != 0 && w.now().UnixNano()-since >= int64(starvedAfter)
}

func (w *watched) failure() error {
	w.failureMu.Lock()
	defer w.failureMu.Unlock()
	return w.failureErr
}
