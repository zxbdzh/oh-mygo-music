package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	// 推荐条先在搜索阶段就出现，等判断完才给出可操作的歌单。
	until(t, ch, func() bool { return a.aiBar.open })
	until(t, ch, func() bool { return a.aiBar.stage == aiBarDone })

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
	requests := make(chan []ai.Message, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []ai.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body.Messages
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"11","confidence":0.95,"reason":"更安静"}`,
			}}},
		})
	}))
	defer srv.Close()

	searched := make(chan string, 1)
	useFakeSearch(t, fakeSearchProvider{queried: searched})
	a := newTestApp()
	preferencesFile := filepath.Join(t.TempDir(), "ai-preferences.json")
	a.aiPrefs = newPreferenceStore(preferencesFile)
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = srv.URL
	a.settings.AIModel = "m"
	a.settings.AIWebProvider = "tavily"
	a.account = &music.Account{ID: "u1"}
	a.libs[0].account = &music.Account{ID: "u1"}
	tr := song("1", "夜曲", "周杰伦")
	tr.Album.Name = "十一月的萧邦"
	a.aiBar = aiBarState{open: true, lib: a.library, track: tr, pick: ai.Suggestion{PlaylistID: "10"}, name: "通勤"}
	mine := []music.Playlist{{ID: "10", Name: "通勤", Mine: true}, {ID: "11", Name: "睡前", Mine: true}}
	a.mine = mine

	var added []string
	f := fake(a)
	f.playlists = func() ([]music.Playlist, error) { return mine, nil }
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
	wait(t, requests, "the first adjustment")
	settled(tt)
	input, ok := tt.AccessByRole(ui.RoleTextField, T("How should it change?"))
	if !ok {
		t.Fatal("the adjustment field disappeared")
	}
	tt.ClickAt(input.Bounds.X+10, input.Bounds.Y+10)
	tt.Type("适合晚上听")
	if err := tt.Click(T("Send")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return !a.aiRefine.busy })
	wait(t, requests, "the second adjustment")
	settled(tt)
	if err := tt.Click(T("Use This")); err != nil {
		t.Fatalf("%v; texts = %q", err, tt.Texts())
	}
	until(t, ch, func() bool { return len(a.aiPrefs.forListener("fake", "u1")) == 1 })
	if added[0] != "11" {
		t.Errorf("added = %v, want the refined 11", added)
	}
	a.aiPrefs = newPreferenceStore(preferencesFile)
	prefs := a.aiPrefs.forListener("fake", "u1")
	if len(prefs) != 1 || prefs[0].Text != "太吵了，换安静一点的\n适合晚上听" || prefs[0].PlaylistID != "11" {
		t.Fatalf("learned = %+v", prefs)
	}
	if prefs[0].TrackID != tr.ID || prefs[0].TrackName != tr.Name ||
		prefs[0].Artists != tr.ArtistNames() || prefs[0].Album != tr.Album.Name {
		t.Errorf("learned song = %+v", prefs[0])
	}

	// 下一次喜欢同一首歌会沿用采纳记录，直接让模型推荐歌单。
	runPost(t, ch)
	a.likedChanged(a.library, tr, true)
	until(t, ch, func() bool { return a.aiBar.stage == aiBarDone })
	var reusedPrompt strings.Builder
	for _, message := range wait(t, requests, "the follow-up recommendation") {
		if message.Role == "system" {
			reusedPrompt.WriteString(message.Content)
		}
	}
	for _, want := range []string{"夜曲", "十一月的萧邦", "睡前", "太吵了，换安静一点的", "适合晚上听"} {
		if !strings.Contains(reusedPrompt.String(), want) {
			t.Errorf("follow-up prompt misses %q: %q", want, reusedPrompt.String())
		}
	}
	select {
	case query := <-searched:
		t.Errorf("searched for a song with a learned adjustment: %q", query)
	default:
	}
}

// 加入歌单失败时，不能把失败的结果保存成已采纳偏好。
func TestFailedRefinedAddDoesNotLearn(t *testing.T) {
	a := newTestApp()
	a.aiRefine = aiRefineState{
		open: true, lib: a.library, track: song("1", "夜曲"),
		pick: ai.Suggestion{PlaylistID: "10"}, learned: "更安静",
	}
	fake(a).addTo = func(string, []string) error { return music.ErrUnsupported }
	ch := posts(a)
	a.adoptRefined()
	runPost(t, ch)
	if got := a.aiPrefs.forListener("fake", ""); len(got) != 0 {
		t.Fatalf("saved a preference after a failed add: %+v", got)
	}
}

// The manual AI dialog can also be adjusted before it writes anything.
func TestManualSuggestionCanBeAdjusted(t *testing.T) {
	requests := make(chan []json.RawMessage, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body.Tools
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
	mine := []music.Playlist{{ID: "10", Name: "通勤", Mine: true}, {ID: "11", Name: "睡前", Mine: true}}
	a.mine = mine
	a.aiSuggestion = aiSuggestionState{open: true, lib: a.library, track: song("1", "夜曲"), pick: ai.Suggestion{PlaylistID: "10"}, name: "通勤"}

	var added []string
	f := fake(a)
	f.playlists = func() ([]music.Playlist, error) { return mine, nil }
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id)
		return nil
	}
	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	if err := tt.Click(T("Adjust")); err != nil {
		t.Fatalf("%v; texts = %q", err, tt.Texts())
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
	until(t, ch, func() bool { return len(a.aiPrefs.forListener("fake", "u1")) == 1 })
	if added[0] != "11" {
		t.Errorf("added = %v, want the refined 11", added)
	}
	if a.aiSuggestion.open {
		t.Error("the manual suggestion stayed open after adopting")
	}
	prefs := a.aiPrefs.forListener("fake", "u1")
	if len(prefs) != 1 || prefs[0].Text != "太吵了，换安静一点的" {
		t.Errorf("learned = %+v", prefs)
	}
	wait(t, requests, "the manual adjustment")
	runPost(t, ch)
	a.settings.AIWebProvider = "native"
	tr := song("1", "夜曲")
	a.openAISuggestion(&tr)
	until(t, ch, func() bool { return !a.aiSuggestion.busy })
	if a.aiSuggestion.failed {
		t.Fatal("could not reuse the manual adjustment")
	}
	if tools := wait(t, requests, "the next manual suggestion"); len(tools) != 0 {
		t.Fatalf("enabled native web search for an already learned song: %s", tools)
	}
}
