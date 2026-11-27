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
	a.aiPrefs.add(Preference{Source: "fake", Account: "", Text: "这首歌比较 EMO，适合放到情绪歌单", TrackID: "1", TrackName: "Say Goodbye", Artists: "LODONI", Album: "Say Goodbye", CoverURL: "preference-cover", PlaylistID: "10", PlaylistName: "EMO歌"})
	a.aiPrefs.add(Preference{Source: "fake", Account: "", Text: "通勤要节奏快一点的", PlaylistID: "11", PlaylistName: "通勤"})
	giveCover(a, "preference-cover", coverImage(ui.RGB(40, 90, 130), ui.RGB(180, 150, 110)))
	a.mine = []music.Playlist{{ID: "10", Name: "睡前", Mine: true}, {ID: "11", Name: "通勤", Mine: true}}
	tester := ui.NewTester(a.settingsView, 780, 640)
	settled(tester)
	tester.Scroll(480, 400, 0, 1200)
	settled(tester)
	saveFrame(t, directory, "ai-prefs.png", tester.Image())
}
