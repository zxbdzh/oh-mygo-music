package app

import (
	"testing"
	"time"

	"mygo-music/internal/lyrics"
)

// 重新播放沿用有效的随机结果，移出随机池后重新选择，新抽取避开上一首。
func TestStageRandomSelection(t *testing.T) {
	s := stageAnimationSettings{Pool: []string{"jump", "rush", "classic"}, Random: map[string]string{"song": "rush"}}
	pick := func(int) int { return 0 }
	if got := stageRandomStyle(s, "song", "rush", pick); got != "rush" {
		t.Fatalf("remembered style = %q", got)
	}
	s.Pool = []string{"jump", "classic"}
	stageKeepRandom(&s)
	if _, ok := s.Random["song"]; ok {
		t.Fatal("removed result was still remembered")
	}
	if got := stageRandomStyle(s, "song", "", pick); got != "jump" {
		t.Fatalf("new style repeated previous: %q", got)
	}
	s.Pool = []string{"spotlight"}
	if got := stageRandomStyle(s, "song", "", pick); got != "classic" {
		t.Fatalf("a pool of withdrawn styles chose %q", got)
	}
	s.Pool = []string{}
	if got := stageRandomStyle(s, "song", "rush", pick); got != "classic" {
		t.Fatalf("empty pool chose %q", got)
	}
}

// 速度档按半开区间分配，测速得到结果后也不能在正在唱的歌词中切换。
func TestStageTempoBandAndSwitchWindow(t *testing.T) {
	for _, c := range []struct {
		bpm  float64
		band int
	}{{89.99, 0}, {90, 1}, {119.99, 1}, {120, 2}, {149.99, 2}, {150, 3}} {
		if got := stageBPMBand(c.bpm); got != c.band {
			t.Errorf("%v BPM: band %d, want %d", c.bpm, got, c.band)
		}
	}
	lines := []lyrics.Line{{At: 2 * time.Second, End: 4 * time.Second}, {At: 5 * time.Second, End: 7 * time.Second}}
	for _, c := range []struct {
		ms      int
		allowed bool
	}{{1000, true}, {1900, false}, {2200, false}, {4200, true}, {4800, false}, {8000, true}} {
		if got := stageTempoCanSwitch(lines, time.Duration(c.ms)*time.Millisecond); got != c.allowed {
			t.Errorf("position %d ms: allowed %v, want %v", c.ms, got, c.allowed)
		}
	}
}

// 拖进度后不能在新位置应用之前等候的速度判定，已生效的样式则保留。
func TestStageTempoSeekDropsPendingStyle(t *testing.T) {
	a, _, _ := playingApp()
	a.settings.StageStyle = "marquee"
	a.settings.StageAnimation.Mode = stageTempo
	tr := a.pl.Current()
	a.updateStageDirector(nil, tr, false)
	d := &a.stageDirector
	d.candidate, d.pending = "jump", true
	d.nextPoll = 30 * time.Second
	a.pl.Seek(6 * time.Second)
	a.updateStageDirector(nil, tr, false)
	if d.pending || d.selected != "marquee" || d.nextPoll != 7*time.Second {
		t.Fatalf("seek left stale tempo state: %+v", d)
	}
	d.selected, d.switched = "jump", true
	a.pl.Seek(7 * time.Second)
	a.updateStageDirector(nil, tr, false)
	if !d.switched || d.selected != "jump" {
		t.Fatalf("seek changed an already applied style: %+v", d)
	}
}

// 单曲指定优先；已下架的旧样式 ID 回落 Classic，其他歌不能继承上一首的自动样式。
func TestStageDirectorStylePriority(t *testing.T) {
	s := defaultSettings()
	s.StageStyle = "marquee"
	s.StageAnimation = stageAnimationSettings{Mode: stageTempo, Songs: map[string]string{"song": "spotlight"}}
	d := stageDirector{key: "song", mode: stageTempo, selected: "jump", switched: true}
	if d.style(s, "song") != "classic" || d.style(s, "next") != "marquee" {
		t.Fatal("pinned or new track picked an unrelated style")
	}
	delete(s.StageAnimation.Songs, "song")
	if got := d.style(s, "song"); got != "jump" {
		t.Fatalf("automatic style = %q", got)
	}
}
