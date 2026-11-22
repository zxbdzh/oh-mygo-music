package betterlyrics

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	lyricparser "mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

type Status struct {
	TrackID            uint64  `json:"trackId"`
	SongKey            SongKey `json:"songKey"`
	Title              string  `json:"title"`
	Artist             string  `json:"artist"`
	Album              string  `json:"album"`
	Playing            bool    `json:"playing"`
	PositionMs         int64   `json:"positionMs"`
	DurationMs         int64   `json:"durationMs"`
	LyricsReady        bool    `json:"lyricsReady"`
	LyricsOrigin       string  `json:"lyricsOrigin,omitempty"`
	LyricsRevision     uint64  `json:"lyricsRevision"`
	LyricsWriteEnabled bool    `json:"lyricsWriteEnabled"`
	InstanceID         string  `json:"instanceId"`
}

type Lyrics struct {
	TrackID     uint64            `json:"trackId"`
	SongKey     SongKey           `json:"songKey"`
	Origin      string            `json:"origin,omitempty"`
	LRC         string            `json:"lrc,omitempty"`
	Translation string            `json:"translation,omitempty"`
	Lines       []music.LyricLine `json:"lines,omitempty"`
}

type LyricsOverrideRequest struct {
	TrackID    uint64  `json:"trackId"`
	InstanceID string  `json:"instanceId"`
	SongKey    SongKey `json:"songKey"`
	Lyrics     Lyrics  `json:"lyrics"`
}

type Server struct {
	mu         sync.RWMutex
	status     Status
	updated    time.Time
	lyrics     *Lyrics
	subs       map[chan struct{}]struct{}
	httpServer *http.Server
	listener   net.Listener
	overrides  *OverrideStore
	allowWrite bool
	onChanged  func(SongKey)
	dispatch   func(context.Context, func()) error
	instanceID string
}

const DefaultAddress = "127.0.0.1:23332"

func NewServer(overrides ...*OverrideStore) *Server {
	var store *OverrideStore
	if len(overrides) > 0 {
		store = overrides[0]
	}
	return &Server{subs: make(map[chan struct{}]struct{}), overrides: store, instanceID: rand.Text()}
}

func (s *Server) SetLyricsWriteEnabled(enabled bool) {
	s.mu.Lock()
	s.allowWrite = enabled
	s.notifySubscribersLocked()
	s.mu.Unlock()
}

func (s *Server) SetLyricsOverrideChanged(fn func(SongKey)) {
	s.mu.Lock()
	s.onChanged = fn
	s.mu.Unlock()
}

func (s *Server) SetOverrideDispatcher(dispatch func(context.Context, func()) error) {
	s.dispatch = dispatch
}

func (s *Server) InvalidateLyrics() {
	s.mu.Lock()
	s.status.LyricsRevision++
	s.lyrics = nil
	s.status.LyricsReady = false
	s.status.LyricsOrigin = ""
	s.notifySubscribersLocked()
	s.mu.Unlock()
}

func (s *Server) Start(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	// 只接受明确的回环 IP，避免解析自定义主机名后意外开放到局域网。
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("BetterLyrics integration must listen on loopback")
	}
	s.mu.RLock()
	alreadyStarted := s.httpServer != nil
	s.mu.RUnlock()
	if alreadyStarted {
		return nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.httpServer != nil {
		s.mu.Unlock()
		_ = listener.Close()
		return nil
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	s.httpServer, s.listener = server, listener
	s.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (s *Server) Address() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Close() error {
	s.mu.Lock()
	server := s.httpServer
	s.httpServer, s.listener = nil, nil
	s.mu.Unlock()
	if server != nil {
		return server.Close()
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.serveStatus)
	mux.HandleFunc("/subscribe-player-status", s.serveStatusStream)
	mux.HandleFunc("/lyrics", s.serveLyrics)
	mux.HandleFunc("/lyrics-override", s.serveLyricsOverride)
	return mux
}

func (s *Server) Publish(status Status, lyrics *Lyrics) {
	s.mu.Lock()
	if s.overrides != nil {
		if override := s.overrides.Get(status.SongKey); override != nil {
			lyrics = &Lyrics{
				TrackID:     status.TrackID,
				SongKey:     status.SongKey,
				Origin:      "override",
				LRC:         override.LRC,
				Translation: override.Translation,
				Lines:       override.Lines,
			}
		}
	}
	status.LyricsReady = lyrics != nil && lyrics.TrackID == status.TrackID
	if lyrics != nil && lyrics.TrackID == status.TrackID && lyrics.SongKey == (SongKey{}) {
		lyrics = cloneLyricsPointer(lyrics)
		lyrics.SongKey = status.SongKey
	}
	if lyrics != nil && lyrics.TrackID == status.TrackID && lyrics.Origin == "override" {
		status.LyricsOrigin = "override"
	}
	status.LyricsRevision = s.status.LyricsRevision
	s.status, s.updated = status, time.Now()
	if lyrics != nil && lyrics.TrackID == status.TrackID {
		copy := cloneLyrics(*lyrics)
		s.lyrics = &copy
	} else {
		s.lyrics = nil
	}
	s.notifySubscribersLocked()
	s.mu.Unlock()
}

func cloneLyrics(lyrics Lyrics) Lyrics {
	lyrics.Lines = slices.Clone(lyrics.Lines)
	for i := range lyrics.Lines {
		lyrics.Lines[i].Words = slices.Clone(lyrics.Lines[i].Words)
	}
	return lyrics
}

func cloneLyricsPointer(lyrics *Lyrics) *Lyrics {
	if lyrics == nil {
		return nil
	}
	copy := cloneLyrics(*lyrics)
	return &copy
}

func (s *Server) currentStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentStatusLocked(time.Now())
}

func (s *Server) currentStatusLocked(now time.Time) Status {
	status := s.status
	status.InstanceID = s.instanceID
	status.LyricsWriteEnabled = s.allowWrite
	if status.Playing && !s.updated.IsZero() {
		status.PositionMs += now.Sub(s.updated).Milliseconds()
	}
	if status.DurationMs > 0 && status.PositionMs > status.DurationMs {
		status.PositionMs = status.DurationMs
	}
	return status
}

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(s.currentStatus()); err != nil {
		http.Error(w, "could not encode playback status", http.StatusInternalServerError)
	}
}

func (s *Server) serveLyrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	trackID, err := strconv.ParseUint(r.URL.Query().Get("trackId"), 10, 64)
	if err != nil {
		http.Error(w, "a valid trackId is required", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	status, lyrics := s.currentStatusLocked(time.Now()), s.lyrics
	if trackID != status.TrackID || lyrics == nil || lyrics.TrackID != trackID {
		s.mu.RUnlock()
		http.NotFound(w, r)
		return
	}
	copy := cloneLyrics(*lyrics)
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(copy); err != nil {
		http.Error(w, "could not encode lyrics", http.StatusInternalServerError)
	}
}

func (s *Server) serveLyricsOverride(w http.ResponseWriter, r *http.Request) {
	// 回环地址不等于网站可信；拒绝浏览器跨站请求及 DNS 重绑定主机。
	host := r.Host
	if parsed, err := url.Parse("http://" + host); err == nil {
		host = parsed.Hostname()
	}
	ip := net.ParseIP(host)
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" ||
		r.Header.Get("X-BetterLyrics-Client") != "BetterLyrics" ||
		(host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		writeOverrideError(w, http.StatusForbidden, "untrusted_client")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.putLyricsOverride(w, r)
	case http.MethodDelete:
		s.deleteLyricsOverride(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) putLyricsOverride(w http.ResponseWriter, r *http.Request) {
	var request LyricsOverrideRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := decoder.Decode(&request); err != nil {
		writeOverrideError(w, http.StatusBadRequest, "invalid_lyrics")
		return
	}
	if decoder.Decode(new(any)) != io.EOF || request.TrackID == 0 || !request.SongKey.valid() || !validLyrics(request.Lyrics) {
		writeOverrideError(w, http.StatusBadRequest, "invalid_lyrics")
		return
	}
	request.Lyrics.TrackID = request.TrackID
	request.Lyrics.SongKey = request.SongKey
	request.Lyrics.Origin = "override"

	code := ""
	statusCode := http.StatusOK
	err := s.runOverride(r.Context(), func() {
		s.mu.Lock()
		if !s.allowWrite {
			statusCode, code = http.StatusForbidden, "write_disabled"
		} else if request.InstanceID != s.instanceID || s.status.TrackID != request.TrackID || s.status.SongKey != request.SongKey {
			statusCode, code = http.StatusConflict, "track_changed"
		} else if s.overrides == nil {
			statusCode, code = http.StatusServiceUnavailable, "storage_unavailable"
		} else if err := s.overrides.Put(request.SongKey, music.Lyrics{LRC: request.Lyrics.LRC, Translation: request.Lyrics.Translation, Lines: request.Lyrics.Lines}); err != nil {
			statusCode, code = http.StatusInternalServerError, "save_failed"
		} else {
			s.status.LyricsReady = true
			s.status.LyricsOrigin = "override"
			s.status.LyricsRevision++
			s.lyrics = cloneLyricsPointer(&request.Lyrics)
			s.notifySubscribersLocked()
		}
		changed := s.onChanged
		s.mu.Unlock()
		if code == "" && changed != nil {
			changed(request.SongKey)
		}
	})
	if err != nil {
		writeOverrideError(w, http.StatusRequestTimeout, "request_cancelled")
		return
	}
	if code != "" {
		writeOverrideError(w, statusCode, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(request.Lyrics)
}

func (s *Server) deleteLyricsOverride(w http.ResponseWriter, r *http.Request) {
	key := SongKey{Source: r.URL.Query().Get("source"), StableID: r.URL.Query().Get("stableId")}
	if !key.valid() {
		http.Error(w, "a valid songKey is required", http.StatusBadRequest)
		return
	}
	code := ""
	statusCode := http.StatusNoContent
	err := s.runOverride(r.Context(), func() {
		s.mu.Lock()
		if !s.allowWrite {
			statusCode, code = http.StatusForbidden, "write_disabled"
		} else if s.overrides == nil {
			statusCode, code = http.StatusServiceUnavailable, "storage_unavailable"
		} else if err := s.overrides.Delete(key); err != nil {
			statusCode, code = http.StatusInternalServerError, "save_failed"
		} else {
			if s.status.SongKey == key {
				s.lyrics = nil
				s.status.LyricsReady = false
				s.status.LyricsOrigin = ""
			}
			s.status.LyricsRevision++
			s.notifySubscribersLocked()
		}
		changed := s.onChanged
		s.mu.Unlock()
		if code == "" && changed != nil {
			changed(key)
		}
	})
	if err != nil {
		writeOverrideError(w, http.StatusRequestTimeout, "request_cancelled")
		return
	}
	if code != "" {
		writeOverrideError(w, statusCode, code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runOverride(ctx context.Context, operation func()) error {
	if s.dispatch != nil {
		return s.dispatch(ctx, operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operation()
	return nil
}

func writeOverrideError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func validLyrics(source Lyrics) bool {
	if len(source.Lines) > 20000 {
		return false
	}
	for _, line := range source.Lines {
		if line.Start < 0 || line.Start > 2147483647 || line.End < 0 || line.End > 2147483647 || line.End != 0 && line.End < line.Start || len(line.Words) > 20000 {
			return false
		}
		var words strings.Builder
		for _, word := range line.Words {
			if word.Start < line.Start || word.End <= word.Start || word.End > 2147483647 || line.End > 0 && word.End > line.End {
				return false
			}
			words.WriteString(word.Text)
		}
		if len(line.Words) > 0 && line.Text != "" && words.String() != line.Text {
			return false
		}
	}
	return len(lyricparser.FromSource(&music.Lyrics{LRC: source.LRC, Translation: source.Translation, Lines: source.Lines})) > 0
}

func (s *Server) notifySubscribersLocked() {
	for sub := range s.subs {
		select {
		case sub <- struct{}{}:
		default:
		}
	}
}

func (s *Server) serveStatusStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	sub := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}()
	writeStatus := func() bool {
		data, err := json.Marshal(s.currentStatus())
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: status\ndata: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !writeStatus() {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub:
			if !writeStatus() {
				return
			}
		case <-ticker.C:
			if s.currentStatus().Playing && !writeStatus() {
				return
			}
		}
	}
}
