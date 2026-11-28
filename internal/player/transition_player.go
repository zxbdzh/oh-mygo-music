package player

import (
	"context"
	"errors"
	"math"
	"time"

	"mygo-music/internal/music"
)

type transitionPreparation struct {
	id       uint64
	index    int
	track    music.Track
	info     *music.Stream
	stream   stream
	watch    *watched
	playback *playbackAttempt
	cancel   context.CancelFunc
	ready    bool
	failed   map[int]bool
	blocked  error
	context  context.Context
}

// RefreshTransitions 让尚未开始的过渡使用新设置，已经输出首帧的交叉保留原快照。
func (p *Player) RefreshTransitions() {
	p.commitTransitions()
	if p.transition != nil && p.transitionNext != nil && p.transition.startedID(p.transitionNext.id) {
		// 解码器可能已把当前过渡填入输出缓冲；参数变化只影响下一次过渡。
		return
	}
	p.cancelTransitionPreparation()
	p.prepareTransition()
}

func (p *Player) transitionSettings() TransitionSettings {
	if p.Transitions == nil {
		return DefaultTransitionSettings()
	}
	return p.Transitions()
}

func (p *Player) pauseFadeDuration() time.Duration {
	settings := p.transitionSettings()
	if !settings.PlaybackFade {
		return 0
	}
	return settings.PauseFadeOut
}
func (p *Player) playFadeDuration() time.Duration {
	settings := p.transitionSettings()
	if !settings.PlaybackFade {
		return 0
	}
	return settings.PlayFadeIn
}

func (p *Player) cancelTransitionPreparation() {
	if p.transitionCancel != nil {
		p.transitionCancel()
		p.transitionCancel = nil
	}
	if p.transitionNext != nil && p.transitionNext.cancel != nil {
		p.transitionNext.cancel()
	}
	if p.transition != nil {
		if p.out != nil && !p.transition.startedID(p.transitionID) {
			// 新曲可能只存在于提前解码的缓冲；用当前可听曲及位置重新加载以撤销它。
			position := p.Position()
			p.transitionNext = nil
			p.loadPlayback(p.at, 0, stay, p.playing, position, p.playback)
			return
		} else {
			p.transition.cancelNext()
		}
	}
	p.transitionNext = nil
}

func (p *Player) nextTransitionIndex() int {
	if p.at < 0 || p.at >= len(p.order) {
		return -1
	}
	if p.repeat == RepeatOne {
		return p.at
	}
	if p.at+1 < len(p.order) {
		return p.at + 1
	}
	if p.repeat == RepeatAll && p.feed == nil {
		return 0
	}
	return -1
}

func (p *Player) prepareTransition() {
	if p.stopAfterTrack || p.transition == nil || p.out == nil || p.transitionNext != nil || !p.playing {
		return
	}
	settings := p.transitionSettings()
	if p.transition.hasTail() {
		return
	}
	if !settings.Crossfade {
		return
	}
	index := p.nextTransitionIndex()
	if index < 0 {
		return
	}
	p.prepareTransitionAt(index, make(map[int]bool))
}

func (p *Player) prepareTransitionAt(index int, failed map[int]bool) {
	if p.transition == nil || index < 0 || index >= len(p.order) {
		return
	}
	track := p.queue[p.order[index]]
	source := p.sourceOf(&track)
	if source != nil && source.Info().Playback == music.PlaybackRemote {
		return
	}
	p.transitionSequence++
	ctx, cancel := context.WithCancel(context.Background())
	preparation := &transitionPreparation{id: p.transitionSequence, index: p.order[index], track: track, playback: &playbackAttempt{dir: forward}, cancel: cancel, failed: failed, context: ctx}
	p.transitionNext, p.transitionCancel = preparation, cancel
	quality := music.Qualities[len(music.Qualities)/2]
	if p.Quality != nil {
		quality = p.Quality()
	}
	pre := p.takeAhead(track, quality)
	generation := p.gen
	install := func(attempt context.Context, info *music.Stream, decoded stream, policy playbackPolicy, admit func(func() error) error) error {
		if p.transitionSettings().Crossfade {
			var failure error
			decoded, failure = trimSilenceStream(decoded)
			if failure != nil {
				return failure
			}
		}
		prepared, failure := prepareStream(attempt, decoded, 0)
		if failure != nil {
			return failure
		}
		installed := make(chan error, 1)
		var accepted <-chan error
		var acknowledge chan error
		if admit != nil {
			acknowledge = make(chan error, 1)
			accepted = acknowledge
		}
		p.post(func() {
			if attempt.Err() != nil || p.gen != generation || p.transitionNext != preparation || p.transition == nil {
				installed <- context.Canceled
				return
			}
			preparation.info, preparation.stream = info, prepared
			preparation.playback.policy = policy
			preparation.watch = &watched{stream: prepared, now: p.now, ended: func() {}, failed: make(chan error, 1)}
			settings := p.transitionSettings()
			if preparation.index == p.order[p.at] {
				settings.Crossfade = false
			}
			gain := 1.0
			if p.Normalize != nil && p.Normalize() {
				gain = normGain(info)
			}
			var admission func() error
			var rejection func(error)
			if admit != nil {
				rejection = func(err error) {
					select {
					case acknowledge <- err:
					default:
					}
				}
				admission = func() error {
					err := admit(func() error { return attempt.Err() })
					if err == nil {
						rejection(nil)
					}
					return err
				}
			}
			buffer := newTransitionBuffer(newTransitionDecoder(preparation.watch))
			p.transition.queueConfigured(buffer, settings, preparation.id, gain, admission, rejection)
			preparation.ready = true
			installed <- nil
		})
		select {
		case failure = <-installed:
		case <-attempt.Done():
			return attempt.Err()
		}
		if failure != nil {
			return failure
		}
		if accepted != nil {
			select {
			case failure = <-accepted:
				return failure
			case <-attempt.Done():
				return attempt.Err()
			}
		}
		return nil
	}
	go func() {
		var failure error
		var info *music.Stream
		var decoded stream
		if pre != nil {
			select {
			case <-pre.done:
				failure = pre.err
			case <-ctx.Done():
				failure = ctx.Err()
			}
		}
		if failure == nil {
			info, decoded, failure = p.open(source, track, quality, pre)
		}
		if failure == nil {
			download, _ := decoded.source()
			stop := context.AfterFunc(ctx, func() { download.Close() })
			failure = install(ctx, info, decoded, playbackPolicy{}, nil)
			stop()
			if failure != nil {
				decoded.Close()
			}
		}
		if failure != nil {
			p.post(func() {
				if generation == p.gen && p.transitionNext == preparation {
					p.transitionPreparationFailed(preparation, failure)
				}
			})
		}
	}()
}

func (p *Player) transitionPreparationFailed(preparation *transitionPreparation, failure error) {
	if p.transitionNext != preparation {
		return
	}
	if preparation.blocked != nil {
		return
	}
	if p.transition != nil {
		p.transition.cancelNext()
	}
	preparation.cancel()
	// 准备失败不修改 A 的 current、音量或播放历史；不可跳过错误留到真实曲尾再处理。
	if errors.Is(failure, music.ErrSignIn) || errors.Is(failure, music.ErrSetup) {
		preparation.blocked = failure
		preparation.ready = false
		return
	}
	preparation.failed[preparation.index] = true
	limit := len(p.order)
	if len(preparation.failed) >= limit || p.repeat == RepeatOne {
		preparation.blocked = failure
		preparation.ready = false
		return
	}
	start := -1
	for position, index := range p.order {
		if index == preparation.index {
			start = position
			break
		}
	}
	for step := 1; step < len(p.order); step++ {
		position := (start + step) % len(p.order)
		if position <= p.at && p.repeat != RepeatAll {
			break
		}
		index := p.order[position]
		if index != p.order[p.at] && !preparation.failed[index] {
			p.transitionNext = nil
			p.prepareTransitionAt(position, preparation.failed)
			return
		}
	}
	preparation.blocked = failure
	preparation.ready = false
}

func (p *Player) watchTransitions(generation int, out audioOutput) {
	if p.transitionDone != nil {
		close(p.transitionDone)
	}
	done := make(chan struct{})
	p.transitionDone = done
	post := p.post
	if p.Quiet != nil {
		post = p.Quiet
	}
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				post(func() {
					if p.gen == generation && p.out == out && p.playing {
						p.commitTransitions()
						p.prepareTransition()
					}
				})
			}
		}
	}()
}

func (p *Player) commitTransitions() {
	// 暂停淡出可能碰到曲尾；先保留事件，恢复播放后再推进，避免 EOF 覆盖暂停意图。
	if p.transition == nil || p.out == nil || !p.playing {
		return
	}
	frame := int64(math.Round(p.out.Position().Seconds() * 48000))
	for _, event := range p.transition.eventsAt(frame) {
		if event.ended {
			if event.id != p.transitionID {
				continue
			}
			if event.failed != nil {
				if p.playback != nil && p.playback.policy.candidate > 0 {
					continue
				}
				source := p.track.Source
				p.stop()
				p.feedback.Stage = "buffering"
				p.loadFailed(source, event.failed, 0, forward)
			} else {
				p.ended()
			}
			return
		}
		if event.id == 0 {
			if event.failed == nil {
				wanted := p.playing
				p.playing = true
				p.record()
				p.playing = wanted
			}
			continue
		}
		preparation := p.transitionNext
		if preparation == nil || preparation.id != event.id {
			continue
		}
		if event.failed != nil {
			p.transitionPreparationFailed(preparation, event.failed)
			continue
		}
		position := -1
		for order, index := range p.order {
			if index == preparation.index {
				position = order
				break
			}
		}
		if position < 0 {
			continue
		}
		p.transitionNext, p.transitionCancel = nil, nil
		p.at = position
		p.track = &preparation.track
		p.info, p.stream, p.watch = preparation.info, preparation.stream, preparation.watch
		p.playback = preparation.playback
		p.gain = normGain(p.info)
		p.transitionStart = event.frame
		p.transitionOffset = silenceTrimOffset(preparation.stream)
		p.transitionID = event.id
		p.applyGain()
		p.pending = 0
		p.recorded = ""
		p.err = ""
		p.feedback = PlaybackFeedback{}
		p.loading = false
		p.lyrics, p.lyricsSource, p.lyricsLoaded = nil, nil, false
		if p.info != nil && p.info.PlaybackID != "" {
			p.track.PlaybackID = p.info.PlaybackID
			p.queue[preparation.index].PlaybackID = p.info.PlaybackID
		}
		p.announce(p.track, nil)
		wanted := p.playing
		p.playing = true
		p.record()
		p.playing = wanted
		p.loadCover(p.gen, p.track)
		p.loadLyrics(p.gen, p.sourceOf(p.track), *p.track)
		if p.playback.policy.candidate > 0 {
			p.monitorDownload(p.gen, p.out, p.stream)
		}
		p.sync()
		p.refill()
		p.prepareTransition()
	}
}

func (output *transitionOutput) discardTail() {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.tail != nil {
		output.retired = append(output.retired, output.tail)
		output.tail = nil
	}
	output.overlap, output.progress = 0, 0
}
