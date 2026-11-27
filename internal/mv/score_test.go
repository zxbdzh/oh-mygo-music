package mv

import (
	"testing"
	"time"
)

// 标题、歌手和时长共同决定候选顺序，音频向内容不应占据第一屏。
func TestRankPrefersMVOverAudioOnly(t *testing.T) {
	candidates := []Candidate{
		{VideoID: "audio", Title: "晴天 纯享版", Uploader: "周杰伦", Duration: 4 * time.Minute},
		{VideoID: "mv", Title: "周杰伦 晴天 MV", Uploader: "周杰伦官方", Duration: 4*time.Minute + 30*time.Second},
	}
	out := Rank(candidates, "晴天", "周杰伦", 4*time.Minute+29*time.Second, 8)
	if out[0].VideoID != "mv" {
		t.Fatalf("top candidate %q, want mv", out[0].VideoID)
	}
	if len(out[0].Reasons) == 0 {
		t.Fatal("no reason to show")
	}
}

// 搜索词为空时不给任何候选加分。
func TestScoreEmptyQuery(t *testing.T) {
	c := Score("", "", 0, Candidate{Title: "晴天 MV"})
	if c.Score < 0 {
		t.Fatalf("negative score: %f", c.Score)
	}
}

// 自动匹配只在标题、时长都对得上、内容像 MV 且明显领先时才认定一条。
func TestAutoMatchNeedsTitleLengthAndLead(t *testing.T) {
	length := 4 * time.Minute
	good := Candidate{VideoID: "good", Title: "晴天 MV", Uploader: "周杰伦", Duration: length}
	for _, c := range []struct {
		name       string
		candidates []Candidate
		song       string
		duration   time.Duration
		want       string
	}{
		{"alone and matching", []Candidate{good}, "晴天", length, "good"},
		{"longer than the song", []Candidate{{VideoID: "short", Title: "晴天 MV", Duration: 2 * time.Minute}}, "晴天", length, ""},
		{"another song's title", []Candidate{{VideoID: "other", Title: "雨天", Duration: length}}, "晴天", length, ""},
		{"a cover", []Candidate{{VideoID: "cover", Title: "晴天 翻唱", Duration: length}}, "晴天", length, ""},
		{"two close matches", []Candidate{
			good,
			{VideoID: "rival", Title: "晴天 MV", Uploader: "周杰伦", Duration: length},
		}, "晴天", length, ""},
		{"no candidates", nil, "晴天", length, ""},
		{"unknown length", []Candidate{{VideoID: "unknown", Title: "晴天 MV"}}, "晴天", length, ""},
		{"unknown song length", []Candidate{good}, "晴天", 0, ""},
	} {
		ranked := Rank(c.candidates, c.song, "", c.duration, 8)
		got, ok := AutoMatch(ranked, c.song, c.duration)
		if c.want == "" {
			if ok {
				t.Errorf("%s: matched %q, want nothing", c.name, got.VideoID)
			}
			continue
		}
		if !ok || got.VideoID != c.want {
			t.Errorf("%s: matched %q (%v), want %q", c.name, got.VideoID, ok, c.want)
		}
	}
}
