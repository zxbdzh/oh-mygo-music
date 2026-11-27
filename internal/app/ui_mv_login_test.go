package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

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

// 设置页能直接展开登录，二维码与 Cookie 输入都在设置窗口里。
func TestMVLoginInlineFromSettings(t *testing.T) {
	previous := credential.System
	credential.System = &memoryCredentialStore{values: map[string]string{}}
	defer func() { credential.System = previous }()
	old := mvLoginQRCode
	mvLoginQRCode = func(context.Context) (*mv.LoginQR, error) {
		return &mv.LoginQR{URL: "https://example.test/qr", Key: "key"}, nil
	}
	defer func() { mvLoginQRCode = old }()

	a := newTestApp()
	ch := posts(a)
	a.settingsPage = "playback"
	a.settingsQuery = strings.ToLower(T("Bilibili Account"))
	tester := ui.NewTester(a.settingsView, 900, 800)
	settled(tester)
	if err := tester.Click(T("Sign In")); err != nil {
		t.Fatal(err)
	}
	runPost(t, ch)
	tester.Frame()
	if !a.mvLogin.open || !a.mvLogin.inline || a.mvLogin.qrCode == nil {
		t.Fatalf("inline login state %+v", a.mvLogin)
	}
	if _, ok := tester.Find(T("Use Cookie")); !ok {
		t.Fatal("cookie input missing from the settings login")
	}
	if err := tester.Click(T("Done")); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if a.mvLogin.open || a.mvLogin.inline {
		t.Fatalf("inline login stayed open: %+v", a.mvLogin)
	}
}

// 已登录时设置页显示账号名，并能直接退出。
func TestMVAccountRowInSettings(t *testing.T) {
	previous := credential.System
	credential.System = &memoryCredentialStore{values: map[string]string{}}
	defer func() { credential.System = previous }()

	a := newTestApp()
	a.saveMVLogin("SESSDATA=synthetic", mv.Account{Name: "Listener"})
	a.toast = ""
	a.settingsPage = "playback"
	a.settingsQuery = strings.ToLower(T("Bilibili Account"))
	tester := ui.NewTester(a.settingsView, 900, 800)
	settled(tester)
	if _, ok := tester.Find(Tf("Signed in as %s", "Listener")); !ok {
		t.Fatal("account name missing from the settings")
	}
	if err := tester.Click(T("Sign Out")); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if a.mvCredential != "" || a.mvAccount != "" {
		t.Fatalf("sign out left %q / %q", a.mvCredential, a.mvAccount)
	}
}

// 复用插件登录时，退出只停止复用，不删除插件或 MV 自己的凭据。
func TestMVSignOutKeepsPluginCookie(t *testing.T) {
	previous := credential.System
	store := &memoryCredentialStore{values: map[string]string{}}
	credential.System = store
	defer func() { credential.System = previous }()

	a := newTestApp()
	a.mvCredential, a.mvFromPlugin = "SESSDATA=plugin", true
	a.signOutMV()
	if a.mvCredential != "" || a.mvFromPlugin {
		t.Fatalf("state %q / %v", a.mvCredential, a.mvFromPlugin)
	}
	if _, ok := store.values[mv.CookieTarget]; ok {
		t.Fatal("MV removed a credential it did not set")
	}
	if a.toast != T("Stopped using the plugin's Bilibili sign-in.") {
		t.Fatalf("toast %q", a.toast)
	}
}
