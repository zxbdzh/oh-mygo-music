package app

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// pagerCards is cards named Card 1 on, for a row that pages.
func pagerCards(ids ...string) []music.Station {
	cards := make([]music.Station, len(ids))
	for i, id := range ids {
		cards[i] = music.Station{ID: id, Name: id}
	}
	return cards
}

// 首帧窄于侧栏时，多页卡片曾因拖动范围为负而让窗口崩溃。
func TestPagerRendersInNarrowFirstFrame(t *testing.T) {
	a := newTestApp()
	without(a, "isLiked")
	a.home = &music.Browse{Sections: []music.Section{{
		Title: "Explore", Pager: true,
		Stations: pagerCards("Card 1", "Card 2", "Card 3", "Card 4", "Card 5", "Card 6"),
	}}}
	tt := ui.NewTester(a.view, 64, 600)
	tt.Frame()
	if ps := a.library.homePagers[0]; ps == nil || ps.count() != 6 {
		t.Fatal("the narrow frame did not render the home pager")
	}
}

// A pager shows a page of its cards at a time: the arrows and Left and Right
// turn them, and its last page asks for the batch after it, which the pages go
// on with.
func TestPagerTurnsItsPagesAndAsksForMore(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	without(a, "isLiked")
	asked := make(chan string, 8)
	fake(a).browse = func(page string) (*music.Browse, error) {
		asked <- page
		return &music.Browse{Sections: []music.Section{{Pager: true, Stations: pagerCards("Card 7", "Card 8", "Card 9", "Card 10")}}}, nil
	}
	a.home = &music.Browse{Sections: []music.Section{{
		Title: "Explore", Pager: true, NextPage: "explore-2",
		Stations: pagerCards("Card 1", "Card 2", "Card 3", "Card 4", "Card 5", "Card 6"),
	}}}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if _, ok := tt.Find("1 / 2"); !ok {
		t.Fatalf("the pager shows %q, want 1 / 2", tt.Texts())
	}
	if len(asked) != 0 {
		t.Fatal("the first page asked for more before its last page showed")
	}
	if err := tt.Click("Next page"); err != nil {
		t.Fatal(err)
	}
	settled(tt)
	if _, ok := tt.Find("2 / 2"); !ok {
		t.Fatalf("the arrow turned to %q, want 2 / 2", tt.Texts())
	}
	if page := wait(t, asked, "the batch after the last page"); page != "explore-2" {
		t.Fatalf("asked for %q", page)
	}
	runPost(t, ch)
	tt.Frame()
	if _, ok := tt.Find("Card 10"); !ok {
		t.Fatalf("the batch's cards aren't there: %q", tt.Texts())
	}
	// Left and Right turn the pages of the pager the page shows.
	tt.Key(0, ui.KeyLeft)
	settled(tt)
	if _, ok := tt.Find("1 / 2"); !ok {
		t.Fatalf("Left went to %q, want 1 / 2", tt.Texts())
	}
	tt.Key(0, ui.KeyRight)
	settled(tt)
	if _, ok := tt.Find("2 / 2"); !ok {
		t.Fatalf("Right went to %q, want 2 / 2", tt.Texts())
	}
	// The last page, with no batch after it, is as far as it goes.
	tt.Key(0, ui.KeyRight)
	settled(tt)
	if _, ok := tt.Find("2 / 2"); !ok {
		t.Fatalf("past the end it shows %q, want 2 / 2", tt.Texts())
	}
}

// A swipe across the cards, as two fingers on the trackpad make, turns a page,
// and leaves what it started on unplayed.
func TestPagerTurnsBySwiping(t *testing.T) {
	a := newTestApp()
	without(a, "isLiked")
	a.home = &music.Browse{Sections: []music.Section{{
		Title:    "Explore",
		Pager:    true,
		Stations: pagerCards("Card 1", "Card 2", "Card 3", "Card 4", "Card 5", "Card 6"),
	}}}
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	card, ok := tt.Find("Card 2")
	if !ok {
		t.Fatalf("the first page shows %q", tt.Texts())
	}
	x, y := card.X+card.W/2, card.Y+card.H/2
	for range 6 {
		tt.Scroll(x, y, 60, 0)
	}
	settled(tt)
	if _, ok := tt.Find("2 / 2"); !ok {
		t.Fatalf("the swipe shows %q, want 2 / 2", tt.Texts())
	}
	if radio := a.pl.Radio(); radio != "" {
		t.Fatalf("the swipe started %q", radio)
	}
	// The one before it comes back the same way, the other way.
	for range 6 {
		tt.Scroll(x, y, -60, 0)
	}
	settled(tt)
	if _, ok := tt.Find("1 / 2"); !ok {
		t.Fatalf("the swipe back shows %q, want 1 / 2", tt.Texts())
	}
}

// A batch that doesn't come is asked for again when the arrow turns to it,
// rather than turning to a page that isn't there.
func TestPagerAsksAgainForABatchThatFailed(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	without(a, "isLiked")
	asked := make(chan string, 8)
	var tries atomic.Int32
	fake(a).browse = func(page string) (*music.Browse, error) {
		asked <- page
		if tries.Add(1) == 1 {
			return nil, errors.New("offline")
		}
		return &music.Browse{Sections: []music.Section{{Pager: true, Stations: pagerCards("Card 7")}}}, nil
	}
	a.home = &music.Browse{Sections: []music.Section{{
		Title: "Explore", Pager: true, NextPage: "explore-2",
		Stations: pagerCards("Card 1", "Card 2", "Card 3", "Card 4", "Card 5", "Card 6"),
	}}}
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	if err := tt.Click("Next page"); err != nil {
		t.Fatal(err)
	}
	settled(tt)
	if page := wait(t, asked, "the batch after the last page"); page != "explore-2" {
		t.Fatalf("asked for %q", page)
	}
	runPost(t, ch)
	settled(tt)
	if len(asked) != 0 {
		t.Fatal("a batch that failed is asked for again without the arrow")
	}
	if err := tt.Click("Next page"); err != nil {
		t.Fatal(err)
	}
	if page := wait(t, asked, "the batch asked for again"); page != "explore-2" {
		t.Fatalf("asked for %q", page)
	}
	runPost(t, ch)
	settled(tt)
	if _, ok := tt.Find("Card 7"); !ok {
		t.Fatalf("the batch asked for again isn't there: %q", tt.Texts())
	}
}

// The next batch of cards is the page's pager section, not the row of modes
// above it, and one that starts the pages over replaces them rather than
// following what the pager held.
func TestPagerTakesOnlyItsOwnSectionAndStartsOver(t *testing.T) {
	a := newTestApp()
	ch := posts(a)
	without(a, "isLiked")
	asked := make(chan string, 8)
	fake(a).browse = func(page string) (*music.Browse, error) {
		asked <- page
		return &music.Browse{Sections: []music.Section{
			{Title: "Modes", Stations: pagerCards("Mode 1", "Mode 2")},
			{Title: "Explore", Pager: true, ResetPager: true, Stations: pagerCards("Fresh 1", "Fresh 2")},
		}}, nil
	}
	a.home = &music.Browse{Sections: []music.Section{{
		Title: "Explore", Pager: true, NextPage: "explore-2",
		Stations: pagerCards("Card 1", "Card 2", "Card 3", "Card 4", "Card 5", "Card 6"),
	}}}
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	if err := tt.Click("Next page"); err != nil {
		t.Fatal(err)
	}
	wait(t, asked, "the batch after the last page")
	runPost(t, ch)
	settled(tt)
	if _, ok := tt.Find("Fresh 2"); !ok {
		t.Fatalf("the batch that started over isn't there: %q", tt.Texts())
	}
	if _, ok := tt.Find("Card 1"); ok {
		t.Fatalf("the pages went on from the cards before them: %q", tt.Texts())
	}
	if _, ok := tt.Find("Mode 1"); ok {
		t.Fatalf("the modes row above the pager came into it: %q", tt.Texts())
	}
}

// A section that shows one row builds the cards the row holds, and leaves the
// rest of them to its page.
func TestOneRowSectionBuildsTheFirstRowOnly(t *testing.T) {
	a := newTestApp()
	without(a, "isLiked")
	a.home = &music.Browse{Sections: []music.Section{{
		Title: "Modes", OneRow: true, MorePage: "modes",
		Stations: pagerCards("Mode 1", "Mode 2", "Mode 3", "Mode 4", "Mode 5", "Mode 6"),
	}}}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if _, ok := tt.Find("Mode 5"); !ok {
		t.Fatalf("the row is short: %q", tt.Texts())
	}
	if _, ok := tt.Find("Mode 6"); ok {
		t.Fatalf("the row's second line is there: %q", tt.Texts())
	}
}
