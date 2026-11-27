package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mygo-music/internal/credential"
	"mygo-music/internal/mv"
)

// mvPluginCookie 复用已安装 B 站插件保存的登录态。
//
// 只读取插件的 storage.json，不复制进 MV 的凭据库：插件退出登录后，
// 下次启动就不会再复用。storage.json 由宿主在插件数据目录里维护。
func mvPluginCookie() string {
	root := userDataDir("plugin-data")
	ids := []string{"bilibili"}
	if entries, err := os.ReadDir(root); err == nil {
		for _, entry := range entries {
			// 官方插件优先；其它插件只要也保存了 B 站登录，同样可以复用。
			if entry.IsDir() && entry.Name() != "bilibili" {
				ids = append(ids, entry.Name())
			}
		}
	}
	for _, id := range ids {
		raw, err := os.ReadFile(filepath.Join(root, id, "storage.json"))
		if err != nil {
			continue
		}
		var stored map[string]any
		if json.Unmarshal(raw, &stored) != nil {
			continue
		}
		text, _ := stored["cookie"].(string)
		// NormalizeCookie 只留下登录与取流需要的 B 站字段。
		cookie := mv.NormalizeCookie(text)
		if strings.Contains(cookie, "SESSDATA=") {
			return cookie
		}
	}
	return ""
}

// loadMVLogin 先读 MV 自己的凭据；检测到 B 站插件登录时只记下来，
// 等用户在设置里明确选择后才复用，避免隐式读取插件数据。
func (a *app) loadMVLogin() {
	if cookie, ok, _ := credential.Get(mv.CookieTarget); ok {
		a.mvCredential = cookie
		return
	}
	a.mvPluginAvailable = mvPluginCookie() != ""
}

// useMVPluginLogin 是用户点「使用插件登录」后的动作；点击时重新读取，
// 插件这期间退出登录就不会复用过期状态。
func (a *app) useMVPluginLogin() {
	cookie := mvPluginCookie()
	if cookie == "" {
		a.mvPluginAvailable = false
		a.toast = T("The Bilibili plugin is not signed in anymore.")
		return
	}
	a.mvCredential, a.mvFromPlugin = cookie, true
	a.mvPluginAvailable = false
	a.closeMV()
}
