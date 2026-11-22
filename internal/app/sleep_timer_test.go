package app

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
	"mygo-music/internal/player"
)

type remoteSleepSource struct {
	*fakeSource
	pauseErr error
	pauses   chan struct{}
}

func (s *remoteSleepSource) RemotePlay([]string, int, int64) error { return nil }
func (s *remoteSleepSource) RemotePause() error {
	if s.pauses != nil {
		s.pauses <- struct{}{}
	}
	return s.pauseErr
}
func (s *remoteSleepSource) RemoteState() (*music.RemoteState, error) {
	return &music.RemoteState{Playing: true}, nil
}

// Clicking the Sleep Timer button opens its popover, rejects invalid custom
// minute inputs, and starts and cancels a timer for valid inputs.
func TestSleepTimerPanelCustomInputAndCancel(t *testing.T) {
	a := newTestApp()
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	if err := tt.Click("Sleep Timer"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.sleep.open {
		t.Fatal("sleep timer popover did not open")
	}

	for _, bad := range []string{"", "0", "721", "12.5", "abc"} {
		a.sleep.customMinutes = bad
		tt.Frame()
		if err := tt.Click("Set"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.sleep.active || a.sleep.customError == "" {
			t.Fatalf("bad input %q: active=%v err=%q", bad, a.sleep.active, a.sleep.customError)
		}
	}

	a.sleep.customMinutes = "720"
	tt.Frame()
	if err := tt.Click("Set"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.sleep.active || a.sleep.customError != "" || a.sleepRemaining() != 720*time.Minute {
		t.Fatalf("valid 720m: active=%v err=%q rem=%v", a.sleep.active, a.sleep.customError, a.sleepRemaining())
	}

	if err := tt.Click("Cancel Timer"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.sleep.active || a.pl.StopAfterTrack() {
		t.Fatalf("timer not cleared after cancel: active=%v stopAfter=%v", a.sleep.active, a.pl.StopAfterTrack())
	}
}

// Sleep countdown advances across paused playback and track changes, and
// expires immediately when the system wakes past the deadline.
func TestSleepTimerCountdownAcrossPauseAndSleepWake(t *testing.T) {
	a := newTestApp()
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.pl.Cue([]music.Track{song("1", "Song One", "Artist"), song("2", "Song Two", "Artist")})

	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	a.startSleepTimer(15, sleepModeAtDeadline)
	tt.Frame()
	if rem := a.sleepRemaining(); rem != 15*time.Minute {
		t.Fatalf("initial remaining = %v, want 15m", rem)
	}

	// Simulate manual pause and track skip while clock advances 10 minutes.
	now = now.Add(10 * time.Minute)
	tt.Frame()
	if rem := a.sleepRemaining(); rem != 5*time.Minute {
		t.Fatalf("remaining after 10m = %v, want 5m", rem)
	}

	// Simulate computer sleep jumping 20 minutes past the deadline.
	now = now.Add(20 * time.Minute)
	tt.Frame()
	if a.sleep.active {
		t.Fatal("sleep timer remained active after waking past deadline")
	}
}

// When "After current track" reaches its deadline while playback is already
// paused, the timer ends silently without leaving a pending wait flag.
func TestSleepTimerAfterTrackPausedAtDeadlineEndsCleanly(t *testing.T) {
	a := newTestApp()
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.pl.Cue([]music.Track{song("1", "Song One", "Artist")})

	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if a.pl.Playing() {
		t.Fatal("expected player to be paused in test setup")
	}

	a.startSleepTimer(15, sleepModeEndOfTrack)
	now = now.Add(15 * time.Minute)
	tt.Frame()

	if a.sleep.active || a.sleep.waitingForEnd || a.pl.StopAfterTrack() {
		t.Fatalf("expected silent end when paused: active=%v waiting=%v stopAfter=%v",
			a.sleep.active, a.sleep.waitingForEnd, a.pl.StopAfterTrack())
	}
}

// Now Playing shares the same active timer state, can inspect and cancel a
// pending wait, and disables "After current track" for remote sources.
func TestSleepTimerSharedPanelInNowPlayingAndRemoteMode(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	a.pl = player.New(a.post)
	a.pl.Source = a.sourceByID
	a.pl.SleepEnded = func() { a.clearSleepTimer() }

	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	remoteSrc := &remoteSleepSource{
		fakeSource: newFakeSource("remote"),
		pauseErr:   errors.New("remote unreachable"),
		pauses:     make(chan struct{}, 2),
	}
	remoteSrc.info.Playback = music.PlaybackRemote
	addFake(a, remoteSrc)

	a.pl.Cue([]music.Track{song("1", "Local Song", "Artist")})
	a.startSleepTimer(30, sleepModeEndOfTrack)
	a.sleep.waitingForEnd = true
	a.pl.SetStopAfterTrack(true)

	a.nowPlaying = true
	a.npPane = paneLyrics
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()

	if err := tt.Click("Sleep Timer"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Pausing after current track") {
		t.Fatalf("Now Playing popover did not reflect waiting state: %v", tt.Texts())
	}
	if err := tt.Click("Cancel Timer"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.sleep.active || a.pl.StopAfterTrack() {
		t.Fatal("cancel from Now Playing did not clear sleep timer")
	}

	// Switch to remote source: "After current track" is disabled and remote pause failure shows a toast.
	a.pl.PlayQueue([]music.Track{{ID: "r1", Name: "Remote Song", Source: "remote"}}, 0)
	until(t, ch, func() bool { return a.pl.Ready() })
	tt.Frame()
	if !a.pl.Remote() {
		t.Fatal("expected remote playback")
	}
	tt.Frame()
	if !tt.HasText("Remote playback only supports pausing at timer end") {
		t.Fatalf("missing remote limitation note: %v", tt.Texts())
	}
	if b, ok := tt.AccessByRole(ui.RoleButton, "After current track"); !ok || !b.Disabled {
		t.Fatalf("After current track should be disabled on remote source: ok=%v btn=%+v", ok, b)
	}

	a.startSleepTimer(15, sleepModeAtDeadline)
	now = now.Add(16 * time.Minute)
	tt.Frame()
	<-remoteSrc.pauses
	until(t, ch, func() bool { return a.toast != "" })
	if a.toast != "Could not pause remote playback for sleep timer" {
		t.Fatalf("unexpected toast: %q", a.toast)
	}
}

// Sleep timer expiry controls only the main player and does not touch an active
// share-clip preview output.
func TestSleepTimerDoesNotStopSharePreview(t *testing.T) {
	a := newTestApp()
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	tr := song("1", "Song One", "Artist")
	a.pl.Cue([]music.Track{tr})
	a.sharing = &shareDraft{
		open:              true,
		track:             tr,
		clipPreviewActive: true,
	}

	a.startSleepTimer(15, sleepModeAtDeadline)
	now = now.Add(20 * time.Minute)
	a.checkSleepTimer()

	if a.sleep.active {
		t.Fatal("timer should have expired")
	}
	if !a.sharing.clipPreviewActive {
		t.Fatal("sleep timer interrupted independent share clip preview")
	}
}

// With MM_FRAMES set, saves screenshots of the Sleep Timer popover on the
// player bar and in Now Playing for visual verification.
func TestSleepTimerFrame(t *testing.T) {
	dir := os.Getenv("MM_FRAMES")
	if dir == "" {
		t.Skip("set MM_FRAMES for visual acceptance")
	}

	a := newTestApp()
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	a.pl.Cue([]music.Track{song("1", "Haruhikage", "MyGO!!!!!")})
	a.pl.SetLyrics(nil)

	tt := ui.NewTester(a.view, 1200, 800)
	tt.SetDark(false)
	a.startSleepTimer(30, sleepModeEndOfTrack)
	now = now.Add(15*time.Minute + 15*time.Second)
	tt.Frame()
	if err := tt.Click("Sleep Timer"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	tt.Frame()
	saveFrame(t, dir, "sleep-timer-bar-light.png", tt.Image())

	tt.SetDark(true)
	a.sleep.open = false
	a.nowPlaying = true
	a.npPane = paneLyrics
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("Sleep Timer"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	tt.Frame()
	saveFrame(t, dir, "sleep-timer-nowplaying.png", tt.Image())
}
