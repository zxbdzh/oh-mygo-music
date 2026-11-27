package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSuggestReturnsCandidateFromModelJSON checks that a suggestion keeps the
// candidate the model picked, its confidence and its reason.
func TestSuggestReturnsCandidateFromModelJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"playlist_id\":\"p1\",\"confidence\":0.9,\"reason\":\"适合通勤\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{
		Song: Song{Name: "夜曲", Artists: "周杰伦", Album: "十一月的萧邦"},
		Candidates: []Candidate{{
			ID: "p1", Name: "通勤", Description: "路上听", TrackCount: 20,
			Samples: []string{"稻香 - 周杰伦"},
		}},
	})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.PlaylistID != "p1" || got.Confidence != 0.9 || got.Reason != "适合通勤" {
		t.Errorf("suggestion = %+v", got)
	}
}

// TestSuggestDropsPlaylistOutsideCandidates checks that a hallucinated id is
// treated as no suggestion, never as a playlist to write to.
func TestSuggestDropsPlaylistOutsideCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"playlist_id\":\"p9\",\"confidence\":0.99,\"reason\":\"x\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{
		Song:       Song{Name: "夜曲"},
		Candidates: []Candidate{{ID: "p1", Name: "通勤"}},
	})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.PlaylistID != "" || got.Confidence != 0 {
		t.Errorf("suggestion = %+v, want none", got)
	}
}

// TestSuggestParsesFencedJSON checks that a reply wrapped in a Markdown fence
// still gives a suggestion, as many services do that.
func TestSuggestParsesFencedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": "```json\n{\"playlist_id\":\"p1\",\"confidence\":0.8,\"reason\":\"通勤\"}\n```",
			}}},
		})
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{
		Song:       Song{Name: "夜曲"},
		Candidates: []Candidate{{ID: "p1", Name: "通勤"}},
	})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.PlaylistID != "p1" || got.Confidence != 0.8 {
		t.Errorf("suggestion = %+v", got)
	}
}

// TestSuggestSkipsModelWithoutCandidates checks that a listener with no own
// playlists costs no request.
func TestSuggestSkipsModelWithoutCandidates(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got, err := Suggest(context.Background(), c, SuggestInput{Song: Song{Name: "夜曲"}})
	if err != nil || got.PlaylistID != "" {
		t.Fatalf("suggestion = %+v, err = %v", got, err)
	}
	if asked != 0 {
		t.Errorf("asked the model %d times, want 0", asked)
	}
}

// TestRefineKeepsTheConversation checks that an adjustment carries the
// earlier suggestion and the listener's request to the model.
func TestRefineKeepsTheConversation(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		for _, m := range body.Messages {
			got += m.Role + ":" + m.Content + "\n"
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"playlist_id\":\"p2\",\"confidence\":0.95,\"reason\":\"更安静\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	got2, err := Refine(context.Background(), c, RefineInput{
		Song:       Song{Name: "夜曲", Artists: "周杰伦"},
		Candidates: []Candidate{{ID: "p1", Name: "通勤"}, {ID: "p2", Name: "睡前"}},
		History: []Message{
			{Role: "assistant", Content: `{"playlist_id":"p1","confidence":0.8,"reason":"节奏合适"}`},
			{Role: "user", Content: "太吵了，换安静一点的"},
		},
	})
	if err != nil {
		t.Fatalf("refine: %v", err)
	}
	if got2.PlaylistID != "p2" || got2.Reason != "更安静" {
		t.Errorf("refined = %+v", got2)
	}
	for _, want := range []string{"节奏合适", "太吵了，换安静一点的", "夜曲"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt misses %q:\n%s", want, got)
		}
	}
}

// TestRefineInjectsPreferences checks that what the listener taught earlier
// is part of the refinement prompt.
func TestRefineInjectsPreferences(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			got += m.Content
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"playlist_id\":\"p1\",\"confidence\":0.9,\"reason\":\"ok\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, err := Refine(context.Background(), c, RefineInput{
		Song:        Song{Name: "夜曲"},
		Candidates:  []Candidate{{ID: "p1", Name: "睡前"}},
		Preferences: []string{"周杰伦的慢歌更适合睡前"},
		History:     []Message{{Role: "user", Content: "换一个"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "周杰伦的慢歌更适合睡前") {
		t.Errorf("prompt misses the preference:\n%s", got)
	}
}

// TestSuggestInjectsPreferences checks that the first suggestion of a song
// already knows what this listener taught earlier.
func TestSuggestInjectsPreferences(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			got += m.Content
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"playlist_id\":\"p1\",\"confidence\":0.9,\"reason\":\"ok\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, err := Suggest(context.Background(), c, SuggestInput{
		Song:        Song{Name: "夜曲"},
		Candidates:  []Candidate{{ID: "p1", Name: "睡前"}},
		Preferences: []string{"慢歌更适合睡前"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "慢歌更适合睡前") {
		t.Errorf("prompt misses the preference:\n%s", got)
	}
}

// TestSuggestInjectsWebResults checks that app-side search results arrive in
// their own system messages, with the untrusted-content boundary, and are
// left out entirely when there are none.
func TestSuggestInjectsWebResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var systems []string
		for _, m := range body.Messages {
			if m.Role == "system" {
				systems = append(systems, m.Content)
			}
		}
		// 必须有一条说明搜索内容不可信，一条带结果与来源 URL。
		joined := strings.Join(systems, "\n")
		if !strings.Contains(joined, "untrusted") {
			t.Errorf("no untrusted-content boundary:\n%s", joined)
		}
		if !strings.Contains(joined, "https://example.com/1") {
			t.Errorf("no result URL:\n%s", joined)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"playlist_id\":\"p1\",\"confidence\":0.9,\"reason\":\"ok\"}"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, err := Suggest(context.Background(), c, SuggestInput{
		Song:       Song{Name: "夜曲"},
		Candidates: []Candidate{{ID: "p1", Name: "睡前"}},
		WebResults: []WebResult{{Title: "Review", URL: "https://example.com/1", Snippet: "A review of the song."}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
