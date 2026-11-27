package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
)

// Preference is one thing the listener taught the AI by adopting an adjusted
// playlist for a song.
type Preference struct {
	Source       string    `json:"source"`
	Account      string    `json:"account"`
	Text         string    `json:"text"`
	PlaylistID   string    `json:"playlistId"`
	PlaylistName string    `json:"playlistName,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// preferenceStore keeps what the AI learned in ai-preferences.json in the user
// data directory, readable and editable by the listener.
type preferenceStore struct {
	path  string
	items []Preference
}

func preferencesPath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ai-preferences.json"), nil
}

func newPreferenceStore(path string) *preferenceStore {
	s := &preferenceStore{path: path}
	if b, err := os.ReadFile(path); err == nil {
		// 坏文件按空库处理且保留原文，避免一次解析失败抹掉用户数据。
		var file struct {
			Preferences []Preference `json:"preferences"`
		}
		if json.Unmarshal(b, &file) == nil {
			s.items = file.Preferences
		}
	}
	return s
}

// add appends a preference and keeps the file.
func (s *preferenceStore) add(p Preference) {
	p.UpdatedAt = time.Now()
	s.items = append(s.items, p)
	s.save()
}

// remove drops the preference shown at index, which is the listener's list
// order, and keeps the file.
func (s *preferenceStore) remove(source, account string, at int) {
	var seen int
	var kept []Preference
	for _, p := range s.items {
		if p.Source == source && p.Account == account {
			if seen == at {
				seen++
				continue
			}
			seen++
		}
		kept = append(kept, p)
	}
	s.items = kept
	s.save()
}

// forListener returns the newest twenty preferences of a source and account,
// newest first, as the model sees them.
func (s *preferenceStore) forListener(source, account string) []Preference {
	var out []Preference
	for i := len(s.items) - 1; i >= 0 && len(out) < 20; i-- {
		if p := s.items[i]; p.Source == source && p.Account == account {
			out = append(out, p)
		}
	}
	return out
}

func (s *preferenceStore) save() {
	if s.path == "" {
		return
	}
	b, err := json.MarshalIndent(map[string]any{"preferences": s.items}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(s.path+".part", b, 0o600)
	_ = os.Rename(s.path+".part", s.path)
}
