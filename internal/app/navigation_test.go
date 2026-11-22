package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

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
