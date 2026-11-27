package mv

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// B 站要求桌面浏览器的 UA，否则搜索和取流会被风控拦下。
	defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	apiBase   = "https://api.bilibili.com"
)

// Doer 是 Client 对网络的唯一依赖，测试用它替换真实请求。
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client 访问 B 站。Cookie 为空时走免登录路径。
type Client struct {
	HTTP    Doer
	Cookie  string
	UA      string
	Quality QualityOptions
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, UA: defaultUA, Quality: QualityOptions{Prefer60FPS: true}}
}

// mixinTab 是 B 站 WBI 签名的固定重排表，用来从 img_key/sub_key 派生 mixin key。
var mixinTab = []int{46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39,
	12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62,
	11, 36, 20, 34, 44, 52}

// mixinKey 取 img_url 与 sub_url 的文件名，按表重排后取前 32 位。
func mixinKey(imgURL, subURL string) string {
	base := func(raw string) string {
		name := raw[strings.LastIndex(raw, "/")+1:]
		if i := strings.LastIndex(name, "."); i >= 0 {
			return name[:i]
		}
		return name
	}
	orig := base(imgURL) + base(subURL)
	if len(orig) < 64 {
		return ""
	}
	var b strings.Builder
	for _, i := range mixinTab {
		b.WriteByte(orig[i])
	}
	return b.String()[:32]
}

// signParams 按 key 排序、URL 编码、拼上 wts 与 w_rid，生成 WBI 签名。
func signParams(params map[string]string, mix string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.ReplaceAll(url.QueryEscape(params[k]), "+", "%20")
		parts = append(parts, k+"="+v)
	}
	query := strings.Join(parts, "&")
	sum := md5.Sum([]byte(query + mix))
	return query + "&w_rid=" + hex.EncodeToString(sum[:])
}

func (c *Client) ua() string {
	if c.UA != "" {
		return c.UA
	}
	return defaultUA
}

func (c *Client) get(ctx context.Context, endpoint, referer string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua())
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if c.Cookie != "" {
		req.Header.Set("Cookie", c.Cookie)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("bilibili %s: response is not JSON (HTTP %d)", endpoint, resp.StatusCode)
	}
	if code, ok := number(payload["code"]); ok && code != 0 {
		message, _ := payload["message"].(string)
		return nil, fmt.Errorf("bilibili %s: code %d %s", endpoint, int(code), message)
	}
	return payload, nil
}

// wbi 从导航接口拿当天的 img_key/sub_key；失败时返回空，调用方退回不签名接口。
func (c *Client) wbi(ctx context.Context) string {
	payload, err := c.get(ctx, apiBase+"/x/web-interface/nav", "https://www.bilibili.com/")
	if err != nil {
		return ""
	}
	data, _ := payload["data"].(map[string]any)
	img, _ := data["wbi_img"].(map[string]any)
	imgURL, _ := img["img_url"].(string)
	subURL, _ := img["sub_url"].(string)
	return mixinKey(imgURL, subURL)
}

// Search 按关键词查视频。只返回视频结果，不做过滤，排序交给评分器。
func (c *Client) Search(ctx context.Context, query string) ([]Candidate, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("mv: empty search query")
	}
	params := map[string]string{
		"search_type": "video",
		"keyword":     query,
		"page":        "1",
		"wts":         strconv.FormatInt(time.Now().Unix(), 10),
	}
	endpoint := apiBase + "/x/web-interface/wbi/search/type"
	if mix := c.wbi(ctx); mix != "" {
		endpoint += "?" + signParams(params, mix)
	} else {
		endpoint += "?search_type=video&keyword=" + url.QueryEscape(query) + "&page=1"
	}
	payload, err := c.get(ctx, endpoint, "https://search.bilibili.com/video?keyword="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	data, _ := payload["data"].(map[string]any)
	items, _ := data["result"].([]any)
	out := make([]Candidate, 0, len(items))
	for _, item := range items {
		record, _ := item.(map[string]any)
		bvid, _ := record["bvid"].(string)
		if bvid == "" {
			continue
		}
		title, _ := record["title"].(string)
		uploader, _ := record["author"].(string)
		pic, _ := record["pic"].(string)
		duration, _ := record["duration"].(string)
		out = append(out, Candidate{
			VideoID:   bvid,
			Title:     cleanTitle(title),
			Uploader:  uploader,
			Duration:  parseClock(duration),
			Thumbnail: pic,
		})
	}
	return out, nil
}

// cleanTitle 去掉搜索结果标题里的高亮标签。
func cleanTitle(title string) string {
	title = strings.ReplaceAll(title, `<em class="keyword">`, "")
	return strings.ReplaceAll(title, "</em>", "")
}

// parseClock 把 "4:30" 或 "1:02:03" 转成时长。
func parseClock(clock string) time.Duration {
	parts := strings.Split(clock, ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0
	}
	total := 0
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			return 0
		}
		total = total*60 + n
	}
	return time.Duration(total) * time.Second
}

// Resolve 把一条 MV 解析成符合当前画质偏好的可播放流。
func (c *Client) Resolve(ctx context.Context, videoID string) (*Resolved, error) {
	referer := "https://www.bilibili.com/video/" + videoID
	view, err := c.get(ctx, apiBase+"/x/web-interface/view?bvid="+url.QueryEscape(videoID), referer)
	if err != nil {
		return nil, err
	}
	data, _ := view["data"].(map[string]any)
	cid, ok := number(data["cid"])
	if !ok || cid == 0 {
		return nil, fmt.Errorf("mv: %s has no playable part", videoID)
	}

	mix := c.wbi(ctx)
	var resolved *Resolved
	if c.Cookie != "" {
		qn := c.Quality.requestQN()
		if play, err := c.playurl(ctx, videoID, int(cid), qn, "4048", mix, referer); err == nil {
			resolved, _ = chooseStream(play, true, c.Quality, time.Now().Add(resolveTTL))
		}
	}
	if resolved == nil {
		// 部分视频不提供请求的 MP4 档位，逐级降档但不超过用户上限。
		var lastErr error
		for _, qn := range c.Quality.progressiveQNs() {
			play, playErr := c.playurl(ctx, videoID, int(cid), qn, "1", mix, referer)
			if playErr != nil {
				lastErr = playErr
				continue
			}
			resolved, lastErr = chooseStream(play, false, c.Quality, time.Now().Add(resolveTTL))
			if resolved != nil {
				break
			}
		}
		if resolved == nil {
			return nil, lastErr
		}
	}
	// Referer 是 B 站防盗链的一部分，跟着流一起交给代理。
	resolved.Referer = referer
	resolved.UA = c.ua()
	return resolved, nil
}

func (o QualityOptions) requestQN() int {
	switch o.Limit() {
	case 480:
		return 32
	case 720:
		if o.Prefer60FPS {
			return 74
		}
		return 64
	}
	if o.Prefer60FPS {
		return 116
	}
	return 112
}

func (o QualityOptions) progressiveQNs() []int {
	switch o.Limit() {
	case 480:
		return []int{32, 16}
	case 720:
		if o.Prefer60FPS {
			return []int{74, 64, 32, 16}
		}
		return []int{64, 32, 16}
	default:
		if o.Prefer60FPS {
			return []int{116, 112, 80, 74, 64, 32, 16}
		}
		return []int{112, 80, 74, 64, 32, 16}
	}
}

func (c *Client) playurl(ctx context.Context, videoID string, cid, qn int, fnval, mix, referer string) (map[string]any, error) {
	params := map[string]string{
		"bvid":  videoID,
		"cid":   strconv.Itoa(cid),
		"qn":    strconv.Itoa(qn),
		"fnval": fnval,
		"fnver": "0",
		"wts":   strconv.FormatInt(time.Now().Unix(), 10),
	}
	endpoint := apiBase + "/x/player/wbi/playurl"
	if mix != "" {
		endpoint += "?" + signParams(params, mix)
	} else {
		query := make(url.Values, len(params))
		for name, value := range params {
			query.Set(name, value)
		}
		endpoint += "?" + query.Encode()
	}
	payload, err := c.get(ctx, endpoint, referer)
	if err != nil {
		return nil, err
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		return nil, errors.New("mv: playurl has no data")
	}
	return data, nil
}

var ErrNoCompatibleStream = errors.New("mv: no compatible H.264 video track")

// chooseStream 是选流策略的纯函数，测试不联网也能覆盖两条路径。
func chooseStream(data map[string]any, preferDASH bool, options QualityOptions, expires time.Time) (*Resolved, error) {
	var resolved *Resolved
	if preferDASH {
		resolved = dashAVC(data, options, expires)
	}
	if resolved == nil {
		resolved = progressive(data, options, expires)
	}
	// 未登录也可能只有 DASH；总比没有画面好。
	if resolved == nil && !preferDASH {
		resolved = dashAVC(data, options, expires)
	}
	if resolved != nil {
		return resolved, nil
	}
	return nil, ErrNoCompatibleStream
}

func (o QualityOptions) accepts(info StreamInfo) bool {
	resolution := info.Resolution()
	return info.Quality <= 116 && resolution > 0 && resolution <= o.Limit()
}

// dashAVC 只选 H.264，先看清晰度，再看同档位的帧率和码率档位。
func dashAVC(data map[string]any, options QualityOptions, expires time.Time) *Resolved {
	dash, _ := data["dash"].(map[string]any)
	if dash == nil {
		return nil
	}
	videos, _ := dash["video"].([]any)
	var best *Resolved
	for _, item := range videos {
		stream, _ := item.(map[string]any)
		codecs, _ := stream["codecs"].(string)
		if !strings.HasPrefix(codecs, "avc1") {
			continue
		}
		address := firstString(stream["baseUrl"], stream["base_url"])
		info := StreamInfo{Height: int(numberOr(stream["height"])), Quality: int(numberOr(stream["id"])), Kind: "dash-avc1"}
		if address == "" || !options.accepts(info) {
			continue
		}
		info.FPS = frameRate(stream, info.Quality)
		if best == nil || options.betterTrack(info, best.StreamInfo) {
			best = &Resolved{
				StreamInfo: info,
				URL:        address,
				ExpiresAt:  expires,
				Codec:      codecs,
			}
		}
	}
	return best
}

func (o QualityOptions) betterTrack(candidate, current StreamInfo) bool {
	if candidate.Resolution() != current.Resolution() {
		return candidate.Resolution() > current.Resolution()
	}
	candidateHigh, currentHigh := candidate.FPS >= 55, current.FPS >= 55
	if candidateHigh != currentHigh {
		return candidateHigh == o.Prefer60FPS
	}
	return candidate.Quality > current.Quality
}

func frameRate(track map[string]any, quality int) int {
	for _, key := range []string{"frameRate", "frame_rate"} {
		fps := numberOr(track[key])
		if value, ok := track[key].(string); ok {
			value = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), "fps"))
			numerator, denominator, ratio := strings.Cut(value, "/")
			fps, _ = strconv.ParseFloat(strings.TrimSpace(numerator), 64)
			if ratio {
				divisor, _ := strconv.ParseFloat(strings.TrimSpace(denominator), 64)
				fps /= divisor
			}
		}
		if fps >= 1 && fps <= 240 {
			return int(math.Round(fps))
		}
	}
	if quality == 74 || quality == 116 {
		return 60
	}
	return 0
}

// progressive 取 durl 的完整 MP4。多段意味着长视频被切片，播放器不能直接拼，故只接受单段。
func progressive(data map[string]any, options QualityOptions, expires time.Time) *Resolved {
	durl, _ := data["durl"].([]any)
	if len(durl) != 1 {
		return nil
	}
	stream, _ := durl[0].(map[string]any)
	address := firstString(stream["url"])
	info := StreamInfo{Quality: int(numberOr(data["quality"])), Kind: "progressive"}
	if address == "" || !options.accepts(info) {
		return nil
	}
	info.FPS = frameRate(stream, info.Quality)
	return &Resolved{
		StreamInfo: info,
		URL:        address,
		ExpiresAt:  expires,
	}
}

func firstString(values ...any) string {
	for _, v := range values {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func number(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func numberOr(v any) float64 {
	f, _ := number(v)
	return f
}
