package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

func navigationApp() (*app, []music.Track) {
	a := newTestApp()
	tracks := make([]music.Track, 90)
	for index := range tracks {
		tracks[index] = song(fmt.Sprint(index), fmt.Sprintf("浏览歌曲%02d", index), "测试艺人")
		tracks[index].Source = "fake"
	}
	tracks[20].Album = music.Album{ID: "detail", Name: "浏览专辑"}
	a.playlists["long"] = &music.Playlist{ID: "long", Name: "长歌单", Tracks: tracks}
	a.plAt["long"] = time.Now()
	a.albums["detail"] = &albumData{album: &music.AlbumPage{
		Album:  music.Album{ID: "detail", Name: "浏览专辑"},
		Tracks: []music.Track{song("album-song", "专辑中的歌")},
	}}
	a.open(route{page: pagePlaylist, id: "long"})
	return a, tracks
}

func scrollNavigation(t *testing.T, tester *ui.Tester, tracks []music.Track) ui.Rect {
	t.Helper()
	bounds, ok := tester.Find(tracks[0].Name)
	if !ok {
		t.Fatal("first track is not available")
	}
	tester.Scroll(bounds.X+bounds.W/2, bounds.Y+20, 0, 1100)
	settled(tester)
	bounds, ok = tester.Find(tracks[20].Name)
	if !ok || bounds.Y < 60 || bounds.Y+bounds.H > 700 {
		t.Fatalf("middle track is not in the viewport: %+v, found=%t", bounds, ok)
	}
	return bounds
}

// 长歌单打开专辑再返回应恢复原位置，下一次点击仍播放刚才浏览的歌曲。
func TestNavigationRestoresPlaylist(t *testing.T) {
	a, tracks := navigationApp()
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	before := scrollNavigation(t, tester, tracks)
	album, ok := tester.Find("浏览专辑")
	if !ok {
		t.Fatal("album link is not available")
	}
	tester.ClickAt(album.X+4, album.Y+album.H/2)
	settled(tester)
	if a.route.page != pageAlbum {
		t.Fatal("album link did not open the album")
	}
	tester.Key(0, ui.KeyBack)
	settled(tester)
	after, ok := tester.Find(tracks[20].Name)
	if !ok || after.Y < before.Y-2 || after.Y > before.Y+2 {
		t.Fatalf("back lost the browsed track: before=%+v after=%+v found=%t", before, after, ok)
	}
	if err := tester.Click(tracks[20].Name); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if current := a.pl.Current(); current == nil || current.ID != tracks[20].ID {
		t.Fatalf("restored row played %+v", current)
	}
	tester.Key(0, ui.KeyForward)
	settled(tester)
	if err := tester.Click("专辑中的歌"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if current := a.pl.Current(); current == nil || current.ID != "album-song" {
		t.Fatalf("forward restored the wrong page: %+v", current)
	}
}

// 刷新在可见歌曲之前插入曲目时应跟随稳定歌曲身份，而不是旧的行号。
func TestNavigationFollowsRefreshedTrack(t *testing.T) {
	a, tracks := navigationApp()
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	before := scrollNavigation(t, tester, tracks)
	a.playlists["long"].Tracks = append([]music.Track{
		song("new-a", "新歌甲"), song("new-b", "新歌乙"), song("new-c", "新歌丙"),
	}, tracks...)
	settled(tester)
	after, ok := tester.Find(tracks[20].Name)
	if !ok || after.Y < before.Y-2 || after.Y > before.Y+2 {
		t.Fatalf("refresh moved the browsed track: before=%+v after=%+v found=%t", before, after, ok)
	}
	tester.ClickAt(before.X+140, before.Y+before.H/2)
	tester.Frame()
	if current := a.pl.Current(); current == nil || current.ID != tracks[20].ID {
		t.Fatalf("refresh made the original position play %+v", current)
	}
}

// 换账号后迟到的歌单响应不能覆盖新账号已经可操作的同 ID 歌单。
func TestNavigationRejectsPreviousAccountResponse(t *testing.T) {
	a := newTestApp()
	without(a, "isLiked", "home", "recent", "playlists")
	post := posts(a)
	requests := make(chan chan *music.Playlist, 2)
	fake(a).playlist = func(id string) (*music.Playlist, error) {
		reply := make(chan *music.Playlist, 1)
		requests <- reply
		return <-reply, nil
	}
	a.account = &music.Account{ID: "old"}
	a.open(route{page: pagePlaylist, id: "private"})
	old := wait(t, requests, "old account playlist")
	tester := ui.NewTester(a.view, 1200, 800)
	a.account = &music.Account{ID: "new"}
	a.afterAccount(a.library)
	current := wait(t, requests, "new account playlist")
	current <- &music.Playlist{ID: "private", Name: "当前歌单", Tracks: []music.Track{song("new", "新账号歌曲")}}
	until(t, post, func() bool { return a.playlists["private"] != nil && !a.homeLoading })
	settled(tester)
	old <- &music.Playlist{ID: "private", Name: "旧账号歌单", Tracks: []music.Track{song("old", "旧账号歌曲")}}
	runPost(t, post)
	settled(tester)
	if err := tester.Click("新账号歌曲"); err != nil {
		t.Fatalf("late answer replaced the current account: %v", err)
	}
	if track := a.pl.Current(); track == nil || track.ID != "new" {
		t.Fatalf("played the previous account's data: %+v", track)
	}
}

// 退出账号、切换账号和重新启用来源都不能恢复已失效的私人浏览位置。
func TestNavigationInvalidatesPersonalPositions(t *testing.T) {
	for _, change := range []string{"account", "sign-out", "disable"} {
		t.Run(change, func(t *testing.T) {
			a, tracks := navigationApp()
			without(a, "isLiked", "home", "recent", "playlists")
			post := posts(a)
			a.account = &music.Account{ID: "listener"}
			fake(a).playlist = func(id string) (*music.Playlist, error) {
				return &music.Playlist{ID: id, Name: "当前歌单", Tracks: tracks}, nil
			}
			tester := ui.NewTester(a.view, 1200, 800)
			settled(tester)
			scrollNavigation(t, tester, tracks)
			switch change {
			case "account":
				a.account = &music.Account{ID: "another"}
				a.afterAccount(a.library)
			case "sign-out":
				a.signOut(a.library)
			case "disable":
				lib := a.library
				addOther(a)
				a.setPluginEnabled(lib, false)
				settled(tester)
				a.setPluginEnabled(lib, true)
				a.open(route{page: pagePlaylist, src: lib.id, id: "long"})
			}
			until(t, post, func() bool { return a.playlists["long"] != nil && !a.homeLoading })
			settled(tester)
			if err := tester.Click(tracks[0].Name); err != nil {
				t.Fatal(err)
			}
			if current := a.pl.Current(); current == nil || current.ID != tracks[0].ID {
				t.Fatalf("invalid personal position survived %s: played %+v", change, current)
			}
		})
	}
}

// 显式重新提交查询从顶部重查；通过历史返回同一查询才保留旧位置。
func TestNavigationExplicitSearchStartsAtTop(t *testing.T) {
	for _, elsewhere := range []bool{false, true} {
		t.Run(fmt.Sprint(elsewhere), func(t *testing.T) {
			a, tracks := navigationApp()
			without(a, "isLiked")
			post := posts(a)
			asked := make(chan searchRequest, 2)
			fake(a).search = func(query string, kind music.SearchKind, offset int) (*music.SearchResult, error) {
				asked <- searchRequest{query, kind, offset}
				return &music.SearchResult{Tracks: tracks}, nil
			}
			query := route{page: pageSearch, src: "fake", query: "海", kind: music.SearchTracks}
			a.searches[query] = &searchData{res: music.SearchResult{Tracks: tracks}, page: paged{loaded: true}}
			a.open(query)
			tester := ui.NewTester(a.view, 1200, 800)
			settled(tester)
			scrollNavigation(t, tester, tracks)
			if elsewhere {
				a.open(route{page: pageAlbum, id: "detail"})
				settled(tester)
			}
			a.searchFor(query)
			if got := wait(t, asked, "explicit search"); got != (searchRequest{"海", music.SearchTracks, 0}) {
				t.Fatalf("searched %+v", got)
			}
			runPost(t, post)
			settled(tester)
			if err := tester.Click(tracks[0].Name); err != nil {
				t.Fatal(err)
			}
			if current := a.pl.Current(); current == nil || current.ID != tracks[0].ID {
				t.Fatalf("explicit search reused the old position: %+v", current)
			}
		})
	}
}

// 详情页返回后 Enter 应作用于刚才的歌曲行，不能留在已消失的详情控件上。
func TestNavigationRestoresTrackFocus(t *testing.T) {
	a, tracks := navigationApp()
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	scrollNavigation(t, tester, tracks)
	album, ok := tester.Find("浏览专辑")
	if !ok {
		t.Fatal("album link is not available")
	}
	tester.ClickAt(album.X+4, album.Y+album.H/2)
	settled(tester)
	tester.Key(0, ui.KeyBack)
	settled(tester)
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if current := a.pl.Current(); current == nil || current.ID != tracks[20].ID {
		t.Fatalf("Enter did not continue from the restored track: %+v", current)
	}
}

// 各类详情与分页的旧账号错误都不能在新账号浏览时弹出过时登录提示。
func TestNavigationIgnoresStalePageErrors(t *testing.T) {
	for _, page := range []pageKind{pageLiked, pageAlbum, pageArtist, pageSearch, pageShow, pageBrowse} {
		t.Run(fmt.Sprint(page), func(t *testing.T) {
			a := newTestApp()
			without(a, "isLiked", "home", "recent", "playlists")
			post := posts(a)
			type answer struct {
				tracks []music.Track
				err    error
			}
			requests := make(chan chan answer, 2)
			fetch := func() ([]music.Track, error) {
				reply := make(chan answer, 1)
				requests <- reply
				got := <-reply
				return got.tracks, got.err
			}
			src := fake(a)
			src.liked = func(int) (*music.Page, error) {
				tracks, err := fetch()
				return &music.Page{Tracks: tracks}, err
			}
			src.album = func(string) (*music.AlbumPage, error) {
				tracks, err := fetch()
				return &music.AlbumPage{Album: music.Album{Name: "专辑"}, Tracks: tracks}, err
			}
			src.artist = func(string) (*music.ArtistPage, error) {
				tracks, err := fetch()
				return &music.ArtistPage{Artist: music.Artist{Name: "艺人"}, Tracks: tracks}, err
			}
			src.search = func(string, music.SearchKind, int) (*music.SearchResult, error) {
				tracks, err := fetch()
				return &music.SearchResult{Tracks: tracks}, err
			}
			src.show = func(string, int) (*music.ShowPage, error) {
				tracks, err := fetch()
				return &music.ShowPage{Show: music.Show{Name: "播客"}, Episodes: tracks}, err
			}
			src.browse = func(string) (*music.Browse, error) {
				tracks, err := fetch()
				return &music.Browse{Sections: []music.Section{{Tracks: tracks}}}, err
			}
			a.account = &music.Account{ID: "old"}
			a.open(route{page: page, id: "private", query: "查询", kind: music.SearchTracks})
			old := wait(t, requests, "old account page")
			tester := ui.NewTester(a.view, 1200, 800)
			a.account = &music.Account{ID: "new"}
			a.afterAccount(a.library)
			current := wait(t, requests, "current account page")
			current <- answer{tracks: []music.Track{song("new", "当前账号可播放的歌")}}
			until(t, post, func() bool {
				if a.homeLoading {
					return false
				}
				switch page {
				case pageLiked:
					return a.likedPaged.loaded
				case pageAlbum:
					return a.albums["private"].album != nil
				case pageArtist:
					return a.artists["private"].page != nil
				case pageSearch:
					return a.searches[a.route].page.loaded
				case pageShow:
					return a.shows["private"].page.loaded
				default:
					return a.browses["private"].page != nil
				}
			})
			old <- answer{err: music.ErrSignIn}
			runPost(t, post)
			settled(tester)
			if a.signIn.open {
				t.Fatal("stale page opened a sign-in dialog")
			}
			if err := tester.Click("当前账号可播放的歌"); err != nil {
				t.Fatal(err)
			}
			if current := a.pl.Current(); current == nil || current.ID != "new" {
				t.Fatalf("stale page changed the playable result: %+v", current)
			}
		})
	}
}

// 来源通知刷新时不能把仍在等待旧请求的分页状态继承成永远加载中。
func TestNavigationReloadsPendingPageAfterLibraryChange(t *testing.T) {
	for _, page := range []pageKind{pageSearch, pageBrowse, pageLiked} {
		t.Run(fmt.Sprint(page), func(t *testing.T) {
			a := newTestApp()
			without(a, "isLiked", "home", "recent", "playlists")
			post := posts(a)
			requests := make(chan chan []music.Track, 2)
			fetch := func() []music.Track {
				reply := make(chan []music.Track, 1)
				requests <- reply
				return <-reply
			}
			fake(a).search = func(string, music.SearchKind, int) (*music.SearchResult, error) {
				return &music.SearchResult{Tracks: fetch()}, nil
			}
			fake(a).browse = func(string) (*music.Browse, error) {
				return &music.Browse{Sections: []music.Section{{Tracks: fetch()}}}, nil
			}
			fake(a).liked = func(int) (*music.Page, error) {
				return &music.Page{Tracks: fetch()}, nil
			}
			a.open(route{page: page, src: "fake", id: "page", query: "海", kind: music.SearchTracks})
			old := wait(t, requests, "original page")
			a.libraryChanged(a.library)
			current := wait(t, requests, "refreshed page")
			current <- []music.Track{song("new", "刷新完成的歌")}
			until(t, post, func() bool {
				switch page {
				case pageSearch:
					return a.searches[a.route].page.loaded && !a.homeLoading
				case pageLiked:
					return a.likedPaged.loaded && !a.homeLoading
				default:
					return a.browses["page"].page != nil && !a.homeLoading
				}
			})
			old <- []music.Track{song("old", "刷新前的歌")}
			runPost(t, post)
			tester := ui.NewTester(a.view, 1200, 800)
			settled(tester)
			if err := tester.Click("刷新完成的歌"); err != nil {
				t.Fatal(err)
			}
			if current := a.pl.Current(); current == nil || current.ID != "new" {
				t.Fatalf("library change retained the old request: %+v", current)
			}
		})
	}
}

// 查询和来源各自恢复浏览位置，返回旧查询不应重新向来源请求已加载的内容。
func TestNavigationSeparatesSearchAndSources(t *testing.T) {
	a, tracks := navigationApp()
	first := route{page: pageSearch, src: "fake", query: "海", kind: music.SearchTracks}
	second := route{page: pageSearch, src: "fake", query: "山", kind: music.SearchTracks}
	for _, query := range []route{first, second} {
		a.searches[query] = &searchData{res: music.SearchResult{Tracks: tracks}, page: paged{loaded: true}}
	}
	asked := fakeSearch(a)
	a.open(first)
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	before := scrollNavigation(t, tester, tracks)
	a.open(second)
	settled(tester)
	if err := tester.Click(tracks[0].Name); err != nil {
		t.Fatal(err)
	}
	tester.Key(0, ui.KeyBack)
	settled(tester)
	after, ok := tester.Find(tracks[20].Name)
	if !ok || after.Y != before.Y || a.query != "海" || a.route.kind != music.SearchTracks {
		t.Fatalf("return mixed search contexts: position=%+v route=%+v query=%q", after, a.route, a.query)
	}
	_, other := addOther(a)
	copied := append([]music.Track(nil), tracks...)
	for index := range copied {
		copied[index].Source, copied[index].Name = "other", fmt.Sprintf("另一来源%02d", index)
	}
	query := first
	query.src = other.id
	other.searches[query] = &searchData{res: music.SearchResult{Tracks: copied}, page: paged{loaded: true}}
	a.open(query)
	settled(tester)
	if err := tester.Click(copied[0].Name); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.Source != other.id || current.ID != "0" {
		t.Fatalf("another source inherited the old position: %+v", current)
	}
	tester.Key(0, ui.KeyBack)
	settled(tester)
	if err := tester.Click(tracks[20].Name); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.Source != "fake" || current.ID != "20" || len(asked) != 0 {
		t.Fatalf("source history failed: current=%+v extra requests=%d", current, len(asked))
	}
}

// 数据重新异步加载时保持恢复意图，多次重绘也只能发出一次同页请求。
func TestNavigationWaitsForReloadedPlaylist(t *testing.T) {
	a, tracks := navigationApp()
	without(a, "isLiked")
	post := posts(a)
	asked := make(chan string, 3)
	reply := make(chan *music.Playlist, 1)
	fake(a).playlist = func(id string) (*music.Playlist, error) {
		asked <- id
		return <-reply, nil
	}
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	before := scrollNavigation(t, tester, tracks)
	a.open(route{page: pageAlbum, id: "detail"})
	settled(tester)
	delete(a.playlists, "long")
	tester.Key(0, ui.KeyBack)
	for range 4 {
		tester.Frame()
	}
	if id := wait(t, asked, "reloaded playlist"); id != "long" || len(asked) != 0 {
		t.Fatalf("unexpected reloads: id=%q additional=%d", id, len(asked))
	}
	reply <- &music.Playlist{ID: "long", Name: "重载歌单", Tracks: tracks}
	runPost(t, post)
	settled(tester)
	after, ok := tester.Find(tracks[20].Name)
	if !ok || after.Y != before.Y {
		t.Fatalf("loading discarded the pending position: %+v -> %+v", before, after)
	}
	if err := tester.Click(tracks[20].Name); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "20" {
		t.Fatalf("reloaded position played %+v", current)
	}
}

// 锚点附近的歌曲被移除时仍能操作相邻歌曲，不出现空白或越界后的错误选播。
func TestNavigationHandlesRemovedAnchor(t *testing.T) {
	a, tracks := navigationApp()
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	scrollNavigation(t, tester, tracks)
	a.playlists["long"].Tracks = append(append([]music.Track(nil), tracks[:14]...), tracks[20:]...)
	settled(tester)
	if err := tester.Click(tracks[23].Name); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "23" {
		t.Fatalf("removed anchor left a stale row action: %+v", current)
	}
}

// 队列抽屉与沉浸播放只覆盖列表，关闭后原位置仍然可选播。
func TestNavigationSurvivesPlaybackOverlays(t *testing.T) {
	a, tracks := navigationApp()
	a.pl.Cue(tracks[:3])
	tester := ui.NewTester(a.view, 1200, 800)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	settled(tester)
	before := scrollNavigation(t, tester, tracks)
	for _, overlay := range []struct{ open, close string }{{"Queue", "Close Queue"}, {"Lyrics", ""}} {
		if err := tester.Click(overlay.open); err != nil {
			t.Fatal(err)
		}
		settled(tester)
		if overlay.close == "" {
			tester.Key(0, ui.KeyBack)
		} else if err := tester.Click(overlay.close); err != nil {
			t.Fatal(err)
		}
		settled(tester)
		after, ok := tester.Find(tracks[20].Name)
		if !ok || after.Y != before.Y {
			t.Fatalf("%s lost browsing: %+v -> %+v", overlay.open, before, after)
		}
	}
	if err := tester.Click(tracks[20].Name); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "20" {
		t.Fatalf("overlay changed the row's action: %+v", current)
	}
}

// 浏览超过容量的页面后旧页安全回顶，最近页面不受无界历史拖累。
func TestNavigationEvictsOldPositions(t *testing.T) {
	a, tracks := navigationApp()
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	scrollNavigation(t, tester, tracks)
	for index := range 34 {
		id := fmt.Sprintf("other-%d", index)
		a.playlists[id] = &music.Playlist{ID: id, Name: id, Tracks: tracks}
		a.plAt[id] = time.Now()
		a.open(route{page: pagePlaylist, id: id})
		tester.Frame()
	}
	a.open(route{page: pagePlaylist, id: "long"})
	settled(tester)
	if err := tester.Click(tracks[0].Name); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "0" {
		t.Fatalf("evicted page did not safely start at the top: %+v", current)
	}
}

// 聚合首页只保留筛选条件，不保留展开面板；打开详情再返回仍能选播筛选结果。
func TestNavigationRestoresAggregateFilters(t *testing.T) {
	a, tracks := navigationApp()
	without(a, "isLiked")
	post := posts(a)
	src := &aggregateRefreshSource{fakeSource: fake(a), refreshes: make(chan string, 1)}
	a.library.src = src
	for index := range tracks {
		tracks[index].AggregateSources = []music.AggregateSource{{Platform: "wy", AccountID: "fixture", TrackID: tracks[index].ID, ListID: "liked"}}
	}
	tracks[40].Name = "唯一目标曲目"
	a.home = &music.Browse{Sections: []music.Section{{Title: "合成曲库", Tracks: tracks}}}
	a.open(route{page: pageHome})
	tester := ui.NewTester(a.view, 1200, 800)
	until(t, post, func() bool {
		view := a.aggregateViews[a.library]
		return view != nil && view.listsLoaded && !view.loadingLists && !view.loadingWriteCaps && !view.loadingWriteGenerations
	})
	settled(tester)
	filter, ok := tester.AccessByRole(ui.RoleTextField, "Filter Tracks")
	if !ok {
		t.Fatal("filter input is not available")
	}
	tester.ClickAt(filter.Bounds.X+10, filter.Bounds.Y+filter.Bounds.H/2)
	tester.Type("唯一目标")
	settled(tester)
	if err := tester.Click("唯一目标曲目"); err != nil {
		t.Fatalf("filter did not make its result playable: %v", err)
	}
	a.open(route{page: pageAlbum, id: "detail"})
	settled(tester)
	tester.Key(0, ui.KeyBack)
	settled(tester)
	if err := tester.Click("唯一目标曲目"); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "40" {
		t.Fatalf("return lost aggregate filters: %+v", current)
	}
}

// 首页、侧栏和收藏目录的迟到错误同样不能打断新账号的浏览。
func TestNavigationIgnoresStaleCatalogErrors(t *testing.T) {
	for _, catalog := range []string{"home", "playlists", "albums", "shows", "stations"} {
		t.Run(catalog, func(t *testing.T) {
			a := newTestApp()
			without(a, "isLiked", "home", "recent", "playlists")
			post := posts(a)
			requests := make(chan chan error, 2)
			fetch := func() error {
				reply := make(chan error, 1)
				requests <- reply
				return <-reply
			}
			src := fake(a)
			a.account = &music.Account{ID: "old"}
			switch catalog {
			case "home":
				a.caps["home"] = true
				src.home = func() (*music.Browse, error) {
					return &music.Browse{Sections: []music.Section{{Tracks: []music.Track{song("new", "当前歌")}}}}, fetch()
				}
				a.loadHome(a.library)
			case "playlists":
				a.caps["playlists"] = true
				src.playlists = func() ([]music.Playlist, error) {
					return []music.Playlist{{ID: "new", Name: "当前歌单", Mine: true}}, fetch()
				}
				a.loadMine(a.library)
			case "albums":
				src.savedAlbums = func(int) (*music.AlbumList, error) { return &music.AlbumList{}, fetch() }
				a.open(route{page: pageSavedAlbums})
			case "shows":
				src.shows = func() ([]music.Show, error) { return nil, fetch() }
				a.open(route{page: pageShows})
			default:
				src.stations = func() ([]music.Station, error) { return nil, fetch() }
				a.open(route{page: pageStations})
			}
			old := wait(t, requests, "old catalog")
			a.account = &music.Account{ID: "new"}
			a.afterAccount(a.library)
			current := wait(t, requests, "current catalog")
			current <- nil
			until(t, post, func() bool {
				if a.homeLoading {
					return false
				}
				switch catalog {
				case "home":
					return a.home != nil
				case "playlists":
					return len(a.mine) > 0
				case "albums":
					return a.savedAlbumsPaged.loaded
				case "shows":
					return a.subscribedOnce
				default:
					return a.stationsOnce
				}
			})
			old <- music.ErrSignIn
			runPost(t, post)
			tester := ui.NewTester(a.view, 1200, 800)
			settled(tester)
			if a.signIn.open {
				t.Fatal("old catalog interrupted the current account with sign-in")
			}
		})
	}
}

// 来源浏览页也通过公共曲目行恢复稳定歌曲，而不是在插入新内容后继续使用旧行号。
func TestNavigationFollowsBrowseRefresh(t *testing.T) {
	a, tracks := navigationApp()
	a.browses["catalog"] = &browseData{at: time.Now(), page: &music.Browse{
		Title: "曲目目录", Sections: []music.Section{{Tracks: tracks}},
	}}
	a.open(route{page: pageBrowse, id: "catalog"})
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	tester.Scroll(650, 420, 0, 550)
	settled(tester)
	before, ok := tester.Find(tracks[15].Name)
	if !ok || before.H == 0 {
		t.Fatal("browse page did not scroll")
	}
	a.browses["catalog"].page.Sections[0].Tracks = append([]music.Track{
		song("new-a", "新歌甲"), song("new-b", "新歌乙"), song("new-c", "新歌丙"),
	}, tracks...)
	settled(tester)
	after, ok := tester.Find(tracks[15].Name)
	if !ok || after.Y != before.Y {
		t.Fatalf("browse refresh moved the stable song: %+v -> %+v", before, after)
	}
	tester.ClickAt(before.X+140, before.Y+before.H/2)
	if current := a.pl.Current(); current == nil || current.ID != "15" {
		t.Fatalf("browse refresh changed the row action: %+v", current)
	}
}

// 艺人数据离页后失效重载，返回仍恢复“专辑”选项和原来的专辑卡片位置。
func TestNavigationRestoresArtistTabAfterReload(t *testing.T) {
	a, tracks := navigationApp()
	without(a, "isLiked")
	post := posts(a)
	albums := make([]music.Album, 40)
	for index := range albums {
		albums[index] = music.Album{ID: fmt.Sprintf("album-%d", index), Name: fmt.Sprintf("作品%02d", index)}
	}
	page := &music.ArtistPage{Artist: music.Artist{ID: "performer", Name: "测试艺人"}, Tracks: tracks, Albums: albums}
	a.artists["performer"] = &artistData{page: page, tab: artistAlbums}
	a.albums["album-25"] = &albumData{album: &music.AlbumPage{Album: albums[25], Tracks: tracks[:1]}}
	fake(a).artist = func(string) (*music.ArtistPage, error) { return page, nil }
	a.open(route{page: pageArtist, id: "performer"})
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	tester.Scroll(650, 420, 0, 1100)
	settled(tester)
	before, ok := tester.Find(albums[25].Name)
	if !ok || before.H == 0 {
		t.Fatal("artist albums did not scroll")
	}
	if err := tester.Click(albums[25].Name); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	delete(a.artists, "performer")
	tester.Key(0, ui.KeyBack)
	runPost(t, post)
	settled(tester)
	after, ok := tester.Find(albums[25].Name)
	if !ok || after.Y != before.Y {
		t.Fatalf("artist lost its tab or position after reload: %+v -> %+v", before, after)
	}
	if err := tester.Click(albums[25].Name); err != nil {
		t.Fatal(err)
	}
	if a.route.page != pageAlbum || a.route.id != "album-25" {
		t.Fatalf("restored artist tab opened %+v", a.route)
	}
}

// 在首页请求尚未结束时禁用再启用来源，应重新加载，旧等待状态不能锁住新首页。
func TestNavigationReloadsHomeAfterSourceReenable(t *testing.T) {
	a := newTestApp()
	without(a, "account", "isLiked", "recent", "playlists")
	post := posts(a)
	requests := make(chan chan *music.Browse, 2)
	fake(a).home = func() (*music.Browse, error) {
		reply := make(chan *music.Browse, 1)
		requests <- reply
		return <-reply, nil
	}
	lib := a.library
	a.loadHome(lib)
	old := wait(t, requests, "original home")
	released := false
	defer func() {
		if !released {
			old <- &music.Browse{}
		}
	}()
	addOther(a)
	tester := ui.NewTester(a.view, 1200, 800)
	a.setPluginEnabled(lib, false)
	settled(tester)
	a.setPluginEnabled(lib, true)
	a.switchSource(lib.id)
	current := wait(t, requests, "reenabled home")
	current <- &music.Browse{Sections: []music.Section{{Tracks: []music.Track{song("new-home", "重新启用后的歌")}}}}
	until(t, post, func() bool { return !lib.homeLoading && lib.home != nil })
	old <- &music.Browse{}
	released = true
	runPost(t, post)
	settled(tester)
	if err := tester.Click("Play"); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "new-home" {
		t.Fatalf("source reenable kept stale home state: %+v", current)
	}
}

// 首页后台刷新失败不丢弃已加载内容，重试成功后才替换成新结果。
func TestNavigationKeepsHomeWhileRefreshFails(t *testing.T) {
	a := newTestApp()
	without(a, "isLiked", "recent")
	post := posts(a)
	a.home = &music.Browse{Sections: []music.Section{{Tracks: []music.Track{song("cached", "已加载歌曲")}}}}
	fake(a).home = func() (*music.Browse, error) { return nil, errors.New("offline") }
	tester := ui.NewTester(a.view, 1200, 800)
	a.loadHome(a.library)
	runPost(t, post)
	settled(tester)
	if err := tester.Click("Play"); err != nil {
		t.Fatalf("refresh failure removed playable cached content: %v", err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "cached" {
		t.Fatalf("refresh failure changed cached playback: %+v", current)
	}
	fake(a).home = func() (*music.Browse, error) {
		return &music.Browse{Sections: []music.Section{{Tracks: []music.Track{song("fresh", "重试后的歌曲")}}}}, nil
	}
	a.loadHome(a.library)
	runPost(t, post)
	settled(tester)
	if err := tester.Click("Play"); err != nil {
		t.Fatal(err)
	}
	if current := a.pl.Current(); current == nil || current.ID != "fresh" {
		t.Fatalf("retry did not publish fresh content: %+v", current)
	}
}

// 旧焦点已经滚出视口时不恢复它，更不能在用户随后滚动时抢走搜索框焦点。
func TestNavigationDoesNotDeferOffscreenTrackFocus(t *testing.T) {
	a, tracks := navigationApp()
	asked := fakeSearch(a)
	tester := ui.NewTester(a.view, 1200, 800)
	settled(tester)
	scrollNavigation(t, tester, tracks)
	if err := tester.Click(tracks[20].Name); err != nil {
		t.Fatal(err)
	}
	tester.Scroll(650, 420, 0, 2600)
	settled(tester)
	a.open(route{page: pageAlbum, id: "detail"})
	settled(tester)
	tester.Key(0, ui.KeyBack)
	settled(tester)
	if err := tester.Click("Search"); err != nil {
		t.Fatal(err)
	}
	tester.Type("新的搜索")
	tester.Scroll(650, 420, 0, -2600)
	settled(tester)
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if a.route.page != pageSearch || a.route.query != "新的搜索" {
		t.Fatalf("offscreen track stole focus while scrolling: %+v", a.route)
	}
	if got := wait(t, asked, "focused search"); got.query != "新的搜索" {
		t.Fatalf("searched %+v", got)
	}
}

// 公共列表的明暗主题、最小窗口、长标题和无封面通过 PNG 目视验收，不断言装饰像素。
func TestNavigationFrames(t *testing.T) {
	directory := framesDir(t)
	language := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(language)
	for _, size := range []struct {
		name          string
		width, height int
	}{{"desktop", 1200, 800}, {"compact", 880, 580}} {
		for _, dark := range []bool{false, true} {
			a, tracks := navigationApp()
			without(a, "isLiked")
			a.playlists["long"].Name = "山海之间 · 继续上一次的音乐旅程"
			a.playlists["long"].ImageURL = "nav-cover"
			a.playlists["long"].Owner = "Oh MyGo Music"
			a.playlists["long"].Description = "浏览歌单、查看专辑，再回到刚才的那首歌。"
			tracks[20].Name = "沿着山谷与海岸继续前行，直到遥远的灯光再一次亮起 · 翻过雪岭，穿过长夜，继续走向记忆里那片没有尽头的海"
			for index := range tracks {
				tracks[index].Artists = []music.Artist{{Name: "逐光乐队 · The Polar Lights"}}
				if index%3 != 1 {
					tracks[index].Album.ImageURL = "nav-cover"
				}
			}
			giveCover(a, "nav-cover", coverImage(ui.RGB(38, 104, 134), ui.RGB(176, 167, 101)))
			a.pl.Cue(tracks[20:23])
			tester := ui.NewTester(a.view, size.width, size.height)
			tester.SetDark(dark)
			tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			settled(tester)
			saveFrame(t, directory, fmt.Sprintf("list-top-%s-dark%t.png", size.name, dark), tester.Image())
			scrollNavigation(t, tester, tracks)
			a.open(route{page: pageAlbum, id: "detail"})
			settled(tester)
			tester.Key(0, ui.KeyBack)
			settled(tester)
			saveFrame(t, directory, fmt.Sprintf("list-restored-%s-dark%t.png", size.name, dark), tester.Image())
		}
	}
}
