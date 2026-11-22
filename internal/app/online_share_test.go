package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 网页律动必须沿用原词时间，不能在分享时丢掉节奏而重新平均分配。
func TestOnlineSharePreservesWordTiming(t *testing.T) {
	a := newTestApp()
	draft := &shareDraft{
		track:      music.Track{ID: "stage-timing", Source: "fake", Name: "Blue hour", Duration: 12000},
		onlineMode: "full", onlineQuality: "high", selected: map[int]bool{},
		lines: []lyrics.Line{{
			Text: "Blue hour", At: time.Second, End: 4 * time.Second, AtSource: true,
			Words: []lyrics.Word{
				{Text: "Blue ", Start: 1100 * time.Millisecond, End: 1800 * time.Millisecond},
				{Text: "hour", Start: 2400 * time.Millisecond, End: 3800 * time.Millisecond},
			},
		}},
	}
	metadata, err := a.onlineShareMetadata(draft)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Lyrics []struct {
			StageEnd int64 `json:"stageEndMs"`
			Words    []struct {
				Text  string `json:"text"`
				Start int64  `json:"startMs"`
				End   int64  `json:"endMs"`
			} `json:"words"`
		} `json:"lyrics"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Lyrics) != 1 || len(payload.Lyrics[0].Words) != 2 {
		t.Fatalf("share lost word timing: %s", encoded)
	}
	words := payload.Lyrics[0].Words
	if words[0].Text != "Blue " || words[0].Start != 1100 || words[0].End != 1800 ||
		words[1].Start != 2400 || words[1].End != 3800 {
		t.Fatalf("share changed source timing: %s", encoded)
	}
	if payload.Lyrics[0].StageEnd != 4000 {
		t.Fatalf("share lost the stage's original vocal end: %s", encoded)
	}
}

// 热评快照只能来自正在分享的歌曲，切换分享对象不能带出上一曲的评论。
func TestOnlineShareHotCommentsMatchSelectedTrack(t *testing.T) {
	a := newTestApp()
	a.settings.HotComments = true
	a.comments = commentState{
		track: "fake\x00current",
		list:  []music.Comment{{Text: "Keep this moment", Author: "Listener", Likes: 12}},
	}
	draft := &shareDraft{
		track:      music.Track{ID: "current", Source: "fake", Name: "Current", Duration: 12000},
		onlineMode: "full", onlineQuality: "high",
	}
	metadata, err := a.onlineShareMetadata(draft)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.HotComments) != 1 || metadata.HotComments[0].Author != "Listener" {
		t.Fatalf("missing current track comment: %+v", metadata.HotComments)
	}
	draft.track.ID = "another"
	metadata, err = a.onlineShareMetadata(draft)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.HotComments) != 0 {
		t.Fatalf("leaked another track's comments: %+v", metadata.HotComments)
	}
}

// 显式配置本机验收服务时，验证真实 Go 快照能通过动态服务而非宽松的测试替身。
func TestShareClientProductionContract(t *testing.T) {
	serverURL := os.Getenv("MM_SHARE_ACCEPTANCE_URL")
	if serverURL == "" {
		t.Skip("set MM_SHARE_ACCEPTANCE_URL for production contract acceptance")
	}
	client, err := newShareClient(serverURL, os.Getenv("MM_SHARE_ACCEPTANCE_TOKEN"), http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	audio := os.Getenv("MM_SHARE_ACCEPTANCE_AUDIO")
	for _, mode := range []string{"full", "clip"} {
		metadata := shareMetadata{Title: "Go contract", SourceID: "desktop", TrackID: mode, Quality: "high", Template: "minimal", Mode: mode, DurationMs: 12000}
		metadata.Lyrics = []shareLyricLine{{
			Text: "First line", AtMs: 0, EndMs: 2000,
			Words: []shareLyricWord{{Text: "First ", StartMs: 100, EndMs: 700}, {Text: "line", StartMs: 1000, EndMs: 1800}},
		}}
		if mode == "clip" {
			metadata.SelectedLineIndexes = []int{0}
			metadata.ClipStartMs, metadata.ClipEndMs = 425, 2125
		}
		key, err := newShareIdempotencyKey()
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.create(ctx, key, metadata, nil, "", filepath.Clean(audio))
		if err != nil {
			t.Fatal(err)
		}
		defer client.revoke(context.Background(), result.ID)
		for result.Status == "processing" {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(30 * time.Millisecond):
			}
			result, err = client.get(ctx, result.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if result.Status != "ready" || result.URL == "" {
			t.Fatalf("%s publication: %+v", mode, result)
		}
		reused, err := client.reuse(ctx, metadata, nil)
		if err != nil || reused == nil || reused.ID != result.ID || reused.URL != result.URL || reused.Expires != result.Expires {
			t.Fatalf("%s production reuse: %+v, original=%+v, error=%v", mode, reused, result, err)
		}
		response, err := client.http.Get(strings.Replace(result.URL, "/s/", "/api/public/", 1))
		if err != nil {
			t.Fatal(err)
		}
		var published struct {
			DurationMs int64            `json:"durationMs"`
			Lyrics     []shareLyricLine `json:"lyrics"`
		}
		err = json.NewDecoder(response.Body).Decode(&published)
		response.Body.Close()
		if err != nil || len(published.Lyrics) == 0 || len(published.Lyrics[0].Words) != 2 ||
			published.Lyrics[0].Words[1].StartMs != 1000-metadata.ClipStartMs {
			t.Fatalf("%s production word timing: %+v, error=%v", mode, published, err)
		}
		wantDuration := metadata.ClipEndMs - metadata.ClipStartMs
		if mode == "clip" && (published.DurationMs < wantDuration-100 || published.DurationMs > wantDuration+100) {
			t.Fatalf("published clip duration %d ms, want about %d ms", published.DurationMs, wantDuration)
		}
	}
}

// Shift 点击应从上次普通点击的歌词延伸连续选择，而不是只切换终点。
func TestShareShiftClickSelectsContinuousLyrics(t *testing.T) {
	a := newTestApp()
	a.sharing = &shareDraft{open: true, selected: map[int]bool{}, onlineMode: "full", lines: []lyrics.Line{
		{Text: "First"}, {Text: "Middle"}, {Text: "Last"},
	}}
	tester := ui.NewTester(func(c *ui.Context) { a.pal = paletteFor(c.Theme()); a.shareDialog(c) }, 1200, 800)
	tester.Frame()
	if err := tester.Click("Select lyric 1"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ClickWith(ui.Shift, "Select lyric 3"); err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		if !a.sharing.selected[index] {
			t.Fatalf("line %d was not selected by Shift click", index+1)
		}
	}
}

// 无头生成实际分享面板，目视检查新控件、片段范围和卡片预览是否互相遮挡。
func TestShareOnlineDialogFrame(t *testing.T) {
	directory := os.Getenv("MM_FRAMES")
	if directory == "" {
		t.Skip("set MM_FRAMES for visual acceptance")
	}
	a := newTestApp()
	posted := posts(a)
	a.sharing = &shareDraft{
		open: true, onlineMode: "clip", onlineQuality: "high", timingReliable: true,
		track:    music.Track{ID: "visual", Source: "fake", Name: "Blue Hour", Artists: []music.Artist{{Name: "Oh MyGo Music"}}, Duration: 12000, URL: "https://example.com/song"},
		cover:    coverImage(ui.RGB(39, 75, 95), ui.RGB(160, 103, 121)),
		selected: map[int]bool{0: true, 1: true},
		lines: []lyrics.Line{
			{Text: "A quiet echo, carried by the wind", At: time.Second, End: 3 * time.Second, AtSource: true, EndSource: true},
			{Text: "This moment is a song for you", At: 4 * time.Second, End: 7 * time.Second, AtSource: true, EndSource: true},
			{Text: "Until the night begins to glow", At: 8 * time.Second, End: 11 * time.Second, AtSource: true, EndSource: true},
		},
	}
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.shareDialog(c)
		a.backgroundTasksLayer(c)
	}, 1200, 850)
	tester.SetDark(true)
	tester.Frame()
	a.requestShareRender()
	until(t, posted, func() bool { tester.Frame(); return a.sharing.preview != nil })
	settled(tester)
	saveFrame(t, directory, "share-online-dialog.png", tester.Image())
}

// 处理状态请求失败曾把结果置空并在下一轮解引用；重试必须保留原请求身份。
func TestSharePublishRetriesAfterPollingFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(path, []byte("test audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	var keys []string
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/api/shares/reuse" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method == http.MethodPost {
			keys = append(keys, request.Header.Get("Idempotency-Key"))
			io.WriteString(w, `{"id":"retry-share","status":"processing"}`)
		} else {
			polls++
			if polls == 1 {
				w.WriteHeader(503)
				io.WriteString(w, `{"error":"temporarily_unavailable"}`)
				return
			}
			io.WriteString(w, `{"id":"retry-share","status":"ready","url":"https://share.example/s/retried"}`)
		}
	}))
	defer server.Close()
	previous := getShareServiceSecret
	getShareServiceSecret = func(string) ([]byte, error) { return []byte("token"), nil }
	defer func() { getShareServiceSecret = previous }()
	a := newTestApp()
	a.settings.ShareServerURL = server.URL
	fake(a).stream = func(string, string) (*music.Stream, error) {
		return &music.Stream{URL: "file:///" + filepath.ToSlash(path), Type: "mp3"}, nil
	}
	posted := posts(a)
	a.sharing = &shareDraft{open: true, selected: map[int]bool{}, onlineMode: "full", onlineQuality: "high", track: music.Track{ID: "retry", Source: "fake", Name: "Retry", Duration: 12000}}
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.shareDialog(c)
		a.backgroundTasksLayer(c)
	}, 1200, 850)
	tester.Frame()
	if err := tester.Click("Create Listen Link"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return !a.sharing.onlinePublishing })
	if a.sharing.onlineError == nil || a.sharing.onlineID != "retry-share" {
		t.Fatalf("failed poll lost retry state: %+v", a.sharing)
	}
	if err := tester.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return a.sharing.onlineURL != "" })
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("retry keys: %v", keys)
	}
}

func TestShareCardPublishesListeningLinkFromTheSelectedDownload(t *testing.T) {
	audioData := []byte("selected-audio")
	audioServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audioData)
	}))
	defer audioServer.Close()

	var mu sync.Mutex
	var uploadedAudio []byte
	var received shareMetadata
	streamRequest := make(chan struct{}, 1)
	createdRequest := make(chan struct{}, 1)
	uploadedRequest := make(chan struct{}, 1)
	shareServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-service-token" {
			t.Error("share request did not use the configured service token")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/shares":
			createdRequest <- struct{}{}
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("share creation omitted its retry key")
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Errorf("decode share snapshot: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"share-1","status":"awaiting_upload"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/shares/share-1/audio":
			uploadedRequest <- struct{}{}
			mu.Lock()
			uploadedAudio, _ = io.ReadAll(r.Body)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"share-1","status":"processing"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/shares/share-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"share-1","status":"ready","url":"https://share.example/s/public-token","expiresAt":"2026-11-17T00:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer shareServer.Close()

	oldSecret := getShareServiceSecret
	getShareServiceSecret = func(string) ([]byte, error) { return []byte("test-service-token"), nil }
	defer func() { getShareServiceSecret = oldSecret }()

	a := newTestApp()
	a.settings.ShareServerURL = shareServer.URL
	posts := posts(a)
	fake(a).lyrics = func(string) (*music.Lyrics, error) {
		return &music.Lyrics{LRC: "[00:01.00]First line\n[00:04.00]Last line"}, nil
	}
	fake(a).stream = func(id, quality string) (*music.Stream, error) {
		select {
		case streamRequest <- struct{}{}:
		default:
		}
		if id != "shared" || quality != "high" {
			t.Errorf("requested stream %q at %q", id, quality)
		}
		return &music.Stream{URL: audioServer.URL, Type: "mp3", Quality: quality}, nil
	}
	track := music.Track{
		ID: "shared", Source: "fake", Name: "Blue Hour",
		Artists: []music.Artist{{Name: "A. Example"}}, Duration: 12_000,
		URL: "https://catalog.example/songs/track-1",
	}
	tester := ui.NewTester(func(c *ui.Context) {
		a.pal = paletteFor(c.Theme())
		a.songMenu(c, &track, 22, a.pal.text)
		a.shareDialog(c)
		a.backgroundTasksLayer(c)
	}, 1200, 800)
	tester.Frame()
	if err := tester.Click("More"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("Share Card…"); err != nil {
		t.Fatal(err)
	}
	until(t, posts, func() bool { tester.Frame(); _, ok := tester.Find("Card Preview"); return ok })
	if err := tester.Click("Create Listen Link"); err != nil {
		t.Fatal(err)
	}
	if a.sharing == nil || (!a.sharing.onlinePublishing && a.sharing.onlineURL == "") {
		t.Fatalf("online publishing did not start: status=%q error=%v", a.sharing.status, a.sharing.onlineError)
	}
	select {
	case <-streamRequest:
	case <-time.After(2 * time.Second):
		t.Fatalf("source stream was not requested: status=%q error=%v", a.sharing.status, a.sharing.onlineError)
	}
	select {
	case <-createdRequest:
	case <-time.After(2 * time.Second):
		t.Fatalf("share request was not sent: status=%q error=%v", a.sharing.status, a.sharing.onlineError)
	}
	select {
	case <-uploadedRequest:
	case <-time.After(2 * time.Second):
		t.Fatalf("audio upload was not sent: status=%q error=%v", a.sharing.status, a.sharing.onlineError)
	}
	until(t, posts, func() bool {
		tester.Frame()
		return tester.HasText("Online link ready")
	})

	mu.Lock()
	gotAudio := append([]byte(nil), uploadedAudio...)
	mu.Unlock()
	if string(gotAudio) != string(audioData) {
		t.Fatalf("uploaded audio %q, want selected source bytes", gotAudio)
	}
	if received.SourceID != "fake" || received.TrackID != "shared" ||
		received.Quality != "high" || received.Mode != "full" {
		t.Fatalf("published snapshot did not preserve the selected song: %+v", received)
	}
	if received.Template != "minimal" || received.SourceURL != "https://catalog.example/songs/track-1" {
		t.Fatalf("published card choices %+v", received)
	}
}
