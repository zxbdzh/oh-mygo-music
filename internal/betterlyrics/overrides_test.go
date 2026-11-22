package betterlyrics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 手动歌词必须跨重启保存，来源随后发布的旧歌词不能覆盖它。
func TestOverridePersistsAcrossTrackChangesAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lyrics.json")
	store, err := NewOverrideStore(path)
	if err != nil {
		t.Fatal(err)
	}
	key := SongKey{Source: "test-source", StableID: "song"}
	server := NewServer(store)
	server.SetLyricsWriteEnabled(true)
	server.Publish(Status{TrackID: 7, SongKey: key}, &Lyrics{TrackID: 7, LRC: "[00:01.000]original"})
	source := Lyrics{
		LRC: "[00:01.234]hello world", Translation: "[00:01.234]你好世界",
		Lines: []music.LyricLine{{
			Start: 1234, End: 3000, Text: "hello world", Translation: "你好世界",
			Words: []music.LyricWord{{Start: 1234, End: 2000, Text: "hello "}, {Start: 2000, End: 3000, Text: "world"}},
		}},
	}
	response := writeOverride(t, server, LyricsOverrideRequest{
		TrackID: 7, InstanceID: server.instanceID, SongKey: key, Lyrics: source,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body)
	}
	server.Publish(Status{TrackID: 8, SongKey: SongKey{Source: "test-source", StableID: "other"}}, nil)
	server.Publish(Status{TrackID: 9, SongKey: key}, &Lyrics{TrackID: 9, LRC: "[00:01.000]original"})
	if status := server.currentStatus(); !status.LyricsReady || status.LyricsOrigin != "override" || status.LyricsRevision == 0 {
		t.Fatalf("override lost after switching tracks: %+v", status)
	}
	store, err = NewOverrideStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewServer(store)
	restarted.Publish(Status{TrackID: 1, SongKey: key}, nil)
	response = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/lyrics?trackId=1", nil))
	var got Lyrics
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || got.Translation != source.Translation || got.Lines[0].Words[1].End != 3000 || got.Origin != "override" {
		t.Fatalf("restart lost lyric content or timing: %+v", got)
	}
	clone := store.Get(key)
	clone.Lines[0].Words[0].Text = "changed"
	if store.Get(key).Lines[0].Words[0].Text != "hello " {
		t.Fatal("a reader mutated the persisted lyric snapshot")
	}
}

// 默认只读、切歌、重启和同名不同来源都必须拒绝旧的写入请求。
func TestOverrideRejectsUnauthorizedAndStaleRequests(t *testing.T) {
	for _, scenario := range []string{"disabled", "track", "instance", "identity"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := NewOverrideStore(filepath.Join(t.TempDir(), "lyrics.json"))
			if err != nil {
				t.Fatal(err)
			}
			key := SongKey{Source: "test-source", StableID: "song"}
			server := NewServer(store)
			server.SetLyricsWriteEnabled(scenario != "disabled")
			server.Publish(Status{TrackID: 7, SongKey: key}, nil)
			request := LyricsOverrideRequest{
				TrackID: 7, InstanceID: server.instanceID, SongKey: key, Lyrics: Lyrics{LRC: "[00:01.000]manual"},
			}
			want := http.StatusConflict
			switch scenario {
			case "disabled":
				want = http.StatusForbidden
			case "track":
				request.TrackID = 6
			case "instance":
				request.InstanceID = "previous-app-instance"
			case "identity":
				request.SongKey.Source = "other-source"
			}
			response := writeOverride(t, server, request)
			if response.Code != want || store.Has(key) || server.currentStatus().LyricsReady {
				t.Fatalf("unsafe request was applied: %d %s", response.Code, response.Body)
			}
		})
	}
}

// 删除覆盖后重新发布来源歌词，订阅版本必须变化且重启也不能恢复旧覆盖。
func TestDeleteOverrideRestoresSourceAndUpdatesRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lyrics.json")
	store, err := NewOverrideStore(path)
	if err != nil {
		t.Fatal(err)
	}
	key := SongKey{Source: "test", StableID: "song"}
	server := NewServer(store)
	server.SetLyricsWriteEnabled(true)
	server.Publish(Status{TrackID: 7, SongKey: key}, nil)
	response := writeOverride(t, server, LyricsOverrideRequest{
		TrackID: 7, InstanceID: server.instanceID, SongKey: key, Lyrics: Lyrics{LRC: "[00:01.000]manual"},
	})
	if response.Code != http.StatusOK {
		t.Fatal(response.Body)
	}
	revision := server.currentStatus().LyricsRevision
	request := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1/lyrics-override?source=test&stableId=song", nil)
	request.Header.Set("X-BetterLyrics-Client", "BetterLyrics")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || store.Has(key) || server.currentStatus().LyricsRevision <= revision {
		t.Fatalf("delete failed: %d %s", response.Code, response.Body)
	}
	server.Publish(Status{TrackID: 7, SongKey: key}, &Lyrics{TrackID: 7, LRC: "[00:01.000]original"})
	if server.lyrics.LRC != "[00:01.000]original" || server.currentStatus().LyricsOrigin != "" {
		t.Fatal("source lyrics did not replace the cleared override")
	}
	reloaded, err := NewOverrideStore(path)
	if err != nil || reloaded.Has(key) {
		t.Fatalf("deleted override reappeared: %v", err)
	}
}

// 浏览器跨站请求即使知道本机端口，也不能创建或删除覆盖。
func TestOverrideRejectsBrowserRequests(t *testing.T) {
	for _, scenario := range []string{"origin", "fetch-site", "host", "missing-header"} {
		t.Run(scenario, func(t *testing.T) {
			server := NewServer()
			request := httptest.NewRequest(http.MethodPut, "http://127.0.0.1/lyrics-override", strings.NewReader("{}"))
			request.Header.Set("X-BetterLyrics-Client", "BetterLyrics")
			switch scenario {
			case "origin":
				request.Header.Set("Origin", "https://example.com")
			case "fetch-site":
				request.Header.Set("Sec-Fetch-Site", "cross-site")
			case "host":
				request.Host = "example.com"
			case "missing-header":
				request.Header.Del("X-BetterLyrics-Client")
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("untrusted request accepted: %d", response.Code)
			}
		})
	}
}

// 在 HTTP 请求排队后切歌，写入必须使用主线程刚刷新的曲目校验。
func TestOverrideChecksTrackAfterDispatch(t *testing.T) {
	store, err := NewOverrideStore(filepath.Join(t.TempDir(), "lyrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := SongKey{Source: "test", StableID: "song"}
	server := NewServer(store)
	server.SetLyricsWriteEnabled(true)
	server.Publish(Status{TrackID: 7, SongKey: key}, nil)
	server.SetOverrideDispatcher(func(ctx context.Context, operation func()) error {
		server.Publish(Status{TrackID: 8, SongKey: SongKey{Source: "test", StableID: "next"}}, nil)
		operation()
		return nil
	})
	response := writeOverride(t, server, LyricsOverrideRequest{
		TrackID: 7, InstanceID: server.instanceID, SongKey: key, Lyrics: Lyrics{LRC: "[00:01.000]manual"},
	})
	if response.Code != http.StatusConflict || store.Has(key) {
		t.Fatalf("stale request applied after dispatch: %d %s", response.Code, response.Body)
	}
}

// 落盘失败不能让内存声称新歌词已保存，也不能丢失此前可用的覆盖。
func TestOverrideStoreRollsBackFailedWrites(t *testing.T) {
	root := t.TempDir()
	key := SongKey{Source: "test", StableID: "song"}
	store, err := NewOverrideStore(filepath.Join(root, "lyrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(key, music.Lyrics{LRC: "[00:01.000]original"}); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(blocker, "lyrics.json")
	if err := store.Put(key, music.Lyrics{LRC: "[00:01.000]replacement"}); err == nil {
		t.Fatal("a failed save reported success")
	}
	if err := store.Delete(key); err == nil {
		t.Fatal("a failed delete reported success")
	}
	if got := store.Get(key); got == nil || got.LRC != "[00:01.000]original" {
		t.Fatal("disk failure changed the in-memory override")
	}
}

// 非法逐字时间不能进入持久存储。
func TestOverrideRejectsInvalidWordTimes(t *testing.T) {
	server := NewServer()
	key := SongKey{Source: "test", StableID: "song"}
	response := writeOverride(t, server, LyricsOverrideRequest{
		TrackID: 7, InstanceID: server.instanceID, SongKey: key,
		Lyrics: Lyrics{Lines: []music.LyricLine{{Start: 1000, End: 3000, Text: "hello",
			Words: []music.LyricWord{{Start: 1000, End: 500, Text: "hello"}}}}},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid timing accepted: %d", response.Code)
	}
}

// 稳定来源身份不受元数据改名影响，缺失 ID 时才严格使用完整元数据。
func TestSongKeysPreferSourceIDAndRejectIncompleteFallback(t *testing.T) {
	track := music.Track{Source: "test", ID: "song", Name: "title"}
	key := KeyForTrack(track)
	track.Name = "new title"
	if KeyForTrack(track) != key {
		t.Fatal("metadata changes altered a stable ID")
	}
	track.ID = ""
	if KeyForTrack(track).valid() {
		t.Fatal("incomplete metadata was accepted as a stable identity")
	}
	track.Artists = []music.Artist{{Name: "artist"}}
	track.Album.Name, track.Duration = "album", 180000
	key = KeyForTrack(track)
	track.Duration++
	if !key.valid() || KeyForTrack(track) == key {
		t.Fatal("fallback ignored duration")
	}
	if LocalSongKey("relative.mp3").valid() || !LocalSongKey(filepath.Join(t.TempDir(), "song.mp3")).valid() {
		t.Fatal("local path identity accepted an unsafe path")
	}
}

func writeOverride(t *testing.T, server *Server, request LyricsOverrideRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	message := httptest.NewRequest(http.MethodPut, "http://127.0.0.1/lyrics-override", bytes.NewReader(data))
	message.Header.Set("Content-Type", "application/json")
	message.Header.Set("X-BetterLyrics-Client", "BetterLyrics")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, message)
	return response
}
