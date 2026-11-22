package app

import (
	"fmt"
	"image"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

// 无头查看品牌区在单音源、多音源和深浅主题下与搜索及导航的布局。
func TestSidebarBrandFrames(t *testing.T) {
	directory := framesDir(t)
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(i18n.English)
	for _, multiple := range []bool{false, true} {
		for _, dark := range []bool{false, true} {
			application := newTestApp()
			fake(application).info.Name = "汽水音乐"
			application.account = &music.Account{ID: "preview", Name: "音乐朋友"}
			tracks := framesTracks(application)
			application.home = &music.Browse{Sections: []music.Section{{Title: "继续听", Tracks: tracks}}}
			application.mine = []music.Playlist{{ID: "10", Name: "我喜欢的音乐", ImageURL: "cover:0", Mine: true}}
			if multiple {
				provider, _ := addOther(application)
				provider.info.Name = "本地音乐"
			}
			tester := ui.NewTester(application.view, 1180, 780)
			tester.SetDark(dark)
			tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			settled(tester)
			appearance, sources := "light", "single"
			if dark {
				appearance = "dark"
			}
			if multiple {
				sources = "multiple"
			}
			name := fmt.Sprintf("sidebar-brand-%s-%s", appearance, sources)
			frame := tester.Image()
			saveFrame(t, directory, name+".png", frame)
			saveFrame(t, directory, name+"-detail.png", frame.SubImage(image.Rect(0, 0, sidebarWidth, 310)))
		}
	}
}
