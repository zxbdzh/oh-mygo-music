// Package mv 把一首歌对应到一条在线 MV，并把它解析成能直接播放的静音视频流。
//
// 首版只接 B 站：搜索候选由听众确认后写入绑定，解析结果带有效期缓存。
// 视频只是画面，声音仍由播放器负责，所以这里不关心音轨。
package mv

import "time"

// Candidate 是一次搜索里的一条待确认结果。它不是绑定，听众确认前不生效。
type Candidate struct {
	VideoID   string
	Title     string
	Uploader  string
	Duration  time.Duration
	Thumbnail string
	Score     float64
	Reasons   []string
}

// Binding 是听众确认过的对应关系，键为音源加曲目 ID。
type Binding struct {
	Source    string    `json:"source"`
	TrackID   string    `json:"trackId"`
	VideoID   string    `json:"videoId"`
	Title     string    `json:"title,omitempty"`
	Uploader  string    `json:"uploader,omitempty"`
	OffsetMs  int       `json:"offsetMs,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Key 是绑定和缓存的主键。不同音源的曲目 ID 会撞，所以必须带上音源。
func (b Binding) Key() string { return bindingKey(b.Source, b.TrackID) }

func bindingKey(source, trackID string) string { return source + "\x00" + trackID }

// Resolved 是一条可以直接交给播放器的流。
//
// Cookie 不在这里：登录凭据只存在内存和系统凭据管理器里，
// 代理转发时由 Client 补上，避免随解析缓存写进磁盘。
type Resolved struct {
	URL       string            `json:"url"`
	Referer   string            `json:"referer"`
	UA        string            `json:"ua,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt time.Time         `json:"expiresAt"`
	Height    int               `json:"height,omitempty"`
	Codec     string            `json:"codec,omitempty"`
	Kind      string            `json:"kind,omitempty"` // dash-avc1 或 progressive
}

// Expired 报告这条流还能不能用；留一分钟余量，避免刚好卡在过期边上。
func (r Resolved) Expired(now time.Time) bool {
	return r.URL == "" || (!r.ExpiresAt.IsZero() && now.Add(time.Minute).After(r.ExpiresAt))
}

// 解析结果的保守有效期。B 站直链的 deadline 通常 45 分钟，这里留出余量。
const resolveTTL = 25 * time.Minute
