package betterlyrics

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"mygo-music/internal/music"
)

// 歌曲来源和来源自己的 ID 跨播放会话稳定，适合用作持久覆盖键。
type SongKey struct {
	Source   string `json:"source"`
	StableID string `json:"stableId"`
}

func (k SongKey) valid() bool {
	return strings.TrimSpace(k.Source) != "" && strings.TrimSpace(k.StableID) != ""
}

func (k SongKey) index() string {
	return k.Source + "\x00" + k.StableID
}

func KeyForTrack(track music.Track) SongKey {
	if track.ID == "" {
		if strings.TrimSpace(track.Name) == "" || strings.TrimSpace(track.ArtistNames()) == "" ||
			strings.TrimSpace(track.Album.Name) == "" || track.Duration <= 0 {
			return SongKey{}
		}
		metadata, _ := json.Marshal([]any{normalizeIdentity(track.Name), normalizeIdentity(track.ArtistNames()), normalizeIdentity(track.Album.Name), track.Duration})
		return SongKey{Source: track.Source, StableID: fmt.Sprintf("metadata:%x", sha256.Sum256(metadata))}
	}
	return SongKey{Source: track.Source, StableID: track.ID}
}

func normalizeIdentity(text string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(text), func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsPunct(character)
	}), " ")
}

func LocalSongKey(path string) SongKey {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return SongKey{}
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return SongKey{Source: "local", StableID: path}
}

type storedOverride struct {
	SongKey   SongKey      `json:"songKey"`
	Lyrics    music.Lyrics `json:"lyrics"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// 覆盖独立于来源文件保存，清缓存和重新扫描曲库不能删除用户的手动选择。
type OverrideStore struct {
	mu    sync.RWMutex
	path  string
	items map[string]storedOverride
}

func NewOverrideStore(path string) (*OverrideStore, error) {
	store := &OverrideStore{path: path, items: make(map[string]storedOverride)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store.items); err != nil {
		return nil, err
	}
	if store.items == nil {
		store.items = make(map[string]storedOverride)
	}
	for index, item := range store.items {
		if !item.SongKey.valid() || index != item.SongKey.index() ||
			!validLyrics(Lyrics{LRC: item.Lyrics.LRC, Translation: item.Lyrics.Translation, Lines: item.Lyrics.Lines}) {
			return nil, errors.New("invalid lyric override identity")
		}
	}
	return store, nil
}

func (s *OverrideStore) Get(key SongKey) *music.Lyrics {
	if s == nil || !key.valid() {
		return nil
	}
	s.mu.RLock()
	item, ok := s.items[key.index()]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	lyrics := cloneSourceLyrics(item.Lyrics)
	return &lyrics
}

func (s *OverrideStore) Has(key SongKey) bool {
	if s == nil || !key.valid() {
		return false
	}
	s.mu.RLock()
	_, ok := s.items[key.index()]
	s.mu.RUnlock()
	return ok
}

func (s *OverrideStore) Put(key SongKey, lyrics music.Lyrics) error {
	if s == nil || !key.valid() {
		return errors.New("a valid song key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make(map[string]storedOverride, len(s.items)+1)
	for index, item := range s.items {
		items[index] = item
	}
	items[key.index()] = storedOverride{
		SongKey:   key,
		Lyrics:    cloneSourceLyrics(lyrics),
		UpdatedAt: time.Now().UTC(),
	}
	return s.saveLocked(items)
}

func (s *OverrideStore) Delete(key SongKey) error {
	if s == nil || !key.valid() {
		return errors.New("a valid song key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.items[key.index()]; !found {
		return nil
	}
	items := make(map[string]storedOverride, len(s.items))
	for index, item := range s.items {
		if index != key.index() {
			items[index] = item
		}
	}
	return s.saveLocked(items)
}

func (s *OverrideStore) saveLocked(items map[string]storedOverride) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".lyrics-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// 磁盘替换成功才发布内存快照，失败时继续使用上一份已保存的覆盖。
	if err := os.Rename(file.Name(), s.path); err != nil {
		return err
	}
	s.items = items
	return nil
}

func cloneSourceLyrics(source music.Lyrics) music.Lyrics {
	source.Lines = slices.Clone(source.Lines)
	for index := range source.Lines {
		source.Lines[index].Words = slices.Clone(source.Lines[index].Words)
	}
	return source
}
