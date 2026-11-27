package mv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Proxy 把带防盗链的流变成播放器能直接读的本机地址。
//
// 播放器（IMFMediaEngine 等）只会发普通 GET/HEAD，不会带 Referer、UA 或 Cookie，
// 而 B 站直链缺了这些会 403，所以必须由这里代发并原样透传 Range。
type Proxy struct {
	resolve func(context.Context) (*Resolved, error)
	cookie  func() string
	client  *http.Client

	token    string
	listener net.Listener
	server   *http.Server
	url      string

	mu      sync.Mutex
	current *Resolved
}

// NewProxy 在 127.0.0.1 的随机端口上启动代理。resolve 返回 null 时请求 502。
// cookie 每次请求时取，退出登录后立即生效，也不落进磁盘缓存。
func NewProxy(resolve func(context.Context) (*Resolved, error), cookie func() string) (*Proxy, error) {
	if resolve == nil {
		return nil, errors.New("mv: proxy needs resolve")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{
		resolve:  resolve,
		cookie:   cookie,
		client:   &http.Client{Timeout: 30 * time.Second},
		token:    token,
		listener: listener,
	}
	// 路径里带随机 token：别的本机程序不能借这个代理顺手用上登录态。
	p.url = "http://" + listener.Addr().String() + "/mv/" + token
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go p.server.Serve(listener)
	return p, nil
}

// URL 是给播放器的地址，可直接交给 ui/video.OpenURL。
func (p *Proxy) URL() string { return p.url }

func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mv/"+p.token {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, err := p.stream(r.Context(), false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := p.forward(w, r, stream); err == nil {
		return
	}
	// 直链过期和瞬时网络错误都走这里：重新解析一次再试，仍失败才交给播放器 502。
	stream, err = p.stream(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := p.forward(w, r, stream); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func (p *Proxy) stream(ctx context.Context, force bool) (*Resolved, error) {
	p.mu.Lock()
	cached := p.current
	p.mu.Unlock()
	if !force && cached != nil && !cached.Expired(time.Now()) {
		return cached, nil
	}
	stream, err := p.resolve(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.current = stream
	p.mu.Unlock()
	return stream, nil
}

// forward 把请求转给上游，并原样透传播放器需要的状态与长度。
// 只在写出响应头之前返回错误，调用方才能安全地重新解析并重试。
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, stream *Resolved) error {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, stream.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", stream.UA)
	req.Header.Set("Referer", stream.Referer)
	for k, v := range stream.Headers {
		req.Header.Set(k, v)
	}
	if c := p.cookie(); c != "" {
		req.Header.Set("Cookie", c)
	}
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone {
		return fmt.Errorf("mv: upstream rejected the stream (HTTP %d)", resp.StatusCode)
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	// 有些 B 站 CDN 对 fMP4 返回 application/octet-stream；播放器需要视频类型。
	if ct := w.Header().Get("Content-Type"); ct == "" || ct == "application/octet-stream" {
		w.Header().Set("Content-Type", "video/mp4")
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return nil
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		// 播放器拖动时经常中途断开，属于正常现象，不当作代理错误。
		return nil
	}
	return nil
}

// String 便于日志和测试看到实际端口。
func (p *Proxy) String() string { return fmt.Sprintf("mv proxy %s", p.url) }
