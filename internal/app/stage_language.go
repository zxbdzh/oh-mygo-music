package app

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

type stageLyricCache struct {
	lyricDisplayCache
	language string
}

var stageLanguages = []struct{ id, label string }{
	{"", N("Bilingual")},
	{"original", N("Original Lyrics")},
	{"translation", N("Translated Lyrics")},
}

func (a *app) stageLyrics(track *music.Track, source []lyrics.Line) []lyrics.Line {
	if len(source) == 0 {
		return source
	}
	// 字形转换仍共用全局选择，但律动语言不能被普通歌词的翻译开关过滤。
	options := a.lyricOptions(track, false)
	cache := &a.stageDisplay
	key, language := a.lyricKey(track), a.settings.StageLanguage
	if cache.source != &source[0] || cache.count != len(source) || cache.key != key || cache.options != options || cache.language != language {
		shown := lyrics.Display(source, options)
		for index := range shown {
			line := &shown[index]
			switch language {
			case "original":
				line.Translation = ""
			case "translation":
				if strings.TrimSpace(line.Translation) != "" {
					line.Text = line.Translation
					// 译文无逐字时间，只按原行区间生成显示动效，不能套用日文原词的逐字时间。
					line.Words = lyrics.SpreadWords(line.Text, line.At, line.End)
				}
				line.Translation = ""
			}
		}
		*cache = stageLyricCache{
			lyricDisplayCache: lyricDisplayCache{source: &source[0], count: len(source), key: key, options: options, shown: shown},
			language:          language,
		}
	}
	return cache.shown
}

func (a *app) stageLanguageButton(c *ui.Context) {
	selected, label := "", stageLanguages[0].label
	for _, option := range stageLanguages {
		if option.id == a.settings.StageLanguage {
			selected, label = option.id, option.label
		}
	}
	button := npGlassControl(ui.ButtonBase(c.Key("stage-language")).Padding(6, 12).Gap(4).AlignItems(ui.Center).
		FocusRing(false).Label(T("Stage Language")).Tooltip(T("Stage Language")))
	foreground := white.Alpha(0.82)
	if button.Hovered() {
		foreground = white
	}
	button.Children(func() {
		ui.Text(c, T(label)).FontSize(13).FontWeight(600).TextColor(button.AnimateColor("fg", foreground, ui.Animation{Duration: 120 * time.Millisecond}))
		ui.Icon(c, iconDown).Size(12, 12).TextColor(button.AnimateColor("fg", foreground, ui.Animation{Duration: 120 * time.Millisecond}))
	})
	button.Menu(func(menu *ui.Menu) {
		for _, option := range stageLanguages {
			item := menu.Item(T(option.label)).Radio(selected == option.id)
			if option.id == "translation" {
				item.Subtitle(T("Use original lyrics when a translation is missing"))
			}
			if item.Chosen() && a.settings.StageLanguage != option.id {
				a.settings.StageLanguage = option.id
				if err := a.settings.save(); err != nil {
					a.toast = T("Could not save display preferences")
				}
			}
		}
	})
}
