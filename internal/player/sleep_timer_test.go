package player

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"mygo-music/internal/music"
)

// “播完再暂停”开启后必须压制交叉淡化、单曲/列表循环和电台续播，只播完当前曲目后暂停且不改 Repeat 设置。
func TestSleepTimerStopAfterTrackOverridesRepeatAndCrossfade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		current.Transitions = func() TransitionSettings {
			settings := DefaultTransitionSettings()
			settings.Crossfade = true
			settings.CrossfadeDuration = 3 * time.Second
			return settings
		}
		current.CycleRepeat()
		current.CycleRepeat()
		if current.Repeat() != RepeatOne {
			t.Fatalf("repeat = %v, want RepeatOne", current.Repeat())
		}
		sleepEnded := 0
		current.SleepEnded = func() { sleepEnded++ }

		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}}, 0)
		for step := 0; step < 12; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		if current.Current() == nil || current.Current().ID != "a" || !current.Playing() {
			t.Fatalf("未开始播放第一首: current=%+v playing=%v", current.Current(), current.Playing())
		}

		current.SetStopAfterTrack(true)
		for step := 0; step < 1000; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			if !current.Playing() {
				break
			}
			advanceTransitionPlayer(current, posted, 480)
		}

		if current.Playing() {
			t.Fatal("曲尾未暂停")
		}
		if current.Current() == nil || current.Current().ID != "a" {
			t.Fatalf("曲尾仍切歌了: current=%+v", current.Current())
		}
		if current.StopAfterTrack() {
			t.Fatal("暂停后 StopAfterTrack 未清除")
		}
		if sleepEnded != 1 {
			t.Fatalf("SleepEnded 触发次数 = %d, want 1", sleepEnded)
		}
		if current.Repeat() != RepeatOne {
			t.Fatalf("睡眠定时修改了用户循环设置: %v", current.Repeat())
		}

		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

// 等待曲尾阶段手动切歌不能取消“播完再暂停”，应改为等待新歌自然播完后暂停。
func TestSleepTimerStopAfterTrackSurvivesManualSkip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		current, posted, _ := transitionPlayerFixture(t)
		sleepEnded := 0
		current.SleepEnded = func() { sleepEnded++ }

		current.PlayQueue([]music.Track{{ID: "a", Source: "test"}, {ID: "b", Source: "test"}, {ID: "c", Source: "test"}}, 0)
		for step := 0; step < 12; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}

		current.SetStopAfterTrack(true)
		current.Next()
		for step := 0; step < 12; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
		}
		if current.Current() == nil || current.Current().ID != "b" || !current.Playing() {
			t.Fatalf("切到第二首失败: current=%+v playing=%v", current.Current(), current.Playing())
		}
		if !current.StopAfterTrack() {
			t.Fatal("手动切歌清除了 StopAfterTrack")
		}
		if current.transitionNext != nil {
			t.Fatal("等待曲尾期间不应准备下一曲交叉淡化")
		}

		for step := 0; step < 1000; step++ {
			synctest.Wait()
			pumpTransitionPosts(posted)
			if !current.Playing() {
				break
			}
			advanceTransitionPlayer(current, posted, 480)
		}
		if current.Playing() || current.Current() == nil || current.Current().ID != "b" || sleepEnded != 1 {
			t.Fatalf("第二首曲尾未按预期暂停: playing=%v current=%+v sleepEnded=%d", current.Playing(), current.Current(), sleepEnded)
		}

		current.stop()
		time.Sleep(200 * time.Millisecond)
		pumpTransitionPosts(posted)
	})
}

type remotePauseFailFake struct {
	remoteFake
	pauseErr error
}

func (s *remotePauseFailFake) RemotePause() error {
	s.note("pause")
	return s.pauseErr
}

// 远程播放到点暂停失败时，除了设置播放器错误状态，还必须触发失败回调供上层弹出 Toast。
func TestSleepTimerRemotePauseFailureReportsCallback(t *testing.T) {
	ch := make(chan func(), 16)
	p := New(func(fn func()) { ch <- fn })
	src := &remotePauseFailFake{
		remoteFake: remoteFake{cmd: make(chan string, 8)},
		pauseErr:   errors.New("device offline"),
	}
	p.Source = func(string) music.Source { return src }
	p.PlayQueue([]music.Track{{ID: "r1", Source: "remote"}}, 0)

	if got := <-src.cmd; got != "play" {
		t.Fatalf("command = %q, want play", got)
	}
	pumpTransitionPosts(ch)
	if !p.Remote() || !p.Playing() {
		t.Fatalf("remote=%v playing=%v", p.Remote(), p.Playing())
	}

	var gotErr error
	done := make(chan struct{})
	p.PauseForSleep(func(err error) {
		gotErr = err
		close(done)
	})
	if got := <-src.cmd; got != "pause" {
		t.Fatalf("command = %q, want pause", got)
	}
	for range 20 {
		pumpTransitionPosts(ch)
		select {
		case <-done:
			if gotErr == nil || p.Err() == "" {
				t.Fatalf("gotErr=%v p.Err=%q", gotErr, p.Err())
			}
			return
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("未收到远程暂停失败回调")
}
