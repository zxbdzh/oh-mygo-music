package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/ai"
	"mygo-music/internal/music"
	"mygo-music/internal/websearch"
)

// The song menu offers the AI playlist suggestion when the listener turned it
// on, beside the plain add-to-playlist items.
func TestSongMenuOffersAIPlaylistSuggestion(t *testing.T) {
	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = "https://example.com/v1"
	a.settings.AIModel = "m"
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "List", Tracks: []music.Track{song("1", "夜曲", "周杰伦")}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	if err := tt.RightClick("夜曲"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tt.Menu(), "\n"), T("AI Add to Playlist…")) {
		t.Fatalf("menu = %q", tt.Menu())
	}
}

// Choosing the AI item shows the model's pick and writes nothing until the
// listener confirms it.
func TestAISuggestionConfirmsBeforeWriting(t *testing.T) {
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
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "List", Tracks: []music.Track{song("1", "夜曲", "周杰伦")}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}

	var added []string
	f := fake(a)
	f.playlist = func(id string) (*music.Playlist, error) {
		return &music.Playlist{ID: id, Name: "通勤", Tracks: []music.Track{song("2", "稻香", "周杰伦")}}, nil
	}
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id+":"+strings.Join(tracks, ","))
		return nil
	}

	ch := posts(a)
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if err := tt.RightClick("夜曲"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(T("AI Add to Playlist…")); err != nil {
		t.Fatalf("%v; menu = %q", err, tt.Menu())
	}
	until(t, ch, func() bool { return !a.aiSuggestion.busy })
	tt.Frame()

	texts := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(texts, "通勤") || !strings.Contains(texts, "适合通勤") {
		t.Fatalf("dialog texts = %q", texts)
	}
	if len(added) != 0 {
		t.Fatalf("wrote before confirming: %v", added)
	}
	if err := tt.Click(T("Add")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return len(added) == 1 })
	if added[0] != "10:1" {
		t.Errorf("added = %v, want 10:1", added)
	}
}

// A confirmed like asks for a suggestion and shows it until the listener
// answers the bar, instead of letting it expire as a toast.
func TestLikingSuggestsPlaylistAfterSourceConfirms(t *testing.T) {
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

	liked := false
	f := fake(a)
	f.like = func(id string, on bool) error {
		liked = on
		return nil
	}
	ch := posts(a)
	a.likeCurrent()
	until(t, ch, func() bool { return a.aiBar.open })
	if !liked {
		t.Fatal("the source was not asked to like the song")
	}
	until(t, ch, func() bool { return a.aiBar.stage == aiBarDone })

	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "通勤") {
		t.Fatalf("texts = %q", tt.Texts())
	}
	if a.aiToast != nil {
		t.Fatal("the suggestion still moved as a toast")
	}

	var added []string
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id)
		return nil
	}
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return len(added) == 1 })
	if added[0] != "10" {
		t.Errorf("added = %v, want 10", added)
	}
}

// Settings has an AI page for the listener to point the app at a service.
func TestAISettingsPageExists(t *testing.T) {
	a := newTestApp()
	tt := ui.NewTester(a.settingsView, 780, 900)
	tt.Frame()
	if err := tt.Click(T("AI")); err != nil {
		t.Fatal(err)
	}
	settled(tt)
	if a.settingsPage != "ai" {
		t.Fatalf("settings page = %q, want ai; texts = %q", a.settingsPage, tt.Texts())
	}
	if !tt.HasText(T("AI Playlist Suggestions")) {
		t.Fatalf("texts = %q", tt.Texts())
	}
}

// The API key goes to the credential store, never into settings.json.
func TestAISettingsSavesKeyOutsideSettings(t *testing.T) {
	a := newTestApp()
	var saved []byte
	real := setAISecret
	setAISecret = func(key string, value []byte) error {
		saved = value
		return nil
	}
	defer func() { setAISecret = real }()

	tt := ui.NewTester(a.settingsView, 780, 1000)
	tt.Frame()
	if err := tt.Click(T("AI")); err != nil {
		t.Fatal(err)
	}
	settled(tt)

	n, ok := tt.AccessByRole(ui.RoleTextField, T("API Key"))
	if !ok {
		t.Fatalf("no key field; texts = %q", tt.Texts())
	}
	r := n.Bounds
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Type("sk-secret")
	if err := tt.Click(T("Save Key")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if string(saved) != "sk-secret" {
		t.Fatalf("saved key %q", saved)
	}
	b, _ := json.Marshal(a.settings)
	if strings.Contains(string(b), "sk-secret") {
		t.Fatal("the API key leaked into settings.json")
	}
}

// With auto-add on, a confident suggestion is written and a toast can undo it.
func TestAutoAddCanBeUndone(t *testing.T) {
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
	a.settings.AIAutoAdd = true
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	tr := song("1", "夜曲", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.account = &music.Account{ID: "1"}

	var added, removed []string
	f := fake(a)
	f.like = func(id string, on bool) error { return nil }
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id)
		return nil
	}
	f.removeFrom = func(id string, tracks []string) error {
		removed = append(removed, id)
		return nil
	}

	ch := posts(a)
	a.likeCurrent()
	until(t, ch, func() bool { return len(added) == 1 })
	until(t, ch, func() bool { return a.aiToast != nil })

	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if err := tt.Click(T("Undo")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return len(removed) == 1 })
	if removed[0] != "10" {
		t.Errorf("removed = %v, want 10", removed)
	}
}

// Candidates are the listener's own playlists, without fixed or saved ones,
// and each is cut to the requested samples.
func TestAICandidatesOnlyOwnPlaylists(t *testing.T) {
	a := newTestApp()
	a.mine = []music.Playlist{
		{ID: "1", Name: "通勤", Mine: true},
		{ID: "2", Name: "喜欢", Mine: true, Fixed: true},
		{ID: "3", Name: "收藏", Mine: false},
	}
	f := fake(a)
	f.playlist = func(id string) (*music.Playlist, error) {
		return &music.Playlist{ID: id, Tracks: []music.Track{song("1", "a", "b"), song("2", "c", "d")}}, nil
	}

	got := a.aiCandidates(a.library, &music.Track{ID: "x"}, 1)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("candidates = %+v", got)
	}
	if len(got[0].Samples) != 1 || got[0].Samples[0] != "a - b" {
		t.Errorf("samples = %v", got[0].Samples)
	}
}

// A listener with many playlists has at most twenty sent to the model.
func TestAICandidatesCapAtTwenty(t *testing.T) {
	a := newTestApp()
	for i := range 25 {
		a.mine = append(a.mine, music.Playlist{ID: string(rune('a' + i)), Name: "list", Mine: true})
	}
	if got := a.aiCandidates(a.library, &music.Track{}, 0); len(got) != 20 {
		t.Errorf("candidates = %d, want 20", len(got))
	}
}

// Unliking a song never asks the model for a playlist.
func TestUnlikingDoesNotSuggestPlaylist(t *testing.T) {
	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = "http://127.0.0.1:1"
	a.settings.AIModel = "m"
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	tr := song("1", "夜曲", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.account = &music.Account{ID: "1"}
	a.liked = map[string]bool{tr.ID: true}

	f := fake(a)
	f.like = func(id string, on bool) error { return nil }
	ch := posts(a)
	a.likeCurrent()
	runPost(t, ch)
	if a.aiToast != nil {
		t.Fatal("unliking suggested a playlist")
	}
}

// A suggestion the model is not sure of is not shown after a like.
func TestLowConfidenceLikeShowsNoToast(t *testing.T) {
	asked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(asked)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"10","confidence":0.5,"reason":"也许"}`,
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
	runPost(t, ch)
	wait(t, asked, "the AI service")
	if a.aiToast != nil {
		t.Fatal("showed a suggestion below the confidence threshold")
	}
}

// Liking the same song again does not ask the model a second time.
func TestAutoSuggestAsksOncePerSong(t *testing.T) {
	asked := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- struct{}{}
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

	ch := posts(a)
	a.autoSuggestPlaylist(a.library, tr)
	a.autoSuggestPlaylist(a.library, tr)
	wait(t, asked, "the first AI call")
	runPost(t, ch)
	select {
	case <-asked:
		t.Fatal("asked the model twice for the same song")
	case <-time.After(100 * time.Millisecond):
	}
}

// With MM_FRAMES set, saves the AI settings, the suggestion dialog, the
// persistent bar after a like, and the adjustment dialog.
func TestAIFrames(t *testing.T) {
	dir := framesDir(t)

	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = "https://api.example.com/v1"
	a.settings.AIModel = "example-model"
	prefs := ui.NewTester(a.settingsView, 780, 900)
	prefs.Frame()
	if err := prefs.Click(T("AI")); err != nil {
		t.Fatal(err)
	}
	settled(prefs)
	saveFrame(t, dir, "ai-settings.png", prefs.Image())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"10","confidence":0.9,"reason":"适合通勤"}`,
			}}},
		})
	}))
	defer srv.Close()

	b := newTestApp()
	b.settings.AIEnabled = true
	b.settings.AIBaseURL = srv.URL
	b.settings.AIModel = "m"
	b.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	b.playlists["42"] = &music.Playlist{ID: "42", Name: "List", Tracks: []music.Track{song("1", "夜曲", "周杰伦")}}
	b.route = route{page: pagePlaylist, src: "fake", id: "42"}
	f := fake(b)
	f.playlist = func(id string) (*music.Playlist, error) {
		return &music.Playlist{ID: id, Tracks: []music.Track{song("2", "稻香", "周杰伦")}}, nil
	}
	ch := posts(b)
	view := ui.NewTester(b.view, 1200, 800)
	view.Frame()
	if err := view.RightClick("夜曲"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem(T("AI Add to Playlist…")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return !b.aiSuggestion.busy })
	view.Frame()
	saveFrame(t, dir, "ai-suggestion.png", view.Image())
	if err := view.Click("Adjust"); err != nil {
		t.Fatal(err)
	}
	settled(view)
	saveFrame(t, dir, "ai-suggestion-adjust.png", view.Image())

	// 手动入口搜索中：弹窗直接显示阶段和实际搜索词，不用等结果。
	busy := newTestApp()
	busy.aiSuggestion = aiSuggestionState{
		open: true, busy: true, stage: aiBarSearching, query: "夜曲 周杰伦",
		track: song("1", "夜曲", "周杰伦"), lib: busy.library,
	}
	busyView := ui.NewTester(busy.view, 1200, 800)
	settled(busyView)
	saveFrame(t, dir, "ai-suggestion-searching.png", busyView.Image())

	// 手动入口出结果并展开依据：搜索词和来源都能在弹窗里核对。
	manual := newTestApp()
	manual.aiSuggestion = aiSuggestionState{
		open: true, stage: aiBarDone, evidenceOpen: true,
		track: song("1", "夜曲", "周杰伦"), lib: manual.library,
		query: "夜曲 周杰伦",
		pick:  ai.Suggestion{PlaylistID: "10", Confidence: 0.9, Reason: "适合通勤"},
		name:  "通勤",
		sources: []websearch.Result{
			{Title: "夜曲 - 周杰伦", URL: "https://example.com/song"},
		},
	}
	manualView := ui.NewTester(manual.view, 1200, 800)
	settled(manualView)
	saveFrame(t, dir, "ai-suggestion-evidence.png", manualView.Image())

	// 折叠后由任务卡接管：完成状态给出建议，并保留添加动作。
	folded := newTestApp()
	folded.backgroundTasksOpen = true
	task := folded.newQuietBackgroundTask("夜曲", T("AI Playlist Suggestion")+" · 周杰伦")
	task.kind = backgroundTaskAI
	task.update(task.generation, backgroundTaskSucceeded, Tf("Suggests %s", "通勤"))
	task.actions = []backgroundTaskAction{{label: T("Add"), icon: iconPlus, run: func() {}}}
	foldedView := ui.NewTester(folded.view, 1200, 800)
	settled(foldedView)
	saveFrame(t, dir, "ai-task-card.png", foldedView.Image())

	// 手动入口失败：说明没猜出来，并提供直接重试。
	failed := newTestApp()
	failed.aiSuggestion = aiSuggestionState{
		open: true, failed: true, stage: aiBarDone,
		track: song("1", "夜曲", "周杰伦"), lib: failed.library,
		query: "夜曲 周杰伦",
	}
	failedView := ui.NewTester(failed.view, 1200, 800)
	settled(failedView)
	saveFrame(t, dir, "ai-suggestion-failed.png", failedView.Image())

	bar := newTestApp()
	bar.aiBar = aiBarState{open: true, stage: aiBarDone, lib: bar.library,
		track: song("1", "夜曲", "周杰伦"), pick: ai.Suggestion{
			PlaylistID: "10", Confidence: 0.9, Reason: "适合通勤",
		}, name: "通勤", query: "夜曲 周杰伦"}
	barView := ui.NewTester(bar.view, 1200, 800)
	settled(barView)
	saveFrame(t, dir, "ai-suggestion-bar.png", barView.Image())

	if err := barView.Click(T("Adjust")); err != nil {
		t.Fatal(err)
	}
	settled(barView)
	saveFrame(t, dir, "ai-adjust-dialog.png", barView.Image())

	// 依据面板：联网搜到的网页压在最前台，能逐个点开核对。
	ev := newTestApp()
	ev.aiBar = aiBarState{open: true, stage: aiBarDone, evidenceOpen: true, lib: ev.library,
		track: song("1", "夜曲", "周杰伦"), pick: ai.Suggestion{
			PlaylistID: "10", Confidence: 0.9, Reason: "Eve 的日语流行，慢速抒情",
		}, name: "流行 日语歌",
		sources: []websearch.Result{
			{Title: "心予報 - Eve 歌曲介绍", URL: "https://example.com/1"},
			{Title: "Eve Smile 专辑曲目", URL: "https://example.com/2"},
		}}
	evView := ui.NewTester(ev.view, 1200, 800)
	settled(evView)
	saveFrame(t, dir, "ai-evidence.png", evView.Image())

	// 封面页打开时推荐条仍要可见：浏览页此时整个不绘制。
	np := newTestApp()
	np.pl.Cue([]music.Track{song("1", "夜曲", "周杰伦")})
	np.nowPlaying = true
	np.aiBar = aiBarState{open: true, stage: aiBarDone, lib: np.library,
		track: song("1", "夜曲", "周杰伦"), pick: ai.Suggestion{
			PlaylistID: "10", Confidence: 0.9, Reason: "适合通勤",
		}, name: "通勤"}
	npView := ui.NewTester(np.view, 1200, 800)
	settled(npView)
	saveFrame(t, dir, "ai-bar-on-now-playing.png", npView.Image())
}

// Manual suggestions show the search stage, the actual query and the pages
// behind the pick, before the listener confirms it.
func TestManualSuggestionShowsStageQueryAndEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"10","confidence":0.9,"reason":"适合通勤"}`,
			}}},
		})
	}))
	defer srv.Close()

	queried := make(chan string, 1)
	useFakeSearch(t, fakeSearchProvider{
		results: []websearch.Result{{Title: "夜曲 - 周杰伦", URL: "https://example.com/song"}},
		queried: queried,
	})

	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = srv.URL
	a.settings.AIModel = "m"
	a.settings.AIWebProvider = "tavily"
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	tr := song("1", "夜曲", "周杰伦")
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "List", Tracks: []music.Track{tr}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	var added []string
	f := fake(a)
	f.playlist = func(id string) (*music.Playlist, error) {
		return &music.Playlist{ID: id, Tracks: []music.Track{song("2", "稻香", "周杰伦")}}, nil
	}
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id)
		return nil
	}

	ch := posts(a)
	a.openAISuggestion(&tr)
	if a.aiSuggestion.stage != aiBarSearching {
		t.Fatalf("stage = %v, want searching", a.aiSuggestion.stage)
	}
	if !strings.Contains(a.aiSuggestion.query, "夜曲") {
		t.Fatalf("query = %q", a.aiSuggestion.query)
	}
	if q := <-queried; !strings.Contains(q, "夜曲") || strings.Contains(q, "通勤") {
		t.Fatalf("query = %q, want the song only", q)
	}
	until(t, ch, func() bool { return a.aiSuggestion.stage == aiBarThinking })
	until(t, ch, func() bool { return !a.aiSuggestion.busy })

	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	texts := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(texts, "通勤") || !strings.Contains(texts, "适合通勤") {
		t.Fatalf("dialog texts = %q", texts)
	}
	if err := tt.Click(Tf("Evidence · %d pages", 1)); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	evidence := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(evidence, "Search: 夜曲 周杰伦") {
		t.Fatalf("query %q missing from evidence: %q", a.aiSuggestion.query, evidence)
	}
	if !strings.Contains(evidence, "夜曲 - 周杰伦") {
		t.Fatalf("evidence texts = %q", tt.Texts())
	}
	if err := tt.Click(T("Add")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return len(added) == 1 })
	if added[0] != "10" {
		t.Errorf("added = %v, want 10", added)
	}
}

// Folding the manual dialog leaves the work in the task center, where its
// result can be added without reopening the dialog.
func TestFoldingManualSuggestionRunsInTaskCenter(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"10","confidence":0.9,"reason":"适合通勤"}`,
			}}},
		})
	}))
	defer srv.Close()

	useFakeSearch(t, fakeSearchProvider{
		results: []websearch.Result{{Title: "夜曲 - 周杰伦", URL: "https://example.com/song"}},
	})

	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = srv.URL
	a.settings.AIModel = "m"
	a.settings.AIWebProvider = "tavily"
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	tr := song("1", "夜曲", "周杰伦")
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "List", Tracks: []music.Track{tr}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	var added []string
	f := fake(a)
	f.playlist = func(id string) (*music.Playlist, error) {
		return &music.Playlist{ID: id, Tracks: []music.Track{song("2", "稻香", "周杰伦")}}, nil
	}
	f.addTo = func(id string, tracks []string) error {
		added = append(added, id)
		return nil
	}

	ch := posts(a)
	a.openAISuggestion(&tr)
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	until(t, ch, func() bool { return a.aiSuggestion.stage == aiBarThinking })
	tt.Frame()
	if err := tt.Click(T("Fold")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.aiSuggestion.open {
		t.Fatal("the dialog stayed open after folding")
	}
	if len(a.backgroundTasks) != 1 || a.backgroundTasks[0].kind != backgroundTaskAI {
		t.Fatalf("tasks = %+v", a.backgroundTasks)
	}
	task := a.backgroundTasks[0]
	if task.title != tr.Name || !strings.Contains(task.summary, T("AI Playlist Suggestion")) {
		t.Fatalf("task = %q / %q", task.title, task.summary)
	}

	unblock()
	until(t, ch, func() bool { return task.state == backgroundTaskSucceeded })
	tt.Frame()
	if task.status != Tf("Suggests %s", "通勤") {
		t.Fatalf("status = %q", task.status)
	}
	if err := tt.Click("Background Tasks (1)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(T("Add")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return len(added) == 1 })
	if added[0] != "10" || len(a.backgroundTasks) != 0 {
		t.Fatalf("added = %v, tasks = %d", added, len(a.backgroundTasks))
	}
}

// A failed manual suggestion can be retried in place, and the retry clears
// the failure once the model answers.
func TestManualSuggestionRetriesAfterFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
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
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "List", Tracks: []music.Track{tr}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}

	ch := posts(a)
	a.openAISuggestion(&tr)
	until(t, ch, func() bool { return !a.aiSuggestion.busy && a.aiSuggestion.failed })
	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	if err := tt.Click(T("Retry")); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return !a.aiSuggestion.busy && a.aiSuggestion.stage == aiBarDone })
	if a.aiSuggestion.failed || a.aiSuggestion.name != "通勤" {
		t.Fatalf("retry kept the failure: failed=%v name=%q", a.aiSuggestion.failed, a.aiSuggestion.name)
	}
}

// A failed auto suggestion says so once and lets a later like ask again.
func TestLikeSuggestionFailureShowsToastAndRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
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
	until(t, ch, func() bool { return a.aiToast != nil })
	if a.aiToast.Title != T("Couldn't suggest a playlist") || a.aiToast.Action != T("Retry") {
		t.Fatalf("toast = %+v", a.aiToast)
	}
	if a.aiBar.open || len(a.backgroundTasks) != 0 {
		t.Fatalf("a failed suggestion left UI behind: bar=%v tasks=%d", a.aiBar.open, len(a.backgroundTasks))
	}
	if a.aiAsked[tr.ID] {
		t.Fatal("a failed suggestion blocked the next attempt")
	}
	// 提示里的重试直接重新发起，不用先取消喜欢再点一次。
	a.aiToast.OnAction()
	if len(a.backgroundTasks) != 1 {
		t.Fatalf("retry did not start a new task: %d", len(a.backgroundTasks))
	}
}

// fakeSearchProvider 是无头测试里的联网桩，只返回预设结果或错误。
type fakeSearchProvider struct {
	results []websearch.Result
	err     error
	queried chan string
}

func (f fakeSearchProvider) Search(ctx context.Context, query string) ([]websearch.Result, error) {
	if f.queried != nil {
		f.queried <- query
	}
	return f.results, f.err
}

// useFakeSearch 把 provider 换成桩，并还原真实实现。
func useFakeSearch(t *testing.T, p websearch.Provider) {
	t.Helper()
	real := newWebProvider
	newWebProvider = func(name, key string) websearch.Provider { return p }
	t.Cleanup(func() { newWebProvider = real })
}

// likingShowsSearchThenEvidence checks that an auto suggestion shows the
// search stage first, then the model's pick with pages the listener can open.
func TestLikingShowsSearchThenEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"10","confidence":0.9,"reason":"适合通勤"}`,
			}}},
		})
	}))
	defer srv.Close()

	queried := make(chan string, 1)
	useFakeSearch(t, fakeSearchProvider{
		results: []websearch.Result{{Title: "夜曲 - 周杰伦", URL: "https://example.com/song", Snippet: "一首慢歌"}},
		queried: queried,
	})

	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = srv.URL
	a.settings.AIModel = "m"
	a.settings.AIWebProvider = "tavily"
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	tr := song("1", "夜曲", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.account = &music.Account{ID: "1"}

	ch := posts(a)
	f := fake(a)
	f.like = func(id string, on bool) error { return nil }
	a.likeCurrent()

	// 搜索阶段只进任务队列，不弹推荐条挡路。
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	if a.aiBar.open {
		t.Fatal("the bar opened before the model answered")
	}
	if a.backgroundTasks[0].kind != backgroundTaskAI ||
		a.backgroundTasks[0].status != T("Searching the web…") {
		t.Fatalf("task = %+v", a.backgroundTasks[0])
	}
	if q := <-queried; !strings.Contains(q, "夜曲") || strings.Contains(q, "通勤") {
		t.Fatalf("query = %q, want the song only", q)
	}
	until(t, ch, func() bool { return a.backgroundTasks[0].status == T("Thinking…") })
	until(t, ch, func() bool { return a.aiBar.stage == aiBarDone })
	if len(a.backgroundTasks) != 0 {
		t.Fatal("the search task stayed after the bar took over")
	}
	if len(a.aiBar.sources) != 1 || a.aiBar.sources[0].URL != "https://example.com/song" {
		t.Fatalf("sources = %+v", a.aiBar.sources)
	}
	if !strings.Contains(a.aiBar.query, "夜曲") {
		t.Fatalf("query = %q", a.aiBar.query)
	}

	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), T("Searched 1 pages")) {
		t.Fatalf("texts = %q", tt.Texts())
	}
	if err := tt.Click(T("Searched 1 pages")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.aiBar.evidenceOpen {
		t.Fatal("evidence panel did not open")
	}
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "夜曲 - 周杰伦") {
		t.Fatalf("evidence texts = %q", tt.Texts())
	}
}

// lowConfidenceHidesBarWithEvidence checks that a shy suggestion still closes
// its bar even when web search returned pages.
func TestLowConfidenceHidesBarWithEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"playlist_id":"10","confidence":0.5,"reason":"也许"}`,
			}}},
		})
	}))
	defer srv.Close()

	useFakeSearch(t, fakeSearchProvider{results: []websearch.Result{{Title: "A", URL: "https://example.com"}}})

	a := newTestApp()
	a.settings.AIEnabled = true
	a.settings.AIBaseURL = srv.URL
	a.settings.AIModel = "m"
	a.settings.AIWebProvider = "tavily"
	a.mine = []music.Playlist{{ID: "10", Name: "通勤", Mine: true}}
	tr := song("1", "夜曲", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.account = &music.Account{ID: "1"}

	ch := posts(a)
	f := fake(a)
	f.like = func(id string, on bool) error { return nil }
	a.likeCurrent()
	// 低置信度：搜索与判断都留在任务队列里，结束后整条安静收走。
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	until(t, ch, func() bool { return len(a.backgroundTasks) == 0 })
	if a.aiBar.open {
		t.Fatal("low-confidence bar stayed open")
	}
}
