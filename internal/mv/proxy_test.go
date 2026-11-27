package mv

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyForwardsRangeAndHeaders(t *testing.T) {
	var gotReferer, gotRange, gotCookie string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		gotRange = r.Header.Get("Range")
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-3/100")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "data")
	}))
	defer upstream.Close()

	proxy, err := NewProxy(nil, func(context.Context) (*Resolved, error) {
		return &Resolved{URL: upstream.URL, Referer: "https://www.bilibili.com/video/BV1", UA: "test-ua", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, "SESSDATA=x")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodGet, proxy.URL(), nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if string(body) != "data" || resp.Header.Get("Content-Range") != "bytes 0-3/100" {
		t.Fatalf("body %q range %q", body, resp.Header.Get("Content-Range"))
	}
	if gotReferer != "https://www.bilibili.com/video/BV1" || gotRange != "bytes=0-3" || gotCookie != "SESSDATA=x" {
		t.Fatalf("upstream got referer %q range %q cookie %q", gotReferer, gotRange, gotCookie)
	}
}

// 首次访问直接使用已解析的流，403 刷新一次并更新实际轨道信息。
func TestProxyRefreshesOnceOnForbidden(t *testing.T) {
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusForbidden)
	}))
	defer stale.Close()
	fresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "fresh")
	}))
	defer fresh.Close()

	var calls atomic.Int32
	initial := &Resolved{URL: stale.URL, StreamInfo: StreamInfo{Quality: 80}, ExpiresAt: time.Now().Add(time.Hour)}
	proxy, err := NewProxy(initial, func(context.Context) (*Resolved, error) {
		calls.Add(1)
		return &Resolved{URL: fresh.URL, StreamInfo: StreamInfo{Quality: 64}, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	resp, err := http.Get(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "fresh" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("resolve called %d times, want 1", got)
	}
	if got := proxy.StreamInfo().Resolution(); got != 720 {
		t.Fatalf("reported resolution %d, want 720", got)
	}
}

// CDN 把 fMP4 标成 octet-stream 时仍要给播放器一个视频类型。
func TestProxyNormalizesVideoContentType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, "data")
	}))
	defer upstream.Close()
	proxy, err := NewProxy(nil, func(context.Context) (*Resolved, error) {
		return &Resolved{URL: upstream.URL, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	resp, err := http.Get(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("content type %q", got)
	}
}

func TestProxyRejectsUnknownPath(t *testing.T) {
	proxy, err := NewProxy(nil, func(context.Context) (*Resolved, error) {
		return &Resolved{URL: "http://127.0.0.1:1", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + proxy.listener.Addr().String() + "/mv/not-the-token")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
