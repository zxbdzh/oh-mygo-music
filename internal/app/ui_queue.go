package app

import (
	"github.com/egoist/mygo/ui"
)

type queueViewState struct {
	list      ui.ListState
	current   uint64
	following bool
}

func (a *app) queueDrawerButton(c *ui.Context) {
	p := a.pal
	color := p.muted
	if a.queueOpen {
		color = p.accent
	}
	button := iconButton(c.Key("queue"), iconQueue, 16, color, T("Queue")).Bind(&a.queueButton)
	if button.Clicked() {
		a.queueOpen = !a.queueOpen
		if a.queueOpen {
			a.queueDrawer.current = 0
		}
	}
	_, height := c.Size()
	panel := ui.PopoverBase(c.Key("queue-drawer"), button, &a.queueOpen, func(panel ui.Element) {
		width, _ := c.Size()
		panel.AttachTo(button, ui.AnchorTopEnd, ui.AnchorBottomEnd).
			Margin(10, 0, 10, 0).Width(min(400, width-sidebarWidth-24)).
			Height(max(200, height-playerBarHeight-topBar(c)-32)).
			Radius(14).Background(p.sidebar).Border(1, p.hairline).
			Shadow(0, 8, 28, -4, ui.RGBA(0, 0, 0, 0.18)).TextColor(p.text).Padding(12).Gap(10)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconQueue).Size(18, 18).TextColor(p.muted)
			ui.Text(c, T("Up Next")).FontSize(16).FontWeight(650).Grow(1)
			ui.Textf(c, "%d", len(a.pl.Queue())).FontSize(12).TextColor(p.muted)
			close := iconButton(c.Key("close-queue"), iconX, 16, p.muted, T("Close Queue")).AutoFocus()
			close.OnClick(func() {
				a.queueOpen = false
				a.queueButton.Focus(c)
			})
		})
		if ui.Button(c.Key("locate-current"), T("Locate Current Track")).Disabled(a.pl.Current() == nil).Clicked() {
			a.queueDrawer.current = 0
		}
		a.queueRows(c, &a.queueDrawer, p.text)
	})
	if panel.Dismissed() {
		a.queueButton.Focus(c)
	}
}

func (a *app) queueRows(c *ui.Context, state *queueViewState, ink ui.Color) {
	pl := a.pl
	order, at := pl.PlayOrder()
	if len(order) == 0 {
		ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
			ui.Icon(c, iconQueue).Size(30, 30).TextColor(ink.Alpha(0.35))
			ui.Text(c, T("The queue is empty")).FontSize(13).TextColor(ink.Alpha(0.6))
		})
		return
	}
	current := uint64(0)
	if at >= 0 && at < len(order) {
		current = pl.QueueEntryID(order[at])
	}
	if state.current == 0 {
		state.following = true
		state.list.ScrollTo(max(at, 0), ui.Start)
	} else {
		first, last := state.list.Visible()
		previous := at
		if current != state.current {
			previous = -1
			for row, index := range order {
				if pl.QueueEntryID(index) == state.current {
					previous = row
					break
				}
			}
		}
		if previous >= 0 && (previous < first || previous > last) {
			// 用户已离开当前项时停止跟随，不能因自然切歌抢回浏览位置。
			state.following = false
		}
		if current != state.current && state.following && at >= 0 {
			state.list.ScrollIntoView(at)
		}
	}
	state.current = current
	state.list.Key = func(row int) any { return pl.QueueEntryID(order[row]) }
	ui.List(c.Key("queue"), &state.list, len(order), func(rowIndex int) {
		index := order[rowIndex]
		entryID := pl.QueueEntryID(index)
		track := pl.Queue()[index]
		row := ui.Row(c.Key(entryID)).Height(58).PaddingX(8).Gap(6).Radius(9).AlignItems(ui.Center).
			ContextMenu(a.songItems(&track, ""))
		if rowIndex == at {
			row.Background(ink.Alpha(0.09))
		} else if row.Hovered() {
			row.Background(ink.Alpha(0.05))
		}
		row.Children(func() {
			play := ui.ButtonBase(c.Key("play")).Grow(1).Shrink(1).MinWidth(0).Gap(10).
				AlignItems(ui.Center).Justify(ui.Start).Label(track.Name).Disabled(track.Unavailable)
			play.Children(func() {
				a.cover(c, track.Album.ImageURL, 96, 38, 6)
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, track.Name).FontSize(13).FontWeight(550).SingleLine().TextColor(ink)
					ui.Text(c, track.ArtistNames()).FontSize(11.5).SingleLine().TextColor(ink.Alpha(0.55))
				})
				ui.Text(c, clock(track.Length())).FontSize(11).FontFeatures("tnum").TextColor(ink.Alpha(0.45))
			})
			play.OnClick(func() { pl.JumpQueued(entryID) })
			iconButton(c.Key("remove"), iconX, 14, ink.Alpha(0.5), Tf("Remove from Queue: %s", track.Name)).
				OnClick(func() { pl.RemoveQueued(entryID) })
		})
	}).Grow(1).MinHeight(0).Padding(4, 0, 12, 0).Gap(2).
		HandleInput(func(event ui.InputEvent) bool {
			if event.Kind == ui.InputScroll || event.Kind == ui.InputPointerDown {
				state.following = false
			}
			return false
		})
}
