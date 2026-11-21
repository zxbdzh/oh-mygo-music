package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockedHomeTimeoutTransport struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

func (transport *blockedHomeTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Hostname() != "u.y.qq.com" ||
		request.URL.Path != "/cgi-bin/musicu.fcg" || request.Method != http.MethodPost {
		return nil, errors.New("unexpected network request")
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Read struct {
			Method string `json:"method"`
		} `json:"req_0"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Read.Method != "GetLoginUserInfo" {
		return nil, errors.New("unexpected synthetic QQ operation")
	}

	transport.startOnce.Do(func() { close(transport.started) })
	select {
	case <-transport.release:
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
	response := `{"code":0,"req_0":{"code":0,"data":{"info":{"nick":"Synthetic account"}}}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(response)),
		Request:    request,
	}, nil
}

func (transport *blockedHomeTimeoutTransport) releaseRequests() {
	transport.stopOnce.Do(func() { close(transport.release) })
}

// 账号请求等待时，已缓存的首页仍须从插件事件循环返回。
func TestAggregateHomeAnswersDuringPendingQQAccountQA(t *testing.T) {
	p, _ := qqAccountTest(t, func(*http.Request, string) (string, error) {
		return "", errors.New("unexpected synthetic request")
	})
	if _, err := p.SignIn(); err != nil {
		t.Fatal("synthetic QQ setup failed")
	}
	if _, err := p.Aggregate("commitSnapshot", map[string]any{
		"platform": "tx", "accountID": "700", "listID": "liked",
		"complete": true, "refreshedAt": int64(1),
		"tracks": []any{map[string]any{"id": "fixture-song", "name": "Synthetic cached song"}},
	}); err != nil {
		t.Fatal("synthetic cache setup failed")
	}

	transport := &blockedHomeTimeoutTransport{started: make(chan struct{}), release: make(chan struct{})}
	defer transport.releaseRequests()
	http.DefaultTransport = transport

	accountDone := make(chan error, 1)
	go func() {
		_, err := p.Account()
		accountDone <- err
	}()
	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic QQ account request did not reach the blocked HTTP boundary")
	}

	type homeResult struct {
		name string
		err  error
	}
	homeDone := make(chan homeResult, 1)
	go func() {
		page, err := p.Home()
		result := homeResult{err: err}
		if err == nil && page != nil && len(page.Sections) > 0 && len(page.Sections[0].Tracks) > 0 {
			result.name = page.Sections[0].Tracks[0].Name
		}
		homeDone <- result
	}()

	select {
	case result := <-homeDone:
		if result.err != nil || result.name != "Synthetic cached song" {
			t.Fatal("Home did not return the synthetic cached song")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Home stayed blocked while the synthetic QQ account request was pending")
	}

	transport.releaseRequests()
	select {
	case err := <-accountDone:
		if err != nil {
			t.Fatal("synthetic QQ account request did not finish after release")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic QQ account request did not settle after release")
	}
}
