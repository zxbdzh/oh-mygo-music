package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCompleteChatsWithOpenAICompatibleServer checks that a completion posts
// the model and messages to /chat/completions and returns the reply.
func TestCompleteChatsWithOpenAICompatibleServer(t *testing.T) {
	var gotPath, gotAuth string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"可以"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "test-model"})
	text, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "只回答两个字：可以"}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if text != "可以" {
		t.Errorf("text = %q, want 可以", text)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if got["model"] != "test-model" {
		t.Errorf("model = %v", got["model"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", got["messages"])
	}
	m, _ := msgs[0].(map[string]any)
	if m["role"] != "user" || m["content"] != "只回答两个字：可以" {
		t.Errorf("message = %v", m)
	}
}

// TestCompleteReportsServiceError checks that a refused request keeps the
// status and the service's message for the settings page to show.
func TestCompleteReportsServiceError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "missing"})
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("complete succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error = %q, want status and service message", err)
	}
}

// TestCompleteRejectsEmptyChoices checks that a reply without choices is an
// error rather than an empty suggestion.
func TestCompleteRejectsEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	text, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil || text != "" {
		t.Fatalf("text = %q, err = %v; want an error and no text", text, err)
	}
}

// TestCompleteStopsWhenContextIsDone checks that a cancelled request does not
// come back as a suggestion.
func TestCompleteStopsWhenContextIsDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	text, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}})
	if err == nil || text != "" {
		t.Fatalf("text = %q, err = %v; want an error and no text", text, err)
	}
}

// TestCompleteFallsBackWithoutWebSearch checks that a service which refuses
// the web search tool still answers, without it.
func TestCompleteFallsBackWithoutWebSearch(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, ok := body["tools"]; ok {
			http.Error(w, "tools not supported", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m", WebSearch: true})
	text, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil || text != "ok" {
		t.Fatalf("text = %q, err = %v", text, err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

// TestCompleteUsesConfiguredWebTool checks that DeepSeek's own web tool can
// be asked for, not only OpenAI's.
func TestCompleteUsesConfiguredWebTool(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(body.Tools) > 0 {
			got = body.Tools[0].Type
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m", WebSearch: true, WebSearchTool: "deepseek_web_search"})
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if got != "deepseek_web_search" {
		t.Errorf("tool = %q", got)
	}
}

// TestCompleteDoesNotRetryNetworkErrors checks that a connection failure with
// the web tool on is not retried without it: 重发不会有不同结果。
func TestCompleteDoesNotRetryNetworkErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m", WebSearch: true})
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on server errors)", calls)
	}
}

// TestCompleteRejectsToolCallResponses checks that a service answering with
// tool_calls instead of text fails clearly instead of returning an empty
// string: 之前 content 为空会被当作模型没猜出来。
func TestCompleteRejectsToolCallResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"web_search","arguments":"{}"}}]}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, Model: "m"})
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "web_search") {
		t.Fatalf("err = %v, want a tool-call error naming the tool", err)
	}
}
