package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
)

func TestNormalizeShareServerURL(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
		ok    bool
	}{
		{"https://share.example.com/", "https://share.example.com", true},
		{"http://127.0.0.1:3000", "http://127.0.0.1:3000", true},
		{"http://share.example.com", "", false},
		{"https://user@example.com", "", false},
		{"https://share.example.com/path", "", false},
		{"", "", false},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := normalizeShareServerURL(test.input)
			if (err == nil) != test.ok {
				t.Fatalf("normalizeShareServerURL(%q) error = %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("normalized URL %q, want %q", got, test.want)
			}
		})
	}
}

// 整曲协议不允许携带片段边界，即便该边界值为零。
func TestShareFullSongSnapshotOmitsClipBounds(t *testing.T) {
	data, err := json.Marshal(shareMetadata{Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["clipStartMs"]; exists {
		t.Fatalf("full song contains clip fields: %s", data)
	}
}

// 无歌词整曲和从零开始的片段必须编码为服务可接受的内容快照。
func TestShareClientPreservesEmptyLyricsAndZeroClipStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["lyrics"]) != "[]" || string(body["selectedLineIndexes"]) != "[]" {
			t.Errorf("empty lyric collections must be arrays: %s / %s", body["lyrics"], body["selectedLineIndexes"])
		}
		if string(body["clipStartMs"]) != "0" {
			t.Errorf("zero clip start was lost: %s", body["clipStartMs"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"zero-start","status":"ready","url":"https://share.example/s/token"}`)
	}))
	defer server.Close()
	client, err := newShareClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.create(context.Background(), "zero-start", shareMetadata{Mode: "clip", ClipEndMs: 2000}, nil, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestShareClipRangeUsesSelectedLineBoundsAndSongLimits(t *testing.T) {
	lines := []lyrics.Line{
		{At: 2 * time.Second, End: 3 * time.Second, AtSource: true, EndSource: true, Text: "First"},
		{At: 4 * time.Second, End: 5 * time.Second, AtSource: true, EndSource: true, Text: "Skipped"},
		{At: 8 * time.Second, End: 9 * time.Second, AtSource: true, EndSource: true, Text: "Last"},
	}
	start, end, ok := shareClipRange(lines, map[int]bool{0: true, 2: true}, 20*time.Second, true)
	if !ok || start != time.Second || end != 10*time.Second {
		t.Fatalf("clip range %s..%s, valid=%v", start, end, ok)
	}

	start, end, ok = shareClipRange(
		[]lyrics.Line{{At: 0, End: 1500 * time.Millisecond, AtSource: true, EndSource: true, Text: "First"}},
		map[int]bool{0: true},
		2*time.Second,
		true,
	)
	if !ok || start != 0 || end != 2*time.Second {
		t.Fatalf("bounded clip range %s..%s, valid=%v", start, end, ok)
	}

	start, end, ok = shareClipRange([]lyrics.Line{
		{At: 2 * time.Second, End: 8 * time.Second, AtSource: true, Text: "First"},
		{At: 4 * time.Second, End: 10 * time.Second, AtSource: true, Text: "Next"},
	}, map[int]bool{0: true}, 20*time.Second, true)
	if !ok || start != time.Second || end != 5*time.Second {
		t.Fatalf("next-line clip range %s..%s, valid=%v", start, end, ok)
	}

	if _, _, ok := shareClipRange(
		[]lyrics.Line{{At: 0, End: 6 * time.Second, Text: "Untimed"}},
		map[int]bool{0: true},
		20*time.Second,
		false,
	); ok {
		t.Fatal("enabled clipping when the selected line had no reliable timestamp")
	}
}

func TestShareClientCreatesAndUploadsAContentSnapshot(t *testing.T) {
	audioPath := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(audioPath, []byte("audio-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	cover := []byte("cover-payload")
	var uploadedAudio, uploadedCover []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-secret" {
			t.Error("request did not carry the configured service token")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/shares":
			if r.Header.Get("Idempotency-Key") != "retry-stable-key" {
				t.Errorf("idempotency key %q", r.Header.Get("Idempotency-Key"))
			}
			var body shareMetadata
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Title != "Blue Hour" || body.Mode != "full" ||
				body.SourceID != "catalog" || body.TrackID != "track-1" || body.Quality != "high" ||
				body.CardSquare {
				t.Fatalf("share snapshot %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"share-1","status":"awaiting_upload"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/shares/share-1/cover":
			uploadedCover, _ = ioReadAll(r)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/api/shares/share-1/audio":
			uploadedAudio, _ = ioReadAll(r)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"share-1","status":"processing"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newShareClient(server.URL, "service-secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.create(context.Background(), "retry-stable-key", shareMetadata{
		Title: "Blue Hour", Artists: "A. Example", SourceID: "catalog", TrackID: "track-1",
		Quality: "high", Template: "cinema", CardSquare: false, Mode: "full", DurationMs: 180000,
	}, cover, "image/jpeg", audioPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "share-1" || result.Status != "processing" {
		t.Fatalf("share result %+v", result)
	}
	if string(uploadedCover) != string(cover) {
		t.Fatalf("uploaded cover %q", uploadedCover)
	}
	if string(uploadedAudio) != "audio-payload" {
		t.Fatalf("uploaded audio %q", uploadedAudio)
	}
}

func ioReadAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func TestShareClientRejectsAnUnsafeServerOrigin(t *testing.T) {
	if _, err := newShareClient("http://share.example.com", "service-secret", http.DefaultClient); err == nil {
		t.Fatal("accepted an insecure remote share server URL")
	}
	if _, err := newShareClient("https://user@example.com", "service-secret", http.DefaultClient); err == nil {
		t.Fatal("accepted credentials embedded in the share server URL")
	}
	if _, err := newShareClient("https://share.example.com/base/path", "service-secret", http.DefaultClient); err == nil {
		t.Fatal("accepted a path-prefixed share server URL")
	}
}

// 公网撤销须通过同源校验，否则分享仍保留在列表里。
func TestShareClientListsAndRevokesShares(t *testing.T) {
	revoked := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-secret" {
			t.Error("request did not carry the configured service token")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/shares":
			w.Header().Set("Content-Type", "application/json")
			if revoked {
				_, _ = io.WriteString(w, `{"shares":[{"id":"share-1","status":"revoked"}]}`)
				return
			}
			_, _ = w.Write([]byte(`{"shares":[{"id":"share-1","title":"Blue Hour","status":"ready","url":"https://share.example/s/token"}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/shares/share-1":
			if r.Header.Get("Origin") != "https://"+r.Host {
				http.Error(w, "Cross-site DELETE form submissions are forbidden", http.StatusForbidden)
				return
			}
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newShareClient(server.URL, "service-secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	shares, err := client.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 || shares[0].ID != "share-1" || shares[0].Title != "Blue Hour" {
		t.Fatalf("listed shares %+v", shares)
	}
	if err := client.revoke(context.Background(), "share-1"); err != nil {
		t.Fatal(err)
	}
	shares, err = client.list(context.Background())
	if err != nil || len(shares) != 1 || shares[0].Status != "revoked" || shares[0].URL != "" {
		t.Fatalf("revoked share remained available: %+v, %v", shares, err)
	}
}

func TestShareTokenKeyIsStablePerServer(t *testing.T) {
	first := shareTokenKey("https://share.example.com/")
	same := shareTokenKey("https://share.example.com")
	other := shareTokenKey("https://other.example.com")
	if first == "" || first != same {
		t.Fatalf("normalized server token key %q, want stable key %q", first, same)
	}
	if first == other {
		t.Fatal("different share servers use the same credential key")
	}
}

func TestShareServiceTokensAreStoredSeparatelyPerServer(t *testing.T) {
	oldGet, oldSet := getShareServiceSecret, setShareServiceSecret
	stored := map[string][]byte{}
	getShareServiceSecret = func(key string) ([]byte, error) {
		value, ok := stored[key]
		if !ok {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), value...), nil
	}
	setShareServiceSecret = func(key string, value []byte) error {
		stored[key] = append([]byte(nil), value...)
		return nil
	}
	defer func() {
		getShareServiceSecret, setShareServiceSecret = oldGet, oldSet
	}()

	if err := saveShareServiceToken("https://one.example", " first-secret "); err != nil {
		t.Fatal(err)
	}
	if err := saveShareServiceToken("https://two.example", "second-secret"); err != nil {
		t.Fatal(err)
	}
	first, err := loadShareServiceToken("https://one.example")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadShareServiceToken("https://two.example")
	if err != nil {
		t.Fatal(err)
	}
	if first != "first-secret" || second != "second-secret" {
		t.Fatalf("loaded service tokens %q and %q", first, second)
	}
}

func TestShareServerAddressPersistsWithoutTheToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings := settings{ShareServerURL: "https://share.example.com", path: path}
	if err := settings.save(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["shareServerUrl"] != "https://share.example.com" {
		t.Fatalf("saved service URL %v", saved["shareServerUrl"])
	}
	if _, exists := saved["shareToken"]; exists {
		t.Fatal("service token was written to settings.json")
	}
}

func TestMySharesPageLoadsAndRevokesFromConfiguredService(t *testing.T) {
	var mu sync.Mutex
	revoked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-secret" {
			t.Error("My Shares did not use the configured service credential")
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/shares":
			status := "ready"
			if revoked {
				status = "revoked"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"shares":[{"id":"share-1","title":"Blue Hour","artists":"A. Example","status":"`+status+`","url":"https://share.example/s/token"}]}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/shares/share-1":
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	previousGet := getShareServiceSecret
	getShareServiceSecret = func(string) ([]byte, error) { return []byte("service-secret"), nil }
	defer func() { getShareServiceSecret = previousGet }()

	a := newTestApp()
	a.settings.ShareServerURL = server.URL
	posted := posts(a)
	tester := ui.NewTester(a.settingsView, 780, 800)
	tester.Frame()
	if err := tester.Click("Online Sharing"); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	if err := tester.Click("Refresh"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return tester.HasText("Blue Hour") })
	if !tester.HasText("Available") {
		t.Fatalf("ready share status missing: %q", tester.Texts())
	}
	if err := tester.Click("Revoke"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool {
		tester.Frame()
		return tester.HasText("Revoked")
	})
}
