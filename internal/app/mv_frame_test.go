package app

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 设置 MM_FRAMES 后保存 MV 背景下的歌词页，供目视核对遮罩与底部控制条。
func TestMVBackgroundFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	a.settings.MVBackground = true
	a.mvStore = newTestMVStore(t, "fake", "mv-frame")
	// 无头视图不能真的解码视频，用一张实测帧占位，让布局和遮罩能看。
	// 帧从 F:\tmp\mv-probe-full.mp4 用 ffmpeg 截取，仓库不带上它。
	raw, err := os.ReadFile(`F:\tmp\mv-real-frame.png`)
	if err != nil {
		t.Skip("no MV frame at F:\\tmp\\mv-real-frame.png")
	}
	frame, err := ui.DecodeBitmap(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.mv.frame = frame
	tr := song("mv-frame", "晴天", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.pl.SetLyrics(lyrics.Build(
		"[00:01]故事的小黄花　从出生那年就飘着\n"+
			"[00:04]童年的荡秋千　随记忆一直晃到现在\n"+
			"[00:08]吹着前奏望着天空　我想起花瓣试着掉落\n"+
			"[00:12]但偏偏雨渐渐大到我看你不见\n"+
			"[00:16]还要多久我才能在你身边",
		"",
	))
	a.pl.Seek(12 * time.Second)
	track := tr
	a.mv.key = bindingKeyOf(&track)
	a.nowPlaying, a.npPane = true, paneLyrics

	tester := ui.NewTester(a.view, 1180, 780)
	tester.SetDark(true)
	tester.Frame()
	tester.Advance(12 * time.Second)
	tester.Frame()
	settled(tester)
	saveFrame(t, dir, "mv-background.png", tester.Image())
}
