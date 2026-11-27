// Package ai talks to OpenAI-compatible chat completion services for the
// playlist suggestions.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config is the listener's AI service: any endpoint that answers the OpenAI
// chat completions protocol.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	// WebSearch asks the service for its own web search, when it has one.
	// 它是服务端执行的内置工具；失败时按服务商约定降级，不视为一般错误。
	WebSearch     bool
	WebSearchTool string
}

// Message is one turn of a completion. ToolCalls 只在服务商按 function
// calling 返回时出现；Content 与它通常互斥，两者都为空说明模型没给结果。
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is the model asking the client to run a hosted tool.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Client asks a service for completions.
type Client struct {
	cfg  Config
	http *http.Client
}

func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// Complete asks for the reply to messages and returns its text. 带联网工具的
// 请求只在「服务端不认识这个工具」这一类失败时降级重试；网络断开或密钥无效
// 重发也不会有不同结果，只会白付一次请求。
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	text, err := c.complete(ctx, messages, c.cfg.WebSearch)
	if err != nil && c.cfg.WebSearch && toolUnsupported(err) {
		return c.complete(ctx, messages, false)
	}
	return text, err
}

func (c *Client) complete(ctx context.Context, messages []Message, web bool) (string, error) {
	if c.cfg.BaseURL == "" || c.cfg.Model == "" {
		return "", fmt.Errorf("ai: base URL and model are required")
	}
	request := map[string]any{
		"model":       c.cfg.Model,
		"messages":    messages,
		"temperature": 0.2,
	}
	if web {
		tool := c.cfg.WebSearchTool
		if tool == "" {
			tool = "web_search"
		}
		request["tools"] = []map[string]string{{"type": tool}}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("ai: encode request: %w", err)
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ai: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("ai: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ai: %s: %s", resp.Status, brief(data))
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("ai: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("ai: response has no choices")
	}
	msg := out.Choices[0].Message
	if msg.Content == "" && len(msg.ToolCalls) > 0 {
		// 服务商把内置搜索当 function calling 走：模型在等客户端执行工具，
		// 但本客户端不执行服务端工具，直接按工具不可用处理。
		return "", toolCallsUnsupportedError(msg.ToolCalls[0].Function.Name)
	}
	return msg.Content, nil
}

// errToolUnsupported 标记「服务端不认识或不执行联网工具」这一类失败。
type errToolUnsupported struct{ msg string }

func (e *errToolUnsupported) Error() string { return e.msg }

func toolCallsUnsupportedError(tool string) error {
	return &errToolUnsupported{msg: fmt.Sprintf("ai: service returned a tool call for %q instead of an answer", tool)}
}

// toolUnsupported 判断是否值得去掉联网参数重试：HTTP 400/422（参数被拒）或
// 服务端返回了 tool_calls 等着客户端执行。
func toolUnsupported(err error) bool {
	var e *errToolUnsupported
	if errors.As(err, &e) {
		return true
	}
	// complete() 对非 2xx 用 "ai: <status>: <body>" 文本包装，只看状态码段。
	text := err.Error()
	return strings.Contains(text, "ai: 400 ") || strings.Contains(text, "ai: 422 ")
}

// brief keeps an error body short enough for a settings message.
func brief(data []byte) string {
	text := strings.TrimSpace(string(data))
	if r := []rune(text); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return text
}
