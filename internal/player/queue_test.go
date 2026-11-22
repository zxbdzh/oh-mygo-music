package player

import (
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/music"
)

// 重复歌曲的队列位置独立，移除一项不重启当前输出或改变其余实际顺序。
func TestRemoveQueuedKeepsCurrentAndDuplicatePositions(t *testing.T) {
	p := &Player{at: -1}
	p.Cue([]music.Track{{ID: "a"}, {ID: "b"}, {ID: "b"}, {ID: "c"}})
	p.order = []int{3, 0, 2, 1}
	p.at, p.shuffle, p.repeat = 1, true, RepeatOne
	p.track = &p.queue[0]
	p.Hold(42 * time.Second)
	removed, surviving := p.QueueEntryID(2), p.QueueEntryID(1)
	changed := 0
	p.Changed = func() { changed++ }
	p.RemoveQueued(removed)
	order, at := p.PlayOrder()
	var got []string
	for _, index := range order {
		got = append(got, p.Queue()[index].ID)
	}
	if !reflect.DeepEqual(got, []string{"c", "a", "b"}) || at != 1 {
		t.Fatalf("remaining order %v at %d", got, at)
	}
	if p.Current().ID != "a" || p.Position() != 42*time.Second || !p.Playing() || !p.Shuffle() || p.Repeat() != RepeatOne {
		t.Fatal("removing a queued position interrupted playback")
	}
	if p.QueueEntryID(1) != surviving || changed != 1 {
		t.Fatal("removal lost the surviving position or session notification")
	}
	p.RemoveQueued(removed)
	if len(p.Queue()) != 3 || changed != 1 {
		t.Fatal("stale removal affected a different position")
	}
}

// 暂停移除当前项按实际顺序接续，跨过失败项也不会开播或恢复已删除的位置。
func TestRemoveQueuedCurrentKeepsPauseAndRestorableOrder(t *testing.T) {
	p, posted, tracks := pickyQueue()
	defer p.stop()
	p.Restore(Session{Queue: tracks, Order: []int{2, 0, 1}, At: 1, Repeat: RepeatOne, Shuffle: true, Position: 12000})
	settle(t, posted, p)
	p.RemoveQueued(p.QueueEntryID(0))
	settle(t, posted, p)
	if p.Current().ID != "c" || p.Playing() || p.out.Playing() {
		t.Fatalf("paused removal selected %+v playing=%t", p.Current(), p.Playing())
	}
	session, ok := p.Session()
	if !ok || len(session.Queue) != 2 || session.Queue[0].ID != "b" || session.Queue[1].ID != "c" ||
		!reflect.DeepEqual(session.Order, []int{1, 0}) || session.At != 0 || session.Position != 0 {
		t.Fatalf("incorrect remaining session: %+v", session)
	}
}

// 移除最后一个位置后清空会话，旧加载结果不能重新安装已移除的歌曲。
func TestRemoveQueuedLastRejectsLateLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, posted, tracks := pickyQueue()
		defer p.stop()
		p.PlayTrack(tracks[:1], 0)
		p.RemoveQueued(p.QueueEntryID(0))
		synctest.Wait()
		pumpTransitionPosts(posted)
		if p.Current() != nil || p.Playing() || p.Loading() || p.Ready() || len(p.Queue()) != 0 {
			t.Fatal("a late result revived the removed song")
		}
		if _, ok := p.Session(); ok {
			t.Fatal("empty queue still has a saved playback")
		}
	})
}

// 插队后旧行的移除仍作用于原位置，整队替换后过期选播和移除均不作用于新队列。
func TestQueuedActionsKeepTheirPositionAcrossEdits(t *testing.T) {
	p := &Player{at: -1}
	p.Cue([]music.Track{{ID: "a"}, {ID: "b"}, {ID: "c"}})
	target := p.QueueEntryID(2)
	p.PlayNext(music.Track{ID: "new"})
	p.RemoveQueued(target)
	if len(p.Queue()) != 3 || p.Queue()[2].ID != "b" {
		t.Fatalf("stale index removed the wrong song: %+v", p.Queue())
	}
	p.Cue([]music.Track{{ID: "replacement"}})
	p.JumpQueued(target)
	p.RemoveQueued(target)
	if len(p.Queue()) != 1 || p.Current().ID != "replacement" {
		t.Fatal("an obsolete row acted on a replacement queue")
	}
}

// 来源仍持有旧队列时，移除待播项不重启当前歌，并在自然切歌时接管后继。
func TestRemoveQueuedOverridesRemoteQueueWithoutRestart(t *testing.T) {
	rig := newRemoteRig(t, 3)
	defer rig.p.stop()
	rig.src.set(playingState("a", 42000), nil)
	rig.settled()
	rig.poll(time.Second)
	before := rig.p.Position()
	rig.p.RemoveQueued(rig.p.QueueEntryID(1))
	if rig.p.Current().ID != "a" || rig.p.Position() != before || !rig.p.Playing() {
		t.Fatal("removing a future track restarted remote playback")
	}
	select {
	case command := <-rig.src.cmd:
		t.Fatalf("unexpected remote command %q during removal", command)
	default:
	}
	rig.src.set(playingState("b", 1000), nil)
	rig.poll(time.Second)
	rig.await("play")
	rig.settle()
	if rig.p.Current().ID != "c" || len(rig.p.Queue()) != 2 || !rig.p.Playing() {
		t.Fatal("remote source revived its removed track")
	}
}

// 点击已经播放的队列位置不从头重载，仍保持原来的播放进度。
func TestJumpQueuedCurrentDoesNotRestart(t *testing.T) {
	p := New(func(func()) {})
	p.Cue([]music.Track{{ID: "a"}, {ID: "b"}})
	p.Hold(42 * time.Second)
	p.JumpQueued(p.QueueEntryID(0))
	if p.Position() != 42*time.Second || p.Loading() || p.Current().ID != "a" {
		t.Fatal("selecting the current queue position restarted it")
	}
}

// 淡化准备前后移除其他位置不回放当前歌曲，旧预加载也不能接入被移除的待播项。
func TestRemoveQueuedAcrossTransitionHandoff(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, posted, source := transitionPlayerFixture(t)
				p.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}, {ID: "c", Source: "test"}}, 0)
				for step := 0; step < 8; step++ {
					synctest.Wait()
					pumpTransitionPosts(posted)
				}
				remove := p.QueueEntryID(1)
				if after {
					for step := 0; step < 350 && p.Current().ID != "b"; step++ {
						advanceTransitionPlayer(p, posted, 480)
					}
					remove = p.QueueEntryID(0)
				}
				output, position, current := p.out, p.Position(), p.Current().ID
				p.RemoveQueued(remove)
				if p.out != output || p.Position() != position || p.Current().ID != current {
					t.Fatal("editing another queue position restarted the current output")
				}
				for step := 0; step < 500 && p.Current().ID != "c"; step++ {
					advanceTransitionPlayer(p, posted, 480)
					if !after && p.Current().ID == "b" {
						t.Fatal("a removed prepared track was handed off")
					}
				}
				if p.Current().ID != "c" {
					t.Fatalf("remaining successor never played: current=%+v error=%s", p.Current(), p.Err())
				}
				synctest.Wait()
				for _, recorded := range source.records() {
					if !after && recorded == "b" {
						t.Fatal("the removed prepared song was recorded as played")
					}
				}
				p.stop()
				time.Sleep(200 * time.Millisecond)
				pumpTransitionPosts(posted)
			})
		})
	}
}

// 播放中移除当前项直接接续后继；原位置在末尾时从剩余实际顺序开头继续。
func TestRemoveQueuedCurrentContinuesPlaying(t *testing.T) {
	for _, last := range []bool{false, true} {
		t.Run(map[bool]string{false: "successor", true: "wrap"}[last], func(t *testing.T) {
			p, posted, tracks := pickyQueue()
			defer p.stop()
			start, want := 0, "c"
			if last {
				start, want = 2, "a"
			}
			p.PlayTrack(tracks, start)
			settle(t, posted, p)
			p.RemoveQueued(p.QueueEntryID(start))
			settle(t, posted, p)
			if p.Current().ID != want || !p.Playing() || len(p.Queue()) != 2 {
				t.Fatalf("current removal did not continue: track=%+v playing=%t", p.Current(), p.Playing())
			}
		})
	}
}

// 下一首插入保留当前进度及其余播放顺序，随机播放时也不能重洗整个队列。
func TestPlayNextKeepsCurrentAndRemainingOrder(t *testing.T) {
	for _, shuffled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "shuffled"}[shuffled], func(t *testing.T) {
			p := &Player{at: -1}
			p.Cue([]music.Track{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}})
			p.at = 1
			want := []string{"a", "b", "picked", "c", "d"}
			if shuffled {
				p.order = []int{2, 0, 3, 1}
				want = []string{"c", "a", "picked", "d", "b"}
			}
			p.shuffle = shuffled
			p.track = &p.queue[p.order[p.at]]
			current := p.Current().ID
			p.Hold(42 * time.Second)
			p.repeat = RepeatOne
			changed := 0
			p.Changed = func() { changed++ }
			p.PlayNext(music.Track{ID: "picked", Source: "other", PlaybackID: "old-playback"})
			order, at := p.PlayOrder()
			var got []string
			for _, queued := range order {
				got = append(got, p.Queue()[queued].ID)
			}
			if !reflect.DeepEqual(got, want) || at != 1 {
				t.Fatalf("play order %v at %d, want %v at 1", got, at, want)
			}
			if p.Current().ID != current || !p.Playing() || p.Position() != 42*time.Second || p.Shuffle() != shuffled || p.Repeat() != RepeatOne {
				t.Fatalf("insertion interrupted playback: current %+v, position %s", p.Current(), p.Position())
			}
			if queued := p.Queue()[order[at+1]]; queued.Source != "other" || queued.PlaybackID != "" {
				t.Fatalf("new queue position inherited another playback: %+v", queued)
			}
			if _, ok := p.Session(); !ok || changed != 1 {
				t.Fatal("queue insertion was not announced for session persistence")
			}
		})
	}
}

// 空队列直接播放；已有队列则保留原曲目，下一次切歌实际播放新插入的位置。
func TestPlayNextStartsOrAdvancesToInsertedTrack(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "empty"}[empty], func(t *testing.T) {
			p, posted, queue := pickyQueue()
			defer p.stop()
			if !empty {
				p.PlayTrack(queue, 0)
				settle(t, posted, p)
			}
			p.PlayNext(queue[2])
			if !empty {
				if p.Current().ID != "a" || len(p.Queue()) != 4 {
					t.Fatal("insertion replaced the original queue or current track")
				}
				p.Next()
			}
			settle(t, posted, p)
			if p.Current().ID != "c" || !p.Playing() || p.Err() != "" {
				t.Fatalf("inserted track did not play: %+v, error %q", p.Current(), p.Err())
			}
		})
	}
}

// 重复入队是独立位置；插入不能把旧位置的失败记录或已播身份错误转移给新项。
func TestPlayNextKeepsFailedPositionsDistinct(t *testing.T) {
	p := &Player{at: -1}
	p.Cue([]music.Track{{ID: "a"}, {ID: "b"}, {ID: "b", PlaybackID: "played-b"}})
	p.playback = &playbackAttempt{failed: map[int]bool{2: true}, recoveryUsed: true}
	p.PlayNext(p.Queue()[2])
	if !reflect.DeepEqual(p.playback.failed, map[int]bool{3: true}) || !p.playback.recoveryUsed {
		t.Fatalf("insertion lost playback history: %+v", p.playback)
	}
	if p.Queue()[1].PlaybackID != "" || p.Queue()[3].PlaybackID != "played-b" {
		t.Fatalf("repeated track inherited or lost a played version: %+v", p.Queue())
	}
}

// 来源仍持有旧队列时，等其自然切歌再接管下一项，插入本身不能重启当前曲目。
func TestPlayNextOverridesOldRemoteQueueWithoutRestartingCurrent(t *testing.T) {
	rig := newRemoteRig(t, 3)
	defer rig.p.stop()
	rig.src.set(playingState("a", 42000), nil)
	rig.settled()
	rig.poll(time.Second)
	position := rig.p.Position()
	rig.p.PlayNext(music.Track{ID: "picked", Source: "remote", Duration: 200000})
	if rig.p.Current().ID != "a" || rig.p.Position() != position || !rig.p.Playing() {
		t.Fatal("insertion restarted remote playback")
	}
	select {
	case command := <-rig.src.cmd:
		t.Fatalf("insertion sent an unexpected remote command: %s", command)
	default:
	}
	rig.src.set(playingState("b", 1000), nil)
	rig.poll(time.Second)
	rig.await("play")
	rig.settle()
	if rig.p.Current().ID != "picked" || !rig.p.Playing() {
		t.Fatalf("remote source bypassed inserted track: %+v", rig.p.Current())
	}
}

// 电台已经等待后续歌曲时，手动插入可继续播放，不必等待旧电台请求返回。
func TestPlayNextResumesRadioWaitingForFeed(t *testing.T) {
	p, posted, queue := pickyQueue()
	defer p.stop()
	p.PlayRadio("station", queue[:1], func(func([]music.Track)) {})
	settle(t, posted, p)
	p.Next()
	if !p.Loading() {
		t.Fatal("radio did not wait for its feed")
	}
	p.PlayNext(queue[2])
	settle(t, posted, p)
	if p.Current().ID != "c" || !p.Playing() || p.Radio() != "station" {
		t.Fatalf("manual insertion did not resume the radio: %+v", p.Current())
	}
}
