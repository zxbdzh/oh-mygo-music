// Package websearch fetches web context for the AI playlist suggestion. 各
// provider 都是一次「查一下就返回」的普通 HTTP 调用；结果拼进提示词，不做
// 多轮工具循环，联网失败也不阻塞本地判断。
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Result is one search hit the suggestion prompt may see.
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Provider searches the web with song context only. 实现必须保证不把用户的
// 歌单名或描述发出去——那属于私有内容。
type Provider interface {
	Search(ctx context.Context, query string) ([]Result, error)
}

const httpTimeout = 15 * time.Second

// postJSON 是两个 provider 共用的请求与错误处理：Bearer 认证、状态码检查、
// 响应体限制，超时由调用方的 http.Client 控制。
func postJSON(ctx context.Context, url, apiKey string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("websearch: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websearch: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("websearch: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 截断到 300 字符，避免把含密钥的响应体直接带进错误提示。
		text := strings.TrimSpace(string(data))
		if r := []rune(text); len(r) > 300 {
			text = string(r[:300]) + "…"
		}
		return nil, fmt.Errorf("websearch: %s: %s", resp.Status, text)
	}
	return data, nil
}

// tavilySearchURL 与 anySearchSearchURL 是包级变量，测试里换成桩服务器地址。
var (
	tavilySearchURL    = "https://api.tavily.com/search"
	anySearchSearchURL = "https://api.anysearch.com/v1/search"
)

// tavilyProvider talks to Tavily's POST /search API.
type tavilyProvider struct {
	apiKey string
}

// TavilyRequest 只列用得到的字段；深度档与域名过滤用不上。
type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (t tavilyProvider) Search(ctx context.Context, query string) ([]Result, error) {
	body, err := json.Marshal(tavilyRequest{Query: query, MaxResults: 5})
	if err != nil {
		return nil, fmt.Errorf("websearch: encode request: %w", err)
	}
	data, err := postJSON(ctx, tavilySearchURL, t.apiKey, body)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("websearch: decode response: %w", err)
	}
	var results []Result
	for _, r := range out.Results {
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return results, nil
}

// anySearchProvider talks to AnySearch's POST /v1/search API.
type anySearchProvider struct {
	apiKey string
}

// anySearchRequest 的 zone/language 留空由网关自行判断；歌曲搜索无需指定。
type anySearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (a anySearchProvider) Search(ctx context.Context, query string) ([]Result, error) {
	body, err := json.Marshal(anySearchRequest{Query: query, MaxResults: 5})
	if err != nil {
		return nil, fmt.Errorf("websearch: encode request: %w", err)
	}
	data, err := postJSON(ctx, anySearchSearchURL, a.apiKey, body)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
				Content string `json:"content"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("websearch: decode response: %w", err)
	}
	var results []Result
	for _, r := range out.Data.Results {
		snippet := r.Snippet
		// AnySearch 的 snippet 可能缺失，content 是更长的正文，取其一。
		if snippet == "" && r.Content != "" {
			snippet = r.Content
		}
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	return results, nil
}

// For returns the named provider; the caller keeps the key in the OS
// credential store. Unknown 名称返回 nil，由调用方按未配置处理。
func For(name, apiKey string) Provider {
	switch name {
	case "tavily":
		return tavilyProvider{apiKey: apiKey}
	case "anysearch":
		return anySearchProvider{apiKey: apiKey}
	}
	return nil
}
