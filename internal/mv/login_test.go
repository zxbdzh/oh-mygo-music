package mv

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type loginDoer func(*http.Request) (*http.Response, error)

func (d loginDoer) Do(r *http.Request) (*http.Response, error) { return d(r) }

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// 二维码生成和轮询要能独立于界面解析状态；成功时把 cookie_info 拼成请求头。
func TestQRCodeLoginParsesStatesAndCookie(t *testing.T) {
	requests := 0
	client := NewClient()
	client.HTTP = loginDoer(func(r *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			return jsonResponse(`{"code":0,"data":{"url":"https://example.test/qr","qrcode_key":"key"}}`), nil
		case 2:
			return jsonResponse(`{"code":0,"data":{"code":86090}}`), nil
		default:
			return jsonResponse(`{"code":0,"data":{"code":0,"cookie_info":{"cookies":[{"name":"SESSDATA","value":"synthetic"},{"name":"bili_jct","value":"csrf"}]}}}`), nil
		}
	})
	qr, err := client.LoginQRCode(context.Background())
	if err != nil || qr.Key != "key" || qr.URL != "https://example.test/qr" {
		t.Fatalf("qr %+v, err %v", qr, err)
	}
	got, err := client.PollLoginQRCode(context.Background(), qr.Key)
	if err != nil || got.State != QRScanned {
		t.Fatalf("scanned %+v, err %v", got, err)
	}
	got, err = client.PollLoginQRCode(context.Background(), qr.Key)
	if err != nil || got.State != QRDone || got.Cookie != "SESSDATA=synthetic; bili_jct=csrf" {
		t.Fatalf("done %+v, err %v", got, err)
	}
}

// Cookie 允许整段粘贴或裸 key=value，换行不能进入请求头。
func TestNormalizeCookie(t *testing.T) {
	got := NormalizeCookie("Cookie: SESSDATA=a%2Cb;\r\nbili_jct=csrf\r\ntheme=light\r\nbad")
	if got != "SESSDATA=a%2Cb; bili_jct=csrf" {
		t.Fatalf("cookie %q", got)
	}
}

// nav 返回 isLogin=false 时要当作未登录，而不是得到空账号。
func TestAccountRequiresLogin(t *testing.T) {
	client := NewClient()
	client.HTTP = loginDoer(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"code":0,"data":{"isLogin":false}}`), nil
	})
	if _, err := client.Account(context.Background()); err != ErrNotLoggedIn {
		t.Fatalf("err %v", err)
	}
}
