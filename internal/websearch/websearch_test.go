package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTavilySearchPostsQueryAndReadsResults checks the request shape and the
// result mapping for the Tavily provider.
func TestTavilySearchPostsQueryAndReadsResults(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		gotBody = string(b)
		w.Write([]byte(`{"results":[{"title":"Song review","url":"https://example.com/1","content":"A review."}]}`))
	}))
	defer srv.Close()

	// 把官方域名换成测试服务器地址：provider 的 URL 常量在测试里被覆盖。
	old := tavilySearchURL
	tavilySearchURL = srv.URL + "/search"
	defer func() { tavilySearchURL = old }()

	p := tavilyProvider{apiKey: "k"}
	results, err := p.Search(context.Background(), "artist album song")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/search" {
		t.Errorf("path = %q, want /search", gotPath)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("auth = %q, want bearer key", gotAuth)
	}
	if !strings.Contains(gotBody, `"max_results":5`) {
		t.Errorf("body = %s, want max_results 5", gotBody)
	}
	if len(results) != 1 || results[0].Title != "Song review" || results[0].URL != "https://example.com/1" {
		t.Errorf("results = %+v", results)
	}
}

// TestAnySearchSearchReadsDataResults checks the AnySearch envelope and the
// fallback from a missing snippet to content.
func TestAnySearchSearchReadsDataResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"results":[
			{"title":"A","url":"https://a","snippet":"short"},
			{"title":"B","url":"https://b","content":"long content"}
		]}}`))
	}))
	defer srv.Close()

	old := anySearchSearchURL
	anySearchSearchURL = srv.URL
	defer func() { anySearchSearchURL = old }()

	p := anySearchProvider{apiKey: "k"}
	results, err := p.Search(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2", results)
	}
	if results[0].Snippet != "short" {
		t.Errorf("first snippet = %q", results[0].Snippet)
	}
	if results[1].Snippet != "long content" {
		t.Errorf("fallback snippet = %q, want content", results[1].Snippet)
	}
}

// TestUnknownProviderIsNil keeps For honest about names it does not know.
func TestUnknownProviderIsNil(t *testing.T) {
	if For("nope", "k") != nil {
		t.Fatal("unknown name returned a provider")
	}
	if For("", "k") != nil {
		t.Fatal("empty name returned a provider")
	}
}
