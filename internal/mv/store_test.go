package mv

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreBindingRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mv.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Source: "net", TrackID: "42", VideoID: "BV1", Title: "晴天"}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Binding("net", "42")
	if !ok || got.VideoID != "BV1" || got.Title != "晴天" {
		t.Fatalf("binding %+v ok=%v", got, ok)
	}
	if err := reopened.DeleteBinding("net", "42"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Binding("net", "42"); ok {
		t.Fatal("binding still there after delete")
	}
}

// ChangeOffset persists bounded per-track alignment values across restarts.
func TestStoreChangeOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mv.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(Binding{Source: "net", TrackID: "42", VideoID: "BV1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeOffset("net", "42", 100); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeOffset("net", "42", 700000); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := reopened.Binding("net", "42")
	if !ok || binding.OffsetMs != 600000 {
		t.Fatalf("binding %+v, want offset 600000", binding)
	}
}

func TestStoreResolvedExpires(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "mv.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutResolved("BV1", Resolved{URL: "https://cdn/x", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Resolved("BV1"); !ok {
		t.Fatal("fresh stream missing")
	}
	if err := store.PutResolved("BV2", Resolved{URL: "https://cdn/y", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Resolved("BV2"); ok {
		t.Fatal("expired stream returned")
	}
}
