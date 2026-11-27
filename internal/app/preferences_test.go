package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Adopting an adjusted suggestion learns a preference for that source and
// account only, and keeps what the listener said.
func TestLearnPreferenceKeepsSourceAndAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-preferences.json")
	store := newPreferenceStore(path)
	store.add(Preference{Source: "netease", Account: "u1", Text: "慢歌更适合睡前", PlaylistID: "10", PlaylistName: "睡前"})
	store.add(Preference{Source: "netease", Account: "u2", Text: "慢歌更适合通勤", PlaylistID: "11", PlaylistName: "通勤"})

	got := store.forListener("netease", "u1")
	if len(got) != 1 || got[0].Text != "慢歌更适合睡前" || got[0].PlaylistID != "10" {
		t.Fatalf("preferences = %+v", got)
	}
	if other := store.forListener("netease", "u2"); len(other) != 1 || other[0].PlaylistID != "11" {
		t.Errorf("the other account sees %+v", other)
	}
}

// The preferences survive a restart, as a readable file next to the settings.
func TestPreferencesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-preferences.json")
	store := newPreferenceStore(path)
	store.add(Preference{Source: "netease", Account: "u1", Text: "慢歌更适合睡前", PlaylistID: "10"})

	again := newPreferenceStore(path)
	got := again.forListener("netease", "u1")
	if len(got) != 1 || got[0].PlaylistID != "10" {
		t.Fatalf("after restart = %+v", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Preferences []Preference `json:"preferences"`
	}
	if err := json.Unmarshal(b, &file); err != nil || len(file.Preferences) != 1 {
		t.Fatalf("file = %s, err = %v", b, err)
	}
}

// 旧文件超限时只启用最新二十条，下一次保存才更新文件。
func TestPreferencesLimitLegacyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-preferences.json")
	var file struct {
		Preferences []Preference `json:"preferences"`
	}
	for index := range preferenceLimit + 1 {
		file.Preferences = append(file.Preferences, Preference{Source: "netease", Account: "u1", Text: string(rune('a' + index))})
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newPreferenceStore(path)
	if got := store.forListener("netease", "u1"); len(got) != preferenceLimit || got[preferenceLimit-1].Text != "b" {
		t.Fatalf("active preferences loaded from legacy file = %+v", got)
	}
	if afterLoad, err := os.ReadFile(path); err != nil || string(afterLoad) != string(raw) {
		t.Fatalf("loading changed the legacy file: %v", err)
	}

	store.remove("netease", "u1", 0)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(saved, &file); err != nil || len(file.Preferences) != preferenceLimit-1 || file.Preferences[0].Text != "b" {
		t.Fatalf("saved preferences kept an evicted record: %+v, %v", file.Preferences, err)
	}
}

// Each source and account keeps twenty preferences, dropping the oldest one.
func TestPreferencesLimitPerListener(t *testing.T) {
	store := newPreferenceStore("")
	for i := range preferenceLimit + 1 {
		store.add(Preference{Source: "netease", Account: "u1", Text: string(rune('a' + i))})
	}
	store.add(Preference{Source: "netease", Account: "u2", Text: "other account"})
	store.add(Preference{Source: "spotify", Account: "u1", Text: "other source"})

	got := store.forListener("netease", "u1")
	if len(got) != preferenceLimit || got[0].Text != "u" || got[len(got)-1].Text != "b" {
		t.Fatalf("limited preferences = %+v", got)
	}
	if got := store.forListener("netease", "u2"); len(got) != 1 || got[0].Text != "other account" {
		t.Fatalf("other account preferences = %+v", got)
	}
	if got := store.forListener("spotify", "u1"); len(got) != 1 || got[0].Text != "other source" {
		t.Fatalf("other source preferences = %+v", got)
	}
	store.remove("netease", "u1", 0)
	if got := store.forListener("netease", "u1"); len(got) != preferenceLimit-1 || got[len(got)-1].Text != "b" {
		t.Fatalf("removing a preference brought back the oldest one: %+v", got)
	}
}

// A broken file is treated as empty and left as it was, not overwritten.
func TestPreferencesIgnoreBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-preferences.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newPreferenceStore(path)
	if got := store.forListener("netease", "u1"); len(got) != 0 {
		t.Fatalf("read %+v from a broken file", got)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "{not json" {
		t.Errorf("the broken file was overwritten: %s", b)
	}
}

// Removing a preference drops just that one.
func TestPreferenceRemove(t *testing.T) {
	store := newPreferenceStore(filepath.Join(t.TempDir(), "ai-preferences.json"))
	store.add(Preference{Source: "netease", Account: "u1", Text: "one", PlaylistID: "10"})
	store.add(Preference{Source: "netease", Account: "u1", Text: "two", PlaylistID: "11"})

	store.remove("netease", "u1", 0)
	got := store.forListener("netease", "u1")
	if len(got) != 1 || got[0].Text != "one" {
		t.Fatalf("after remove = %+v", got)
	}
}

// New preferences append rather than replacing an older one on the same
// subject, so the listener can still delete either.
func TestPreferencesAppend(t *testing.T) {
	store := newPreferenceStore(filepath.Join(t.TempDir(), "ai-preferences.json"))
	store.add(Preference{Source: "netease", Account: "u1", Text: "周杰伦慢歌适合睡前", PlaylistID: "10"})
	store.add(Preference{Source: "netease", Account: "u1", Text: "周杰伦慢歌更适合通勤", PlaylistID: "11"})
	got := store.forListener("netease", "u1")
	if len(got) != 2 || !slices.Contains([]string{got[0].PlaylistID, got[1].PlaylistID}, "11") {
		t.Fatalf("preferences = %+v", got)
	}
}
