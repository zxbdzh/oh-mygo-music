package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

// 偏好列表与添加行的帧图验收，仅本机验收用。
func TestAIPrefsFrameLocal(t *testing.T) {
	directory := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	a := newTestApp()
	a.settingsPage = "ai"
	a.aiPrefs.add(Preference{Source: "fake", Account: "", Text: "周杰伦慢歌适合睡前", PlaylistID: "10", PlaylistName: "睡前"})
	a.aiPrefs.add(Preference{Source: "fake", Account: "", Text: "通勤要节奏快一点的", PlaylistID: "11", PlaylistName: "通勤"})
	a.mine = []music.Playlist{{ID: "10", Name: "睡前", Mine: true}, {ID: "11", Name: "通勤", Mine: true}}
	tester := ui.NewTester(a.settingsView, 780, 640)
	settled(tester)
	tester.Scroll(480, 400, 0, -600)
	settled(tester)
	saveFrame(t, directory, "ai-prefs.png", tester.Image())
}
