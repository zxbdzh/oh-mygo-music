package mv

import (
	"strings"
	"testing"
	"time"
)

func TestMixinKeyTakesThirtyTwoCharacters(t *testing.T) {
	// 两段各 32 位，拼起来正好是 B 站真实返回的形状。
	img := "https://i0.hdslb.com/bfs/wbi/0123456789abcdefghijklmnopqrstuv.png"
	sub := "https://i0.hdslb.com/bfs/wbi/wxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+-.png"
	got := mixinKey(img, sub)
	if len(got) != 32 {
		t.Fatalf("mixin key length %d, want 32", len(got))
	}
	if strings.IndexAny(got, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+-") < 0 {
		t.Fatalf("mixin key %q has characters outside the source", got)
	}
	// 重排表决定首字符，不是简单截断。
	const source = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+-"
	if got[0] != source[mixinTab[0]] {
		t.Fatalf("mixin key %q does not follow the table", got)
	}
}

func TestSignParamsSortsAndAddsOversight(t *testing.T) {
	got := signParams(map[string]string{"b": "2", "a": "1", "wts": "100"}, "mixin")
	if !strings.HasPrefix(got, "a=1&b=2&wts=100&w_rid=") {
		t.Fatalf("signed query %q", got)
	}
	rid := got[strings.LastIndex(got, "=")+1:]
	if len(rid) != 32 {
		t.Fatalf("w_rid %q has length %d", rid, len(rid))
	}
	for _, c := range rid {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("w_rid %q is not hexadecimal", rid)
		}
	}
}

func TestChooseStreamPrefersDashAVCWhenLoggedIn(t *testing.T) {
	data := map[string]any{
		"quality": 64.0,
		"durl":    []any{map[string]any{"url": "https://cdn/progressive.mp4"}},
		"dash": map[string]any{"video": []any{
			map[string]any{"codecs": "hev1.1.6", "height": 1080.0, "baseUrl": "https://cdn/hevc.mp4"},
			map[string]any{"codecs": "avc1.640032", "height": 720.0, "baseUrl": "https://cdn/low.mp4"},
			map[string]any{"codecs": "avc1.640028", "height": 1080.0, "baseUrl": "https://cdn/high.mp4"},
		}},
	}
	got, err := chooseStream(data, true, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://cdn/high.mp4" || got.Kind != "dash-avc1" || got.Height != 1080 {
		t.Fatalf("chose %+v", got)
	}
}

func TestChooseStreamUsesProgressiveWhenAnonymous(t *testing.T) {
	data := map[string]any{
		"quality": 64.0,
		"durl":    []any{map[string]any{"url": "https://cdn/progressive.mp4"}},
		"dash": map[string]any{"video": []any{
			map[string]any{"codecs": "avc1.64001F", "height": 480.0, "baseUrl": "https://cdn/dash.mp4"},
		}},
	}
	got, err := chooseStream(data, false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://cdn/progressive.mp4" || got.Kind != "progressive" {
		t.Fatalf("chose %+v", got)
	}
}

func TestChooseStreamRejectsSplitProgressive(t *testing.T) {
	// 多段 MP4 播放器不能直接拼，宁可报错也不给一条只播一半的流。
	data := map[string]any{
		"durl": []any{
			map[string]any{"url": "https://cdn/part1.mp4"},
			map[string]any{"url": "https://cdn/part2.mp4"},
		},
	}
	if _, err := chooseStream(data, false, time.Time{}); err == nil {
		t.Fatal("expected an error for split progressive streams")
	}
}

func TestParseClock(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"4:30", 4*time.Minute + 30*time.Second},
		{"1:02:03", time.Hour + 2*time.Minute + 3*time.Second},
		{"", 0},
		{"x:y", 0},
	} {
		if got := parseClock(tc.in); got != tc.want {
			t.Fatalf("parseClock(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
