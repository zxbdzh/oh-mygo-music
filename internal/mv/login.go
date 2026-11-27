package mv

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const passportBase = "https://passport.bilibili.com"

// CookieTarget is the system credential key for the Bilibili login cookie.
const CookieTarget = "MyGo-Music/MV/Bilibili"

var ErrNotLoggedIn = errors.New("mv: bilibili account is not signed in")

// LoginQR 是一次扫码登录的二维码内容。二维码本身由界面层编码成方块。
type LoginQR struct {
	URL string
	Key string
}

type QRState int

const (
	QRWaiting QRState = iota
	QRScanned
	QRExpired
	QRDone
)

// QRResult 是轮询二维码后的状态；完成时 Cookie 已经是可直接转发的请求头。
type QRResult struct {
	State  QRState
	Cookie string
}

// Account 是 B 站登录账号的最小身份信息，不包含 Cookie。
type Account struct {
	Name string
	MID  int64
	Face string
}

// LoginQRCode 向 B 站申请一张登录二维码。
func (c *Client) LoginQRCode(ctx context.Context) (*LoginQR, error) {
	payload, err := c.get(ctx, passportBase+"/x/passport-login/web/qrcode/generate", "https://www.bilibili.com/")
	if err != nil {
		return nil, err
	}
	data, _ := payload["data"].(map[string]any)
	raw := firstString(data["url"])
	key := firstString(data["qrcode_key"])
	if raw == "" || key == "" {
		return nil, errors.New("mv: qrcode response is incomplete")
	}
	return &LoginQR{URL: raw, Key: key}, nil
}

// PollLoginQRCode 查询扫码状态；扫码成功后把 cookie_info 拼成 Cookie。
func (c *Client) PollLoginQRCode(ctx context.Context, key string) (QRResult, error) {
	if key == "" {
		return QRResult{}, errors.New("mv: empty qrcode key")
	}
	endpoint := passportBase + "/x/passport-login/web/qrcode/poll?qrcode_key=" + url.QueryEscape(key)
	payload, err := c.get(ctx, endpoint, "https://www.bilibili.com/")
	if err != nil {
		return QRResult{}, err
	}
	data, _ := payload["data"].(map[string]any)
	code, _ := number(data["code"])
	switch int(code) {
	case 86101:
		return QRResult{State: QRWaiting}, nil
	case 86090:
		return QRResult{State: QRScanned}, nil
	case 86038:
		return QRResult{State: QRExpired}, nil
	case 0:
		cookie := cookieFromData(data)
		if cookie == "" {
			return QRResult{}, errors.New("mv: qrcode login returned no cookie")
		}
		return QRResult{State: QRDone, Cookie: cookie}, nil
	default:
		message := firstString(data["message"])
		return QRResult{}, fmt.Errorf("mv: qrcode state %s %s", strconv.Itoa(int(code)), message)
	}
}

// Account 校验当前 Cookie，并返回显示的账号名。
func (c *Client) Account(ctx context.Context) (Account, error) {
	payload, err := c.get(ctx, apiBase+"/x/web-interface/nav", "https://www.bilibili.com/")
	if err != nil {
		return Account{}, ErrNotLoggedIn
	}
	data, _ := payload["data"].(map[string]any)
	if logged, _ := data["isLogin"].(bool); !logged {
		return Account{}, ErrNotLoggedIn
	}
	return Account{
		Name: firstString(data["uname"]),
		MID:  int64(numberOr(data["mid"])),
		Face: firstString(data["face"]),
	}, nil
}

// NormalizeCookie 接受整段 Cookie 头或裸 key=value，并去掉换行，避免请求头注入。
func NormalizeCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "Cookie:")
	raw = strings.NewReplacer("\r", ";", "\n", ";").Replace(raw)
	var parts []string
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if name, value, ok := strings.Cut(part, "="); ok && strings.TrimSpace(name) != "" && strings.TrimSpace(value) != "" {
			name, value = strings.TrimSpace(name), strings.TrimSpace(value)
			// 只留下 B 站登录和取流需要的 Cookie，整段浏览器 Cookie 常超过系统凭据上限。
			if loginCookieNames[name] {
				parts = append(parts, name+"="+value)
			}
		}
	}
	return strings.Join(parts, "; ")
}

var loginCookieNames = map[string]bool{
	"SESSDATA":          true,
	"bili_jct":          true,
	"DedeUserID":        true,
	"DedeUserID__ckMd5": true,
	"sid":               true,
	"buvid3":            true,
	"buvid4":            true,
	"buvid_fp":          true,
	"b_nut":             true,
}

func cookieFromData(data map[string]any) string {
	info, _ := data["cookie_info"].(map[string]any)
	items, _ := info["cookies"].([]any)
	var parts []string
	for _, item := range items {
		cookie, _ := item.(map[string]any)
		name := firstString(cookie["name"])
		value := firstString(cookie["value"])
		if name != "" && value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	if len(parts) > 0 {
		return NormalizeCookie(strings.Join(parts, "; "))
	}
	// 兼容只返回 login url 的旧响应；只取登录必需的参数，不把整个 URL 当 Cookie。
	loginURL, _ := url.Parse(firstString(data["url"]))
	if loginURL == nil {
		return ""
	}
	for _, name := range []string{"SESSDATA", "bili_jct", "DedeUserID", "DedeUserID__ckMd5", "sid"} {
		if value := loginURL.Query().Get(name); value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	return NormalizeCookie(strings.Join(parts, "; "))
}
