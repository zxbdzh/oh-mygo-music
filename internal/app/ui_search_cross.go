package app

import (
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

// crossSearch 是“全部来源”的一次搜索：每个能搜的来源各占一组，互不等待。
type crossSearch struct {
	query string
	kind  music.SearchKind
	epoch uint64 // 本轮的世代，上一轮的迟到响应按它丢弃
	// filter 是按来源筛选时留下的源 ID，"" 表示全部显示；它只筛已加载的结果。
	filter string
	groups []*crossGroup
}

// crossGroup 是一个来源在这次搜索里的结果和分页状态。
type crossGroup struct {
	lib  *library
	res  music.SearchResult
	page paged
}

// settled says every group has answered, one way or another.
func (cs *crossSearch) settled() bool {
	for _, g := range cs.groups {
		if g.page.loading {
			return false
		}
	}
	return true
}

// crossSearchFor returns this query's groups, searching every source anew
// when the page shows another query.
func (a *app) crossSearchFor(r route) *crossSearch {
	if r.query == "" {
		return nil
	}
	if cs := a.cross; cs != nil && cs.query == r.query && cs.kind == r.kind {
		return cs
	}
	return a.startCrossSearch(r)
}

// startCrossSearch asks every source that can search at once, each into its
// own group, so a slow or broken one holds up no other.
func (a *app) startCrossSearch(r route) *crossSearch {
	a.crossEpoch++
	cs := &crossSearch{query: r.query, kind: r.kind, epoch: a.crossEpoch}
	for _, lib := range a.libs {
		if lib.can("search") {
			cs.groups = append(cs.groups, &crossGroup{lib: lib, page: paged{loading: true}})
		}
	}
	a.cross = cs
	for _, g := range cs.groups {
		go a.askCrossGroup(cs, g, 0)
	}
	return cs
}

// askCrossGroup loads one group from offset, off the main thread. 首屏（offset
// 为 0）替换整组；重试或续页只追加，已经显示的结果不会闪掉。
func (a *app) askCrossGroup(cs *crossSearch, g *crossGroup, offset int) {
	lib := g.lib
	res, err := answered(lib.src.Search(cs.query, cs.kind, offset))
	a.post(func() {
		// 换过关键词或离开页面后，这份结果属于旧的一轮，不能再改写。
		if a.cross != cs || cs.epoch != a.crossEpoch {
			return
		}
		g.page.loading = false
		if err != nil {
			g.page.err = a.problem(lib, err, T("The search failed"))
			return
		}
		if offset == 0 {
			g.res = *res
		} else {
			g.res.Tracks = append(g.res.Tracks, res.Tracks...)
			g.res.More = res.More
		}
		// 来源没写自己的 ID 时补上，播放时才能找回它。
		for i := range g.res.Tracks {
			if g.res.Tracks[i].Source == "" {
				g.res.Tracks[i].Source = lib.id
			}
		}
		g.page.loaded = true
		g.page.more = res.More && res.Len() > 0
	})
}

// retryCrossGroup 只重发失败的这个来源，其它组不跟着重来。
func (a *app) retryCrossGroup(g *crossGroup) {
	cs := a.cross
	if cs == nil || g.page.loading {
		return
	}
	g.page.loading, g.page.err = true, ""
	go a.askCrossGroup(cs, g, len(g.res.Tracks))
}

// moreCrossGroup 只让这个来源从自己已加载的数量续页，别的组不受影响。
func (a *app) moreCrossGroup(g *crossGroup) {
	cs := a.cross
	if cs == nil || g.page.loading || !g.page.more {
		return
	}
	g.page.loading = true
	go a.askCrossGroup(cs, g, len(g.res.Tracks))
}

func (a *app) crossSearchPage(c *ui.Context) {
	cs := a.crossSearchFor(a.route)
	if cs == nil {
		a.scrollPage(c, func() { a.heading(c, T("Search"), T("Search · all sources")) })
		return
	}
	a.scrollPage(c, func() {
		a.crossHeader(c, cs)
		for _, g := range cs.groups {
			if cs.filter != "" && g.lib.id != cs.filter {
				continue
			}
			a.crossGroupSection(c, g)
		}
	})
}

// crossHeader is what was searched for across sources, and the switch back.
func (a *app) crossHeader(c *ui.Context, cs *crossSearch) {
	sub := T("Search · all sources")
	if cs.settled() {
		n := 0
		for _, g := range cs.groups {
			n += len(g.res.Tracks)
		}
		sub = Tf("%s · all sources", i18n.Songs(n))
	}
	a.heading(c, "“"+cs.query+"”", sub)
	a.searchScopeControl(c)
	a.crossSourceChips(c, cs)
}

// crossSourceChips 在“全部来源”的结果里按来源筛选；切换只影响显示，
// 已经拿到的结果和分页状态都保留，不会重新请求。
func (a *app) crossSourceChips(c *ui.Context, cs *crossSearch) {
	ui.Row(c).Wrap().Gap(8).Margin(0, 0, 16, 0).Children(func() {
		a.crossChip(c, cs, "", T("All"))
		for _, g := range cs.groups {
			a.crossChip(c, cs, g.lib.id, g.lib.name())
		}
	})
}

func (a *app) crossChip(c *ui.Context, cs *crossSearch, id, label string) {
	p := a.pal
	on := cs.filter == id
	chip := ui.ButtonBase(c.Key("cross-filter:" + id)).Height(26).PaddingX(11).Radius(999).
		Center().FocusRing(false).Label(label)
	if on {
		chip.Background(p.accent.Alpha(0.14)).Border(1, p.accent.Alpha(0.5))
	} else {
		chip.Background(p.hover).Border(1, p.hairline)
	}
	chip.Children(func() {
		text := p.muted
		if on {
			text = p.accent
		}
		ui.Text(c, label).FontSize(12).TextColor(text).SingleLine()
	})
	if chip.Clicked() {
		cs.filter = id
	}
}

// searchScopeControl switches between the active source and every source.
// 跨源首版只搜歌曲，切过去时从别的分类回到歌曲。
func (a *app) searchScopeControl(c *ui.Context) {
	at := 0
	if a.settings.SearchAll {
		at = 1
	}
	ui.Row(c).Margin(0, 0, 14, 0).Children(func() {
		if !segmented(c.Key("search-scope"), &at, T("This Source"), T("All Sources")) {
			return
		}
		a.settings.SearchAll = at == 1
		a.settings.save()
		r := a.route
		if a.settings.SearchAll && r.kind != music.SearchTracks {
			r.kind = music.SearchTracks
			a.resetPagePosition(r)
			a.show(r)
		}
		a.changed()
	})
}

// crossGroupSection is one source's heading and songs; its own loading and
// error stay here, away from the other sources' groups.
func (a *app) crossGroupSection(c *ui.Context, g *crossGroup) {
	p := a.pal
	ui.Column(c).Margin(0, 0, 20, 0).Children(func() {
		ui.Row(c).Height(30).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, g.lib.name()).FontSize(13).FontWeight(700)
			switch {
			case g.page.loading:
				ui.Text(c, T("Loading…")).FontSize(12).TextColor(p.muted)
			case g.page.err != "":
				ui.Text(c, g.page.err).FontSize(12).TextColor(p.muted)
				ui.Box(c).Width(4)
				if ui.Button(c.Key("cross-retry:"+g.lib.id), T("Retry")).Height(24).PaddingX(10).Clicked() {
					a.retryCrossGroup(g)
				}
			case g.page.loaded:
				ui.Text(c, i18n.Songs(len(g.res.Tracks))).FontSize(12).TextColor(p.muted)
				if g.page.more {
					ui.Box(c).Width(4)
					if ui.Button(c.Key("cross-more:"+g.lib.id), T("Load More")).Height(24).PaddingX(10).Clicked() {
						a.moreCrossGroup(g)
					}
				}
			}
		})
		switch {
		case g.page.loading && !g.page.loaded:
			return
		case g.page.err != "" && len(g.res.Tracks) == 0:
			return
		case len(g.res.Tracks) == 0:
			ui.Text(c, T("Nothing found")).FontSize(12.5).TextColor(p.muted).Padding(2, 12, 10, 12)
		default:
			for i := range g.res.Tracks {
				a.trackRow(c, g.res.Tracks, i, true, "")
			}
		}
	})
}
