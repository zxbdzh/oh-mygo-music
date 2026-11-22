package player

import (
	"slices"

	"mygo-music/internal/music"
)

func (p *Player) resetQueueIDs() {
	p.queueIDs = make([]uint64, len(p.queue))
	for index := range p.queueIDs {
		p.nextQueueID++
		p.queueIDs[index] = p.nextQueueID
	}
}

func (p *Player) ensureQueueIDs() {
	if len(p.queueIDs) != len(p.queue) {
		p.resetQueueIDs()
	}
}

// QueueEntryID identifies one queued position, including repeated songs.
func (p *Player) QueueEntryID(index int) uint64 {
	p.ensureQueueIDs()
	if index < 0 || index >= len(p.queueIDs) {
		return 0
	}
	return p.queueIDs[index]
}

func (p *Player) queuedIndex(id uint64) int {
	p.ensureQueueIDs()
	if id == 0 {
		return -1
	}
	return slices.Index(p.queueIDs, id)
}

// JumpQueued ignores actions from a row whose queued position has gone away.
func (p *Player) JumpQueued(id uint64) {
	p.commitTransitions()
	if index := p.queuedIndex(id); index >= 0 && (p.at < 0 || p.at >= len(p.order) || p.order[p.at] != index) {
		p.Jump(index)
	}
}

// RemoveQueued removes a position without treating repeated songs as one.
func (p *Player) RemoveQueued(id uint64) {
	p.commitTransitions()
	index := p.queuedIndex(id)
	if index < 0 {
		return
	}
	position := slices.Index(p.order, index)
	if position < 0 {
		return
	}
	current := position == p.at
	p.cancelTransitionPreparation()
	p.queue = slices.Delete(slices.Clone(p.queue), index, index+1)
	p.queueIDs = slices.Delete(p.queueIDs, index, index+1)
	p.order = slices.Delete(slices.Clone(p.order), position, position+1)
	for order, queued := range p.order {
		if queued > index {
			p.order[order]--
		}
	}
	if p.playback != nil {
		failed := make(map[int]bool, len(p.playback.failed))
		for queued, value := range p.playback.failed {
			if queued == index {
				continue
			}
			if queued > index {
				queued--
			}
			failed[queued] = value
		}
		p.playback.failed = failed
	}
	if !current && position < p.at {
		p.at--
	}
	if p.remote != nil {
		p.remote.queueChanged = true
	}
	p.dropAhead()
	if current {
		if len(p.order) == 0 {
			p.stopForFeed()
			// 停止会取消加载，但旧来源的不可取消结果仍须由代次门禁拒绝。
			p.gen++
			p.endRadio()
			p.track, p.playback = nil, nil
			p.at, p.pending = -1, 0
			p.loading, p.playing, p.err = false, false, ""
			p.lyrics, p.lyricsSource, p.lyricsLoaded = nil, nil, false
			p.sync()
			return
		}
		next := position
		if next >= len(p.order) {
			next = 0
		}
		p.loadSkipping(next, 0, forward, p.playing, 0)
		return
	}
	p.sync()
	p.prepareTransition()
}

// PlayNext inserts a new queue entry immediately after the current track.
// Without a current track it starts playback instead.
func (p *Player) PlayNext(track music.Track) {
	p.commitTransitions()
	if track.Unavailable {
		return
	}
	p.cancelTransitionPreparation()
	// 新入队是独立位置，不能继承另一个位置已经成功开播的版本。
	track.PlaybackID = ""
	if p.track == nil || p.at < 0 || p.at >= len(p.order) {
		p.PlayTrack([]music.Track{track}, 0)
		return
	}
	insert := p.order[p.at] + 1
	p.ensureQueueIDs()
	p.queue = slices.Insert(p.queue, insert, track)
	p.nextQueueID++
	p.queueIDs = slices.Insert(p.queueIDs, insert, p.nextQueueID)
	for index, queued := range p.order {
		if queued >= insert {
			p.order[index]++
		}
	}
	// 同时调整原队列与实际播放顺序，随机模式下也只改变紧邻的下一项。
	p.order = slices.Insert(p.order, p.at+1, insert)
	if p.playback != nil && len(p.playback.failed) > 0 {
		failed := make(map[int]bool, len(p.playback.failed))
		for queued, value := range p.playback.failed {
			if queued >= insert {
				queued++
			}
			failed[queued] = value
		}
		p.playback.failed = failed
	}
	if p.remote != nil {
		// 来源可能已接收旧队列；等其自然切歌时接管，而不是重启当前歌曲。
		p.remote.queueChanged = true
	}
	p.dropAhead()
	if p.awaiting {
		p.load(p.at + 1)
		return
	}
	if p.out != nil {
		quality := music.Qualities[len(music.Qualities)/2]
		if p.Quality != nil {
			quality = p.Quality()
		}
		p.getAhead(quality)
	}
	p.sync()
	p.prepareTransition()
}
