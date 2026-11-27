package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/ai"
	"mygo-music/internal/music"
)

// After a like the suggestion stays in a bar above the player, with Add,
// Adjust and a way to close it, rather than in a toast that goes away.
func TestLikeShowsPersistentSuggestionBar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"10","confidence":0.9,"reason":"适合通勤"}`,
			}}},
		})
	}))
	defer srv.Close()

	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = srv.URL
	a.settings.AIModel = "m"
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	tr := song("1", "夜曲", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.account = &music.Account{ID: "1"}

	f := fake(a)
	f.like = func(id string, on bool) error { return nil }
	ch := posts(a)
	a.likeCurrent()
	until(t, ch, func() bool { return a.aiBar.open })

	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	texts := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"通勤", T("Add"), T("Adjust"), T("Close")} {
		if !strings.Contains(texts, want) {
			t.Errorf("bar misses %q: %q", want, texts)
		}
	}
	if a.aiToast != nil {
		t.Error("the suggestion still moved as a toast")
	}
}

// Closing the bar drops the suggestion without adding anything.
func TestSuggestionBarCloseKeepsTheSongOutOfPlaylists(t *testing.T) {
	var added []string
	a := newTestApp()
	a.aiBar = aiBarState{open: true, lib: a.library, track: song("1", "夜曲"), pick: ai.Suggestion{PlaylistID: "10"}, name: "通勤"}
	f := fake(a)
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id)
		return nil
	}
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	if err := tt.Click(T("Close")); err != nil {
		t.Fatal(err)
	}
	settled(tt)
	if a.aiBar.open {
		t.Error("the bar is still open")
	}
	if len(added) != 0 {
		t.Errorf("closed the bar but added %v", added)
	}
}

// Add on the bar writes the suggested playlist.
func TestSuggestionBarAdds(t *testing.T) {
	var added []string
	a := newTestApp()
	a.aiBar = aiBarState{open: true, lib: a.library, track: song("1", "夜曲"), pick: ai.Suggestion{PlaylistID: "10"}, name: "通勤"}
	f := fake(a)
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id+":"+tracks[0])
		return nil
	}
	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	if err := tt.Click(T("Add")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return len(added) == 1 })
	if a.aiBar.open {
		t.Error("the bar stayed open after adding")
	}
	if added[0] != "10:1" {
		t.Errorf("added = %v", added)
	}
}

// Adjust opens the conversation about the suggestion, and adopting the new
// pick learns what the listener said.
func TestAdjustAdoptsAndLearns(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"11","confidence":0.95,"reason":"更安静"}`,
			}}},
		})
	}))
	defer srv.Close()

	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = srv.URL
	a.settings.AIModel = "m"
	a.account = &music.Account{ID: "u1"}
	a.libs[0].account = &music.Account{ID: "u1"}
	a.aiBar = aiBarState{open: true, lib: a.library, track: song("1", "夜曲"), pick: ai.Suggestion{PlaylistID: "10"}, name: "通勤"}
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}, {ID: "11", Name: "睡前", Mine: true}}

	var added []string
	f := fake(a)
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id)
		return nil
	}
	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	if err := tt.Click(T("Adjust")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return len(a.aiRefine.candidates) == 2 })
	settled(tt)

	n, ok := tt.AccessByRole(ui.RoleTextField, T("How should it change?"))
	if !ok {
		t.Fatalf("no adjustment field: %q", tt.Texts())
	}
	r := n.Bounds
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Type("太吵了，换安静一点的")
	if err := tt.Click(T("Send")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return a.aiRefine.learned != "" })
	settled(tt)
	if err := tt.Click(T("Use This")); err != nil {
		t.Fatalf("%v; texts = %q", err, tt.Texts())
	}
	until(t, ch, func() bool { return len(added) == 1 })
	if calls == 0 {
		t.Error("the model was not asked")
	}
	if added[0] != "11" {
		t.Errorf("added = %v, want the refined 11", added)
	}
	prefs := a.aiPrefs.forListener("fake", "u1")
	if len(prefs) != 1 || prefs[0].Text != "太吵了，换安静一点的" || prefs[0].PlaylistID != "11" {
		t.Errorf("learned = %+v", prefs)
	}
}
