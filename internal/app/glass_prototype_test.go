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

func playlistWithPlayingTrack(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	return playlistFrame(t, true)
}

func playlistFrame(t *testing.T, playing bool) (*app, *ui.Tester) {
	t.Helper()
	a := newTestApp()
	a.account = &music.Account{ID: "1", Name: "Alex"}
	tracks := framesTracks(a)
	a.mine = []music.Playlist{{ID: "10", Name: "Polar Night", ImageURL: "cover:0", Mine: true}}
	a.playlists["10"] = &music.Playlist{
		ID: "10", Name: "Polar Night", Owner: "Alex", ImageURL: "cover:0", Mine: true, Tracks: tracks,
	}
	a.route = route{page: pagePlaylist, src: "fake", id: "10"}
	if playing {
		a.pl.Cue([]music.Track{tracks[0]})
		a.pl.Hold(10 * time.Second)
	}
	return a, ui.NewTester(a.view, 1180, 780)
}

// 歌单页在有当前歌曲时显示封面氛围，并保持内容层的浅色可读性。
func TestPlaylistAmbientBackdropFrame(t *testing.T) {
	for _, dark := range []bool{false, true} {
		_, plainTester := playlistFrame(t, false)
		plainTester.SetDark(dark)
		settled(plainTester)
		plain := samplePlaylistBackdrop(plainTester.Image())

		_, tester := playlistWithPlayingTrack(t)
		tester.SetDark(dark)
		settled(tester)
		ambient := samplePlaylistBackdrop(tester.Image())
		if colorSpread(ambient) <= colorSpread(plain)+1000000 {
			t.Fatalf("playlist background has no visible cover tint (dark=%t): plain=%v ambient=%v", dark, plain, ambient)
		}
		if dir := os.Getenv("MM_FRAMES"); dir != "" {
			name := "playlist-ambient-light.png"
			if dark {
				name = "playlist-ambient-dark.png"
			}
			saveFrame(t, dir, name, tester.Image())
		}
	}
}

func samplePlaylistBackdrop(img image.Image) [3]uint64 {
	var red, green, blue uint64
	// 只取内容右侧留白，避免首页封面卡片的取色和动画污染背景覆盖检查。
	for y := 120; y < 245; y++ {
		for x := 1150; x < 1170; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			red += uint64(r)
			green += uint64(g)
			blue += uint64(b)
		}
	}
	return [3]uint64{red, green, blue}
}

func colorSpread(color [3]uint64) uint64 {
	high, low := color[0], color[0]
	for _, value := range color[1:] {
		high, low = max(high, value), min(low, value)
	}
	return high - low
}

// 返回首页时仍保留当前歌曲的封面氛围，导航不能改变播放队列。
func TestAmbientBackdropFollowsNavigation(t *testing.T) {
	a, tester := playlistWithPlayingTrack(t)
	a.home = &music.Browse{Sections: []music.Section{{Title: "Recently Played", Tracks: a.pl.Queue()}}}
	settled(tester)
	track := *a.pl.Current()
	if err := tester.Click("Home"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if a.route.page != pageHome {
		t.Fatalf("navigation stayed on page %v", a.route.page)
	}
	withCover := samplePlaylistBackdrop(tester.Image())
	a.pl.Current().Album.ImageURL = ""
	settled(tester)
	withoutCover := samplePlaylistBackdrop(tester.Image())
	if colorSpread(withCover) <= colorSpread(withoutCover)+1000000 {
		t.Fatalf("navigation lost the current cover background: with=%v without=%v", withCover, withoutCover)
	}
	if current := a.pl.Current(); current.ID != track.ID || len(a.pl.Queue()) != 1 {
		t.Fatal("navigation changed the playing track or queue")
	}
}

// 播放页切换明暗主题时更新背景，同时保留曲目、队列和播放位置。
func TestImmersiveAppearanceFollowsTheme(t *testing.T) {
	a, tester := playlistWithPlayingTrack(t)
	a.nowPlaying, a.npPane = true, paneLyrics
	a.pl.SetLyrics(nil)
	tester.SetDark(true)
	settled(tester)
	dark := samplePlaylistBackdrop(tester.Image())
	tester.SetDark(false)
	settled(tester)
	light := samplePlaylistBackdrop(tester.Image())
	if light[0]+light[1]+light[2] <= dark[0]+dark[1]+dark[2]+20000000 {
		t.Fatalf("light player kept its dark background: light=%v dark=%v", light, dark)
	}
	if a.pl.Current().ID != "1" || len(a.pl.Queue()) != 1 || a.pl.Position() != 10*time.Second {
		t.Fatal("changing appearance reset playback")
	}
}

// 无头截图覆盖桌面外壳、沉浸歌词和紧凑窗口，封面可用本机图片替换。
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
			if dark {
				a.pl.SetLyrics(nil)
				tester.Frame()
				saveFrame(t, dir, fmt.Sprintf("%s-%s-no-lyrics.png", size.name, appearance), tester.Image())
			}
		}
	}
}
