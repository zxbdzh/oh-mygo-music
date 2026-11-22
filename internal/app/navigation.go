package app

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

const pagePositionLimit = 32

type pagePosition struct {
	page      pageIdentity
	list      ui.ListState
	scroll    ui.ScrollState
	focus     trackRowFocus
	filters   aggregateFilterState
	artistTab int
}

type pageIdentity struct {
	route
	owner    *library
	listener string
}

func (a *app) currentPageIdentity() pageIdentity {
	lib := a.libOf(a.route.src)
	if lib == nil {
		return pageIdentity{route: a.route}
	}
	return pageIdentity{a.route, lib, lib.listener()}
}

func (a *app) restorePagePosition() {
	page := a.currentPageIdentity()
	if a.scrolled == page {
		return
	}
	next := pagePosition{page: page, artistTab: -1}
	for index, position := range a.positions {
		if position.page == page {
			next = position
			a.positions = slices.Delete(a.positions, index, index+1)
			break
		}
	}
	if lib := a.libOf(a.scrolled.src); lib != nil && lib == a.scrolled.owner && lib.listener() == a.scrolled.listener {
		focus := a.pageFocus
		focus.restore = false
		if focus.valid && len(a.rowKeys.keys) > 0 {
			first, last := a.trackList.Visible()
			row := slices.Index(a.rowKeys.keys, focus.key) + 1
			focus.restore = row > 0 && row >= first && row <= last
		}
		// 当前页单独持有状态，历史只保留最近使用的有限页面，不落盘保存浏览历史。
		if len(a.positions) >= pagePositionLimit-1 {
			a.positions = slices.Delete(a.positions, 0, 1)
		}
		a.positions = append(a.positions, pagePosition{
			page: a.scrolled, list: a.trackList, scroll: a.pageScroll,
			focus: focus, filters: a.pageFilters, artistTab: a.artistTab,
		})
	}
	a.pageFocusHandle.CancelFocus()
	// 只有一个浏览容器在场，复用句柄，避免每次访问新页面都向窗口注册永久句柄。
	next.list.Handle, next.scroll.Handle = a.trackList.Handle, a.pageScroll.Handle
	a.trackList, a.pageScroll, a.scrolled = next.list, next.scroll, page
	a.rowKeys = trackRowKeys{}
	a.pageFocus = next.focus
	a.pageFilters = next.filters
	a.artistTab, a.restoreArtistTab = next.artistTab, next.artistTab >= 0
}

func (a *app) discardPagePositions(lib *library) {
	delete(a.aggregateViews, lib)
	a.positions = slices.DeleteFunc(a.positions, func(position pagePosition) bool { return position.page.owner == lib })
	if a.scrolled.owner == lib {
		// 延迟到 content 再换状态，避免在列表头的操作里重置仍在构建的列表。
		a.scrolled = pageIdentity{}
	}
}

func (a *app) resetPagePosition(r route) {
	a.positions = slices.DeleteFunc(a.positions, func(position pagePosition) bool { return position.page.route == r })
	if a.scrolled.route == r {
		a.scrolled = pageIdentity{}
	}
}

type trackRowKey struct {
	source, id string
	occurrence int
}

type trackRowFocus struct {
	key     trackRowKey
	valid   bool
	restore bool
}

func (a *app) trackFocus(c *ui.Context, row ui.Element, tracks []music.Track, index int) {
	if !sameList(a.rowKeys.tracks, tracks) {
		return
	}
	focus := &a.pageFocus
	key := a.rowKeys.keys[index]
	focused := row.FocusWithin()
	if focused {
		focus.key = key
		focus.valid = true
	}
	if focus.valid && focus.key == key {
		row.Bind(&a.pageFocusHandle)
		if focus.restore {
			// 只恢复已重新构建的行，不为迟到数据永久排队一个隐藏控件的焦点请求。
			a.pageFocusHandle.Focus(c)
			focus.restore = false
		}
	}
}

type aggregateFilterState struct {
	kind, platform, list, query string
}

type trackRowKeys struct {
	tracks []music.Track
	keys   []trackRowKey
}

func (keys *trackRowKeys) update(tracks []music.Track) {
	if sameList(keys.tracks, tracks) {
		return
	}
	keys.tracks = tracks
	keys.keys = make([]trackRowKey, len(tracks))
	counts := make(map[[2]string]int, len(tracks))
	for index, track := range tracks {
		identity := [2]string{track.Source, track.ID}
		// 同曲重复出现仍是不同位置；不同来源的同名 ID 也不能合成一个锚点。
		keys.keys[index] = trackRowKey{track.Source, track.ID, counts[identity]}
		counts[identity]++
	}
}
