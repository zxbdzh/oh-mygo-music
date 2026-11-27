package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"rsc.io/qr"

	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
	"mygo-music/internal/mv"
)

// 设置 MM_FRAMES 后保存 MV 背景下的歌词页，供目视核对遮罩与底部控制条。
func TestMVBackgroundFrame(t *testing.T) {
	dir := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
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
	tr.Source = "fake"
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
	a.nowPlaying, a.npPane = true, paneMV

	tester := ui.NewTester(a.view, 1180, 780)
	tester.SetDark(true)
	tester.Frame()
	tester.Move(600, 400)
	tester.Advance(12 * time.Second)
	tester.Frame()
	settled(tester)
	saveFrame(t, dir, "mv-background.png", tester.Image())
	compact := ui.NewTester(a.view, 880, 650)
	compact.SetDark(true)
	compact.Move(440, 300)
	settled(compact)
	saveFrame(t, dir, "mv-background-compact.png", compact.Image())
}

// 设置搜索可直接定位 MV 分组，截图用于核对选项布局。
func TestMVSettingsFrame(t *testing.T) {
	dir := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	a := newTestApp()
	a.settingsPage, a.settingsQuery = "playback", "MV"
	tester := ui.NewTester(a.settingsView, 900, 850)
	tester.SetDark(true)
	settled(tester)
	saveFrame(t, dir, "mv-settings.png", tester.Image())
}

// 设置 MM_FRAMES 后保存 MV 设置里的账号行与内联登录，供目视核对。
func TestMVAccountSettingsFrame(t *testing.T) {
	dir := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	a := newTestApp()
	a.settingsPage = "playback"
	a.settingsQuery = strings.ToLower(T("Bilibili Account"))
	code, err := qr.Encode("https://example.test/bilibili-login", qr.M)
	if err != nil {
		t.Fatal(err)
	}
	a.mvLogin = mvLoginState{open: true, inline: true, status: T("Scan with Bilibili."), qrCode: code}
	tester := ui.NewTester(a.settingsView, 900, 850)
	tester.SetDark(true)
	settled(tester)
	saveFrame(t, dir, "mv-account-settings.png", tester.Image())
}

// 设置 MM_FRAMES 后保存检测到插件登录、等待用户确认时的账号行。
func TestMVPluginAccountSettingsFrame(t *testing.T) {
	dir := framesDir(t)
	defer i18n.SetLanguage(i18n.English)
	i18n.SetLanguage(i18n.Chinese)
	a := newTestApp()
	a.mvPluginAvailable = true
	a.settingsPage = "playback"
	a.settingsQuery = strings.ToLower(T("Bilibili Account"))
	tester := ui.NewTester(a.settingsView, 900, 850)
	tester.SetDark(true)
	settled(tester)
	saveFrame(t, dir, "mv-account-plugin.png", tester.Image())
}

// 设置 MM_FRAMES 后保存 MV 搜索面板，供目视核对候选与搜索框。
// 候选多到一屏放不下，顺手核对列表被裁在面板里、关闭按钮还在面板内。
func TestMVSearchFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	tr := song("mv-search", "晴天", "周杰伦")
	a.pl.Cue([]music.Track{tr})
	a.nowPlaying, a.npPane = true, paneLyrics
	a.mvDialog = mvDialogState{
		open:  true,
		track: &tr,
		query: "晴天 周杰伦",
		candidates: []mv.Candidate{
			{VideoID: "BV1", Title: "晴天 MV", Uploader: "周杰伦", Reasons: []string{"Title matches", "Artist matches", "MV or official"}},
			{VideoID: "BV2", Title: "晴天 现场版", Uploader: "周杰伦", Reasons: []string{"Title contains the song", "Artist matches"}},
			{VideoID: "BV3", Title: "晴天 纯享", Uploader: "音乐搬运", Reasons: []string{"Title contains the song", "Audio-only or non-MV content"}},
			{VideoID: "BV4", Title: "ChiChill乐团 · 晴天 现场版", Uploader: "周杰伦官方", Reasons: []string{"Song title words match", "Duration matches"}},
			{VideoID: "BV5", Title: "晴天 (Hi-Res 无损)", Uploader: "{{PERSON_9w9tt0oc}}", Reasons: []string{"Title contains the song", "Audio-only or non-MV content"}},
			{VideoID: "BV6", Title: "翻唱 晴天", Uploader: "时触", Reasons: []string{"Title contains the song", "Duration differs a lot"}},
			{VideoID: "BV7", Title: "晴天 · 钢琴版", Uploader: "时触", Reasons: []string{"Title contains the song", "Duration matches"}},
			{VideoID: "BV8", Title: "晴天 MV 官方", Uploader: "周杰伦", Reasons: []string{"Title contains the song", "MV or official"}},
		},
	}
	tester := ui.NewTester(a.view, 1180, 780)
	tester.SetDark(true)
	settled(tester)
	saveFrame(t, dir, "mv-search.png", tester.Image())
}

// 设置 MM_FRAMES 后保存 MV 登录面板，供目视核对二维码和 Cookie 输入。
func TestMVLoginFrame(t *testing.T) {
	dir := framesDir(t)
	code, err := qr.Encode("https://example.test/bilibili-login", qr.M)
	if err != nil {
		t.Fatal(err)
	}
	a := newTestApp()
	a.mvLogin = mvLoginState{open: true, status: T("Scan with Bilibili."), qrCode: code}
	tester := ui.NewTester(a.view, 1180, 780)
	tester.SetDark(true)
	settled(tester)
	saveFrame(t, dir, "mv-login.png", tester.Image())
}

// 设置 MM_FRAMES 后保存 MV 失败回退图，确认封面背景和角落提示都在。
func TestMVFailureFrame(t *testing.T) {
	dir := framesDir(t)
	a := newTestApp()
	tr := song("mv-failure", "晴天", "周杰伦")
	tr.Source = "fake"
	a.pl.Cue([]music.Track{tr})
	a.mvStore = newTestMVStore(t, "fake", "mv-failure")
	a.nowPlaying, a.npPane = true, paneLyrics
	a.settings.MVBackground = false
	tester := ui.NewTester(a.view, 1180, 780)
	tester.SetDark(true)
	settled(tester)
	a.settings.MVBackground = true
	a.npPane = paneMV
	a.mv.key = bindingKeyOf(&tr)
	a.mv.err = "synthetic failure"
	tester.Frame()
	saveFrame(t, dir, "mv-failure.png", tester.Image())
}
