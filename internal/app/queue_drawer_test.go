package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

// 浏览时打开队列只覆盖右侧，不进入大播放页或重置当前播放。
func TestQueueDrawerKeepsBrowsing(t *testing.T) {
	a := newTestApp()
	tracks := []music.Track{song("1", "第一首"), song("2", "第二首")}
	a.playlists["10"] = &music.Playlist{ID: "10", Name: "测试歌单", Tracks: tracks}
	a.route = route{page: pagePlaylist, src: "fake", id: "10"}
	a.pl.Cue(tracks)
	a.pl.Hold(18 * time.Second)
	tester := ui.NewTester(a.view, 1180, 780)
	settled(tester)
	if err := tester.Click("Queue"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if a.nowPlaying || a.route.page != pagePlaylist || a.route.id != "10" {
		t.Fatalf("opening queue left browsing: nowPlaying=%t route=%+v", a.nowPlaying, a.route)
	}
	if _, ok := tester.Find("Close Queue"); !ok {
		t.Fatal("queue drawer has no close action")
	}
	if a.pl.Current().ID != "1" || a.pl.Position() != 18*time.Second || !a.pl.Playing() {
		t.Fatal("opening queue changed playback")
	}
}

// 截图覆盖常规与最小窗口、明暗主题、长标题和空队列，供无窗口目视验收。
func TestQueueDrawerFrames(t *testing.T) {
	directory := framesDir(t)
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(i18n.English)
	for _, size := range []struct {
		name          string
		width, height int
	}{{"desktop", 1180, 780}, {"compact", 880, 580}} {
		for _, dark := range []bool{false, true} {
			a := newTestApp()
			tracks := framesTracks(a)
			tracks[1].Name = "沿着山谷与海岸继续前行，直到遥远的灯光再一次亮起"
			a.pl.Cue(tracks)
			a.home = &music.Browse{Sections: []music.Section{{Title: "Recently Played", Tracks: tracks}}}
			tester := ui.NewTester(a.view, size.width, size.height)
			tester.SetDark(dark)
			tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			settled(tester)
			if err := tester.Click(T("Queue")); err != nil {
				t.Fatal(err)
			}
			settled(tester)
			saveFrame(t, directory, fmt.Sprintf("queue-%s-dark%t.png", size.name, dark), tester.Image())
			for len(a.pl.Queue()) > 0 {
				a.pl.RemoveQueued(a.pl.QueueEntryID(len(a.pl.Queue()) - 1))
			}
			settled(tester)
			saveFrame(t, directory, fmt.Sprintf("queue-empty-%s-dark%t.png", size.name, dark), tester.Image())
		}
	}
}

// 关闭侧栏后焦点返回队列入口，Escape 不改变浏览页或播放状态。
func TestQueueDrawerEscapeRestoresTriggerFocus(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("1", "第一首")})
	tester := ui.NewTester(a.view, 1180, 780)
	settled(tester)
	if err := tester.Click("Queue"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	tester.Key(0, ui.KeyEscape)
	settled(tester)
	if _, ok := tester.Find("Close Queue"); ok {
		t.Fatal("Escape left the drawer open")
	}
	tester.Key(0, ui.KeyEnter)
	settled(tester)
	if _, ok := tester.Find("Close Queue"); !ok {
		t.Fatal("Enter did not reopen the focused queue trigger")
	}
	if a.nowPlaying || a.pl.Playing() {
		t.Fatal("closing the drawer changed the page or playback")
	}
}

// 菜单打开时 Escape 先关闭最上层菜单，不能同时关闭待播侧栏。
func TestQueueDrawerEscapeClosesMenuFirst(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("1", "当前歌"), song("2", "队列中的歌曲")})
	// 无头环境用同框架的绘制菜单验证层级；真实原生菜单由系统接收 Escape。
	tester := ui.NewTester(func(c *ui.Context) {
		c.SetMenuPresentation(ui.PopoverMenus)
		a.view(c)
	}, 1180, 780)
	settled(tester)
	if err := tester.Click("Queue"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if err := tester.RightClick("队列中的歌曲"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(tester.Menu()) == 0 {
		t.Fatal("song menu did not open")
	}
	if _, ok := tester.Find("Close Queue"); !ok {
		t.Fatal("opening a song menu already closed the queue")
	}
	tester.Key(0, ui.KeyEscape)
	settled(tester)
	if _, ok := tester.Find("Close Queue"); !ok {
		t.Fatal("closing the song menu also closed the queue")
	}
	tester.Key(0, ui.KeyEscape)
	settled(tester)
	if _, ok := tester.Find("Close Queue"); ok {
		t.Fatal("second Escape did not close the queue")
	}
}

// 移除按钮只编辑指定位置，抽屉和全屏队列随即展示同一份剩余队列。
func TestQueueDrawerRemovalIsSharedWithNowPlaying(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("1", "第一首"), song("2", "第二首"), song("3", "第三首")})
	a.pl.Hold(18 * time.Second)
	tester := ui.NewTester(a.view, 1180, 780)
	settled(tester)
	if err := tester.Click("Queue"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if err := tester.Click("Remove from Queue: 第二首"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if len(a.pl.Queue()) != 2 || a.pl.Current().ID != "1" || a.pl.Position() != 18*time.Second || a.nowPlaying {
		t.Fatal("removing a queued item interrupted browsing or playback")
	}
	if err := tester.Click("Close Queue"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if err := tester.Click("Lyrics"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if err := tester.Click("Up Next"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if err := tester.Click("Remove from Queue: 第三首"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if len(a.pl.Queue()) != 1 || a.pl.Queue()[0].ID != "1" {
		t.Fatal("full player did not edit the drawer's queue")
	}
}

// 用户浏览远处待播项后自然切歌不能抢回位置，定位按钮可恢复跟随。
func TestQueueDrawerRespectsManualScrolling(t *testing.T) {
	a := newTestApp()
	tracks := make([]music.Track, 80)
	for index := range tracks {
		tracks[index] = song(fmt.Sprint(index), fmt.Sprintf("队列歌曲%02d", index))
	}
	a.pl.Cue(tracks)
	tester := ui.NewTester(a.view, 1180, 780)
	settled(tester)
	if err := tester.Click("Queue"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	bounds, ok := tester.Find("队列歌曲00")
	if !ok {
		t.Fatal("current row was not located on open")
	}
	tester.Scroll(bounds.X+bounds.W/2, bounds.Y+20, 0, 900)
	settled(tester)
	visible := ""
	for index := 8; index < 40; index++ {
		name := fmt.Sprintf("队列歌曲%02d", index)
		if _, ok := tester.Find(name); ok {
			visible = name
			break
		}
	}
	if visible == "" {
		t.Fatal("queue did not scroll")
	}
	a.pl.Next()
	settled(tester)
	if _, ok := tester.Find(visible); !ok {
		t.Fatal("next track stole the manually browsed queue position")
	}
	if err := tester.Click("Locate Current Track"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if _, ok := tester.Find("队列歌曲01"); !ok {
		t.Fatal("locating current did not return to the playing position")
	}
}
