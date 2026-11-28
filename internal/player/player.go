package player

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"

	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

type RepeatMode int

const (
	RepeatOff RepeatMode = iota
	RepeatAll
	RepeatOne
)

// Player plays a queue of tracks, from whichever source each is of. Its
// methods run on the main thread; loading happens on goroutines, whose results
// post brings back there.
type Player struct {
	post func(func())
	// Quiet is post without a redraw, for what mostly changes nothing, if set.
	Quiet func(func())
	// Source finds the source of an ID, or nil if it's gone.
	Source func(id string) music.Source
	// Failed, if set, hears that a source said the listener has something to
	// do first: ErrSignIn or ErrSetup.
	Failed func(source string, err error)
	// Quality returns the quality to ask sources for, as the settings say.
	Quality func() string
	// Normalize reports whether to even out the loudness of tracks, as
	// the settings say.
	Normalize func() bool
	// Transitions 在开始下一次过渡时读取；当前过渡持有自己的参数快照。
	Transitions func() TransitionSettings
	// Cache keeps the songs played, if set.
	Cache *Cache
	// 手动覆盖优先读取，不改写音源本来的歌词。
	LyricsOverride func(music.Track) *music.Lyrics
	// Changed, if set, hears that what plays, where it is or whether it
	// plays changed.
	Changed func()
	// SleepEnded 在“播完再暂停”到达曲尾并暂停时触发。
	SleepEnded func()

	queue       []music.Track
	queueIDs    []uint64
	nextQueueID uint64
	order       []int // the queue's indices in play order: shuffled or not
	at          int   // where in order the current track is, -1 for none
	shuffle     bool
	repeat      RepeatMode
	volume      float64

	// A radio goes on with the tracks its feed gives, as the queue nears
	// its end, for as long as nothing else is played.
	radio    string
	feed     func(got func([]music.Track))
	radioGen int  // counts radios, to drop what an old one gives
	feeding  bool // the feed is asked
	awaiting bool // at the queue's end, for what the feed gives

	track      *music.Track
	out        audioOutput
	newOutput  func(audio.Decoder) audioOutput
	stream     stream
	watch      *watched      // stream, as out plays it
	info       *music.Stream // what the current track streams
	fade       fader
	gain       float64 // the current track's normalizing gain
	loading    bool
	playing    bool // what the listener wants: false while paused
	err        string
	feedback   PlaybackFeedback
	liveMu     sync.Mutex // 后台解析/缓冲阶段直接写入并按 gen 隔离，不阻塞主线程 post 通道。
	liveGen    int
	liveStage  string
	liveTried  int
	gen        int // counts loads, to drop the results of stale ones
	loadCancel context.CancelFunc
	playback   *playbackAttempt
	monitorCancel context.CancelFunc

	lyrics           []lyrics.Line
	lyricsSource     *music.Lyrics
	lyricsLoaded     bool
	lyricsGeneration uint64

	// pending holds a seek asked for while the track loads.
	pending time.Duration
	// A seek playing fades out, then lands, then fades in: meanwhile the
	// position is where it goes.
	seeking bool
	seekTo  time.Duration
	seekGen int

	// remote is set while the current track is a PlaybackRemote source's,
	// which plays it elsewhere: see remote.go.
	remote          *remote
	stopAfterTrack  bool
	sleepRemoteFail func(error)
	// after runs fn after d, on another goroutine; tests replace it.
	after func(d time.Duration, fn func())
	now   func() time.Time
	cmds  commands
	// The volume last asked of a remote source, and whether that's on its
	// way: guarded by cmds.mu.
	volumeAsked  int
	volumeQueued bool

	recorded string // the track last told its source it played

	ahead              *ahead // the next track's audio, asked for before its turn
	transition         *transitionOutput
	transitionNext     *transitionPreparation
	transitionCancel   context.CancelFunc
	transitionSequence uint64
	transitionStart    int64
	transitionOffset   time.Duration
	transitionID       uint64
	transitionDone     chan struct{}
}

func New(post func(func())) *Player {
	p := &Player{post: post, at: -1, volume: 0.8, gain: 1}
	p.after = func(d time.Duration, fn func()) { time.AfterFunc(d, fn) }
	p.now = time.Now
	p.newOutput = func(decoder audio.Decoder) audioOutput { return audio.NewPlayer(decoder) }
	// Music, mixed far enough ahead to play on while macOS keeps the app
	// waiting, as it does while its appearance changes.
	audio.SetLatency(audio.LatencyPlayback)
	// The media keys, and the controls of Control Center.
	mygo.NowPlaying.OnCommand(func(cmd mygo.MediaCommand, pos time.Duration) {
		switch cmd {
		case mygo.MediaPlay, mygo.MediaPause:
			if (cmd == mygo.MediaPlay) != p.playing {
				p.TogglePlay()
			}
		case mygo.MediaNext:
			p.Next()
		case mygo.MediaPrevious:
			p.Prev()
		case mygo.MediaSeek:
			p.Seek(pos)
		}
		p.post(func() {})
	})
	return p
}

// sync shows the system, and Changed, where playback is.
func (p *Player) sync() {
	if p.Changed != nil {
		defer p.Changed()
	}
	if p.track == nil {
		mygo.NowPlaying.Clear()
		return
	}
	state := mygo.PlaybackPaused
	if p.playing {
		state = mygo.PlaybackPlaying
	}
	mygo.NowPlaying.SetPlayback(state, p.Position())
}

// announce shows the system the track, and its cover once loaded.
func (p *Player) announce(t *music.Track, artwork []byte) {
	state := mygo.PlaybackPaused
	if p.playing {
		state = mygo.PlaybackPlaying
	}
	mygo.NowPlaying.Set(mygo.NowPlayingInfo{
		Title:    t.Name,
		Artist:   t.ArtistNames(),
		Album:    t.Album.Name,
		Artwork:  artwork,
		Duration: t.Length(),
		Position: p.Position(),
		State:    state,
		Commands: mygo.MediaPlay | mygo.MediaPause | mygo.MediaNext | mygo.MediaPrevious | mygo.MediaSeek,
	})
}

func (p *Player) ended() {
	p.commitTransitions()
	if p.stopAfterTrack {
		p.stopAfterTrack = false
		if p.monitorCancel != nil {
			p.monitorCancel()
			p.monitorCancel = nil
		}
		if p.playback != nil {
			p.playback.completed = true
		}
		p.playing = false
		if p.out != nil {
			p.out.Pause()
		}
		p.sync()
		if p.SleepEnded != nil {
			p.SleepEnded()
		}
		return
	}
	if p.transitionNext != nil && p.transitionNext.blocked != nil {
		preparation := p.transitionNext
		failure := preparation.blocked
		source := preparation.track.Source
		p.cancelTransitionPreparation()
		p.feedback = PlaybackFeedback{Stage: "resolving"}
		p.setFailure(source, failure)
		p.playing = false
		if p.Failed != nil && (errors.Is(failure, music.ErrSignIn) || errors.Is(failure, music.ErrSetup)) {
			p.Failed(source, failure)
		}
		p.sync()
		return
	}
	if p.transitionNext != nil && len(p.transitionNext.failed) > 0 {
		preparation := p.transitionNext
		for position, index := range p.order {
			if index == preparation.index {
				playback := &playbackAttempt{dir: forward, skipped: len(preparation.failed), failed: preparation.failed}
				p.loadPlayback(position, len(preparation.failed), forward, true, 0, playback)
				return
			}
		}
	}
	if p.repeat == RepeatOne {
		if p.transition != nil {
			p.loadSkipping(p.at, 0, stay, true, 0)
			return
		}
		if p.playback != nil {
			// 只有真实正常结束才开启新一遍；错误仍走原播放的最终失败分支。
			p.playback = &playbackAttempt{dir: p.playback.dir, policy: p.playback.policy}
		}
		p.out.Seek(0)
		p.out.Play()
		if p.playback != nil && p.playback.policy.candidate > 0 {
			p.monitorDownload(p.gen, p.out, p.stream)
		}
		return
	}
	if p.at+1 >= len(p.order) && p.feed != nil {
		p.awaitFeed()
		return
	}
	if p.at+1 >= len(p.order) && p.repeat == RepeatOff {
		if p.playback != nil {
			p.playback.completed = true
		}
		p.playing = false
		p.sync()
		return
	}
	p.next()
}

// PlayQueue replaces the queue with tracks and plays the one at start, or the
// first after it that can play.
func (p *Player) PlayQueue(tracks []music.Track, start int) {
	p.playQueue(tracks, start, forward)
}

// PlayTrack replaces the queue with tracks and plays the one at i, the
// listener's pick: one that can't play stays, saying why, rather than another
// playing in its place.
func (p *Player) PlayTrack(tracks []music.Track, i int) {
	p.playQueue(tracks, i, stay)
}

// PlayShuffled replaces the queue with tracks in a random order, any of them
// first, and plays it with shuffle on.
func (p *Player) PlayShuffled(tracks []music.Track) {
	p.shuffle = true
	p.playQueue(tracks, -1, forward)
}

// playQueue plays tracks from start; a start below 0, when shuffled, lets
// any track come first.
func (p *Player) playQueue(tracks []music.Track, start, dir int) {
	if len(tracks) == 0 {
		return
	}
	p.endRadio()
	start = min(start, len(tracks)-1)
	p.queue = append([]music.Track(nil), tracks...)
	p.resetQueueIDs()
	p.order = make([]int, len(p.queue))
	for i := range p.order {
		p.order[i] = i
	}
	if p.shuffle {
		p.shuffleOrder(start)
		start = 0
	}
	p.loadSkipping(max(start, 0), 0, dir, true, 0)
}

// PlayRadio plays tracks in their order, then goes on with those feed
// gives as the queue nears its end, until something else is played. The
// radio's name says which it is. feed calls got on the main thread.
func (p *Player) PlayRadio(name string, tracks []music.Track, feed func(got func([]music.Track))) {
	if len(tracks) == 0 {
		return
	}
	shuffle := p.shuffle
	p.shuffle = false
	p.PlayQueue(tracks, 0)
	p.shuffle = shuffle
	p.AttachRadio(name, feed)
}

// AttachRadio makes the queue the radio name's, which feed goes on with,
// as after restoring a session that played it.
func (p *Player) AttachRadio(name string, feed func(got func([]music.Track))) {
	p.endRadio()
	p.radio, p.feed = name, feed
	p.refill()
}

// EndRadio stops the radio playing: the song playing plays on, but the
// songs the radio queued after it go, and it adds no more.
func (p *Player) EndRadio() {
	if p.radio == "" && p.feed == nil {
		return
	}
	awaiting := p.awaiting
	p.endRadio()
	if p.at >= 0 && p.at+1 < len(p.order) {
		kept := make([]music.Track, p.at+1)
		keptIDs := make([]uint64, p.at+1)
		var failed map[int]bool
		for i, k := range p.order[:p.at+1] {
			kept[i] = p.queue[k]
			keptIDs[i] = p.QueueEntryID(k)
			if p.playback != nil && p.playback.failed[k] {
				if failed == nil {
					failed = make(map[int]bool)
				}
				failed[i] = true
			}
		}
		if p.playback != nil {
			// 截断按播放顺序重建索引，失败记录只跟随保留下来的原队列位置。
			p.playback.failed = failed
		}
		p.queue, p.order = kept, make([]int, len(kept))
		p.queueIDs = keptIDs
		for i := range p.order {
			p.order[i] = i
		}
	}
	if awaiting {
		// At its end, waiting for more: nothing plays on.
		p.playing = false
		p.sync()
	}
}

func (p *Player) endRadio() {
	p.radioGen++
	p.radio, p.feed, p.feeding, p.awaiting = "", nil, false, false
}

// Radio returns the name of the radio playing, or "".
func (p *Player) Radio() string { return p.radio }

// refill asks the radio for more once the queue is near its end.
func (p *Player) refill() {
	if p.feed == nil || p.feeding || p.at < len(p.order)-2 {
		return
	}
	p.feeding = true
	gen := p.radioGen
	p.feed(func(more []music.Track) {
		if gen != p.radioGen {
			return
		}
		p.feeding = false
		p.appendTracks(more)
		switch {
		case !p.awaiting:
		case p.at+1 < len(p.order):
			p.awaiting = false
			p.load(p.at + 1)
		default:
			// The radio has nothing more: it stops at its end.
			p.awaiting, p.playing = false, false
			p.err = i18n.T("The station has nothing more right now")
			p.feedback = PlaybackFeedback{Failure: "unavailable"}
			if p.track != nil {
				p.feedback.Source = p.track.Source
			}
			p.sync()
		}
	})
}

// awaitFeed waits at the end of a radio's queue for what its feed gives.
func (p *Player) awaitFeed() {
	p.awaiting = true
	p.refill()
}

// appendTracks adds tracks to the end of the queue, but those in it.
func (p *Player) appendTracks(tracks []music.Track) {
	type key struct{ source, id string }
	in := make(map[key]bool, len(p.queue))
	for _, t := range p.queue {
		in[key{t.Source, t.ID}] = true
	}
	for _, t := range tracks {
		if k := (key{t.Source, t.ID}); !in[k] {
			in[k] = true
			p.ensureQueueIDs()
			p.order = append(p.order, len(p.queue))
			p.queue = append(p.queue, t)
			p.nextQueueID++
			p.queueIDs = append(p.queueIDs, p.nextQueueID)
		}
	}
}

// shuffleOrder puts the order in a random one with first at its head, or
// with any at its head when first is below 0.
func (p *Player) shuffleOrder(first int) {
	rand.Shuffle(len(p.order), func(i, j int) { p.order[i], p.order[j] = p.order[j], p.order[i] })
	for i, v := range p.order {
		if v == first {
			p.order[0], p.order[i] = p.order[i], p.order[0]
		}
	}
}

func (p *Player) ToggleShuffle() {
	p.commitTransitions()
	p.cancelTransitionPreparation()
	p.shuffle = !p.shuffle
	if len(p.order) == 0 {
		return
	}
	cur := p.order[max(p.at, 0)]
	if p.shuffle {
		p.shuffleOrder(cur)
		p.at = 0
	} else {
		for i := range p.order {
			p.order[i] = i
		}
		p.at = cur
	}
	p.prepareTransition()
}

func (p *Player) CycleRepeat() {
	p.commitTransitions()
	p.cancelTransitionPreparation()
	p.repeat = (p.repeat + 1) % 3
	p.prepareTransition()
}

// Current returns the track playing or loading, or nil.
func (p *Player) Current() *music.Track { return p.track }

// Queue returns the tracks queued, in the order they were given.
func (p *Player) Queue() []music.Track { return p.queue }

// Playing reports what the listener wants: false while paused.
func (p *Player) Playing() bool { return p.playing }

// Remote reports whether the current track is played by a remote source.
func (p *Player) Remote() bool {
	if p.remote != nil {
		return true
	}
	if p.track != nil {
		if src := p.sourceOf(p.track); src != nil && src.Info().Playback == music.PlaybackRemote {
			return true
		}
	}
	return false
}

// StopAfterTrack reports whether playback will pause at the end of the current track.
func (p *Player) StopAfterTrack() bool { return p.stopAfterTrack }

// SetStopAfterTrack enables or disables pausing at the end of the current track.
func (p *Player) SetStopAfterTrack(stop bool) {
	if p.stopAfterTrack == stop {
		return
	}
	p.stopAfterTrack = stop
	if stop {
		// 阻止已准备或即将开始的交叉淡化把下一首提前混入。
		p.cancelTransitionPreparation()
	} else {
		p.prepareTransition()
	}
}

// PauseForSleep pauses playback for the sleep timer, keeping local pause fade
// settings and calling onRemoteFail if a remote pause command fails.
func (p *Player) PauseForSleep(onRemoteFail func(error)) {
	if !p.playing {
		return
	}
	if p.remote != nil {
		if p.remote.started {
			p.remotePause(onRemoteFail)
			return
		}
		p.sleepRemoteFail = onRemoteFail
		p.playing = false
		p.sync()
		return
	}
	if p.out == nil && !p.loading {
		// 电台续播等待期间没有激活输出，直接停播而不能重走开播分支。
		p.playing = false
		p.awaiting = false
		p.sync()
		return
	}
	p.TogglePlay()
}

// Loading reports whether the current track is still loading.
// Loading reports whether the current track is on its way: loading, or
// waiting for the next of its bytes, after a seek past what has come in.
func (p *Player) Loading() bool {
	return p.loading || p.awaiting || p.watch != nil && p.watch.starved()
}

// Ready reports whether the current track has loaded, to play.
func (p *Player) Ready() bool { return p.out != nil || p.remote != nil && p.remote.started }

// Err returns why the current track did not play, or "".
func (p *Player) Err() string { return p.err }

// Stream returns what the current track streams, once loaded, or nil.
func (p *Player) Stream() *music.Stream { return p.info }

func (p *Player) Shuffle() bool      { return p.shuffle }
func (p *Player) Repeat() RepeatMode { return p.repeat }

// Lyrics returns the current track's lyrics, and whether they have loaded.
func (p *Player) Lyrics() ([]lyrics.Line, bool) { return p.lyrics, p.lyricsLoaded }

// LyricsSource 返回当前有效歌词，不暴露解析器内部的可变切片。
func (p *Player) LyricsSource() (*music.Lyrics, bool) {
	if p.lyricsSource == nil {
		return nil, p.lyricsLoaded
	}
	source := *p.lyricsSource
	source.Lines = append([]music.LyricLine(nil), source.Lines...)
	for i := range source.Lines {
		source.Lines[i].Words = append([]music.LyricWord(nil), source.Lines[i].Words...)
	}
	return &source, p.lyricsLoaded
}

// 只重载歌词，不重启音频或改变播放位置。
func (p *Player) ReloadLyrics() {
	if p.track == nil {
		return
	}
	p.lyrics, p.lyricsSource, p.lyricsLoaded = nil, nil, false
	p.loadLyrics(p.gen, p.sourceOf(p.track), *p.track)
}

// Downloaded returns the share of the current track downloaded so far, or
// false before it has started to.
func (p *Player) Downloaded() (float64, bool) {
	if p.stream == nil {
		return 0, false
	}
	dl, _ := p.stream.source()
	return dl.Progress(), true
}

// PlayOrder returns the queue's indices in play order, and where the
// current track is among them, -1 for none.
func (p *Player) PlayOrder() ([]int, int) { return p.order, p.at }

func (p *Player) Next() {
	p.commitTransitions()
	p.next()
}

func (p *Player) next() {
	if len(p.order) == 0 {
		return
	}
	at := p.at + 1
	if at >= len(p.order) {
		if p.feed != nil {
			p.stopForFeed()
			p.awaitFeed()
			return
		}
		at = 0
	}
	p.loadSkipping(at, 0, forward, true, 0)
}

// Prev plays the track before this one, however far this one has played:
// once a few seconds in, going back to its start only kept the listener
// pressing twice.
func (p *Player) Prev() {
	p.commitTransitions()
	if len(p.order) == 0 {
		return
	}
	at := p.at - 1
	if at < 0 {
		at = len(p.order) - 1
	}
	// One that can't play is passed going back, as going forward.
	p.loadSkipping(at, 0, back, true, 0)
}

// Jump plays the queue's track i, the listener's pick: one that can't play
// stays, saying why.
func (p *Player) Jump(i int) {
	p.commitTransitions()
	for k, v := range p.order {
		if v == i {
			p.loadSkipping(k, 0, stay, true, 0)
			return
		}
	}
}

// Reload reapplies source settings without starting a new song playback.
func (p *Player) Reload() {
	p.commitTransitions()
	if p.track == nil || p.at < 0 || p.at >= len(p.order) {
		return
	}
	if p.playback != nil {
		// 重载是用户要求的新解析，不是新一遍播放；已用机会保留，旧恢复截止不继承。
		p.playback.dir, p.playback.skipped = stay, 0
		p.playback.recovery = nil
		if p.playback.completed {
			// 末尾没有可准备的首帧；释放旧音频但保留结束位置，等明确重播再解析。
			from := p.Position()
			p.stop()
			p.pending = from
			return
		}
	}
	p.loadPlayback(p.at, 0, stay, p.playing, p.Position(), p.playback)
}

// TogglePlay pauses, fading out, or plays, fading in.
func (p *Player) TogglePlay() {
	p.commitTransitions()
	if p.remote != nil && p.remote.started {
		p.remoteToggle()
		return
	}
	if p.out == nil {
		switch {
		case p.loading:
			if p.loadCancel != nil && p.playing {
				// 暂停是取消本轮，不是一次来源失败；保留本项和恢复位置。
				p.loadCancel()
				p.loadCancel = nil
				p.gen++
				p.loading, p.playing = false, false
				p.feedback = PlaybackFeedback{}
				p.sync()
				return
			}
			// It plays once loaded, or not.
			p.playing = !p.playing
			p.sync()
		case len(p.order) > 0:
			from := p.pending
			// 手动续播沿用已用机会，只有真实正常结束后的明确重播才从头开始新一遍。
			if p.playback != nil {
				if p.playback.completed {
					p.playback = &playbackAttempt{dir: stay, policy: p.playback.policy}
					from = 0
				} else {
					p.playback.dir, p.playback.skipped = stay, 0
					p.playback.recovery = nil
				}
			}
			p.loadPlayback(max(p.at, 0), 0, stay, true, from, p.playback)
		}
		return
	}
	out := p.out
	if p.playing {
		if p.transition != nil {
			p.transition.setAllowed(false)
			if !p.transition.startedID(p.transitionID) {
				position := p.Position()
				p.loadPlayback(p.at, 0, stay, false, position, p.playback)
				return
			}
		}
		// 暂停立即隔离旧检测回调；续播不能继承暂停期间累计的停滞时间。
		if p.monitorCancel != nil {
			p.monitorCancel()
			p.monitorCancel = nil
		}
		p.playing = false
		p.fade.fade(0, p.pauseFadeDuration(), func() {
			p.post(func() {
				if p.out == out && !p.playing {
					out.Pause()
					p.sync()
				}
			})
		})
		p.sync()
		return
	}
	// 正常播完后从头播放属于新一遍；普通暂停续播仍保留已用的恢复机会。
	if p.playback != nil {
		if p.playback.completed {
			if p.transition != nil {
				p.loadSkipping(p.at, 0, stay, true, 0)
				return
			}
			p.playback = &playbackAttempt{dir: stay, policy: p.playback.policy}
			out.Seek(0)
		} else {
			p.playback.dir, p.playback.skipped = stay, 0
			p.playback.recovery = nil
		}
	}
	p.playing = true
	if p.transition != nil {
		p.transition.setAllowed(true)
	} else {
		p.record()
	}
	if !p.seeking && !out.Playing() {
		out.Play()
	}
	if p.playback != nil && p.playback.policy.candidate > 0 {
		p.monitorDownload(p.gen, out, p.stream)
	}
	p.fade.fade(1, p.playFadeDuration(), nil)
	p.prepareTransition()
	p.sync()
}

func (p *Player) PauseForPreview() {
	if p.playing {
		p.TogglePlay()
	}
	if !p.playing && p.out != nil {
		// 试听紧接着起音，不能等常规淡出，也不能让待完成的定位重新起播。
		p.fade.attach(p.out, 0)
		p.out.Pause()
	}
}

// seekGap is how long a seek waits for what plays to fade out, which the
// audio player does over 5 ms, as it pauses: within the next buffer or
// two it sends the system.
const seekGap = 30 * time.Millisecond

// Seek moves to t. Playing, it pauses and plays again around the jump,
// which the audio player fades over a few milliseconds, rather than
// cutting from one sound to the other with a click.
func (p *Player) Seek(t time.Duration) {
	p.commitTransitions()
	if p.playback != nil {
		// 定位延续本次播放；过渡路径也必须先清除完成标记，避免续播又回到曲首。
		p.playback.completed = false
	}
	if p.transition != nil {
		// 网络解码可能正等待数据；通过原加载路径重新定位，先取消旧流以唤醒等待线程。
		p.loadPlayback(p.at, 0, stay, p.playing, t, p.playback)
		return
	}
	if p.remote != nil && p.remote.started {
		p.remoteSeek(t)
		return
	}
	if p.out == nil {
		p.pending = t
		return
	}
	out := p.out
	// One past what has come in waits for it: a frame once that would show.
	p.after(seekGap+starvedAfter+20*time.Millisecond, func() { p.post(func() {}) })
	// Paused, it jumps at once; but a seek on its way paused it only for
	// the jump, and this one takes its place, playing on from there.
	if !out.Playing() && !p.seeking {
		out.Seek(t)
		p.sync()
		return
	}
	p.seekGen++
	gen := p.seekGen
	p.seeking, p.seekTo = true, t
	out.Pause()
	time.AfterFunc(seekGap, func() {
		p.post(func() {
			if gen != p.seekGen || p.out != out {
				return
			}
			p.seeking = false
			out.Seek(t)
			if p.playing || !p.fade.silent() {
				out.Play()
			}
			p.sync()
			p.prepareTransition()
		})
	})
	p.sync()
}

func (p *Player) SetVolume(v float64) {
	p.volume = v
	p.applyGain()
	if p.remote != nil {
		p.remoteVolume(v)
	}
}

// applyGain sets the volume of what plays: the listener's, which the ear
// hears in powers, and the track's gain, if normalizing.
func (p *Player) applyGain() {
	g := p.volume * p.volume
	if p.transition != nil {
		gain := 1.0
		if p.Normalize != nil && p.Normalize() {
			gain = p.gain
		}
		p.transition.setGain(p.transitionID, gain)
		p.fade.setGain(g)
		return
	}
	if p.Normalize != nil && p.Normalize() {
		g *= p.gain
	}
	p.fade.setGain(g)
}

// normPreamp lifts normalized tracks, which their gains bring to the quiet
// loudness of ReplayGain, nearer to how loud music plays elsewhere.
const normPreamp = 4

// normGain is the gain that brings a track as loud as others, as its source
// measured it, but never so loud that its peaks would clip.
func normGain(st *music.Stream) float64 {
	if st == nil || st.Gain == 0 && st.Peak == 0 {
		return 1
	}
	g := math.Pow(10, (st.Gain+normPreamp)/20)
	if st.Peak > 0 {
		g = min(g, 1/st.Peak)
	}
	return g
}

func (p *Player) Position() time.Duration {
	switch {
	case p.remote != nil && p.remote.started:
		return p.remotePosition()
	case p.out == nil:
		return p.pending
	case p.seeking:
		return p.seekTo
	}
	position := p.out.Position()
	if p.transition != nil {
		position -= time.Duration(p.transitionStart) * time.Second / 48000
		frame := int64(math.Round(position.Seconds() * 48000))
		position = time.Duration(p.transition.sourceFrameOf(p.transitionID, max(frame, 0))) * time.Second / 48000
		position += p.transitionOffset
	}
	return max(position, 0)
}

func (p *Player) Duration() time.Duration {
	switch {
	case p.track == nil:
		return 0
	case p.remote != nil && p.remote.duration > 0:
		return p.remote.duration
	}
	if p.transition != nil {
		if frames := p.transition.lengthOf(p.transitionID); frames >= 0 {
			// 恢复流从中途开始，但进度条总长仍属于已提交的完整歌曲。
			return time.Duration(frames)*time.Second/48000 + p.transitionOffset
		}
	}
	if p.track.Duration > 0 || p.out == nil {
		return p.track.Length()
	}
	return p.out.Duration() // a source that didn't say
}

// Levels returns the spectrum of what plays now.
func (p *Player) Levels() ([Bands]float32, bool) {
	if p.stream == nil || p.out == nil || !p.playing {
		return [Bands]float32{}, false
	}
	_, spec := p.stream.source()
	frame := int64(p.Position().Seconds() * float64(p.stream.SampleRate()))
	return spec.at(frame)
}

func (p *Player) stop() {
	// 停止不需要重建未听到的缓冲，直接关闭输出先唤醒任何等待网络的解码。
	if p.transition != nil {
		p.transition.Close()
	}
	if p.transitionCancel != nil {
		p.transitionCancel()
		p.transitionCancel = nil
	}
	if p.transitionNext != nil && p.transitionNext.cancel != nil {
		p.transitionNext.cancel()
	}
	p.transitionNext = nil
	if p.transitionDone != nil {
		close(p.transitionDone)
		p.transitionDone = nil
	}
	if p.transition != nil {
		p.transition = nil
		p.transitionStart = 0
		p.transitionID = 0
		p.transitionOffset = 0
	}
	if p.monitorCancel != nil {
		p.monitorCancel()
		p.monitorCancel = nil
	}
	if p.loadCancel != nil {
		p.loadCancel()
		p.loadCancel = nil
	}
	if out := p.out; out != nil {
		// Paused first, it fades out over a few milliseconds, rather than
		// stopping with a click.
		out.Pause()
		time.AfterFunc(100*time.Millisecond, out.Close)
	}
	p.out, p.stream, p.watch, p.info, p.remote = nil, nil, nil, nil, nil
	p.seeking = false
	p.fade.attach(nil, 1)
}

// Which way a load goes past a track that can't play: on to the next, back to
// the one before, or nowhere, staying on it to say why.
const (
	forward = 1
	back    = -1
	stay    = 0
)

// load plays the track at order[at], skipping those that cannot play.
func (p *Player) load(at int) {
	p.loadSkipping(at, 0, forward, true, 0)
}

// stopForFeed stops what plays, at the end of a radio's queue, while its feed
// gives more.
func (p *Player) stopForFeed() {
	if was := p.remote; was != nil && was.started && p.playing {
		p.command(was.src, was.src.RemotePause)
	}
	p.stop()
}

// sourceOf returns the source of track t, or nil if it's gone.
func (p *Player) sourceOf(t *music.Track) music.Source {
	if p.Source == nil {
		return nil
	}
	return p.Source(t.Source)
}

// record tells the source of the current track, if it wants to know, that it
// plays, once for each time it's played.
func (p *Player) record() {
	if p.track == nil || !p.playing {
		return
	}
	if p.playback != nil && p.playback.recorded {
		return
	}
	src, ok := p.sourceOf(p.track).(music.Recorder)
	key := p.track.Source + "\x00" + p.track.ID
	if !ok || key == p.recorded {
		return
	}
	p.recorded = key
	if p.playback != nil {
		p.playback.recorded = true
	}
	t := *p.track
	go src.Played(t)
}

// loadSkipping loads the track at order[at], to play or not, from from,
// skipping those that cannot play the way dir goes: skipped of them so far.
func (p *Player) loadSkipping(at, skipped, dir int, play bool, from time.Duration) {
	p.loadPlayback(at, skipped, dir, play, from, nil)
}

func (p *Player) loadPlayback(at, skipped, dir int, play bool, from time.Duration, playback *playbackAttempt) {
	if playback == nil {
		playback = &playbackAttempt{dir: dir, skipped: skipped}
	}
	t := p.queue[p.order[at]]
	src := p.sourceOf(&t)
	if was := p.remote; was != nil && was.started && p.playing && (src == nil || src.Info().ID != was.src.Info().ID) {
		// Whatever plays next isn't its to play.
		p.command(was.src, was.src.RemotePause)
	}
	p.stop()
	p.sleepRemoteFail = nil
	p.gen++
	gen := p.gen
	p.playback = playback
	p.at = at
	p.track = &t
	p.loading, p.playing, p.err = true, play, ""
	p.feedback = PlaybackFeedback{Stage: "resolving"}
	if playback.recovery != nil {
		p.feedback.Stage = "recovering"
	}
	p.setLiveFeedback(gen, p.feedback.Stage, 0)
	p.awaiting = false
	p.lyrics, p.lyricsSource, p.lyricsLoaded = nil, nil, false
	p.pending = from
	if !playback.recorded {
		p.recorded = ""
	}
	p.announce(&t, nil)
	p.refill()

	p.loadCover(gen, &t)
	p.loadLyrics(gen, src, t)
	if src != nil && src.Info().Playback == music.PlaybackRemote {
		p.feedback.Stage = "remote"
		p.loadRemote(gen, src, at, skipped, dir, play, from)
		return
	}
	quality := music.Qualities[len(music.Qualities)/2]
	if p.Quality != nil {
		quality = p.Quality()
	}
	pre := p.takeAhead(t, quality)
	go func() {
		var info *music.Stream
		var s stream
		var err error
		info, s, err = p.openWithFeedback(src, t, quality, pre, func() {
			p.setLiveStage(gen, "buffering")
		})
			settings := p.transitionSettings()
			if err == nil && p.Transitions != nil && settings.Crossfade {
				s, err = trimSilenceStream(s)
			}
			if err == nil && p.Transitions != nil {
				trimmedFrom := max(from-silenceTrimOffset(s), 0)
				var prepared stream
				prepared, err = prepareStream(context.Background(), s, trimmedFrom)
				if err == nil {
					s = prepared
				} else {
					s.Close()
				}
			}
		p.post(func() {
			if gen != p.gen {
				if s != nil {
					s.Close()
				}
				return
			}
			p.loading = false
			if err != nil {
				p.loadFailed(t.Source, err, skipped, dir)
				return
			}
			if err := p.startLoaded(context.Background(), gen, s, info, quality, nil); err != nil {
				s.Close()
				p.stop()
				p.loadFailed(t.Source, err, skipped, dir)
			}
		})
	}()
}

// startLoaded 只在主线程安装已核验的流；协作调用在原截止内等待此处执行。
func (p *Player) startLoaded(ctx context.Context, gen int, s stream, info *music.Stream, quality string, handoff func(func() error) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.stream, p.info = s, info
	p.gain = normGain(info)
	watch := &watched{stream: s, now: p.now, ended: func() { p.post(func() {}) }, failed: make(chan error, 1)}
	p.watch = watch
	from := p.pending
	trimOffset := silenceTrimOffset(s)
	trimmedFrom := max(from-trimOffset, 0)
	var decoder audio.Decoder = watch
	if p.Transitions != nil {
		// 定位先于输出构造；原生输出会立即预读，不能让未激活的恢复流先返回 EOF。
		positioned := newTransitionDecoder(watch)
		if trimmedFrom > 0 {
			frame := int64(trimmedFrom) * 48000 / int64(time.Second)
			if prepared, ok := s.(*preparedStream); ok {
				frame = prepared.frame * 48000 / int64(prepared.SampleRate())
			}
			if failure := positioned.SetPosition(frame); failure != nil {
				return failure
			}
			positioned.origin = frame
		}
		buffer := newTransitionBuffer(positioned)
		p.transition = newTransitionOutput(buffer)
		p.transitionID, p.transitionStart = 0, 0
		p.transitionOffset = max(from, trimOffset)
		decoder = p.transition
	}
	factory := p.newOutput
	if factory == nil {
		factory = func(decoder audio.Decoder) audioOutput { return audio.NewPlayer(decoder) }
	}
	out := factory(decoder)
	p.out = out
	defer func() {
		if err != nil {
			// 只撤回本候选，不能调用 stop 取消整轮；下一来源仍使用原预算和定位。
			out.Close()
			if p.out == out {
				p.out, p.stream, p.watch, p.info = nil, nil, nil, nil
				p.pending = from
				p.fade.attach(nil, 1)
			}
		}
	}()
	// From its middle, as a session picked up again, it fades in.
	level := 1.0
	if from > 0 || p.Transitions != nil && p.playFadeDuration() > 0 {
		level = 0
	}
	p.fade.attach(out, level)
	p.applyGain()
	if err := ctx.Err(); err != nil {
		return err
	}
	out.OnEnd(func() {
		p.post(func() {
			if gen == p.gen && p.out == out {
				if p.transition != nil {
					p.commitTransitions()
					return
				}
				if handoff != nil && watch.failure() != nil {
					// 由监控统一固定检测快照，结束回调不能另起一套时间规则。
					return
				}
				p.ended()
			}
		})
	})
	if from > 0 && p.transition == nil {
		position := from
		if prepared, ok := s.(*preparedStream); ok {
			// 准备阶段向下取整到样本；交接使用同一帧，不能四舍五入后重新估算定位。
			position = time.Duration(prepared.frame) * time.Second / time.Duration(prepared.SampleRate())
		}
		out.Seek(position)
	}
	// 主线程已进入回调也不代表及时开播，设备准备与定位后仍须重查原截止。
	if err := ctx.Err(); err != nil {
		return err
	}
	start := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.playing {
			out.Play()
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		return nil
	}
	if handoff != nil {
		err = handoff(start)
	} else {
		err = start()
	}
	if err != nil {
		return err
	}
	if p.playing {
		if p.playback != nil {
			// 成功开播截断连续失败链，但同一次播放的恢复机会不能因此返还。
			p.playback.skipped = 0
			p.playback.failed = nil
		}
		p.fade.fade(1, p.playFadeDuration(), nil)
		if p.transition == nil {
			p.record()
		}
	}
	p.pending = 0
	p.sync()
	if p.transition != nil {
		p.watchTransitions(gen, out)
	}
	p.getAhead(quality)
	p.prepareTransition()
	return nil
}

// loadCover shows the system the cover of t, once it has it.
func (p *Player) loadCover(gen int, t *music.Track) {
	expected := p.playback
	url := t.Album.ImageURL
	if url == "" {
		return
	}
	go func() {
		// The system shows it small, as Now Playing does.
		cover, err := music.Fetch(music.SizedImage(url, 600))
		if err == nil {
			p.post(func() {
				if gen == p.gen && p.playback == expected {
					p.announce(t, cover)
				}
			})
		}
	}()
}

// loadLyrics asks the source of t for its lyrics, if it has any.
func (p *Player) loadLyrics(gen int, src music.Source, t music.Track) {
	expected := p.playback
	p.lyricsGeneration++
	revision := p.lyricsGeneration
	var override *music.Lyrics
	if p.LyricsOverride != nil {
		override = p.LyricsOverride(t)
	}
	go func() {
		var lines []lyrics.Line
		var source *music.Lyrics
		if override != nil {
			source = override
			lines = lyrics.FromSource(source)
		}
		if source == nil && src != nil && src.Can("lyrics") {
			if l, err := src.Lyrics(t.ID); err == nil && l != nil {
				source = l
				lines = lyrics.FromSource(l)
			}
		}
		p.post(func() {
			if gen == p.gen && p.playback == expected && revision == p.lyricsGeneration {
				p.lyrics, p.lyricsSource = lines, source
				p.lyricsLoaded = true
				if p.Changed != nil {
					p.Changed()
				}
			}
		})
	}()
}

// loadFailed says why the current track can't play, and skips it the way
// dir goes, unless it stays, nothing in the queue can play, or the listener has
// to do something first, which would stop the next track too.
func (p *Player) loadFailed(source string, err error, skipped, dir int) {
	p.setFailure(source, err)
	if errors.Is(err, music.ErrSignIn) || errors.Is(err, music.ErrSetup) {
		p.playing = false
		if p.Failed != nil {
			p.Failed(source, err)
		}
		p.sync()
		return
	}
	limit := len(p.order)
	if dir != stay && skipped+1 < limit {
		failed := p.playback.failed
		if failed == nil {
			failed = make(map[int]bool)
			p.playback.failed = failed
		}
		failed[p.order[p.at]] = true
		// 重排只改变顺序，不开始新的失败区间；跳过已失败位置，但保留重复入队的独立位置。
		for step := 1; step < len(p.order); step++ {
			next := (p.at + dir*step + len(p.order)) % len(p.order)
			if !failed[p.order[next]] {
				attempt := &playbackAttempt{dir: dir, skipped: skipped + 1, failed: failed}
				p.loadPlayback(next, skipped+1, dir, p.playing, 0, attempt)
				return
			}
		}
	}
	p.playing = false
	p.sync()
}

// errorText says what went wrong with a track, in the listener's terms where
// the source's error is one the app knows.
func errorText(err error) string {
	switch {
	case errors.Is(err, errNoSource):
		return i18n.T("The plugin of this song is gone")
	case errors.Is(err, music.ErrUnavailable):
		return i18n.T("This song isn't available")
	case errors.Is(err, music.ErrSignIn):
		return i18n.T("Sign in to play this song")
	case errors.Is(err, context.DeadlineExceeded):
		return i18n.T("Playback timed out; try again")
	case errors.Is(err, music.ErrSetup):
		if reason := music.Reason(err); reason != "" {
			return reason
		}
		return i18n.T("The plugin needs setting up")
	}
	return err.Error()
}

var errNoSource = errors.New("player: no such source")

// open opens track t's audio at quality: what was asked for ahead of it, if
// that worked; or kept in the cache, or else a file on disk, played from
// there, or else streamed into the cache, and kept once it has all come in.
// What failed ahead is asked for again: it may have been a moment's.
func (p *Player) open(src music.Source, t music.Track, quality string, pre *ahead) (*music.Stream, stream, error) {
	return p.openWithFeedback(src, t, quality, pre, nil)
}

func (p *Player) openWithFeedback(src music.Source, t music.Track, quality string, pre *ahead, buffering func()) (*music.Stream, stream, error) {
	if src == nil {
		if pre != nil {
			pre.close()
		}
		return nil, nil, errNoSource
	}
	var dl *download
	var info *music.Stream
	ok := false
	if pre != nil {
		<-pre.done
		if pre.err == nil {
			info, dl, ok = pre.info, pre.dl, true
			if dl == nil {
				path, _ := music.PathOf(info.URL)
				var err error
				if dl, err = openFile(path); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	if !ok {
		dl, info, ok = p.Cache.lookup(t.Source, t.ID, quality)
	}
	if !ok {
		var err error
		info, err = src.Stream(t.ID, quality)
		if err != nil {
			return nil, nil, err
		}
		if info == nil {
			return nil, nil, music.ErrUnavailable
		}
		if path, isFile := music.PathOf(info.URL); isFile {
			dl, err = openFile(path)
			if err != nil {
				return nil, nil, err
			}
		} else {
			dl = p.Cache.download(t.Source, t.ID, quality, info)
		}
	}
	if buffering != nil {
		buffering()
	}
	s, err := openStream(dl, t.Duration)
	if err != nil {
		dl.Close()
		return nil, nil, err
	}
	return info, s, nil
}

// openFile opens a song on disk as a download that has all of it in.
func openFile(path string) (*download, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return opened(f, info.Size()), nil
}

// Session is where playback was left, to pick up again.
type Session struct {
	Queue    []music.Track `json:"queue"`
	Order    []int         `json:"order"`
	At       int           `json:"at"`
	Shuffle  bool          `json:"shuffle"`
	Repeat   RepeatMode    `json:"repeat"`
	Position int64         `json:"position"` // milliseconds
	Radio    string        `json:"radio,omitempty"`
}

// Session returns where playback is, or false with nothing queued.
func (p *Player) Session() (Session, bool) {
	if p.track == nil || p.at < 0 {
		return Session{}, false
	}
	return Session{
		Queue:    p.queue,
		Order:    p.order,
		At:       p.at,
		Shuffle:  p.shuffle,
		Repeat:   p.repeat,
		Position: p.Position().Milliseconds(),
		Radio:    p.radio,
	}, true
}

// Restore picks up a session where it was left, paused, and loads its
// track to play at once when asked.
func (p *Player) Restore(s Session) {
	n := len(s.Queue)
	if n == 0 || len(s.Order) != n || s.At < 0 || s.At >= n {
		return
	}
	seen := make([]bool, n)
	for _, i := range s.Order {
		if i < 0 || i >= n || seen[i] {
			return
		}
		seen[i] = true
	}
	p.queue, p.order = s.Queue, s.Order
	p.resetQueueIDs()
	p.shuffle, p.repeat = s.Shuffle, s.Repeat%3
	p.loadSkipping(s.At, 0, stay, false, time.Duration(s.Position)*time.Millisecond)
}

// Audio is the current track's sound, opened apart from what plays by OpenAudio:
// interleaved stereo at its rate, read as it downloads.
type Audio interface {
	SampleRate() int
	Channels() int
	Read(dst []float32) (int, error)
	// SetPosition goes to a frame, of SampleRate a second.
	SetPosition(frame int64) error
	Close() error
}

// OpenAudio opens the current track's sound again, for decoding apart from what
// plays, as for transcribing an episode. It reads the same download, waiting for
// what has not come in, and fails once the track changes, or for a track that
// plays elsewhere. It works on any goroutine once opened; close it when done.
func (p *Player) OpenAudio() (Audio, error) {
	if p.stream == nil || p.track == nil {
		return nil, errors.New("player: nothing loaded")
	}
	dl, _ := p.stream.source()
	return openShared(dl, p.track.Duration)
}
