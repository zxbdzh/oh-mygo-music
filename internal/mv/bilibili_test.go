package mv

import (
	"context"
	"io"
	"net/http"
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

// DASH 的档位来自实际选中的 H.264 轨道，不能用响应顶层的 quality。
func TestChooseStreamPrefersDashAVCWhenLoggedIn(t *testing.T) {
	data := map[string]any{
		"quality": 64.0,
		"durl":    []any{map[string]any{"url": "https://cdn/progressive.mp4"}},
		"dash": map[string]any{"video": []any{
			map[string]any{"codecs": "hev1.1.6", "height": 1080.0, "baseUrl": "https://cdn/hevc.mp4"},
			map[string]any{"codecs": "avc1.640032", "id": 64.0, "height": 720.0, "baseUrl": "https://cdn/low.mp4"},
			map[string]any{"codecs": "avc1.640028", "id": 80.0, "height": 1080.0, "baseUrl": "https://cdn/high.mp4"},
		}},
	}
	got, err := chooseStream(data, true, QualityOptions{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://cdn/high.mp4" || got.Kind != "dash-avc1" || got.Height != 1080 || got.Quality != 80 {
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
	got, err := chooseStream(data, false, QualityOptions{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://cdn/progressive.mp4" || got.Kind != "progressive" || got.Resolution() != 720 {
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
	if _, err := chooseStream(data, false, QualityOptions{}, time.Time{}); err == nil {
		t.Fatal("expected an error for split progressive streams")
	}
}

// 60 帧偏好只比较同档轨道，裁剪后的 1080p 也必须服从画质上限。
func TestChooseStreamHonorsQualityAndFrameRate(t *testing.T) {
	data := map[string]any{"dash": map[string]any{"video": []any{
		map[string]any{"codecs": "avc1", "id": 64.0, "height": 720.0, "baseUrl": "https://cdn/720.mp4", "frameRate": "30"},
		map[string]any{"codecs": "avc1", "id": 74.0, "height": 720.0, "baseUrl": "https://cdn/720-60.mp4", "frame_rate": "60000/1001"},
		map[string]any{"codecs": "avc1", "id": 80.0, "height": 700.0, "baseUrl": "https://cdn/1080.mp4", "frameRate": 29.97},
		map[string]any{"codecs": "avc1", "id": 116.0, "height": 1080.0, "baseUrl": "https://cdn/1080-60.mp4"},
		map[string]any{"codecs": "avc1", "id": 120.0, "height": 2160.0, "baseUrl": "https://cdn/2160.mp4"},
	}}}
	for _, scenario := range []struct {
		name         string
		options      QualityOptions
		quality, fps int
	}{
		{"720p standard", QualityOptions{MaxHeight: 720}, 64, 30},
		{"720p high frame rate", QualityOptions{MaxHeight: 720, Prefer60FPS: true}, 74, 60},
		{"1080p standard", QualityOptions{MaxHeight: 1080}, 80, 30},
		{"1080p high frame rate", QualityOptions{MaxHeight: 1080, Prefer60FPS: true}, 116, 60},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			got, err := chooseStream(data, true, scenario.options, time.Time{})
			if err != nil || got.Quality != scenario.quality || got.FPS != scenario.fps {
				t.Fatalf("resolved %+v, %v; want qn %d / %d fps", got, err, scenario.quality, scenario.fps)
			}
		})
	}
}

type testMVTransport func(*http.Request) (*http.Response, error)

func (transport testMVTransport) Do(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// 低画质设置要进真实 playurl 请求，登录时无兼容 DASH 轨也能回退到 MP4。
func TestResolveRequestsQualityAndFallsBackToProgressive(t *testing.T) {
	for _, cookie := range []string{"", "test-session"} {
		client := NewClient()
		client.Cookie = cookie
		client.Quality.MaxHeight = 480
		var formats []string
		client.HTTP = testMVTransport(func(request *http.Request) (*http.Response, error) {
			var body string
			switch request.URL.Path {
			case "/x/web-interface/view":
				body = `{"code":0,"data":{"cid":123}}`
			case "/x/web-interface/nav":
				body = `{"code":0,"data":{}}`
			case "/x/player/wbi/playurl":
				query := request.URL.Query()
				format := query.Get("fnval")
				wantQN := "32"
				if query.Get("qn") != wantQN {
					t.Fatalf("requested qn %q for fnval %s, want %s", query.Get("qn"), format, wantQN)
				}
				formats = append(formats, format)
				if format == "4048" {
					body = `{"code":0,"data":{"dash":{"video":[{"id":32,"codecs":"hev1","baseUrl":"https://cdn/hevc.mp4"}]}}}`
				} else {
					body = `{"code":0,"data":{"quality":32,"durl":[{"url":"https://cdn/progressive.mp4"}]}}`
				}
			default:
				t.Fatalf("unexpected request: %s", request.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		resolved, err := client.Resolve(context.Background(), "BVTEST")
		if err != nil || resolved.Kind != "progressive" || resolved.Resolution() != 480 {
			t.Fatalf("resolved %+v, %v", resolved, err)
		}
		want := "1"
		if cookie != "" {
			want = "4048,1"
		}
		if got := strings.Join(formats, ","); got != want {
			t.Fatalf("requested formats %q, want %q", got, want)
		}
	}
}

// MP4 请求档位不可用时继续降档，登录回退和免登录都遵守清晰度上限。
func TestResolveRetriesLowerProgressiveQuality(t *testing.T) {
	for _, cookie := range []string{"", "test-session"} {
		client := NewClient()
		client.Cookie = cookie
		client.Quality.MaxHeight = 720
		var requested []string
		client.HTTP = testMVTransport(func(request *http.Request) (*http.Response, error) {
			var body string
			switch request.URL.Path {
			case "/x/web-interface/view":
				body = `{"code":0,"data":{"cid":123}}`
			case "/x/web-interface/nav":
				body = `{"code":0,"data":{}}`
			case "/x/player/wbi/playurl":
				query := request.URL.Query()
				qn := query.Get("qn")
				format := query.Get("fnval")
				if cookie != "" && format == "4048" {
					if qn != "74" || format != "4048" {
						t.Fatalf("requested DASH qn %q / fnval %q", qn, format)
					}
					body = `{"code":0,"data":{"dash":{"video":[{"id":32,"codecs":"hev1","baseUrl":"https://cdn/hevc.mp4"}]}}}`
				} else {
					if format != "1" {
						t.Fatalf("requested MP4 fnval %q, want 1", format)
					}
					requested = append(requested, qn)
					if qn == "74" {
						body = `{"code":0,"data":{"quality":120,"durl":[{"url":"https://cdn/over-limit.mp4"}]}}`
					} else if qn == "64" {
						body = `{"code":0,"data":{"quality":64,"durl":[{"url":"https://cdn/720.mp4"}]}}`
					} else {
						t.Fatalf("unexpected lower-quality request qn %q", qn)
					}
				}
			default:
				t.Fatalf("unexpected request: %s", request.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		resolved, err := client.Resolve(context.Background(), "BVTEST")
		if err != nil || resolved.Resolution() != 720 || resolved.Quality != 64 {
			t.Fatalf("resolved %+v, %v", resolved, err)
		}
		if strings.Join(requested, ",") != "74,64" {
			t.Fatalf("requested MP4 qualities %v, want [74 64]", requested)
		}
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
