package mv

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// 联网验收，平时跳过；改动取流或代理后手动跑一次：
//
//	MV_LIVE=1 go test ./internal/mv -run TestLive -v
func TestLiveSearchResolveAndProxy(t *testing.T) {
	if os.Getenv("MV_LIVE") != "1" {
		t.Skip("set MV_LIVE=1 to run the live Bilibili check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	client := NewClient()
	client.Cookie = NormalizeCookie(os.Getenv("MV_LIVE_COOKIE"))
	if client.Cookie != "" {
		account, err := client.Account(ctx)
		if err != nil {
			t.Fatalf("account: %v", err)
		}
		t.Logf("account: %s (%d)", account.Name, account.MID)
	}
	candidates, err := client.Search(ctx, "周杰伦 晴天")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("no candidates")
	}
	t.Logf("first candidate: %s %s %s", candidates[0].VideoID, candidates[0].Title, candidates[0].Duration)

	resolved, err := client.Resolve(ctx, candidates[0].VideoID)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(resolved.URL)
	t.Logf("resolved: kind=%s quality=%d resolution=%d height=%d fps=%d host=%s", resolved.Kind, resolved.Quality, resolved.Resolution(), resolved.Height, resolved.FPS, parsed.Host)

	proxy, err := NewProxy(resolved, func(context.Context) (*Resolved, error) { return resolved, nil }, client.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodGet, proxy.URL(), nil)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || len(body) != 1024 {
		t.Fatalf("status %d, %d bytes: %s", resp.StatusCode, len(body), body)
	}
	if resp.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	t.Logf("range ok, total %s", resp.Header.Get("Content-Range"))
}

// 扫码接口的真实形状会变；联网验收至少确认能生成二维码并轮询到等待状态。
func TestLiveLoginQRCode(t *testing.T) {
	if os.Getenv("MV_LIVE") != "1" {
		t.Skip("set MV_LIVE=1 to run the live Bilibili check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := NewClient()
	login, err := client.LoginQRCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if login.URL == "" || login.Key == "" {
		t.Fatalf("incomplete qr: %+v", login)
	}
	result, err := client.PollLoginQRCode(ctx, login.Key)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != QRWaiting && result.State != QRScanned && result.State != QRExpired {
		t.Fatalf("unexpected state %d", result.State)
	}
	t.Logf("qr state %d", result.State)
}
