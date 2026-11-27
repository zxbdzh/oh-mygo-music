package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// A section whose cards come as a pager shows them a page at a time, as wide as
// the row: the pointer turns the pages, as do Left and Right, and the arrows
// beside the section's title. The last page asks the source for the next batch
// of them, which pages that come after it hold, so the row pages on without
// leaving the page it is on.

// pagerGlide is how a pager goes to the page it was turned to.
var pagerGlide = ui.Animation{Spring: ui.SpringOptions{Duration: 420 * time.Millisecond, Bounce: 0.08}}

// pagerState is a pager's place, and the cards its pages hold beyond the first
// batch, which is what the section came with.
type pagerState struct {
	cards   []any    // music.Station, music.Playlist, or music.Link in batch order
	next    string   // the page of the next batch of cards, none at the end
	loading bool     // the next batch is being asked for
	err     string   // why the next batch didn't come
	off     ui.Point // where the cards rest, dragged sideways
	by      float32  // how far a swipe across them has gone
}

// seed takes the cards the section came with, once.
func (ps *pagerState) seed(sec *music.Section) {
	if sec == nil {
		return
	}
	for _, card := range sec.Stations {
		ps.cards = append(ps.cards, card)
	}
	for _, card := range sec.Playlists {
		ps.cards = append(ps.cards, card)
	}
	for _, card := range sec.Links {
		ps.cards = append(ps.cards, card)
	}
	ps.next = sec.NextPage
}

// count is how many cards the pager holds.
func (ps *pagerState) count() int { return len(ps.cards) }

// pages is how many pages the cards make, cols across.
func (ps *pagerState) pages(cols int) int {
	if cols < 1 {
		cols = 1
	}
	return max(1, (ps.count()+cols-1)/cols)
}

// page is the page shown: the one the cards rest nearest to.
func (ps *pagerState) page(width float32, pages int) int {
	if width <= 0 {
		return 0
	}
	at := int((-ps.off.X + width/2) / width)
	return min(max(at, 0), pages-1)
}

// turn goes dir pages on, or back, as far as the pages reach.
func (ps *pagerState) turn(dir int, width float32, pages int) {
	at := min(max(ps.page(width, pages)+dir, 0), pages-1)
	ps.off = ui.Point{X: -float32(at) * width}
	ps.by = 0
}

// swipe turns a page once a swipe across the cards has gone far enough: to the
// left for the one after, to the right for the one before.
func (ps *pagerState) swipe(dx, width float32, pages int) {
	if width <= 0 {
		return
	}
	ps.by -= dx
	switch {
	case ps.by <= -width/4:
		ps.turn(1, width, pages)
	case ps.by >= width/4:
		ps.turn(-1, width, pages)
	}
}

// pagerSnap comes to rest a page at a time.
func pagerSnap(width float32, pages int) func(ui.Point) ui.Point {
	if pages < 2 {
		return func(ui.Point) ui.Point { return ui.Point{} }
	}
	points := make([]ui.Point, pages)
	for i := range points {
		points[i] = ui.Point{X: -float32(i) * width}
	}
	return ui.Nearest(points...)
}

// pageWidth is how wide a pager's page is: the row's cards across, from edge to
// edge of the page.
func (a *app) pageWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	return w - sidebarWidth - 2*pagePad
}

// pagerStores is where a page's pagers keep their places, made as it shows.
func (a *app) pagerStores(lib *library, pageID string) map[int]*pagerState {
	if lib == nil {
		return nil
	}
	if pageID == "" {
		if lib.homePagers == nil {
			lib.homePagers = map[int]*pagerState{}
		}
		return lib.homePagers
	}
	d := lib.browses[pageID]
	if d == nil {
		return nil
	}
	if d.pagers == nil {
		d.pagers = map[int]*pagerState{}
	}
	return d.pagers
}

// pagerOf is the place of a section's pager, made with the cards it came with.
func (a *app) pagerOf(lib *library, pageID string, section int, sec *music.Section) *pagerState {
	store := a.pagerStores(lib, pageID)
	if store == nil {
		return nil
	}
	ps := store[section]
	if ps == nil {
		ps = &pagerState{}
		ps.seed(sec)
		store[section] = ps
	}
	return ps
}

// loadPager asks the source for the next batch of a pager's cards, which its
// pages go on with once it comes.
func (a *app) loadPager(lib *library, pageID string, section int, page string) {
	store := a.pagerStores(lib, pageID)
	ps := store[section]
	if ps == nil || ps.loading || page == "" {
		return
	}
	ps.loading, ps.err = true, ""
	epoch := lib.pageEpoch
	go func() {
		b, err := answered(lib.src.Browse(page))
		a.post(func() {
			if !a.libraryCurrent(lib, epoch) || a.pagerStores(lib, pageID)[section] != ps {
				return
			}
			ps.loading = false
			if err != nil {
				ps.err = a.problem(lib, err, T("Couldn't load more of these cards"))
				return
			}
			ps.err = ""
			if b != nil {
				for _, sec := range b.Sections {
					// The page is the next batch of the rows that page: those
					// of them a source marks as pages, not a row of modes
					// above them.
					if !sec.Pager || len(sec.Stations)+len(sec.Playlists)+len(sec.Links) == 0 {
						continue
					}
					// A source that expired the cards behind them starts the
					// pages over rather than showing them after the new first
					// batch.
					if sec.ResetPager {
						ps.cards, ps.off, ps.by = nil, ui.Point{}, 0
					}
					ps.seed(&sec)
					return
				}
			}
			ps.next = ""
		})
	}()
}

// cardPager shows a section's cards a page at a time.
func (a *app) cardPager(c *ui.Context, pageID string, section, cols int, sec *music.Section) {
	lib := a.library
	ps := a.pagerOf(lib, pageID, section, sec)
	if ps == nil {
		return
	}
	n, width := ps.count(), a.pageWidth(c)
	pages := ps.pages(cols)
	at := ps.page(width, pages)
	// What comes after the last page is asked for as it shows, so turning to
	// it finds the cards there. A batch that failed waits for the arrow to ask
	// for it again rather than asking every frame.
	if ps.next != "" && !ps.loading && ps.err == "" && at >= pages-1 {
		a.loadPager(lib, pageID, section, ps.next)
	}
	// No room under it: the list keeps its own, and the page's rows fit a
	// window with the one the row above takes already.
	box := ui.Box(c.Key(pagerKey{pageID, section})).Width(width).Shrink(0).Clip().Margin(0, 12, 0, 12)
	// The keys turn the pages of the pager a page shows, whatever the focus is
	// on, as the app's own keys do. A field taking them still comes first.
	c.OnWindowShortcut(0, ui.KeyLeft, func() { ps.turn(-1, width, pages) })
	c.OnWindowShortcut(0, ui.KeyRight, func() { ps.turn(1, width, pages) })
	box.Children(func() {
		row := ui.Row(c.Key(pagerRowKey{pageID, section})).Width(float32(pages) * width).Shrink(0)
		row.Draggable(&ps.off, ui.DraggableOptions{
			Axis:      ui.Horizontal,
			Bounds:    &ui.Rect{X: -float32(pages-1) * width, W: float32(pages-1) * width},
			Snap:      pagerSnap(width, pages),
			Animation: pagerGlide,
		})
		// A swipe across the cards, two fingers on the trackpad, turns them too.
		row.HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputScroll || ev.Momentum || ev.DX*ev.DX <= ev.DY*ev.DY {
				return false
			}
			ps.swipe(ev.DX, width, pages)
			return true
		})
		row.Children(func() {
			for p := range pages {
				ui.Box(c).Width(width).Shrink(0).Children(func() {
					ui.Grid(c).Columns(cols).GapX(20).Children(func() {
						a.pagerCards(c, ps, p*cols, min((p+1)*cols, n))
					})
				})
			}
		})
	})
}

// pagerCards shows the cards of a page, from up to, those of the section first
// and then those of the batches asked for after it.
func (a *app) pagerCards(c *ui.Context, ps *pagerState, from, to int) {
	for i := from; i < to && i < ps.count(); i++ {
		switch card := ps.cards[i].(type) {
		case music.Station:
			a.stationCard(c.Key(card.ID), &card)
		case music.Playlist:
			a.playlistCard(c.Key(card.ID), &card)
		case music.Link:
			if a.coverCard(c.Key(card.Page), card.Title, card.ImageURL, card.Subtitle, nil) {
				a.open(route{page: pageBrowse, id: card.Page})
			}
		}
	}
}

// pagerKey names a pager among the elements of a page.
type pagerKey struct {
	page    string
	section int
}

// pagerRowKey names the row a pager drags, which keeps its own state as it goes.
type pagerRowKey struct {
	page    string
	section int
}

// pagerControls are the arrows that turn a pager's pages, and the page it shows
// of them, beside the section's title.
func (a *app) pagerControls(c *ui.Context, pageID string, section, cols int, sec *music.Section) {
	lib := a.library
	ps := a.pagerOf(lib, pageID, section, sec)
	if ps == nil {
		return
	}
	width := a.pageWidth(c)
	pages := ps.pages(cols)
	at := ps.page(width, pages)
	// More cards may come, so the last page is still a page to turn to.
	more := ps.next != "" || ps.loading || ps.err != ""
	if pages < 2 && !more {
		return
	}
	p := a.pal
	arrow := func(key, label, glyph string, on bool, turn func()) {
		b := ui.ButtonBase(c.Key(key)).Padding(2, 8).Radius(6).FocusRing(false).Label(T(label))
		if !on {
			b.Disabled(true)
		} else if b.Hovered() {
			b.Background(p.hover)
		}
		b.Children(func() {
			color := p.muted
			if !on {
				color = p.faint
			}
			ui.Text(c, glyph).FontSize(15).TextColor(color)
		})
		if on && b.Clicked() {
			turn()
		}
	}
	ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Children(func() {
		arrow("pager-back", "Previous page", "‹", at > 0, func() { ps.turn(-1, width, pages) })
		if ps.err != "" {
			// The batch that didn't come says why, and the arrow asks again.
			ui.Text(c, ps.err).FontSize(12).TextColor(p.muted).MaxLines(1).Margin(0, 8, 0, 0)
		}
		ui.Text(c, fmt.Sprintf("%d / %d", at+1, pages)).FontSize(12).TextColor(p.muted).Margin(0, 6, 0, 6)
		next := at < pages-1 || (more && !ps.loading)
		arrow("pager-on", "Next page", "›", next, func() {
			if ps.err != "" {
				// A batch that didn't come is asked for again, in its place.
				a.loadPager(lib, pageID, section, ps.next)
				return
			}
			ps.turn(1, width, pages)
		})
	})
}
