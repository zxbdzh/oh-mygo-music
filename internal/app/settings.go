package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"mygo-music/internal/i18n"
	"mygo-music/internal/player"
)

// settings are the listener's choices, kept in settings.json in the user
// data directory.
type settings struct {
	// Language is what the app speaks: i18n.English or i18n.Chinese, or
	// i18n.System for the system's.
	Language string `json:"language"`
	// Source is the ID of the source last used.
	Source string `json:"source,omitempty"`
	// Captions writes down episodes as they play, for their captions, on this
	// device, once the listener has the model.
	Captions bool `json:"captions"`
	// AIEnabled 开启 AI 歌单建议；地址或模型为空时不出现入口。
	AIEnabled bool   `json:"aiEnabled"`
	AIBaseURL string `json:"aiBaseUrl,omitempty"`
	AIModel   string `json:"aiModel,omitempty"`
	// AIWebProvider 联网方式：off 不联网；native 用服务商自己的联网增强；
	// tavily / anysearch 由应用先搜索再拼进提示词。空值兼容旧配置。
	AIWebProvider string `json:"aiWebProvider,omitempty"`
	// AIWebSearch 旧版布尔开关，只读它迁移成 aiWebProvider，不再写入。
	AIWebSearch bool `json:"aiWebSearch,omitempty"`
	// AIAutoAdd 高置信度时直接写入；默认关闭。
	AIAutoAdd bool `json:"aiAutoAdd,omitempty"`
	// AIAutoSuggestDisabled 关闭喜欢后的自动推荐；默认开启，所以存反向值。
	AIAutoSuggestDisabled bool `json:"aiAutoSuggestDisabled,omitempty"`
	// DisabledPlugins are the IDs of the plugins turned off. All are on until
	// then, the built-in one included.
	DisabledPlugins []string `json:"disabledPlugins,omitempty"`
	// PluginsChecked is when plugins were last checked for new versions, once a
	// day, and PluginUpdates the versions found, by plugin.
	PluginsChecked time.Time         `json:"pluginsChecked,omitzero"`
	PluginUpdates  map[string]string `json:"pluginUpdates,omitempty"`
	// Notch shows the quick player around the notch: the listener turns
	// it on.
	Notch bool `json:"notch"`
	// NotchActivity shows the cover and the music's levels beside the
	// notch while music plays, which hides the menu bar there.
	NotchActivity bool `json:"notchActivity"`
	// NotchVirtual keeps a notch of the app's own showing at the top of a
	// display without one; else it shows only while the pointer asks.
	NotchVirtual bool `json:"notchVirtual"`
	// NotchGreeted says the notch showed itself, once, as the first song
	// played (greet).
	NotchGreeted bool `json:"notchGreeted"`
	// Quality is what's asked of sources: standard, high or lossless. A source
	// may ignore it.
	Quality string `json:"quality"`
	// 空值表示沿用播放音质（Quality），显式设置时作为下载的默认请求音质。
	DownloadQuality string `json:"downloadQuality,omitempty"`
	// DownloadDirectory 为默认下载文件夹；空值表示使用系统默认下载目录。
	DownloadDirectory string `json:"downloadDirectory,omitempty"`
	// PromptDownload 控制下载前是否弹出确认窗口；正值不省略以兼容缺省配置默认开启。
	PromptDownload bool `json:"promptDownload"`
	// Appearance is light, dark, or system to follow the system.
	Appearance string `json:"appearance"`
	// 保存显式关闭的值，旧配置缺少此项时仍采用默认开启。
	CoverBackdrop      bool               `json:"coverBackdrop"`
	CoverBackdropStyle coverBackdropStyle `json:"coverBackdropStyle"`
	// GrowFromCover opens Now Playing out of the player bar's cover, and
	// closes it back into it: an experiment the listener turns on. Off, it
	// slides up from the bottom of the window.
	GrowFromCover bool `json:"growFromCover"`
	// Smoke makes the progress bars comets trailing smoke as music plays,
	// for a few percent of a core: the listener turns it on.
	Smoke bool `json:"smoke"`
	// LyricsStage shows lyrics on the stage, Folia's style, in Now
	// Playing, rather than scrolling.
	LyricsStage bool `json:"lyricsStage"`
	// 反向字段让旧配置缺失时保持显示译文，用户明确关闭后可跨重启保留。
	HidePlaybackTranslation bool            `json:"hidePlaybackTranslation,omitempty"`
	HideCardTranslation     bool            `json:"hideCardTranslation,omitempty"`
	LyricsScript            string          `json:"lyricsScript,omitempty"`
	ChineseLyrics           map[string]bool `json:"chineseLyrics,omitempty"`
	CardTemplate            string          `json:"cardTemplate,omitempty"`
	CardSquare              bool            `json:"cardSquare,omitempty"`
	CardDirectory           string          `json:"cardDirectory,omitempty"`
	// ShareServerURL is the user's own HTTPS service; its token is stored in the OS credential store.
	ShareServerURL string `json:"shareServerUrl,omitempty"`
	// StageStyle is how the stage sets its lyrics (stageStyles): empty for
	// Folia's classic, or the id of another of its visualizers.
	StageStyle     string `json:"stageStyle,omitempty"`
	StageIntensity string `json:"stageIntensity,omitempty"`
	// 律动独立选择原文、译文或双语；空值保留原有双语呈现。
	StageLanguage string `json:"stageLanguage,omitempty"`
	// PlayerStyle is how Now Playing shows the song playing
	// (playerStyles): empty for its cover, or a retro player's id.
	PlayerStyle string `json:"playerStyle,omitempty"`
	// StageSparks throws sparks off each word on the stage as it is sung,
	// whatever its style.
	StageSparks bool `json:"stageSparks,omitempty"`
	// Volume is where the volume was left, from 0 to 1.
	Volume float64 `json:"volume"`
	// 静音退出后仍恢复原来的非零音量；零值表示尚无有效历史。
	MutedFrom float64         `json:"mutedFrom,omitempty"`
	Shortcuts *shortcutConfig `json:"globalShortcuts,omitempty"`
	// Normalize evens out the loudness of tracks, by what their source
	// measured of each.
	Normalize           bool `json:"normalize"`
	CrossfadeEnabled    bool `json:"crossfadeEnabled"`
	CrossfadeDurationMs int  `json:"crossfadeDurationMs"`
	// 开启默认项不能省略 false，否则重启会丢失用户关闭的选择。
	PlaybackFadeEnabled bool `json:"playbackFadeEnabled"`
	PauseFadeOutMs      int  `json:"pauseFadeOutMs"`
	PlayFadeInMs        int  `json:"playFadeInMs"`
	// HotComments shows a song's top comments in its quiet parts, in place of
	// the lyrics (quietComment), for a source with comments: the listener
	// turns it on.
	HotComments bool `json:"hotComments"`
	// BetterLyricsIntegration shares playback and source lyrics over loopback when enabled.
	BetterLyricsIntegration bool `json:"betterLyricsIntegration"`
	// 独立授权默认关闭，旧版本只读联动不能在升级后自动获得写权限。
	BetterLyricsLyricsWrite bool `json:"betterLyricsLyricsWrite"`
	// Fog fills Now Playing's backdrop with fog of the cover's color,
	// rising and swelling with the bass: an experiment the listener turns
	// on.
	Fog bool `json:"fog"`
	// MVBackground shows a song's online MV quietly behind the lyrics
	// (在线 MV 背景)：默认开启，绑定过的歌才有画面。
	MVBackground bool    `json:"mvBackground"`
	MVScale      float64 `json:"mvScale"`
	MVBlur       float64 `json:"mvBlur"`
	MVBrightness float64 `json:"mvBrightness"`
	MVOverlay    float64 `json:"mvOverlay"`
	MVPanX       float64 `json:"mvPanX"`
	MVPanY       float64 `json:"mvPanY"`
	MVHideLyrics bool    `json:"mvHideLyrics"`
	// AppIcon is where the app's icon shows on macOS (appIcons); elsewhere
	// it is always in the notification area.
	AppIcon string `json:"appIcon"`
	// LaunchAtLoginBackground keeps login launches in the tray until the
	// listener asks to see the main window.
	LaunchAtLoginBackground bool `json:"launchAtLoginBackground"`
	// CacheMB is how many megabytes the songs kept on disk may take; 0
	// keeps none.
	CacheMB int `json:"cacheMB"`
	// SearchHistory keeps submitted keywords, most recent first.
	SearchHistory []string `json:"searchHistory,omitempty"`
	// SearchAll 让搜索一次问遍每个能搜的来源；关闭时只搜当前来源。
	SearchAll bool `json:"searchAll,omitempty"`

	path string // where they were loaded from, and are saved to
}

func defaultSettings() settings {
	return settings{Language: i18n.System, NotchActivity: true, Quality: "high", PromptDownload: true, Appearance: "system", CoverBackdrop: true, CoverBackdropStyle: defaultCoverBackdropStyle(), Volume: 0.8, Normalize: true, AppIcon: "dock-and-menubar", CacheMB: 2048, StageIntensity: "standard", CrossfadeEnabled: true, CrossfadeDurationMs: 5000, PlaybackFadeEnabled: true, PauseFadeOutMs: 180, PlayFadeInMs: 260, MVBackground: true, MVScale: 115, MVBrightness: 100}
}

type coverBackdropStyle struct {
	Blur  float64 `json:"blur"`
	Shade float64 `json:"shade"`
}

func defaultCoverBackdropStyle() coverBackdropStyle {
	return coverBackdropStyle{Blur: 70, Shade: 50}
}

// 遮罩最低档仍留一点暗色护底，避免封面高光冲掉白字。
func (s coverBackdropStyle) shadeScale() float32 {
	return 0.45 + 1.1*clamp01(float32(s.Shade/100))
}

func (s settings) playbackTransitions() player.TransitionSettings {
	crossfade, pause, play := s.CrossfadeDurationMs, s.PauseFadeOutMs, s.PlayFadeInMs
	if crossfade < 1000 || crossfade > 12000 {
		crossfade = 5000
	}
	if pause < 50 || pause > 1000 {
		pause = 180
	}
	if play < 50 || play > 1000 {
		play = 260
	}
	return player.TransitionSettings{Crossfade: s.CrossfadeEnabled, CrossfadeDuration: time.Duration(crossfade) * time.Millisecond, PlaybackFade: s.PlaybackFadeEnabled, PauseFadeOut: time.Duration(pause) * time.Millisecond, PlayFadeIn: time.Duration(play) * time.Millisecond}
}

func settingsPath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// loadSettings reads the settings, or returns the defaults.
func loadSettings() settings {
	s := defaultSettings()
	if path, err := settingsPath(); err == nil {
		if b, err := os.ReadFile(path); err == nil {
			json.Unmarshal(b, &s)
			s.CoverBackdropStyle.Blur = min(100, max(0, s.CoverBackdropStyle.Blur))
			s.CoverBackdropStyle.Shade = min(100, max(0, s.CoverBackdropStyle.Shade))
			s.MVScale = min(220, max(100, s.MVScale))
			s.MVBlur = min(16, max(0, s.MVBlur))
			s.MVBrightness = min(140, max(60, s.MVBrightness))
			s.MVOverlay = min(60, max(0, s.MVOverlay))
			s.MVPanX = min(60, max(-60, s.MVPanX))
			s.MVPanY = min(60, max(-60, s.MVPanY))
			// 旧版布尔联网开关迁移：开过就当作原生的服务商联网增强；
			// 新字段已有值时以新字段为准，不再覆盖。
			if s.AIWebSearch && s.AIWebProvider == "" {
				s.AIWebProvider = "native"
			}
		}
		s.path = path
	}
	return s
}

// save keeps the settings where they were loaded from; defaults that were
// never loaded, as in tests, are not saved.
func (s settings) save() error {
	path := s.path
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// 同目录替换，写入失败时不截断用户原来的设置。
	return os.Rename(f.Name(), path)
}

// applyAppearance makes the windows light, dark, or follow the system. Only
// a new one: it repaints every window, waiting for the main thread, and any
// setting changed applies it.
func (s settings) applyAppearance() {
	src := mygo.ThemeSystem
	switch s.Appearance {
	case "light":
		src = mygo.ThemeLight
	case "dark":
		src = mygo.ThemeDark
	}
	if src != mygo.Theme.Source() {
		mygo.Theme.SetSource(src)
	}
}

// qualityLevels are what can be asked of sources, by what they sound like.
var qualityLevels = []struct{ level, label string }{
	{"standard", N("Standard")},
	{"high", N("High")},
	{"lossless", N("Lossless")},
}

// downloadQualityLevels 为默认下载音质的可选项；空值表示沿用当前播放音质。
var downloadQualityLevels = []struct{ level, label string }{
	{"", N("Follow Playback")},
	{"standard", N("Standard")},
	{"high", N("High")},
	{"lossless", N("Lossless")},
}

// defaultSystemDownloadDir 返回系统默认下载目录，测试可替换。
var defaultSystemDownloadDir = func() string {
	dir, _ := mygo.App.Path(mygo.PathDownloads)
	return dir
}

// downloadFolder 返回当前默认下载文件夹，未自定义时沿用系统下载目录。
func (a *app) downloadFolder() string {
	if dir := strings.TrimSpace(a.settings.DownloadDirectory); dir != "" {
		return dir
	}
	return defaultSystemDownloadDir()
}

// downloadQualitySelection 返回配置中有效的下载音质档位（空字符串表示跟随播放音质）。
func (a *app) downloadQualitySelection() string {
	for _, q := range qualityLevels {
		if q.level == a.settings.DownloadQuality {
			return q.level
		}
	}
	return ""
}

// resolveDownloadQuality 把配置或弹窗中的音质选择解析为真正请求音源的音质标识。
func (a *app) resolveDownloadQuality(selection string) string {
	for _, q := range qualityLevels {
		if q.level == selection {
			return q.level
		}
	}
	for _, q := range qualityLevels {
		if q.level == a.settings.Quality {
			return q.level
		}
	}
	return "high"
}

// downloadQuality 返回有效的默认下载音质，未指定或配置非法时回退到有效播放音质。
func (a *app) downloadQuality() string {
	return a.resolveDownloadQuality(a.settings.DownloadQuality)
}

// cacheLimits are the sizes the song cache may take, in megabytes.
var cacheLimits = []struct {
	mb    int
	label string
}{
	{0, N("Off")},
	{1024, N("1 GB")},
	{2048, N("2 GB")},
	{5120, N("5 GB")},
	{10240, N("10 GB")},
}
