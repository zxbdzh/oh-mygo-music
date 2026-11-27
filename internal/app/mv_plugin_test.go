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
