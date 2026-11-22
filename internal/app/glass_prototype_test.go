package app

import (
	"fmt"
	"image"
	"image/draw"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 浅色主题下律动控制栏保留深色背景，避免白色面板遮住白色控件。
func TestStageLightThemeBackdropCoversControls(t *testing.T) {
	a, tester, _ := playingApp()
	track := a.pl.Current()
	track.Album.ImageURL = "stage-cover"
	giveCover(a, track.Album.ImageURL, coverImage(ui.RGB(25, 92, 132), ui.RGB(210, 164, 82)))
	a.nowPlaying, a.npPane = true, paneStage
	tester.SetDark(false)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tester)
	frame := tester.Image()
	bounds := frame.Bounds()
	x := bounds.Min.X + bounds.Dx()*3/4
	red, green, blue, _ := frame.At(x, bounds.Min.Y+bounds.Dy()-70).RGBA()
	aboveRed, aboveGreen, aboveBlue, _ := frame.At(x, bounds.Min.Y+bounds.Dy()-140).RGBA()
	if red+green+blue > aboveRed+aboveGreen+aboveBlue+3*20*257 {
		t.Fatalf("stage controls are covered by a light surface: %04x %04x %04x", red, green, blue)
	}
}

// 无头截图覆盖桌面外壳、歌词播放页和紧凑窗口，封面可用本机图片替换。
func TestDesktopAtmosphereFrames(t *testing.T) {
	dir := framesDir(t)
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(i18n.English)
	cover := coverImage(ui.RGB(45, 102, 115), ui.RGB(174, 196, 164))
	if path := os.Getenv("MM_FRAME_COVER"); path != "" {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		decoded, _, err := image.Decode(file)
		if err != nil {
			t.Fatal(err)
		}
		cover = image.NewRGBA(decoded.Bounds())
		draw.Draw(cover, cover.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	}
	for _, size := range []struct {
		name          string
		width, height int
	}{{"desktop", 1280, 840}, {"compact", 860, 620}} {
		for _, dark := range []bool{false, true} {
			a := newTestApp()
			a.account = &music.Account{ID: "1", Name: "Alex"}
			tracks := framesTracks(a)
			tracks[0].Name = "山海之间"
			tracks[0].Artists = []music.Artist{{ID: "1", Name: "听见远方"}}
			for _, url := range []string{"cover:0", "cover:1", "cover:2"} {
				giveCover(a, url, cover)
			}
			a.mine = []music.Playlist{{ID: "10", Name: "山海之间", ImageURL: "cover:0", Mine: true}}
			a.playlists["10"] = &music.Playlist{
				ID: "10", Name: "山海之间", Owner: "Alex", Description: "让旋律陪你走过山海。", ImageURL: "cover:0", Tracks: tracks,
			}
			a.home = &music.Browse{Sections: []music.Section{{Title: "Recently Played", Tracks: tracks}}}
			a.route = route{page: pagePlaylist, src: "fake", id: "10"}
			a.pl.Cue(tracks)
			a.pl.SetLyrics(lyrics.Build(
				"[00:04]风经过山谷\n[00:09]把远方轻轻唤醒\n[00:14]我们沿着光的方向\n[00:19]走进更辽阔的天地\n[00:24]让每一次心跳\n[00:29]都成为旅途的回声", ""))
			a.pl.Hold(16 * time.Second)
			tester := ui.NewTester(a.view, size.width, size.height)
			tester.SetDark(dark)
			tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			appearance := "light"
			if dark {
				appearance = "dark"
			}
			settled(tester)
			tester.Move(float32(size.width-80), 45)
			settled(tester)
			saveFrame(t, dir, fmt.Sprintf("%s-%s-library.png", size.name, appearance), tester.Image())
			a.route.page = pageHome
			settled(tester)
			saveFrame(t, dir, fmt.Sprintf("%s-%s-home.png", size.name, appearance), tester.Image())
			a.route.page = pagePlaylist
			settled(tester)
			a.nowPlaying, a.npPane = true, paneLyrics
			settled(tester)
			saveFrame(t, dir, fmt.Sprintf("%s-%s-player.png", size.name, appearance), tester.Image())
			a.npPane = paneStage
			settled(tester)
			saveFrame(t, dir, fmt.Sprintf("%s-%s-stage.png", size.name, appearance), tester.Image())
			if dark {
				a.npPane = paneLyrics
				a.pl.SetLyrics(nil)
				settled(tester)
				saveFrame(t, dir, fmt.Sprintf("%s-%s-no-lyrics.png", size.name, appearance), tester.Image())
			}
		}
	}
}
