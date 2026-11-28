package app

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 悬停让玻璃胶囊亮边；按下与松开后动画结束，不再持续请求帧。
func TestStageGlassPillsShowHoverAndPressFeedbackThenSettle(t *testing.T) {
	a, tester, _ := playingApp()
	a.pl.SetLyrics(lyrics.Build("[00:01]风经过山谷\n[00:09]把远方轻轻唤醒", ""))
	a.nowPlaying, a.npPane = true, paneStage
	settled(tester)
	for _, label := range []string{"Stage Style", "Stage Language"} {
		hoverChangesGlass(t, tester, label)
		pressThenSettle(t, tester, label)
	}
	if count := framesAt(a, tester, 0); count > 1 {
		t.Fatalf("idle stage glass controls requested %d frames per second after hover/press", count)
	}
}

// 减少动效时玻璃直接跳到终态：悬停仍可见，按压后也不再持续重绘。
func TestStageGlassPillFeedbackUnderReduceMotion(t *testing.T) {
	a, tester, _ := playingApp()
	a.pl.SetLyrics(lyrics.Build("[00:01]风经过山谷", ""))
	a.nowPlaying, a.npPane = true, paneStage
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tester)
	hoverChangesGlass(t, tester, "Stage Style")
	pressThenSettle(t, tester, "Stage Style")
	if count := framesAt(a, tester, 0); count > 1 {
		t.Fatalf("reduced-motion stage glass requested %d frames per second at rest", count)
	}
}

// 减少透明度下玻璃走不透明绘制：悬停不再有亮边，但按压仍靠外扩可见。
func TestStageGlassPillPressUnderReduceTransparency(t *testing.T) {
	a, tester, _ := playingApp()
	a.pl.SetLyrics(lyrics.Build("[00:01]风经过山谷", ""))
	a.nowPlaying, a.npPane = true, paneStage
	tester.SetPreferences(ui.Preferences{ReduceTransparency: true})
	settled(tester)
	pressThenSettle(t, tester, "Stage Style")
	if count := framesAt(a, tester, 0); count > 1 {
		t.Fatalf("opaque stage glass requested %d frames per second at rest", count)
	}
}

// hoverChangesGlass moves the pointer onto a glass control from rest and checks
// the material itself responds: efab7b1 replaced the hover colour with a
// Background that Material paints over, so the glass stopped responding and only
// the label's own colour changed. The strip compared holds no text, so a pass
// cannot come from the label turning white.
func hoverChangesGlass(t *testing.T, tester *ui.Tester, label string) {
	t.Helper()
	tester.Move(4, 4)
	settled(tester)
	rest := cloneRGBA(tester.Image())
	box, ok := tester.Find(T(label))
	if !ok {
		t.Fatalf("%s glass control was not rendered", label)
	}
	tester.Move(box.X+box.W/2, box.Y+box.H/2)
	settled(tester)
	// A hover highlight changes much of the strip; a tooltip or a caret moves a
	// few pixels, so a bare "something changed" would pass without the glass.
	if changed := changedPixels(rest, tester.Image(), glassOnly(box)); changed < 40 {
		t.Fatalf("hovering %s changed only %d pixels of glass, so the material did not respond", label, changed)
	}
}

// glassOnly is a vertical strip inside a pill's left padding: glass, no glyphs.
func glassOnly(box ui.Rect) ui.Rect {
	return ui.Rect{X: box.X + 2, Y: box.Y + 2, W: 6, H: box.H - 4}
}

// pressThenSettle presses a control and checks it answers: these pills open
// their menu on press, and the menu covers the material, so the glass growth is
// not what is observed here. The check is that the press reaches the control
// and that nothing keeps animating afterwards.
func pressThenSettle(t *testing.T, tester *ui.Tester, label string) {
	t.Helper()
	box, ok := tester.Find(T(label))
	if !ok {
		t.Fatalf("%s glass control was not rendered", label)
	}
	tester.Move(box.X+box.W/2, box.Y+box.H/2)
	settled(tester)
	tester.Press(box.X+box.W/2, box.Y+box.H/2)
	tester.Frame()
	if len(tester.Menu()) == 0 {
		t.Fatalf("pressing %s opened no menu, so the control did not take the press", label)
	}
	tester.Release(box.X+box.W/2, box.Y+box.H/2)
	tester.CloseMenu()
	settled(tester)
	if tester.FramePending() {
		t.Fatalf("%s kept asking for frames after the press ended", label)
	}
}

func cloneRGBA(source *image.RGBA) *image.RGBA {
	copy := image.NewRGBA(source.Bounds())
	copy.Pix = append(copy.Pix[:0], source.Pix...)
	return copy
}

func imagesDifferWithin(before, after *image.RGBA, box ui.Rect) bool {
	return changedPixels(before, after, box) != 0
}

// changedPixels counts the pixels that differ inside box, so a check can tell a
// real material change from a few pixels of noise.
func changedPixels(before, after *image.RGBA, box ui.Rect) int {
	if before.Bounds() != after.Bounds() {
		return -1
	}
	region := image.Rect(int(box.X), int(box.Y), int(box.X+box.W), int(box.Y+box.H)).Intersect(before.Bounds())
	changed := 0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if before.RGBAAt(x, y) != after.RGBAAt(x, y) {
				changed++
			}
		}
	}
	return changed
}

func TestStageLanguageUsesTranslationAndSurvivesRestart(t *testing.T) {
	makeView := func(a *app) *ui.Tester {
		a.pl.Cue([]music.Track{{ID: "stage-language", Source: "fake", Name: "Language choices"}})
		a.pl.SetLyrics(lyrics.Build("[00:01]風が通り過ぎる\n[00:09]次の景色", "[00:01]讓風慢慢經過\n[00:09]我們一起出發"))
		a.pl.Hold(2 * time.Second)
		a.nowPlaying, a.npPane = true, paneStage
		tester := ui.NewTester(a.view, 1200, 800)
		tester.SetPreferences(ui.Preferences{ReduceMotion: true})
		tester.Move(600, 400)
		settled(tester)
		return tester
	}
	a := newTestApp()
	a.settings.path = filepath.Join(t.TempDir(), "settings.json")
	a.settings.LyricsScript = "simplified"
	a.settings.HidePlaybackTranslation, a.settings.HideCardTranslation = true, true
	tester := makeView(a)
	source, _ := a.pl.Lyrics()
	expected := lyrics.Build("[00:01]風が通り過ぎる\n[00:09]次の景色", "[00:01]讓風慢慢經過\n[00:09]我們一起出發")
	if err := tester.Click("Stage Language"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Translated Lyrics"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	visible := strings.Join(tester.Texts(), "")
	if !strings.Contains(visible, "让") || !strings.Contains(visible, "经") || strings.Contains(visible, "が") || !tester.HasText("我们一起出发") {
		t.Fatalf("translated mode did not replace the primary lyrics: %q", tester.Texts())
	}
	if err := tester.Click("Lyrics Display"); err == nil {
		t.Fatal("stage still opens the general lyric display settings")
	}
	if !a.settings.HidePlaybackTranslation || !a.settings.HideCardTranslation {
		t.Fatal("stage language changed another page's translation choice")
	}
	if !reflect.DeepEqual(source, expected) || a.pl.Position() != 2*time.Second || a.pl.Current().ID != "stage-language" {
		t.Fatal("changing stage language modified the source or playback")
	}
	data, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newTestApp()
	if err := json.Unmarshal(data, &restarted.settings); err != nil {
		t.Fatal(err)
	}
	shown := makeView(restarted)
	visible = strings.Join(shown.Texts(), "")
	if !strings.Contains(visible, "让") || !strings.Contains(visible, "经") || strings.Contains(visible, "が") || !shown.HasText("我们一起出发") {
		t.Fatalf("restart lost the stage language: %q", shown.Texts())
	}
	for _, choice := range []string{"Original Lyrics", "Bilingual"} {
		if err := shown.Click("Stage Language"); err != nil {
			t.Fatal(err)
		}
		if err := shown.ChooseMenuItem(choice); err != nil {
			t.Fatal(err)
		}
		shown.Frame()
		visible = strings.Join(shown.Texts(), "")
		if !strings.Contains(visible, "が") || shown.HasText("让风慢慢经过") != (choice == "Bilingual") {
			t.Fatalf("language %s did not update primary and translated lyrics: %q", choice, shown.Texts())
		}
	}
	if err := shown.Click("Lyrics"); err != nil {
		t.Fatal(err)
	}
	shown.Frame()
	if err := shown.Click("Lyrics Display"); err != nil {
		t.Fatal("normal lyrics lost their own display settings")
	}
	shown.CloseMenu()
	if shown.HasText("让风慢慢经过") {
		t.Fatal("stage choice changed the normal lyrics translation preference")
	}
}

// 译文仅借用原行时间生成显示动效，缺译行回退原词，所有模式都不覆盖来源的逐字时间。
func TestStageLanguageKeepsSourceTimingAndFallsBack(t *testing.T) {
	a := newTestApp()
	track := &music.Track{ID: "timing", Source: "fake"}
	source := []lyrics.Line{
		{At: time.Second, End: 5 * time.Second, Text: "hello world", Translation: "我们一起出发",
			Words: []lyrics.Word{{Text: "hello ", Start: time.Second, End: 1200 * time.Millisecond}, {Text: "world", Start: 1200 * time.Millisecond, End: 5 * time.Second}}},
		{At: 10 * time.Second, End: 13 * time.Second, Text: "original fallback", Translation: " \t ",
			Words: []lyrics.Word{{Text: "original fallback", Start: 10 * time.Second, End: 13 * time.Second}}},
	}
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	a.settings.HidePlaybackTranslation = true
	a.settings.StageLanguage = "translation"
	shown := a.stageLyrics(track, source)
	if shown[0].Text != "我们一起出发" || shown[0].Translation != "" || shown[0].At != time.Second || shown[0].End != 5*time.Second {
		t.Fatalf("translation changed its line interval: %+v", shown[0])
	}
	var text strings.Builder
	for _, word := range shown[0].Words {
		text.WriteString(word.Text)
		if word.Start < time.Second || word.End > 5*time.Second || word.End < word.Start {
			t.Fatalf("translated animation escaped the source line: %+v", word)
		}
	}
	if text.String() != "我们一起出发" || len(shown[0].Words) == 0 || shown[0].Words[0].Start != time.Second {
		t.Fatalf("translation inherited the original word text or timing: %+v", shown[0].Words)
	}
	if shown[1].Text != source[1].Text || shown[1].Translation != "" || !reflect.DeepEqual(shown[1].Words, source[1].Words) {
		t.Fatalf("missing translation did not fall back: %+v", shown[1])
	}
	a.settings.StageLanguage = "original"
	shown = a.stageLyrics(track, source)
	if shown[0].Text != source[0].Text || shown[0].Translation != "" || !reflect.DeepEqual(shown[0].Words, source[0].Words) {
		t.Fatalf("original mode did not restore the source words: %+v", shown[0])
	}
	a.settings.StageLanguage = ""
	shown = a.stageLyrics(track, source)
	if shown[0].Text != source[0].Text || shown[0].Translation != source[0].Translation {
		t.Fatal("bilingual mode followed the normal page's hidden translation")
	}
	after, err := json.Marshal(source)
	if err != nil || string(before) != string(after) {
		t.Fatal("language selection mutated the source lyrics")
	}
}

// 导出语言选择和各律动样式的译文帧，人工检查排版而不固定像素快照。
func TestStageLanguageFrames(t *testing.T) {
	directory := framesDir(t)
	a, tester, _ := playingApp()
	a.pl.Current().Name = "春の風 · Spring Breeze"
	a.pl.SetLyrics(lyrics.Build("[00:01]風が通り過ぎる\n[00:09]次の景色", "[00:01]让风慢慢经过\n[00:09]我们一起出发"))
	a.pl.Hold(4 * time.Second)
	a.nowPlaying, a.npPane = true, paneStage
	tester.SetDark(true)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Move(600, 400)
	settled(tester)
	for _, language := range []string{"original", "translation", ""} {
		a.settings.StageLanguage = language
		tester.Frame()
		name := language
		if name == "" {
			name = "bilingual"
		}
		saveFrame(t, directory, "stage-language-"+name+".png", tester.Image())
	}
	a.settings.StageLanguage = "translation"
	for _, style := range []string{"partita", "tilt", "spotlight", "wave"} {
		a.settings.StageStyle = style
		tester.Frame()
		saveFrame(t, directory, "stage-translation-"+style+".png", tester.Image())
	}
	a.settings.StageStyle = "spotlight"
	compact := ui.NewTester(a.view, 900, 640)
	compact.SetDark(true)
	compact.SetPreferences(ui.Preferences{ReduceMotion: true})
	compact.Move(450, 320)
	settled(compact)
	saveFrame(t, directory, "stage-language-compact.png", compact.Image())
}
