package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"

	"mygo-music/internal/credential"
)

func withPluginData(t *testing.T, storage string) {
	t.Helper()
	previous, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mygo.App.SetPath(mygo.PathUserData, dir)
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathUserData, previous) })
	if storage == "" {
		return
	}
	pluginDir := filepath.Join(dir, "plugin-data", "bilibili")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "storage.json"), []byte(storage), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 已安装 B 站插件有登录时复用它的 Cookie，只保留取流需要的字段。
func TestMVPluginCookieReusesBilibiliSignIn(t *testing.T) {
	withPluginData(t, `{"cookie":"SESSDATA=synthetic; bili_jct=csrf; _uuid=drop"}`)
	if got := mvPluginCookie(); got != "SESSDATA=synthetic; bili_jct=csrf" {
		t.Fatalf("plugin cookie %q", got)
	}
}

// 插件没登录或没安装时不能拿访客 Cookie 冒充登录态。
func TestMVPluginCookieIgnoresAnonymousStorage(t *testing.T) {
	for _, storage := range []string{"", `{"cookie":"buvid3=visitor"}`, `{"visitor":"buvid3=visitor"}`} {
		withPluginData(t, storage)
		if got := mvPluginCookie(); got != "" {
			t.Fatalf("anonymous storage %q produced %q", storage, got)
		}
	}
}

// 启动时只标记插件可复用，用户点了才真正使用插件登录。
func TestMVPluginLoginNeedsConfirmation(t *testing.T) {
	previous := credential.System
	credential.System = &memoryCredentialStore{values: map[string]string{}}
	defer func() { credential.System = previous }()
	withPluginData(t, `{"cookie":"SESSDATA=synthetic; bili_jct=csrf"}`)

	a := newTestApp()
	a.loadMVLogin()
	if a.mvCredential != "" || a.mvFromPlugin || !a.mvPluginAvailable {
		t.Fatalf("startup state %q / %v / %v", a.mvCredential, a.mvFromPlugin, a.mvPluginAvailable)
	}
	a.useMVPluginLogin()
	if a.mvCredential != "SESSDATA=synthetic; bili_jct=csrf" || !a.mvFromPlugin || a.mvPluginAvailable {
		t.Fatalf("confirmed state %q / %v / %v", a.mvCredential, a.mvFromPlugin, a.mvPluginAvailable)
	}
}

// 选过一次「使用插件登录」之后，重启直接复用，不再要求点第二次。
func TestMVPluginLoginIsRemembered(t *testing.T) {
	previous := credential.System
	credential.System = &memoryCredentialStore{values: map[string]string{}}
	defer func() { credential.System = previous }()
	withPluginData(t, `{"cookie":"SESSDATA=synthetic; bili_jct=csrf"}`)

	a := newTestApp()
	a.loadMVLogin()
	a.useMVPluginLogin()
	if !a.settings.MVUsePluginLogin {
		t.Fatal("the choice wasn't remembered")
	}
	// 下次启动：新的 app 带着同一条设置，直接复用插件登录。
	b := newTestApp()
	b.settings.MVUsePluginLogin = true
	b.loadMVLogin()
	if b.mvCredential != "SESSDATA=synthetic; bili_jct=csrf" || !b.mvFromPlugin || b.mvPluginAvailable {
		t.Fatalf("startup state %q / %v / %v", b.mvCredential, b.mvFromPlugin, b.mvPluginAvailable)
	}
}

// 插件自己退出登录后，记住的选择作废，不能拿访客 Cookie 冒充登录。
func TestMVPluginLoginForgottenWhenPluginSignsOut(t *testing.T) {
	previous := credential.System
	credential.System = &memoryCredentialStore{values: map[string]string{}}
	defer func() { credential.System = previous }()
	withPluginData(t, `{"cookie":"buvid3=visitor"}`)

	a := newTestApp()
	a.settings.MVUsePluginLogin = true
	a.loadMVLogin()
	if a.mvCredential != "" || a.mvFromPlugin || a.settings.MVUsePluginLogin {
		t.Fatalf("state %q / %v / %v", a.mvCredential, a.mvFromPlugin, a.settings.MVUsePluginLogin)
	}
}

// 停止复用插件登录后，下次启动不再自动读回来。
func TestMVSignOutForgetsPluginLogin(t *testing.T) {
	previous := credential.System
	credential.System = &memoryCredentialStore{values: map[string]string{}}
	defer func() { credential.System = previous }()
	a := newTestApp()
	a.mvCredential, a.mvFromPlugin = "SESSDATA=synthetic", true
	a.settings.MVUsePluginLogin = true
	a.signOutMV()
	if a.mvCredential != "" || a.settings.MVUsePluginLogin {
		t.Fatalf("state %q / %v", a.mvCredential, a.settings.MVUsePluginLogin)
	}
}
