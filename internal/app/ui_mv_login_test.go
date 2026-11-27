package app

import (
	"errors"
	"testing"

	"mygo-music/internal/credential"
	"mygo-music/internal/mv"
)

type memoryCredentialStore struct {
	values  map[string]string
	readErr error
}

func (s *memoryCredentialStore) Get(target string) (string, bool, error) {
	if s.readErr != nil {
		return "", false, s.readErr
	}
	value, ok := s.values[target]
	return value, ok, nil
}

func (s *memoryCredentialStore) Set(target, value string) error {
	if s.readErr != nil {
		return s.readErr
	}
	s.values[target] = value
	return nil
}

func (s *memoryCredentialStore) Remove(target string) error {
	if s.readErr != nil {
		return s.readErr
	}
	delete(s.values, target)
	return nil
}

// 登录成功写系统凭据，退出登录删除；不能把 Cookie 留在内存或存储里。
func TestMVLoginSavesAndRemovesCredential(t *testing.T) {
	previous := credential.System
	store := &memoryCredentialStore{values: map[string]string{}}
	credential.System = store
	defer func() { credential.System = previous }()

	a := newTestApp()
	a.saveMVLogin("SESSDATA=synthetic; bili_jct=csrf", mv.Account{Name: "Listener"})
	if a.mvCredential == "" || a.mvAccount != "Listener" {
		t.Fatalf("login state %q / %q", a.mvCredential, a.mvAccount)
	}
	if store.values[mv.CookieTarget] != a.mvCredential {
		t.Fatal("cookie was not written to the credential store")
	}
	a.signOutMV()
	if a.mvCredential != "" || a.mvAccount != "" {
		t.Fatalf("logout left state %q / %q", a.mvCredential, a.mvAccount)
	}
	if _, ok := store.values[mv.CookieTarget]; ok {
		t.Fatal("cookie remained after sign out")
	}
}

// 凭据库不可写时仍允许本次运行登录，但不能假装已经持久化。
func TestMVLoginKeepsSessionWhenCredentialStoreFails(t *testing.T) {
	previous := credential.System
	credential.System = &memoryCredentialStore{values: map[string]string{}, readErr: errors.New("unavailable")}
	defer func() { credential.System = previous }()

	a := newTestApp()
	a.saveMVLogin("SESSDATA=synthetic", mv.Account{Name: "Listener"})
	if a.mvCredential == "" {
		t.Fatal("session login was discarded")
	}
	if a.toast != T("Signed in for this session only.") {
		t.Fatalf("toast %q", a.toast)
	}
}
