package app

import (
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

type lyricDisplayCache struct {
	source  *lyrics.Line
	count   int
	key     string
	options lyrics.DisplayOptions
	shown   []lyrics.Line
}

var lyricScripts = []struct{ id, label string }{
	{"", N("Follow Interface")},
	{"simplified", N("Simplified Chinese")},
}

func (a *app) lyricKey(track *music.Track) string {
	source := track.Source
	if source == "" && a.library != nil {
		source = a.library.id
	}
	return source + "\x00" + track.ID
}

func (a *app) lyricOptions(track *music.Track, hideTranslation bool) lyrics.DisplayOptions {
	simplify := a.settings.LyricsScript == "simplified" || a.settings.LyricsScript == "" && i18n.Language() == i18n.Chinese
	return lyrics.DisplayOptions{Simplify: simplify, OriginalChinese: a.settings.ChineseLyrics[a.lyricKey(track)], HideTranslation: hideTranslation}
}

func (a *app) displayedLyrics(track *music.Track, source []lyrics.Line) []lyrics.Line {
	if len(source) == 0 {
		return source
	}
	options := a.lyricOptions(track, a.settings.HidePlaybackTranslation)
	cache := &a.lyricDisplay
	key := a.lyricKey(track)
	// 播放帧只复用结果；歌词数据或任一显示选择变化时才重新转换。
	if cache.source != &source[0] || cache.count != len(source) || cache.key != key || cache.options != options {
		*cache = lyricDisplayCache{source: &source[0], count: len(source), key: key, options: options, shown: lyrics.Display(source, options)}
	}
	return cache.shown
}

func hasTranslation(lines []lyrics.Line) bool {
	for _, line := range lines {
		if line.Translation != "" {
			return true
		}
	}
	return false
}

func (a *app) saveLyricChoices() {
	if err := a.settings.save(); err != nil {
		a.toast = T("Could not save display preferences")
	}
	a.requestShareRender()
	a.redraw()
}

func (a *app) lyricScriptItems(menu *ui.Menu, track *music.Track) {
	for _, option := range lyricScripts {
		if menu.Item(T(option.label)).Radio(a.settings.LyricsScript == option.id).Chosen() {
			a.settings.LyricsScript = option.id
			a.saveLyricChoices()
		}
	}
	menu.Separator()
	key := a.lyricKey(track)
	if menu.Item(T("Treat Lyrics as Chinese")).Checked(a.settings.ChineseLyrics[key]).Chosen() {
		if a.settings.ChineseLyrics == nil {
			a.settings.ChineseLyrics = make(map[string]bool)
		}
		if a.settings.ChineseLyrics[key] {
			delete(a.settings.ChineseLyrics, key)
		} else {
			a.settings.ChineseLyrics[key] = true
		}
		a.saveLyricChoices()
	}
}

func (a *app) lyricDisplayButton(c *ui.Context, track *music.Track) {
	button := ui.ButtonBase(c.Key("lyrics-display")).Padding(6, 10).Radius(999).Label(T("Lyrics Display")).Background(white.Alpha(0.09))
	button.Children(func() { ui.Text(c, T("Lyrics Display")).FontSize(12).TextColor(white.Alpha(0.8)) })
	button.Menu(func(menu *ui.Menu) {
		lines, _ := a.pl.Lyrics()
		available := hasTranslation(lines)
		item := menu.Item(T("Show Translation")).Checked(!a.settings.HidePlaybackTranslation).Disabled(!available)
		if !available {
			item.Subtitle(T("No translation available"))
		}
		if item.Chosen() {
			a.settings.HidePlaybackTranslation = !a.settings.HidePlaybackTranslation
			a.saveLyricChoices()
		}
		menu.Separator()
		a.lyricScriptItems(menu, track)
	})
}
