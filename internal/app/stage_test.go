package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
	"mygo-music/internal/player"
)

// 从面板切换样式和强度后，保存重载仍保留选择、火花和原歌词时间。
func TestStageChoicesSurviveRestart(t *testing.T) {
	a, tester, _ := playingApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.settings.StageSparks = true
	a.nowPlaying, a.npPane = true, paneStage
	tester.Move(600, 400)
	tester.Advance(time.Second)
	stageTestClick(t, tester, "Stage Style", "Rush", "Gentle", "Save and Apply")
	if a.settings.StageStyle != "rush" || a.settings.StageIntensity != "gentle" {
		t.Fatal("save did not apply the draft")
	}
	stageTestClick(t, tester, "Stage Style", "52 Hz", "Balanced", "Save and Apply")
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newTestApp()
	if err := json.Unmarshal(data, &restarted.settings); err != nil {
		t.Fatal(err)
	}
	if restarted.settings.StageStyle != "52hz" || !restarted.settings.StageSparks {
		t.Fatalf("lost choices after restart: %+v", restarted.settings)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["stageIntensity"] != "standard" {
		t.Fatalf("lost intensity: %s", data)
	}
	if lines, _ := a.pl.Lyrics(); lines[1].At != 10*time.Second || lines[1].Words[0].Start != 10*time.Second {
		t.Fatal("display choices changed lyric timing")
	}
}

// 已下架的旧样式 ID 从设置加载后按 Classic 播放。
func TestWithdrawnStylesFallBackToClassic(t *testing.T) {
	tr := &music.Track{ID: "withdrawn", Source: "fake"}
	for _, style := range []string{"", "partita", "tilt", "spotlight", "wave"} {
		a := newTestApp()
		a.settings.StageStyle = style
		if got := a.animationStyle(tr); got != "classic" {
			t.Errorf("saved style %q played as %q", style, got)
		}
	}
}

// 减少动态效果时只在当前或下一句真实歌词边界后重绘。
func TestStageBoundaryWaitTracksLyrics(t *testing.T) {
	lines := []lyrics.Line{
		{At: time.Second, End: 5 * time.Second, Words: []lyrics.Word{{Start: time.Second, End: 2 * time.Second}, {Start: 3 * time.Second, End: 4 * time.Second}}},
		{At: 10 * time.Second, End: 14 * time.Second, Words: []lyrics.Word{{Start: 10 * time.Second, End: 12 * time.Second}}},
	}
	if got := stageBoundaryWait(lines, 0, 2500*time.Millisecond); got != 520*time.Millisecond {
		t.Fatalf("wait %s, want next word at 3s", got)
	}
	if got := stageBoundaryWait(lines, 0, 5*time.Second); got != 1020*time.Millisecond {
		t.Fatalf("wait %s, want bounded idle wait", got)
	}
}

func stageTestClick(t *testing.T, tt *ui.Tester, labels ...string) {
	t.Helper()
	for _, label := range labels {
		if err := tt.Click(T(label)); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		tt.Frame()
	}
}

// 选卡片只改草稿。Escape 后继续编辑，取消后把焦点交回入口。
func TestStagePanelDraftAndFocus(t *testing.T) {
	a, tt, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneStage
	tt.Move(600, 400)
	tt.Advance(time.Second)
	stageTestClick(t, tt, "Stage Style", "Rush")
	if a.settings.StageStyle != "" || a.stagePanel.draft.fixed != "rush" {
		t.Fatal("card selection leaked from the draft")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if !a.stagePanel.open || !a.stagePanel.discard || !a.nowPlaying {
		t.Fatal("Escape did not keep the changed draft in the stage")
	}
	stageTestClick(t, tt, "Continue Editing", "Cancel")
	if a.stagePanel.open || a.settings.StageStyle != "" || !tt.Focused(T("Stage Style")) {
		t.Fatal("closing the panel did not discard the draft and restore focus")
	}
}

// 面板中 Space 只点选当前卡片，保存失败时能保留草稿重试。
func TestStagePanelKeyboardAndFailedSave(t *testing.T) {
	a, tt, _ := playingApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	if err := os.Mkdir(a.settings.path, 0700); err != nil {
		t.Fatal(err)
	}
	a.nowPlaying, a.npPane = true, paneStage
	tt.Move(600, 400)
	tt.Advance(time.Second)
	stageTestClick(t, tt, "Stage Style", "Random", "Rush")
	playing := a.pl.Playing()
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if !slices.Contains(a.stagePanel.draft.pool, "rush") || a.pl.Playing() != playing {
		t.Fatal("Space did not toggle the card independently of playback")
	}
	stageTestClick(t, tt, "Sparks", "Save and Apply")
	if !a.stagePanel.open || a.stagePanel.err == "" || a.settings.StageSparks || !a.stagePanel.draft.sparks {
		t.Fatal("save failure did not preserve the draft or the previous settings")
	}
}

// 单曲指定立即生效，取消后恢复全局设置，面板草稿可独立放弃。
func TestStagePanelPin(t *testing.T) {
	a, tt, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneStage
	tt.Move(600, 400)
	tt.Advance(time.Second)
	stageTestClick(t, tt, "Stage Style", "Classic Lyrics", "Set for This Song…", "Rush")
	tr := a.pl.Current()
	if a.animationStyle(tr) != "rush" || a.stagePanel.draft.fixed != "classic" || a.settings.StageStyle != "" {
		t.Fatal("song override did not apply independently of the draft")
	}
	stageTestClick(t, tt, "Clear Song Style", "Cancel")
	if a.animationStyle(tr) != "classic" {
		t.Fatal("clearing song override did not restore fixed style")
	}
}

// 速度档保存后独立于固定样式；没有可分析的音频时仍用固定样式。
func TestStagePanelTempoFallback(t *testing.T) {
	a, tt, _ := playingApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.settings.StageStyle = "marquee"
	a.nowPlaying, a.npPane = true, paneStage
	tt.Move(600, 400)
	tt.Advance(time.Second)
	stageTestClick(t, tt, "Stage Style", "By Tempo", "120–149 BPM", "Rush", "Save and Apply")
	a.pl.Hold(20 * time.Second)
	tt.Frame()
	if a.stagePanel.open || a.animationStyle(a.pl.Current()) != "marquee" {
		t.Fatal("tempo without audio should fall back to the fixed style")
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	var stored settings
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if want := ([4]string{"vinyl", "52hz", "rush", "jump"}); stored.StageAnimation.mode() != stageTempo || stored.StageAnimation.speed() != want || stored.StageStyle != "marquee" {
		t.Fatalf("saving one tempo band changed other choices: %s", data)
	}
}

// 换歌后结束上首歌的指定操作，避免点中的卡片误写到下一首。
func TestStagePanelPinCancelsOnTrackChange(t *testing.T) {
	a, tt, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneStage
	tt.Move(600, 400)
	tt.Advance(time.Second)
	stageTestClick(t, tt, "Stage Style", "Set for This Song…")
	a.pl.Cue([]music.Track{song("new", "下一首")})
	tt.Frame()
	stageTestClick(t, tt, "Rush")
	if !a.stagePanel.open || a.stagePanel.pinning || len(a.settings.StageAnimation.Songs) != 0 || a.stagePanel.draft.fixed != "rush" {
		t.Fatal("changing the track did not return the cards to draft editing")
	}
}

// Timed lines a source answers, each word with its time, reach the stage: the
// words being sung there, the line's translation and the line next under them.
func TestTimedLyricsReachTheStage(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	a.pl = player.New(a.post)
	a.pl.Source = a.sourceByID
	fake(a).lyrics = func(string) (*music.Lyrics, error) {
		return &music.Lyrics{Lines: []music.LyricLine{
			{Start: 1000, End: 3000, Translation: "你好世界", Words: []music.LyricWord{{Start: 1000, End: 2000, Text: "hello "}, {Start: 2000, End: 3000, Text: "world"}}},
			{Start: 4000, End: 6000, Text: "the next line"},
		}}, nil
	}
	tr := song("7", "宝船")
	tr.Source = "fake"
	a.pl.PlayQueue([]music.Track{tr}, 0)
	until(t, ch, func() bool { _, loaded := a.pl.Lyrics(); return loaded })
	if lines, _ := a.pl.Lyrics(); len(lines) != 2 || lines[0].Text != "hello world" {
		t.Fatalf("lines %+v", lines)
	}
	original, loaded := a.pl.LyricsSource()
	if !loaded || original == nil || original.Lines[0].Words[0].Start != 1000 || original.Lines[0].Words[1].End != 3000 {
		t.Fatalf("original lyrics did not preserve word times: %+v", original)
	}

	a.pl.Hold(2500 * time.Millisecond)
	// 舞台把歌词画成字形，不回无障碍文本；交给舞台的显示行仍要保留逐词时间和译文。
	lines, _ := a.pl.Lyrics()
	display := a.stageLyrics(a.pl.Current(), lines)
	if len(display) != 2 || display[0].Text != "hello world" || display[0].Translation != "你好世界" {
		t.Fatalf("stage display %+v", display)
	}
}
