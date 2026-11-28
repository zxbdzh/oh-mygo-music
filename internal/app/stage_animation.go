package app

import (
	"maps"
	"math/rand/v2"
	"slices"
	"time"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"

	"github.com/egoist/mygo/ui"
)

const (
	stageFixed  = "fixed"
	stageRandom = "random"
	stageTempo  = "tempo"
)

var stageTempoDefaults = [4]string{"vinyl", "52hz", "ripple", "jump"}

type stageAnimationSettings struct {
	Mode           string            `json:"mode,omitempty"`
	Pool           []string          `json:"pool"`
	Speed          *[4]string        `json:"speed,omitempty"`
	Songs          map[string]string `json:"songs,omitempty"`
	Random         map[string]string `json:"random,omitempty"`
	Spectrum       bool              `json:"spectrum,omitempty"`
	RushBackground string            `json:"rushBackground,omitempty"`
	RushRhythm     string            `json:"rushRhythm,omitempty"`
}

func (s stageAnimationSettings) mode() string {
	switch s.Mode {
	case stageRandom, stageTempo:
		return s.Mode
	default:
		return stageFixed
	}
}

func (s stageAnimationSettings) pool() []string {
	if s.Pool != nil {
		return s.Pool
	}
	pool := make([]string, 0, len(stageStyles))
	for _, style := range stageStyles {
		pool = append(pool, style.id)
	}
	return pool
}

func (s stageAnimationSettings) speed() [4]string {
	if s.Speed == nil {
		return stageTempoDefaults
	}
	return *s.Speed
}

func validStageStyle(id string) bool {
	return slices.ContainsFunc(stageStyles, func(s stageStyleSpec) bool { return s.id == id })
}

func stageStyleOrClassic(id string) string {
	if validStageStyle(id) {
		return id
	}
	return "classic"
}

func stageBPMBand(bpm float64) int {
	switch {
	case bpm < 90:
		return 0
	case bpm < 120:
		return 1
	case bpm < 150:
		return 2
	default:
		return 3
	}
}

// 已抽中的结果仍在池中时优先沿用。只有新抽取时才避开上一首。
func stageRandomStyle(s stageAnimationSettings, key, previous string, pick func(int) int) string {
	pool := s.pool()
	if style, ok := s.Random[key]; ok && validStageStyle(style) && slices.Contains(pool, style) {
		return style
	}
	var choices []string
	for _, style := range pool {
		if validStageStyle(style) && !slices.Contains(choices, style) {
			choices = append(choices, style)
		}
	}
	if len(choices) > 1 {
		choices = slices.DeleteFunc(choices, func(s string) bool { return s == previous })
	}
	if len(choices) == 0 {
		return "classic"
	}
	return choices[pick(len(choices))]
}

type stageDirector struct {
	key               string
	playback          uint64
	tempoRevision     uint64
	mode              string
	selected          string
	previous          string
	nextPoll          time.Duration
	candidate         string
	pending, switched bool
}

func (d *stageDirector) style(s settings, key string) string {
	if chosen, ok := s.StageAnimation.Songs[key]; ok {
		return stageStyleOrClassic(chosen)
	}
	if d.key == key && d.mode == s.StageAnimation.mode() && d.mode != stageFixed {
		return d.selected
	}
	return stageStyleOrClassic(s.StageStyle)
}

// 唱到一半不切画面。接近下一句时也留出时间，避免刚切换便重新排版。
func stageTempoCanSwitch(lines []lyrics.Line, pos time.Duration) bool {
	if len(lines) == 0 {
		return true
	}
	index := lyrics.Index(lines, pos)
	return (index == -1 || pos >= lines[index].End) &&
		(index+1 >= len(lines) || lines[index+1].At-pos >= 350*time.Millisecond)
}

func (a *app) updateStageDirector(c *ui.Context, tr *music.Track, force bool) {
	d := &a.stageDirector
	s := &a.settings.StageAnimation
	key, playback, mode := a.lyricKey(tr), a.pl.StagePlaybackID(), s.mode()
	changed := d.key != key || d.playback != playback
	if changed || d.mode != mode || force {
		previous := d.previous
		if changed && d.key != "" {
			previous = d.style(a.settings, d.key)
		}
		*d = stageDirector{key: key, playback: playback, mode: mode, previous: previous,
			selected: stageStyleOrClassic(a.settings.StageStyle), nextPoll: 4 * time.Second}
		if mode == stageRandom {
			_, pinned := s.Songs[key]
			if !pinned {
				d.selected = stageRandomStyle(*s, key, previous, rand.IntN)
			}
			prior, recorded := s.Random[key]
			if !pinned && (!recorded || prior != d.selected) && slices.Contains(s.pool(), d.selected) {
				// 只有真正使用的随机结果才入库；单曲指定时不提前抽签。
				if s.Random == nil {
					s.Random = make(map[string]string)
				}
				s.Random[key] = d.selected
				if err := a.settings.save(); err != nil {
					a.toast = T("Could not save display preferences")
				}
			}
		}
	}
	if _, pinned := s.Songs[key]; pinned || mode != stageTempo {
		return
	}
	if a.pl.Remote() {
		d.selected = stageStyleOrClassic(a.settings.StageStyle)
		d.pending, d.switched, d.candidate = false, false, ""
		d.nextPoll = 4 * time.Second
		return
	}
	if d.switched {
		return
	}
	pos, revision := a.pl.Position(), a.pl.StageTempoRevision()
	scrubbing := a.npScrub.held || a.barScrub.held
	if d.tempoRevision != revision || scrubbing {
		// 定位会丢弃还未使用的节奏；已生效的样式仍保持到本次播放结束。
		d.tempoRevision, d.pending, d.candidate = revision, false, ""
		d.nextPoll = max(4*time.Second, pos)
	}
	if scrubbing || !a.pl.Playing() && !force {
		return
	}
	if pos >= 4*time.Second && (pos >= d.nextPoll || force) {
		bpm, confidence := a.pl.StageTempo()
		d.nextPoll = pos + time.Second
		if bpm > 0 && confidence >= .5 {
			d.candidate = stageStyleOrClassic(s.speed()[stageBPMBand(bpm)])
			d.pending = true
		}
	}
	if d.pending {
		lines, loaded := a.pl.Lyrics()
		if force || (a.pl.Playing() && !a.npScrub.held && loaded && stageTempoCanSwitch(lines, pos)) {
			d.selected, d.switched, d.pending = d.candidate, true, false
		}
	}
	if c != nil && a.pl.Playing() && !d.switched && !a.pl.Remote() {
		if d.pending {
			c.After(180 * time.Millisecond)
		} else {
			c.After(max(100*time.Millisecond, min(time.Second, d.nextPoll-pos)))
		}
	}
}

func stageKeepRandom(s *stageAnimationSettings) {
	s.Random = maps.Clone(s.Random)
	pool := s.pool()
	maps.DeleteFunc(s.Random, func(_ string, style string) bool { return !slices.Contains(pool, style) })
}
