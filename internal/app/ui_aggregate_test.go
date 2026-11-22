package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
	"mygo-music/internal/player"
	"mygo-music/internal/plugin"
)

func aggregateApp(t *testing.T) (*app, *plugin.Plugin, *ui.Tester, chan func()) {
	t.Helper()
	dir := os.Getenv("MM_AGGREGATE_PLUGIN")
	if dir == "" {
		dir = "F:/github/mygo-aggregate-plugin"
		if _, err := os.Stat(dir); err != nil {
			t.Skip("设置 MM_AGGREGATE_PLUGIN 后运行真实聚合界面验收")
		}
	}
	src, err := plugin.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(src.Close)
	a := newTestApp()
	a.library = newLibrary(src)
	a.allLibs = []*library{a.library}
	a.libs = a.allLibs
	if !a.library.can("aggregate") {
		t.Fatalf("聚合插件能力未被探测：exports aggregate=%v", src.Can("aggregate"))
	}
	post := posts(a)
	a.pl = player.New(func(fn func()) { post <- fn })
	a.pl.Source = a.sourceByID
	// Close 允许后续请求重新启动插件，必须先收齐本用例启动的异步任务。
	t.Cleanup(func() {
		until(t, post, func() bool {
			edit, view := a.aggregateEdits[a.library], a.aggregateViews[a.library]
			// 设置页还会异步查询账号，必须等回执落地，避免 Close 后被迟到调用重启。
			if edit != nil {
				for _, account := range edit.accounts {
					if account.busy {
						return false
					}
				}
			}
			return !a.library.accountBusy && (edit == nil || !edit.busy) && (view == nil || !view.busy && !view.loadingLists) && !a.pl.Loading()
		})
		if a.pl.Playing() {
			a.pl.TogglePlay()
		}
	})
	tt := ui.NewTester(func(c *ui.Context) { a.pluginSettings(c, a.library, paletteFor(c.Theme())) }, 760, 1200)
	return a, src, tt, post
}

// 设置页启动的账号查询必须在关闭插件前收齐，否则迟到调用可能重建已删除的临时目录。
func TestAggregateCleanupWaitsForAccount(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var a *app
	var post chan func()
	released := false
	t.Run("pending-account", func(t *testing.T) {
		a, _, _, post = aggregateApp(t)
		// NewTester 的首帧可能已经查询账号，先收齐它，再控制下一次查询。
		until(t, post, func() bool {
			edit := a.aggregateEdits[a.library]
			return !a.library.accountBusy && (edit == nil || !edit.busy)
		})
		src := newFakeSource("pending-aggregate-account")
		src.account = func() (*music.Account, error) {
			close(entered)
			<-release
			return nil, nil
		}
		a.library.src = src
		a.library.caps = probeCaps(src)
		a.loadAccount(a.library)
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("账号查询未进入受控等待")
		}
		// 只允许 helper 的清理循环处理该回执；旧条件会直接退出并跳过它。
		post <- func() {
			if !released {
				released = true
				close(release)
			}
		}
	})
	if a == nil {
		t.Skip("聚合插件夹具不可用")
	}
	if !released {
		t.Error("插件关闭前没有等待设置页账号查询")
		released = true
		close(release)
	}
	if a != nil {
		until(t, post, func() bool { return !a.library.accountBusy })
	}
}

func aggregateFixture(t *testing.T) (*app, *plugin.Plugin, *ui.Tester, chan func()) {
	t.Helper()
	a, src, _, post := aggregateApp(t)
	var page func(*ui.Context)
	page = func(c *ui.Context) {
		if a.library.home == nil {
			browse, err := src.Home()
			if err != nil {
				t.Fatal(err)
			}
			a.library.home = browse
		}
		a.library.started = true
		a.library.homeAt = time.Now()
		a.route = route{page: pageHome, src: a.library.id}
		a.library = a.libOf(a.route.src)
		a.homePage(c)
	}
	return a, src, ui.NewTester(page, 760, 1200), post
}

// 聚合筛选仅隐藏页面行；点击一行时仍将该来源 section 的完整顺序交给播放器。
func TestAggregateFilteredTrackPlaysFullSectionQueue(t *testing.T) {
	a, src, tt, _ := aggregateFixture(t)
	for _, item := range []struct{ platform, account, id, name string }{{"wy", "a", "raw-01", "Wy title"}, {"tx", "b", "01", "Tx title"}} {
		if _, err := src.Aggregate("commitSnapshot", map[string]any{"platform": item.platform, "accountID": item.account, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": item.id, "name": item.name}}}); err != nil {
			t.Fatal(err)
		}
	}
	browse, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = browse
	tt.Frame()
	a.aggregateViews[a.library].platform = "wy"
	tt.Frame()
	if err := tt.Click("Wy title"); err != nil {
		t.Fatal(err)
	}
	queue := a.pl.Queue()
	want := browse.Sections[0].Tracks
	if len(queue) != len(want) {
		t.Fatalf("筛选后的行替换了播放器的完整来源队列: got %+v, want %+v", queue, want)
	}
	for i := range want {
		if queue[i].ID != want[i].ID {
			t.Fatalf("筛选后的行改变了来源队列顺序: got %+v, want %+v", queue, want)
		}
	}
}

// 聚合曲库同一来源列表分组只展开一项，切换曲目后旧详情必须收起。
func TestAggregateTrackDetailsKeepOneExpandedRow(t *testing.T) {
	a, _, _, _ := aggregateFixture(t)
	a.library.home = &music.Browse{Sections: []music.Section{{Title: "Saved", Tracks: []music.Track{
		{ID: "detail-one", Name: "Detail One", AggregateSources: []music.AggregateSource{{Platform: "wy", AccountID: "account-a", TrackID: "detail-one", ListName: "Liked"}}},
		{ID: "detail-two", Name: "Detail Two", AggregateSources: []music.AggregateSource{{Platform: "tx", AccountID: "account-b", TrackID: "detail-two", ListName: "Road songs"}}},
	}}}}
	tt := ui.NewTester(func(c *ui.Context) {
		a.aggregateLibrary(c, a.library, paletteFor(c.Theme()), false)
	}, 760, 900)
	tt.Frame()
	if err := tt.Click("More Detail One"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Access("Recording Source Detail One"); !ok {
		t.Fatal("第一首曲目的详情没有展开")
	}
	if err := tt.Click("More Detail Two"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Access("Recording Source Detail One"); ok {
		t.Fatal("展开第二首曲目后第一首详情仍可见")
	}
	if _, ok := tt.Access("Recording Source Detail Two"); !ok {
		t.Fatal("第二首曲目的详情没有展开")
	}
}

// 不同来源列表分组可以各自展开详情，避免一个分组的操作影响另一个分组。
func TestAggregateTrackDetailsExpandIndependentlyPerSection(t *testing.T) {
	a, _, _, _ := aggregateFixture(t)
	a.library.home = &music.Browse{Sections: []music.Section{
		{Title: "Saved", Tracks: []music.Track{{
			ID: "saved-detail", Name: "Saved Detail",
			AggregateSources: []music.AggregateSource{{Platform: "wy", AccountID: "account-a", TrackID: "saved-detail", ListID: "liked", ListName: "Liked"}},
		}}},
		{Title: "Road", Tracks: []music.Track{{
			ID: "road-detail", Name: "Road Detail",
			AggregateSources: []music.AggregateSource{{Platform: "tx", AccountID: "account-b", TrackID: "road-detail", ListID: "road", ListName: "Road songs"}},
		}}},
	}}
	tt := ui.NewTester(func(c *ui.Context) {
		a.aggregateLibrary(c, a.library, paletteFor(c.Theme()), false)
	}, 760, 900)
	tt.Frame()
	if err := tt.Click("More Saved Detail"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("More Road Detail"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Access("Recording Source Saved Detail"); !ok {
		t.Fatal("展开第二个分组后第一个分组详情被错误收起")
	}
	if _, ok := tt.Access("Recording Source Road Detail"); !ok {
		t.Fatal("第二个分组详情没有展开")
	}
}

// 同一原始曲目出现在多个歌单时，详情仍保留全部列表归属并按来源身份合并。
func TestAggregateSourceDetailsKeepListMemberships(t *testing.T) {
	sources := []music.AggregateSource{
		{Platform: "wy", AccountID: "account-a", TrackID: "track-1", ListID: "liked", ListName: "Liked"},
		{Platform: "wy", AccountID: "account-a", TrackID: "track-1", ListID: "road", ListName: "Road songs"},
		{Platform: "tx", AccountID: "account-b", TrackID: "track-2", ListID: "liked", ListName: "Liked"},
	}
	details := aggregateSourceDetails(sources)
	if len(details) != 2 {
		t.Fatalf("来源身份被列表归属重复拆分：%+v", details)
	}
	if len(details[0].lists) != 2 || details[0].lists[0] != "Liked" || details[0].lists[1] != "Road songs" {
		t.Fatalf("第一来源没有保留全部列表归属：%+v", details[0])
	}
	if aggregateSourceListCount(sources) != 3 {
		t.Fatalf("列表归属数量错误：%d", aggregateSourceListCount(sources))
	}
}

// 筛选无结果后清空条件应恢复可播放曲目，不能清除来源选择或改写缓存。
func TestAggregateClearFiltersRestoresTracks(t *testing.T) {
	a, _, _, _ := aggregateFixture(t)
	source := music.AggregateSource{Platform: "wy", AccountID: "account-a", TrackID: "cached", ListID: "liked"}
	home := &music.Browse{Sections: []music.Section{{Tracks: []music.Track{{
		ID: "cached", Name: "Cached Track", AggregateSources: []music.AggregateSource{source},
	}}}}}
	a.library.home = home
	tt := ui.NewTester(func(c *ui.Context) {
		a.aggregateLibrary(c, a.library, paletteFor(c.Theme()), false)
	}, 900, 760)
	view := a.aggregateViews[a.library]
	view.query, view.platform, view.list, view.kind = "missing", "tx", "other", "Playlists"
	tt.Frame()
	if err := tt.Click("Clear"); err != nil {
		t.Fatal(err)
	}
	if view.query != "" || view.platform != "All" || view.list != "All" || view.kind != "All" || a.library.home != home {
		t.Fatal("清空筛选没有恢复浏览条件，或意外改写缓存")
	}
	if err := tt.Click("Cached Track"); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "cached" {
		t.Fatal("清空后曲目没有恢复正常的选播操作")
	}
}

// 完整主窗口中滚动到底部后，最后一首必须避开播放器，不能只在单独页面里可点。
func TestAggregateLibraryScrollsPastPlayerBar(t *testing.T) {
	a, _, _, _ := aggregateFixture(t)
	tracks := make([]music.Track, 0, 24)
	for index := range 24 {
		tracks = append(tracks, music.Track{
			ID:   fmt.Sprintf("scroll-%02d", index),
			Name: fmt.Sprintf("Scroll Track %02d", index),
			AggregateSources: []music.AggregateSource{{
				Platform: "wy", AccountID: "account-a", TrackID: fmt.Sprintf("scroll-%02d", index), ListName: "Liked",
			}},
		})
	}
	a.library.home = &music.Browse{Sections: []music.Section{{Title: "Long list", Tracks: tracks}}}
	tt := ui.NewTester(a.view, 900, 760)
	tt.Frame()
	tt.Scroll(820, 500, 0, 100000)
	tt.Advance(time.Second)
	tt.Frame()
	last, ok := tt.Access("Scroll Track 23")
	if !ok || last.Bounds.Y+last.Bounds.H > 760-playerBarHeight {
		t.Fatalf("最后一首仍被真实播放器覆盖：%+v", last)
	}
	if err := tt.Click("Scroll Track 23"); err != nil {
		t.Fatalf("滚动到底部后最后一首不可操作：%v", err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "scroll-23" {
		t.Fatalf("最后一首没有进入播放队列：%+v", current)
	}
}

// 聚合曲库的浅色、深色和窄窗口底部截图只使用合成来源，供人工检查层级与遮挡。
func TestAggregateLibraryFrames(t *testing.T) {
	dir := os.Getenv("MM_FRAMES")
	if dir == "" {
		t.Skip("设置 MM_FRAMES 保存聚合曲库截图")
	}
	lang := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(lang)
	a, _, _, _ := aggregateFixture(t)
	tracks := make([]music.Track, 0, 12)
	for index := range 12 {
		tracks = append(tracks, music.Track{
			ID:   fmt.Sprintf("frame-%02d", index),
			Name: fmt.Sprintf("Frame Track %02d", index),
			AggregateSources: []music.AggregateSource{{
				Platform: "wy", AccountID: "frame-account", TrackID: fmt.Sprintf("frame-%02d", index), ListName: "Liked",
			}},
		})
	}
	a.library.home = &music.Browse{Sections: []music.Section{{Title: "Synthetic library", Tracks: tracks}}}
	for _, width := range []int{900, 640} {
		for _, dark := range []bool{false, true} {
			tt := ui.NewTester(func(c *ui.Context) {
				a.pal = paletteFor(c.Theme())
				a.aggregateLibrary(c, a.library, a.pal, false)
			}, width, 760)
			tt.SetDark(dark)
			settled(tt)
			saveFrame(t, dir, fmt.Sprintf("aggregate-library-%d-dark-%v.png", width, dark), tt.Image())
			if width == 640 {
				tt.Scroll(float32(width-80), 620, 0, 100000)
				settled(tt)
				saveFrame(t, dir, fmt.Sprintf("aggregate-library-640-lower-dark-%v.png", dark), tt.Image())
			}
		}
	}
	a.pageScroll = ui.ScrollState{}
	details := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.aggregateLibrary(c, a.library, a.pal, false)
	}, 900, 760)
	settled(details)
	if err := details.Click("更多 Frame Track 00"); err != nil {
		t.Fatal(err)
	}
	saveFrame(t, dir, "aggregate-library-details.png", details.Image())
	for _, width := range []int{900, 1180} {
		for _, dark := range []bool{false, true} {
			a.pageScroll = ui.ScrollState{}
			window := ui.NewTester(a.view, width, 760)
			window.SetDark(dark)
			settled(window)
			saveFrame(t, dir, fmt.Sprintf("aggregate-window-%d-dark-%v.png", width, dark), window.Image())
			window.Scroll(float32(width-80), 550, 0, 100000)
			settled(window)
			saveFrame(t, dir, fmt.Sprintf("aggregate-window-%d-lower-dark-%v.png", width, dark), window.Image())
		}
	}
}

func aggregateTarget(t *testing.T, src *plugin.Plugin) string {
	t.Helper()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Settings struct{ TargetQuality string }
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Settings.TargetQuality
}

// 设置界面的目标和最低音质一次联合提交，校验失败时插件保留上一份有效设置。
func TestAggregateSettingsSubmitAsOneValidatedChange(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	tt.Frame()
	runPost(t, post)
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit.loaded })
	tt.Frame()
	clickSettingRole(t, tt, ui.RolePopUpButton, "Target Quality")
	if err := tt.Click("320k"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	if got := aggregateTarget(t, src); got != "320k" {
		t.Fatalf("界面没有保存目标: %q", got)
	}
	clickSettingRole(t, tt, ui.RolePopUpButton, "Minimum Quality")
	if err := tt.Click("flac"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	if got := aggregateTarget(t, src); got != "320k" {
		t.Fatalf("非法修改破坏上一份设置: %q", got)
	}
	_ = a
}

// 设置中的重载重新打开当前曲目，但不改变队列、进度或原来的暂停状态。
func TestAggregateSettingsReloadKeepsCurrentTrackAndPosition(t *testing.T) {
	a, _, tt, post := aggregateApp(t)
	streamSource := newFakeSource(a.library.id)
	audioPath, err := filepath.Abs(filepath.Join("..", "player", "testdata", "seek.flac"))
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{}, 8)
	streamSource.stream = func(id, quality string) (*music.Stream, error) {
		opened <- struct{}{}
		return &music.Stream{URL: "file:///" + filepath.ToSlash(audioPath)}, nil
	}
	a.pl.Source = func(string) music.Source { return streamSource }
	a.pl.Restore(player.Session{
		Queue: []music.Track{{ID: "one", Source: a.library.id}, {ID: "two", Source: a.library.id}},
		Order: []int{0, 1}, At: 0, Position: 1000,
	})
	until(t, post, func() bool { return a.pl.Ready() && !a.pl.Loading() })
	<-opened
	tt.Frame()
	until(t, post, func() bool { return a.aggregateEdits[a.library].loaded })
	tt.Frame()
	if err := tt.Click("Reload Current Track"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return a.pl.Ready() && !a.pl.Loading() })
	select {
	case <-opened:
	default:
		t.Fatal("重载没有重新打开当前曲目")
	}
	if a.pl.Playing() || a.pl.Current().ID != "one" || len(a.pl.Queue()) != 2 || a.pl.Queue()[1].ID != "two" || a.pl.Position() < 990*time.Millisecond {
		t.Fatalf("重载改变了播放意图、位置或队列：playing=%v position=%v queue=%+v", a.pl.Playing(), a.pl.Position(), a.pl.Queue())
	}
}

// 标题相同不构成录音证据；用户在实际列表中明确选两首曲目后才能关联。
func TestAggregateUIConfirmsAndRevokesManualLink(t *testing.T) {
	a, src, tt, post := aggregateFixture(t)
	for _, item := range []struct{ platform, account, id string }{{"wy", "a", "raw-01"}, {"tx", "b", "01"}} {
		if _, err := src.Aggregate("commitSnapshot", map[string]any{"platform": item.platform, "accountID": item.account, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": item.id, "name": "Same title"}}}); err != nil {
			t.Fatal(err)
		}
	}
	browse, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = browse
	tt.Frame()
	if err := tt.Click("Select for Link wy"); err != nil {
		t.Fatalf("找不到第一个来源选择按钮；当前文本：%q；首页曲目：%+v；错误：%v", tt.Texts(), browse.Sections, err)
	}
	tt.Frame()
	if err := tt.Click("Link Same Recording wy + tx"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	view := a.aggregateViews[a.library]
	if view == nil || !view.busy {
		t.Fatal("关联操作没有显示处理中状态")
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || !page.Sections[0].Tracks[0].AggregateManual {
		t.Fatalf("界面操作没有持久化关联: %+v", page.Sections)
	}
	tt.Frame()
	if err := tt.Click("Revoke Link tx + wy"); err != nil {
		t.Fatalf("缺少撤销控件；UI文本=%q；Home=%+v；错误=%v", tt.Texts(), page.Sections, err)
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	page, err = src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("界面撤销没有拆分录音: %+v", page.Sections)
	}
}

// 多个列表中的同一平台曲目仍是一个身份，可与另一平台曲目明确建立人工关联。
func TestAggregateUICanLinkTracksPresentInMultipleLists(t *testing.T) {
	a, src, tt, post := aggregateFixture(t)
	for _, snapshot := range []map[string]any{
		{"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": "wy-one", "name": "Repeated"}}},
		{"platform": "tx", "accountID": "b", "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": "tx-one", "name": "Repeated"}}},
		{"platform": "tx", "accountID": "b", "listID": "playlist-one", "selected": true, "complete": true, "refreshedAt": 2, "tracks": []any{map[string]any{"id": "tx-one", "name": "Repeated"}}},
	} {
		if _, err := src.Aggregate("commitSnapshot", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = page
	tt.Frame()
	if err := tt.Click("Select for Link wy"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Link Same Recording wy + tx"); err != nil {
		t.Fatalf("同源多列表重复项隐藏了另一来源的关联操作：%q；%v", tt.Texts(), err)
	}
	view := a.aggregateViews[a.library]
	until(t, post, func() bool { return !view.busy })
	joined, err := src.Home()
	if err != nil || len(joined.Sections[0].Tracks) != 1 || !joined.Sections[0].Tracks[0].AggregateManual {
		t.Fatalf("按唯一原始曲目身份关联失败：%+v %v", joined.Sections, err)
	}
}

// 已合并曲目含多个来源时，关联操作仍须由用户明确选择具体原始身份。
func TestAggregateUISelectsExactSourceFromMergedTrack(t *testing.T) {
	a, src, tt, post := aggregateFixture(t)
	kg := music.AggregateSource{Platform: "kg", AccountID: "account · a", TrackID: "b · c"}
	wy := music.AggregateSource{Platform: "kg", AccountID: "account", TrackID: "a · b · c"}
	tx := music.AggregateSource{Platform: "tx", AccountID: "account-tx", TrackID: "tx-version"}
	for _, source := range []music.AggregateSource{kg, wy, tx} {
		if _, err := src.Aggregate("commitSnapshot", map[string]any{"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1, "tracks": []any{map[string]any{"id": source.TrackID, "name": source.TrackID}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.Aggregate("confirmLink", map[string]any{"left": kg, "right": wy}); err != nil {
		t.Fatal(err)
	}
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = page
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RolePopUpButton, "Recording Source"); !ok {
		t.Fatalf("合并曲目没有具体来源选择器：%q", tt.Texts())
	}
	clickSettingRole(t, tt, ui.RolePopUpButton, "Recording Source")
	if err := tt.Click(`kg · "account" · "a · b · c"`); err != nil {
		t.Fatalf("不能选择第二个原始来源：%v", err)
	}
	if err := tt.Click("Select for Link kg"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Link Same Recording kg + tx"); err != nil {
		t.Fatalf("不能把明确选择的来源与另一曲目关联：%q；%v", tt.Texts(), err)
	}
	view := a.aggregateViews[a.library]
	until(t, post, func() bool { return !view.busy })
	linked, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(linked.Sections[0].Tracks) != 1 {
		t.Fatalf("具体来源关联后未合入同录音组：%+v", linked.Sections)
	}
	var exactLink bool
	for _, link := range linked.Sections[0].Tracks[0].AggregateLinks {
		if sameAggregateSource(link.Left, wy) && sameAggregateSource(link.Right, tx) || sameAggregateSource(link.Right, wy) && sameAggregateSource(link.Left, tx) {
			exactLink = true
		}
	}
	if !exactLink {
		t.Fatalf("关联使用了未选中的另一来源：%+v", linked.Sections[0].Tracks[0].AggregateLinks)
	}
}

// 具体来源选择只决定新队列的版本锚点，不限制同版候选、不改同档平台顺序或原始列表。
func TestAggregateSelectedSourceAnchorsFullQueue(t *testing.T) {
	a, src, tt, _ := aggregateFixture(t)
	left := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "tx-one"}
	right := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "wy-one"}
	other := music.AggregateSource{Platform: "kg", AccountID: "c", TrackID: "kg-two"}
	for _, source := range []music.AggregateSource{left, right, other} {
		name := "Joined recording"
		if source.Platform == "kg" {
			name = "Another recording"
		}
		if _, err := src.Aggregate("commitSnapshot", map[string]any{
			"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1,
			"tracks": []any{map[string]any{"id": source.TrackID, "name": name, "qualities": []string{"flac"}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	pair := map[string]any{"left": left, "right": right}
	if _, err := src.Aggregate("confirmLink", pair); err != nil {
		t.Fatal(err)
	}
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = page
	tt.Frame()
	a.aggregateViews[a.library].platform = "wy"
	tt.Frame()
	clickSettingRole(t, tt, ui.RolePopUpButton, "Recording Source")
	if err := tt.Click(`wy · "a" · "wy-one"`); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Joined recording"); err != nil {
		t.Fatal(err)
	}
	queue := a.pl.Queue()
	if len(queue) != len(page.Sections[0].Tracks) {
		t.Fatalf("来源选择或筛选截断队列：%+v", queue)
	}
	var queued music.Track
	for i, track := range queue {
		if track.Name != page.Sections[0].Tracks[i].Name {
			t.Fatalf("来源选择重排队列：%+v", queue)
		}
		if track.Name == "Joined recording" {
			queued = track
			if track.ID == page.Sections[0].Tracks[i].ID {
				t.Fatal("显式选择没有改变新队列的版本锚点")
			}
		}
	}
	var identity struct {
		Version  int
		Identity music.AggregateSource
		Sources  []music.AggregateSource
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(queued.ID, "aggregate:")), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Version != 1 || !sameAggregateSource(identity.Identity, right) || len(identity.Sources) != 2 || queued.PlaybackID != "" {
		t.Fatalf("选择来源丢失冻结集合或冒充已播：%+v %+v", identity, queued)
	}
	raw, err := src.Aggregate("playbackPlan", map[string]any{"id": queued.ID})
	if err != nil {
		t.Fatal(err)
	}
	var plan music.PlaybackPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 2 || plan.Candidates[0].Platform != "tx" || plan.Candidates[1].Platform != "wy" {
		t.Fatalf("版本锚点错误限制候选或改变平台顺序：%s", raw)
	}
	if _, err := src.Aggregate("revokeLink", pair); err != nil {
		t.Fatal(err)
	}
	raw, err = src.Aggregate("playbackPlan", map[string]any{"id": queued.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Platform != "wy" {
		t.Fatalf("拆分后的未播项没有跟随所选来源：%s", raw)
	}
}

// 未操作来源选择器时，展示排序和筛选不能代替用户选择或改写插件的稳定锚点。
func TestAggregateUnselectedSourceKeepsStableQueueAnchor(t *testing.T) {
	a, src, tt, _ := aggregateFixture(t)
	left := music.AggregateSource{Platform: "tx", AccountID: "b", TrackID: "tx-one"}
	right := music.AggregateSource{Platform: "wy", AccountID: "a", TrackID: "wy-one"}
	for _, source := range []music.AggregateSource{left, right} {
		if _, err := src.Aggregate("commitSnapshot", map[string]any{
			"platform": source.Platform, "accountID": source.AccountID, "listID": "liked", "complete": true, "refreshedAt": 1,
			"tracks": []any{map[string]any{"id": source.TrackID, "name": "Stable recording"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.Aggregate("confirmLink", map[string]any{"left": left, "right": right}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Aggregate("configure", map[string]any{"platformOrder": []string{"wy", "tx", "kg", "kw", "mg", "qs"}}); err != nil {
		t.Fatal(err)
	}
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = page
	tt.Frame()
	a.aggregateViews[a.library].platform = "wy"
	tt.Frame()
	if err := tt.Click("Stable recording"); err != nil {
		t.Fatal(err)
	}
	queue := a.pl.Queue()
	if len(queue) != 1 || queue[0].ID != page.Sections[0].Tracks[0].ID || queue[0].PlaybackID != "" {
		t.Fatalf("未选择来源却改写版本锚点：%+v", queue)
	}
	// 平台首位不一定是默认版本，不能因选择器已显示该平台而吞掉用户选择。
	clickSettingRole(t, tt, ui.RolePopUpButton, "Recording Source")
	if err := tt.Click(`wy · "a" · "wy-one"`); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Stable recording"); err != nil {
		t.Fatal(err)
	}
	var frozen struct{ Identity music.AggregateSource }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(a.pl.Queue()[0].ID, "aggregate:")), &frozen); err != nil {
		t.Fatal(err)
	}
	if !sameAggregateSource(frozen.Identity, right) {
		t.Fatalf("显式选择被默认显示值吞掉：%+v", frozen.Identity)
	}
}

// 已声明的高级目标可从现有设置保存；最低音质仍采用可核验的普通档位。
func TestAggregateSettingsCanChooseDeclaredAdvancedTarget(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	if _, err := src.Aggregate("commitSnapshot", map[string]any{
		"platform": "wy", "accountID": "a", "listID": "liked", "complete": true, "refreshedAt": 1,
		"tracks": []any{map[string]any{"id": "one", "qualities": []string{"immersive", "flac", "128k"}}},
	}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit != nil && edit.loaded })
	tt.Frame()
	if err := tt.Click("Reload aggregate status"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	clickSettingRole(t, tt, ui.RolePopUpButton, "Target Quality")
	if err := tt.Click("immersive"); err != nil {
		t.Fatalf("已声明的高级目标不能选择：%v", err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	if target := aggregateTarget(t, src); target != "immersive" {
		t.Fatalf("界面未保存选定高级目标：%q，错误：%s", target, edit.err)
	}
}

// 候选平台顺序可由用户在现有设置行中调整并持久化。
func TestAggregateSettingsCanReorderPlatforms(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit != nil && edit.loaded })
	tt.Frame()
	if err := tt.Click("Move Up QQ Music"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Save Aggregate Settings"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !edit.busy })
	tt.Frame()
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Settings struct{ PlatformOrder []string }
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Settings.PlatformOrder) != 6 || got.Settings.PlatformOrder[0] != "kg" || got.Settings.PlatformOrder[3] != "tx" {
		t.Fatalf("平台排序没有按设置保存: %v", got.Settings.PlatformOrder)
	}
}

// 本机完整缓存歌单可纳入或移出聚合，喜欢快照始终保留；未读取的列表不可选。
func TestAggregateCachedPlaylistCanBeSelectedFromHome(t *testing.T) {
	a, src, _, post := aggregateApp(t)
	emptyHome, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	a.library.home = emptyHome
	a.library.started = true
	a.route = route{page: pageHome, src: a.library.id}
	a.library = a.libOf(a.route.src)
	tt := ui.NewTester(func(c *ui.Context) { a.homePage(c) }, 760, 1200)
	tt.Frame()
	var view *aggregateView
	until(t, post, func() bool {
		view = a.aggregateViews[a.library]
		return view != nil && !view.loadingLists && view.listsLoaded
	})
	if len(view.lists) != 0 {
		t.Fatalf("空测试起点意外有缓存列表：%+v", view.lists)
	}
	for _, snapshot := range []map[string]any{
		{"platform": "wy", "accountID": "account-a", "listID": "liked", "listName": "Liked", "complete": true, "refreshedAt": 100, "selected": true, "tracks": []any{map[string]any{"id": "song-1", "name": "Liked song"}}},
		{"platform": "wy", "accountID": "account-a", "listID": "playlist-1", "listName": "Road songs", "complete": true, "refreshedAt": 200, "tracks": []any{map[string]any{"id": "song-2", "name": "Road song"}}},
	} {
		if _, err := src.Aggregate("commitSnapshot", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("Reload Cached Data"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	if len(view.lists) != 2 {
		t.Fatalf("重载后没有更新缓存列表目录：%+v", view.lists)
	}
	if err := tt.Click("Include Cached List Road songs"); err != nil {
		t.Fatalf("完整的未选歌单不可管理：%q；%v", tt.Texts(), err)
	}
	until(t, post, func() bool { return !view.busy })
	tt.Frame()
	page, err := src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 2 {
		t.Fatalf("纳入歌单后未出现曲目：%+v", page.Sections)
	}
	if err := tt.Click("Exclude Cached List Road songs"); err != nil {
		t.Fatalf("已纳入歌单不可移出：%q；%v", tt.Texts(), err)
	}
	until(t, post, func() bool { return !view.busy })
	page, err = src.Home()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sections[0].Tracks) != 1 || page.Sections[0].Tracks[0].Name != "Liked song" {
		t.Fatalf("移出歌单损坏了喜欢快照：%+v", page.Sections)
	}
	if _, err := src.Aggregate("selectList", map[string]any{"platform": "wy", "accountID": "account-a", "listID": "missing", "selected": true}); err == nil {
		t.Fatal("尚未完整缓存的列表可以纳入")
	}
}

type aggregateRefreshSource struct {
	*fakeSource
	refreshes  chan string
	refreshed  bool
	refreshErr error
}

func (s *aggregateRefreshSource) Aggregate(action string, input map[string]any) (json.RawMessage, error) {
	if action == "refresh" {
		s.refreshed = true
		s.refreshes <- action
		return nil, s.refreshErr
	}
	return json.RawMessage(`[]`), nil
}

// 手动云刷新与本机重载分开；点击后采用新首页，但不能改动已排好的播放队列。
func TestAggregateCloudRefreshPreservesQueue(t *testing.T) {
	aggregateCloudRefreshTest(t, false)
}

// 某列表失败不应遮住其他列表已成功提交的新内容，同时清楚保留错误提示。
func TestAggregateCloudRefreshPartialSuccessIsVisible(t *testing.T) {
	aggregateCloudRefreshTest(t, true)
}

func aggregateCloudRefreshTest(t *testing.T, partial bool) {
	t.Helper()
	a := newTestApp()
	post := posts(a)
	src := &aggregateRefreshSource{fakeSource: fake(a), refreshes: make(chan string, 1)}
	if partial {
		src.refreshErr = errors.New("部分列表读取失败")
	}
	src.home = func() (*music.Browse, error) {
		name := "旧快照"
		if src.refreshed {
			name = "已刷新歌曲"
		}
		return &music.Browse{Sections: []music.Section{{Tracks: []music.Track{{ID: "track", Name: name}}}}}, nil
	}
	a.library.src = src
	a.library.home, _ = src.Home()
	a.library.started = true
	a.library.homeAt = time.Now()
	a.route = route{page: pageHome, src: a.library.id}
	a.pl.Restore(player.Session{Queue: []music.Track{{ID: "queued", Name: "原队列", Source: a.library.id}}, Order: []int{0}, At: 0})
	tt := ui.NewTester(func(c *ui.Context) { a.aggregateLibrary(c, a.library, paletteFor(c.Theme()), false) }, 1100, 900)
	tt.Frame()
	view := a.aggregateViews[a.library]
	until(t, post, func() bool { return view.listsLoaded && !view.loadingLists })
	tt.Frame()
	if err := tt.Click("Refresh Cloud Lists"); err != nil {
		t.Fatal(err)
	}
	until(t, post, func() bool { return !view.busy })
	select {
	case <-src.refreshes:
	default:
		t.Fatal("按钮只重载本机缓存，没有发起云端刷新")
	}
	if (view.err != "") != partial {
		t.Fatal("云刷新没有准确反馈部分失败")
	}
	if a.library.home.Sections[0].Tracks[0].Name != "已刷新歌曲" {
		t.Fatal("云刷新没有展示已成功更新的内容")
	}
	if current := a.pl.Current(); current == nil || current.ID != "queued" {
		t.Fatal("云刷新改变了当前播放队列")
	}
}

// 新插件通过正式设置入口展示音源管理，不再回填或展示旧密钥输入。
func TestAggregateSettingsCustomSourcesReplaceLegacyKeys(t *testing.T) {
	a, src, tt, post := aggregateApp(t)
	const secret = "synthetic-key-that-must-not-appear"
	// 测试只用当前插件 VM 中的临时值，不向开发机的系统凭据库写入。
	if _, err := src.Aggregate("setResolverKey", map[string]any{"value": secret, "temporary": true}); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	edit := a.aggregateEdits[a.library]
	until(t, post, func() bool { return edit != nil && edit.loaded })
	tt.Frame()
	if err := tt.Click("Add Source"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	n, ok := tt.AccessByRole(ui.RoleTextField, "Source Link")
	if !ok || !n.Password || n.Value != "" {
		t.Fatal("新插件未展示空白的密码链接输入")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Confirm Add Source"); !ok {
		t.Fatal("展开后缺少确认添加操作")
	}
	for _, label := range []string{"Resolver Key", "NetEase Resolver Key", "QQ Resolver Key", "Qishui Resolver Key"} {
		if _, ok := tt.AccessByRole(ui.RoleTextField, label); ok {
			t.Fatalf("新插件仍暴露旧密钥输入：%s", label)
		}
	}
	if strings.Contains(strings.Join(tt.Texts(), " "), secret) {
		t.Fatal("设置页面泄露旧密钥")
	}
	raw, err := src.Aggregate("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("状态接口泄露解析密钥")
	}
}
