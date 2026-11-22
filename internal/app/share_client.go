package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"mygo-music/internal/lyrics"
)

const shareServiceTokenKeyPrefix = "online-share-token-"

var getShareServiceSecret = func(key string) ([]byte, error) {
	return mygo.Secrets.Get(key)
}

var setShareServiceSecret = func(key string, value []byte) error {
	return mygo.Secrets.Set(key, value)
}

var deleteShareServiceSecret = func(key string) error {
	return mygo.Secrets.Delete(key)
}

func saveShareServiceToken(serverURL, token string) error {
	key := shareTokenKey(serverURL)
	if key == "" {
		return errors.New("enter a valid share service address before saving its token")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("enter the share service token")
	}
	return setShareServiceSecret(key, []byte(token))
}

func loadShareServiceToken(serverURL string) (string, error) {
	key := shareTokenKey(serverURL)
	if key == "" {
		return "", errors.New("enter a valid share service address")
	}
	token, err := getShareServiceSecret(key)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(token)) == "" {
		return "", errors.New("the saved share service token is empty")
	}
	return string(token), nil
}

type shareMetadata struct {
	Title               string            `json:"title"`
	Artists             string            `json:"artists"`
	SourceID            string            `json:"sourceId"`
	AccountID           string            `json:"accountId,omitempty"`
	TrackID             string            `json:"trackId"`
	PlaybackID          string            `json:"playbackId,omitempty"`
	Quality             string            `json:"quality"`
	Template            string            `json:"template"`
	CardSquare          bool              `json:"cardSquare"`
	DefaultView         string            `json:"defaultView,omitempty"`
	Mode                string            `json:"mode"`
	DurationMs          int64             `json:"durationMs"`
	Lyrics              []shareLyricLine  `json:"lyrics"`
	SelectedLineIndexes []int             `json:"selectedLineIndexes"`
	HotComments         []shareHotComment `json:"hotComments,omitempty"`
	ClipStartMs         int64             `json:"clipStartMs"`
	ClipEndMs           int64             `json:"clipEndMs"`
	SourceURL           string            `json:"sourceUrl,omitempty"`
	CoverHash           string            `json:"coverHash,omitempty"`
}

type shareLyricLine struct {
	Text        string           `json:"text"`
	Translation string           `json:"translation,omitempty"`
	AtMs        int64            `json:"atMs"`
	EndMs       int64            `json:"endMs"`
	Words       []shareLyricWord `json:"words,omitempty"`
	StageEndMs  int64            `json:"stageEndMs,omitempty"`
}

type shareLyricWord struct {
	Text    string `json:"text"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

type shareHotComment struct {
	Text   string `json:"text"`
	Author string `json:"author"`
	Likes  int    `json:"likes"`
}

func (metadata shareMetadata) MarshalJSON() ([]byte, error) {
	type snapshot shareMetadata
	if metadata.Lyrics == nil {
		metadata.Lyrics = []shareLyricLine{}
	}
	if metadata.SelectedLineIndexes == nil {
		metadata.SelectedLineIndexes = []int{}
	}
	var start, end *int64
	// 零是合法片段起点；整曲则不能携带任何片段字段。
	if metadata.Mode == "clip" {
		start, end = &metadata.ClipStartMs, &metadata.ClipEndMs
	}
	return json.Marshal(struct {
		snapshot
		Start *int64 `json:"clipStartMs,omitempty"`
		End   *int64 `json:"clipEndMs,omitempty"`
	}{snapshot(metadata), start, end})
}

type shareResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
	Expires string `json:"expiresAt,omitempty"`
	Error   string `json:"error,omitempty"`
}

type shareListItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artists    string `json:"artists"`
	Mode       string `json:"mode"`
	Template   string `json:"template"`
	Status     string `json:"status"`
	URL        string `json:"url,omitempty"`
	Expires    string `json:"expiresAt,omitempty"`
	LastOpened string `json:"lastOpenedAt,omitempty"`
	Error      string `json:"error,omitempty"`
}

type shareClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type shareServiceError struct {
	Status int
	Code   string
}

func (e *shareServiceError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("share service returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("share service: %s", e.Code)
}

func normalizeShareServerURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("enter a valid share service address")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && shareURLIsLoopback(parsed.Hostname())) {
		return "", errors.New("share service must use HTTPS, except on localhost")
	}
	if parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("enter the share service origin without credentials, path, or query")
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func shareURLIsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newShareClient(baseURL, token string, client *http.Client) (*shareClient, error) {
	origin, err := normalizeShareServerURL(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("save the share service token in Settings first")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &shareClient{baseURL: origin, token: token, http: client}, nil
}

func (a *app) shareClientForSettings() (*shareClient, error) {
	key := shareTokenKey(a.settings.ShareServerURL)
	if key == "" {
		return nil, errors.New("set a valid share service address in Settings first")
	}

	token := ""
	if a.shareTokenTempKey == key {
		token = a.shareTokenTemp
	}
	if token == "" {
		var err error
		token, err = loadShareServiceToken(a.settings.ShareServerURL)
		if err != nil {
			return nil, err
		}
	}
	return newShareClient(a.settings.ShareServerURL, token, defaultShareHTTPClient())
}

func (c *shareClient) create(
	ctx context.Context,
	idempotencyKey string,
	metadata shareMetadata,
	cover []byte,
	coverType string,
	audioPath string,
) (*shareResult, error) {
	// JSON 的 null 不是空数组；无歌词整曲也必须满足服务端快照契约。
	if metadata.Lyrics == nil {
		metadata.Lyrics = []shareLyricLine{}
	}
	if metadata.SelectedLineIndexes == nil {
		metadata.SelectedLineIndexes = []int{}
	}
	if len(cover) > 0 {
		metadata.CoverHash = fmt.Sprintf("%x", sha256.Sum256(cover))
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode share details: %w", err)
	}
	response, err := c.request(ctx, http.MethodPost, "/api/shares", bytes.NewReader(body), int64(len(body)),
		"application/json", map[string]string{"Idempotency-Key": idempotencyKey})
	if err != nil {
		return nil, err
	}
	var result shareResult
	if err := decodeShareResponse(response, &result); err != nil {
		return nil, err
	}
	if result.Status == "ready" || result.Status == "processing" {
		return &result, nil
	}
	if result.Status == "revoked" || result.Status == "expired" {
		return &result, &shareServiceError{Status: http.StatusGone, Code: result.Status}
	}
	if result.Status != "awaiting_upload" && result.Status != "failed" {
		return &result, fmt.Errorf("share service returned an unsupported status: %s", result.Status)
	}

	if len(cover) > 0 {
		response, err = c.request(ctx, http.MethodPut, path.Join("/api/shares", result.ID, "cover"),
			bytes.NewReader(cover), int64(len(cover)), coverType, nil)
		if err != nil {
			return &result, err
		}
		if err := decodeShareResponse(response, nil); err != nil {
			return &result, err
		}
	}

	audio, err := os.Open(audioPath)
	if err != nil {
		return &result, fmt.Errorf("open prepared audio: %w", err)
	}
	defer audio.Close()
	info, err := audio.Stat()
	if err != nil {
		audio.Close()
		return &result, fmt.Errorf("read prepared audio: %w", err)
	}
	response, err = c.request(ctx, http.MethodPut, path.Join("/api/shares", result.ID, "audio"),
		audio, info.Size(), shareMediaType(audioPath), nil)
	if err != nil {
		return &result, err
	}
	if err := decodeShareResponse(response, &result); err != nil {
		return &result, err
	}
	return &result, nil
}

func (c *shareClient) get(ctx context.Context, id string) (*shareResult, error) {
	response, err := c.request(ctx, http.MethodGet, path.Join("/api/shares", id), nil, 0, "", nil)
	if err != nil {
		return nil, err
	}
	var result shareResult
	if err := decodeShareResponse(response, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *shareClient) reuse(ctx context.Context, metadata shareMetadata, cover []byte) (*shareResult, error) {
	if len(cover) > 0 {
		metadata.CoverHash = fmt.Sprintf("%x", sha256.Sum256(cover))
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode share details: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, err := c.request(ctx, http.MethodPost, "/api/shares/reuse", bytes.NewReader(body), int64(len(body)), "application/json", nil)
	if err != nil {
		return nil, err
	}
	// 旧服务尚无查询接口时仍允许发布；明确支持查询的服务失败则保留错误，避免盲目重复上传。
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, nil
	}
	var result shareResult
	if err := decodeShareResponse(response, &result); err != nil {
		return nil, err
	}
	if result.Status != "ready" || result.ID == "" || result.URL == "" {
		return nil, errors.New("share service returned an invalid reusable share")
	}
	return &result, nil
}

func (c *shareClient) list(ctx context.Context) ([]shareListItem, error) {
	response, err := c.request(ctx, http.MethodGet, "/api/shares", nil, 0, "", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&failure)
		return nil, &shareServiceError{Status: response.StatusCode, Code: failure.Error}
	}
	var result struct {
		Shares []shareListItem `json:"shares"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("read share list: %w", err)
	}
	return result.Shares, nil
}

func (c *shareClient) revoke(ctx context.Context, id string) error {
	response, err := c.request(ctx, http.MethodDelete, path.Join("/api/shares", id), nil, 0, "", nil)
	if err != nil {
		return err
	}
	return decodeShareResponse(response, nil)
}

func (c *shareClient) request(
	ctx context.Context,
	method, route string,
	body io.Reader,
	contentLength int64,
	contentType string,
	headers map[string]string,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+route, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	// 桌面请求也须声明服务自身的来源，才能通过公网服务的跨站校验。
	request.Header.Set("Origin", c.baseURL)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if body != nil {
		request.ContentLength = contentLength
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func decodeShareResponse(response *http.Response, result any) error {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&failure)
		return &shareServiceError{Status: response.StatusCode, Code: failure.Error}
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(result); err != nil {
		return fmt.Errorf("read share service response: %w", err)
	}
	return nil
}

func shareMediaType(audioPath string) string {
	switch strings.ToLower(path.Ext(audioPath)) {
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	default:
		return "audio/mpeg"
	}
}

func newShareIdempotencyKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func shareTokenKey(serverURL string) string {
	origin, err := normalizeShareServerURL(serverURL)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(origin))
	return shareServiceTokenKeyPrefix + hex.EncodeToString(sum[:16])
}

func shareClipRange(lines []lyrics.Line, selected map[int]bool, duration time.Duration, reliableTiming bool) (time.Duration, time.Duration, bool) {
	if duration <= 0 || !reliableTiming {
		return 0, 0, false
	}
	indexes := make([]int, 0, len(selected))
	for index, on := range selected {
		if !on {
			continue
		}
		if index < 0 || index >= len(lines) {
			return 0, 0, false
		}
		line := lines[index]
		if strings.TrimSpace(line.Text) == "" || !line.AtSource || line.At < 0 || line.At >= duration {
			return 0, 0, false
		}
		end := lyricSourceEnd(lines, index, duration)
		if end <= line.At {
			return 0, 0, false
		}
		indexes = append(indexes, index)
	}
	if len(indexes) == 0 {
		return 0, 0, false
	}
	sort.Ints(indexes)
	first := lines[indexes[0]]
	start := max(0*time.Second, first.At-time.Second)
	end := min(duration, lyricSourceEnd(lines, indexes[len(indexes)-1], duration)+time.Second)
	if end <= start {
		return 0, 0, false
	}
	return start, end, true
}

func lyricSourceEnd(lines []lyrics.Line, index int, duration time.Duration) time.Duration {
	line := lines[index]
	if line.EndSource && line.End > line.At {
		return line.End
	}
	for next := index + 1; next < len(lines); next++ {
		if lines[next].AtSource && lines[next].At > line.At {
			return lines[next].At
		}
	}
	return duration
}

func defaultShareHTTPClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Minute}
}
