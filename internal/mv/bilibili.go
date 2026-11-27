package mv

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	HTTP   Doer
	Cookie string
	UA     string
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, UA: defaultUA}
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

// Resolve 把一条 MV 解析成可播放的流。
//
// 登录后 DASH 的 H.264 轨能到 1080P；未登录时 DASH 只有 480P，
// 而渐进式 MP4 有 720P，所以两条路径按登录状态分开选。
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
	loggedIn := c.Cookie != ""
	// 登录时先试 DASH 的 1080P；未登录直接要渐进式，避免只拿到 480P。
	fnval := "1"
	if loggedIn {
		fnval = "4048"
	}
	play, err := c.playurl(ctx, videoID, int(cid), fnval, mix, referer)
	if err != nil && loggedIn {
		play, err = c.playurl(ctx, videoID, int(cid), "1", mix, referer)
	}
	if err != nil {
		return nil, err
	}
	resolved, err := chooseStream(play, loggedIn, time.Now().Add(resolveTTL))
	if err != nil {
		return nil, err
	}
	// Referer 是 B 站防盗链的一部分，跟着流一起交给代理。
	resolved.Referer = referer
	resolved.UA = c.ua()
	return resolved, nil
}

func (c *Client) playurl(ctx context.Context, videoID string, cid int, fnval, mix, referer string) (map[string]any, error) {
	params := map[string]string{
		"bvid":  videoID,
		"cid":   strconv.Itoa(cid),
		"qn":    "120",
		"fnval": fnval,
		"fnver": "0",
		"fourk": "1",
		"wts":   strconv.FormatInt(time.Now().Unix(), 10),
	}
	endpoint := apiBase + "/x/player/wbi/playurl"
	if mix != "" {
		endpoint += "?" + signParams(params, mix)
	} else {
		endpoint += "?" + url.Values{
			"bvid": {videoID}, "cid": {strconv.Itoa(cid)}, "qn": {"120"},
			"fnval": {fnval}, "fnver": {"0"}, "fourk": {"1"},
		}.Encode()
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

// chooseStream 是选流策略的纯函数，测试不联网也能覆盖两条路径。
func chooseStream(data map[string]any, loggedIn bool, expires time.Time) (*Resolved, error) {
	var resolved *Resolved
	if loggedIn {
		resolved = dashAVC(data, expires)
	}
	if resolved == nil {
		resolved = progressive(data, expires)
	}
	// 未登录也可能只有 DASH；总比没有画面好。
	if resolved == nil {
		resolved = dashAVC(data, expires)
	}
	if resolved != nil {
		resolved.Quality = int(numberOr(data["quality"]))
		return resolved, nil
	}
	return nil, errors.New("mv: no H.264 video track")
}

// dashAVC 选 DASH 里最高的 H.264 轨。HEVC/AV1 不选，系统解码器不保证支持。
func dashAVC(data map[string]any, expires time.Time) *Resolved {
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
		url := firstString(stream["baseUrl"], stream["base_url"])
		if url == "" {
			continue
		}
		height := int(numberOr(stream["height"]))
		if best == nil || height > best.Height {
			best = &Resolved{
				URL:       url,
				ExpiresAt: expires,
				Height:    height,
				Codec:     codecs,
				Kind:      "dash-avc1",
			}
		}
	}
	return best
}

// progressive 取 durl 的完整 MP4。多段意味着长视频被切片，播放器不能直接拼，故只接受单段。
func progressive(data map[string]any, expires time.Time) *Resolved {
	durl, _ := data["durl"].([]any)
	if len(durl) != 1 {
		return nil
	}
	stream, _ := durl[0].(map[string]any)
	url := firstString(stream["url"])
	if url == "" {
		return nil
	}
	return &Resolved{
		URL:       url,
		ExpiresAt: expires,
		// quality 是 B 站的 qn 编号，不是像素高度，别混用。
		Height: 0,
		Kind:   "progressive",
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
