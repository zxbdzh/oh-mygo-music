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
