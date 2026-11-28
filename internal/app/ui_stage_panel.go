package app

import (
	"maps"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

type stagePanelDraft struct {
	mode, fixed, intensity     string
	pool                       []string
	speed                      [4]string
	sparks, spectrum           bool
	rushBackground, rushRhythm string
}

func stageDraft(s settings) stagePanelDraft {
	a := s.StageAnimation
	return stagePanelDraft{mode: a.mode(), fixed: stageStyleOrClassic(s.StageStyle),
		intensity: s.StageIntensity, pool: a.pool(), speed: a.speed(), sparks: s.StageSparks,
		spectrum: a.Spectrum, rushBackground: a.RushBackground, rushRhythm: a.RushRhythm}
}

type stagePanelState struct {
	open, discard, pinning, options bool
	draft                           stagePanelDraft
	band                            int
	pinKey                          string
	err                             string
	anchor                          ui.Handle
	scroll                          ui.ScrollState
}

func (a *app) openStagePanel() {
	anchor := a.stagePanel.anchor
	a.stagePanel = stagePanelState{open: true, draft: stageDraft(a.settings), anchor: anchor}
}

func (a *app) closeStagePanel(c *ui.Context) {
	d := &a.stagePanel
	d.open, d.discard, d.pinning = false, false, false
	d.anchor.Focus(c)
}

func (a *app) askStagePanelClose(c *ui.Context) {
	d := &a.stagePanel
	saved, draft := stageDraft(a.settings), d.draft
	poolsEqual := slices.Equal(saved.pool, draft.pool)
	saved.pool, draft.pool = nil, nil
	// DialogBase 会先解除 open，恢复它才能保留未保存的选择。
	if !poolsEqual || saved.mode != draft.mode || saved.fixed != draft.fixed ||
		saved.intensity != draft.intensity || saved.speed != draft.speed || saved.sparks != draft.sparks ||
		saved.spectrum != draft.spectrum || saved.rushBackground != draft.rushBackground || saved.rushRhythm != draft.rushRhythm {
		d.open, d.discard = true, true
		return
	}
	a.closeStagePanel(c)
}

func (a *app) saveStagePanel(c *ui.Context, tr *music.Track) {
	d := &a.stagePanel
	next := a.settings
	next.StageStyle, next.StageIntensity, next.StageSparks = d.draft.fixed, d.draft.intensity, d.draft.sparks
	s := &next.StageAnimation
	s.Mode, s.Pool = d.draft.mode, d.draft.pool
	speed := d.draft.speed
	s.Speed = &speed
	s.Spectrum, s.RushBackground, s.RushRhythm = d.draft.spectrum, d.draft.rushBackground, d.draft.rushRhythm
	stageKeepRandom(s)
	if err := next.save(); err != nil {
		d.err = T("Could not save display preferences")
		return
	}
	a.settings = next
	a.updateStageDirector(c, tr, true)
	a.closeStagePanel(c)
}

func (a *app) pinStageStyle(c *ui.Context, tr *music.Track, style string, pin bool) {
	d := &a.stagePanel
	key := a.lyricKey(tr)
	if d.pinKey != key {
		return
	}
	next := a.settings
	next.StageAnimation.Songs = maps.Clone(next.StageAnimation.Songs)
	if pin {
		if next.StageAnimation.Songs == nil {
			next.StageAnimation.Songs = make(map[string]string)
		}
		next.StageAnimation.Songs[key] = style
	} else {
		delete(next.StageAnimation.Songs, key)
	}
	if err := next.save(); err != nil {
		d.err = T("Could not save display preferences")
		return
	}
	a.settings = next
	d.pinning, d.err = false, ""
	a.updateStageDirector(c, tr, true)
}

var stagePreviewLyrics = lyrics.FromSource(&music.Lyrics{Lines: []music.LyricLine{
	{Start: 0, End: 2600, Text: "Let the evening drift by"},
	{Start: 3000, End: 6300, Words: []music.LyricWord{
		{Start: 3000, End: 3800, Text: "Starlight "}, {Start: 3800, End: 4400, Text: "on "},
		{Start: 4400, End: 4900, Text: "the "}, {Start: 4900, End: 6300, Text: "road"},
	}},
	{Start: 6500, End: 9500, Text: "We set off together"},
}})

func stageChinesePreview(first, middle, last string) []lyrics.Line {
	lines := lyrics.FromSource(&music.Lyrics{Lines: []music.LyricLine{
		{Start: 0, End: 2600, Text: first},
		{Start: 3000, End: 6300, Text: middle},
		{Start: 6500, End: 9500, Text: last},
	}})
	// 预览使用示例字的时间，让 Rush 同时展示已唱和未唱的区域。
	lines[1].WordsSource = true
	return lines
}

func (a *app) stagePanelView(c *ui.Context) {
	d := &a.stagePanel
	tr := a.pl.Current()
	if !d.open {
		return
	}
	if tr == nil || !a.nowPlaying || a.npPane != paneStage {
		d.open = false
		return
	}
	key := a.lyricKey(tr)
	if d.pinning && d.pinKey != key {
		d.pinning = false
	}
	using := a.animationStyle(tr)
	lines, loaded := a.pl.Lyrics()
	hasLyrics := len(lines) > 0
	status := Tf("This song · Now using: %s", stageStyleLabel(using))
	if !loaded {
		status = T("Loading lyrics…")
	} else if len(lines) == 0 {
		status = T("No lyrics. Enjoy the music.")
	}
	playingArt := a.art.get(sized(tr.Album.ImageURL, 600), true)
	accent := ui.Hex("#a5b8e2")
	if playingArt != nil && !playingArt.failed {
		accent = playingArt.tint.Mix(white, .4)
	}
	w, h := c.Size()
	width := max(1, min(w-32, 868))
	p := stagePanelPalette
	dialog := ui.DialogBase(c.Key("stage-panel"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, .36))
		panel.Width(width).Height(max(1, min(h-64, 646))).Padding(14, 18).Gap(8).Radius(16).
			Background(p.bg).Border(1, p.hairline).TextColor(p.text).Label(T("Lyric Animation"))
		if d.discard {
			ui.Column(c.Key("discard-stage")).Grow(1).Center().Gap(16).Children(func() {
				ui.Text(c, T("Discard unsaved animation changes?")).FontSize(16).FontWeight(650).TextColor(p.text)
				ui.Row(c).Gap(8).Children(func() {
					if stagePanelButton(c, "continue", T("Continue Editing"), false).AutoFocus().Clicked() {
						d.discard = false
					}
					if stagePanelButton(c, "discard", T("Discard Changes"), true).Clicked() {
						a.closeStagePanel(c)
					}
				})
			})
			return
		}
		ui.Row(c.Key("heading")).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, T("Lyric Animation")).Grow(1).FontSize(20).FontWeight(700).TextColor(p.text)
			if stagePanelButton(c, "close", T("Close"), false).AutoFocus().Clicked() {
				a.askStagePanelClose(c)
			}
		})
		ui.Row(c.Key("now-using")).AlignItems(ui.Center).Gap(8).Padding(5, 8).Radius(8).Background(p.sidebar).Children(func() {
			ui.Text(c, status).Grow(1).MinWidth(0).SingleLine().FontSize(13).TextColor(p.text)
			_, pinned := a.settings.StageAnimation.Songs[key]
			if pinned && stagePanelButton(c, "clear-pin", T("Clear Song Style"), false).Clicked() {
				d.pinKey = key
				a.pinStageStyle(c, tr, "", false)
			}
			if d.pinning {
				if stagePanelButton(c, "close-pin", T("Cancel Song Style"), false).Clicked() {
					d.pinning = false
				}
			} else if stagePanelButton(c, "pin", T("Set for This Song…"), false).Clicked() {
				d.pinning, d.pinKey = true, key
			}
		})
		if d.pinning {
			ui.Text(c, T("Choose a style for this song. Applies immediately.")).FontSize(12).TextColor(p.muted)
		} else {
			ui.Row(c.Key("mode")).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, T("Change Animation")).Grow(1).FontSize(13).FontWeight(600).TextColor(p.text)
				for _, mode := range []struct{ id, label string }{{stageFixed, "Fixed"}, {stageRandom, "Random"}, {stageTempo, "By Tempo"}} {
					if stagePanelButton(c, mode.id, T(mode.label), d.draft.mode == mode.id).Selected(d.draft.mode == mode.id).Clicked() {
						d.draft.mode = mode.id
					}
				}
			})
			switch d.draft.mode {
			case stageTempo:
				ui.Row(c.Key("speed-bands")).Wrap().AlignItems(ui.Center).Gap(5).Children(func() {
					for i, label := range []string{"<90", "90–119", "120–149", "150+"} {
						if stagePanelButton(c, label, label+" BPM", d.band == i).Selected(d.band == i).Clicked() {
							d.band = i
						}
					}
				})
				message := Tf("Without a reliable tempo: %s", stageStyleLabel(d.draft.fixed))
				_, pinned := a.settings.StageAnimation.Songs[key]
				if !pinned && a.settings.StageAnimation.mode() == stageTempo && !a.stageDirector.switched && a.stageDirector.pending {
					message = T("Will switch after the current line")
				}
				ui.Text(c, message).FontSize(12).TextColor(p.muted).SingleLine()
			case stageRandom:
				ui.Text(c, T("Choose styles to shuffle between songs")).FontSize(12).TextColor(p.muted)
			}
		}
		previewLines := stagePreviewLyrics
		if i18n.Language() == i18n.Chinese {
			previewLines = *ui.Local(panel, "chinese-preview", func() []lyrics.Line {
				return stageChinesePreview(T("Let the evening drift by"), T("Starlight on the road"), T("We set off together"))
			})
		}
		ui.Text(c, Tf("Animation Styles · %d", len(stageStyles))).FontSize(13).FontWeight(650).TextColor(p.text)
		ui.Scroll(c.Key("style-scroll")).Grow(1).MinHeight(0).TrackScroll(&d.scroll).Padding(0, 8, 4, 0).Gap(6).Children(func() {
			cols := 1
			if width >= 650 {
				cols = 2
			}
			for first := 0; first < len(stageStyles); first += cols {
				ui.Row(c.Key(first)).Gap(6).Children(func() {
					for _, style := range stageStyles[first:min(first+cols, len(stageStyles))] {
						a.stageStyleCard(c, tr, style, hasLyrics && style.id == using, previewLines, playingArt, accent, width/float32(cols))
					}
					if cols == 2 && first+1 == len(stageStyles) {
						ui.Box(c).Grow(1).Basis(0)
					}
				})
			}
		})
		a.stagePanelOptions(c)
		if d.err != "" {
			ui.Text(c, d.err).Role(ui.RoleAlert).FontSize(12).TextColor(ui.Hex("#ffb2a7"))
		}
		ui.Row(c.Key("actions")).Justify(ui.End).Gap(8).Children(func() {
			if stagePanelButton(c, "cancel", T("Cancel"), false).Clicked() {
				a.closeStagePanel(c)
			}
			if stagePanelButton(c, "save", T("Save and Apply"), true).Clicked() {
				a.saveStagePanel(c, tr)
			}
		})
	})
	if dialog.Dismissed() {
		if d.discard {
			d.discard, d.open = false, true
		} else if d.pinning {
			d.pinning, d.open = false, true
		} else {
			a.askStagePanelClose(c)
		}
	}
}

var stagePanelPalette = palette{
	bg: ui.Hex("#212227"), sidebar: ui.Hex("#2a2b32"), text: ui.Hex("#f1f1f2"), muted: ui.Hex("#b9bac3"),
	hairline: ui.Hex("#494b55"), accent: ui.Hex("#e3e5ec"), onAccent: ui.Hex("#202127"),
}

func stagePanelButton(c *ui.Context, key, label string, strong bool) ui.Element {
	p := stagePanelPalette
	fill, fg := p.sidebar, p.text
	if strong {
		fill, fg = p.accent, p.onAccent
	}
	return pillButton(c.Key(key), nil, label, fill, fg).Label(label)
}

func (a *app) stageStyleCard(c *ui.Context, tr *music.Track, style stageStyleSpec, using bool, previewLines []lyrics.Line, art *artwork, accent ui.Color, width float32) {
	d := &a.stagePanel
	p := stagePanelPalette
	picked := d.draft.fixed == style.id
	switch {
	case d.pinning:
		value, ok := a.settings.StageAnimation.Songs[d.pinKey]
		picked = ok && value == style.id
	case d.draft.mode == stageRandom:
		picked = slices.Contains(d.draft.pool, style.id)
	case d.draft.mode == stageTempo:
		picked = d.draft.speed[d.band] == style.id
	}
	border := p.hairline
	if picked {
		border = p.accent
	}
	button := ui.ButtonBase(c.Key(style.id)).Row().Height(62).Grow(1).Basis(0).MinWidth(0).Padding(5).Gap(9).Radius(8).
		Border(1, border).Background(p.sidebar).AlignItems(ui.Center).Label(T(style.label)).Selected(picked)
	if button.Hovered() || button.Focused() {
		button.Background(ui.Hex("#373942"))
	}
	button.Children(func() {
		previewW, previewH := min(134, max(60, width*.31)), float32(50)
		color := ui.Hex("#151b2b").Mix(accent, .12)
		ui.Box(c.Key("thumbnail")).Size(previewW, previewH).Shrink(0).Clip().Radius(5).Background(color).PassThrough().Children(func() {
			a.renderStageAnimation(c, stageFrame{style: style.id, lines: previewLines, current: 1, pos: 4200 * time.Millisecond,
				w: previewW, h: previewH, motion: 1, accent: accent, preview: true, art: art,
				rushBackground: d.draft.rushBackground, rushRhythm: d.draft.rushRhythm})
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).PassThrough().Children(func() {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Text(c, T(style.label)).FontSize(13).FontWeight(700).TextColor(p.text).MinWidth(0).SingleLine()
				if using {
					ui.Text(c, T("Now Using")).FontSize(10).TextColor(ui.Hex("#c1dfca")).SingleLine()
				}
			})
			ui.Text(c, T(style.detail)).FontSize(11).TextColor(p.muted).SingleLine()
		})
	})
	if !button.Clicked() {
		return
	}
	switch {
	case d.pinning:
		a.pinStageStyle(c, tr, style.id, true)
	case d.draft.mode == stageRandom:
		// 草稿与设置共享只读切片，编辑时复制，取消才能真正撤回。
		if picked {
			d.draft.pool = slices.DeleteFunc(slices.Clone(d.draft.pool), func(id string) bool { return id == style.id })
		} else {
			d.draft.pool = append(slices.Clone(d.draft.pool), style.id)
		}
	case d.draft.mode == stageTempo:
		d.draft.speed[d.band] = style.id
	default:
		d.draft.fixed = style.id
	}
}

func (a *app) stagePanelOptions(c *ui.Context) {
	d := &a.stagePanel
	p := stagePanelPalette
	ui.Row(c.Key("motion-options")).Wrap().Gap(5).AlignItems(ui.Center).Children(func() {
		ui.Text(c, T("Motion Intensity")).FontSize(12).TextColor(p.muted)
		for _, intensity := range stageIntensities {
			selected := intensity.id == d.draft.intensity || d.draft.intensity == "" && intensity.id == "standard"
			if stagePanelButton(c, intensity.id, T(intensity.label), selected).Selected(selected).Clicked() {
				d.draft.intensity = intensity.id
			}
		}
		ui.Box(c).Grow(1)
		if stagePanelButton(c, "sparks", T("Sparks"), d.draft.sparks).Selected(d.draft.sparks).Clicked() {
			d.draft.sparks = !d.draft.sparks
		}
		if stagePanelButton(c, "options", T("More Animation Options…"), d.options).Clicked() {
			d.options = !d.options
		}
	})
	if d.options {
		ui.Row(c.Key("extra-options")).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			if stagePanelButton(c, "spectrum", T("Spectrum"), d.draft.spectrum).Selected(d.draft.spectrum).Clicked() {
				d.draft.spectrum = !d.draft.spectrum
			}
			stagePanelSelect(c, "rush-background", T("Rush Background"), &d.draft.rushBackground,
				[]string{"", "solid", "flow", "rhythm"}, []string{"Cover", "Solid Color", "Flow", "Audio Reactive"})
			if d.draft.rushBackground == "rhythm" {
				stagePanelSelect(c, "rush-rhythm", T("Rush Rhythm"), &d.draft.rushRhythm,
					[]string{"wave", "gradient", "stripes"}, []string{"Wave", "Gradient", "Stripes"})
			}
		})
	}
}

func stagePanelSelect(c *ui.Context, key, label string, value *string, ids, labels []string) {
	ui.Text(c, label).FontSize(12).TextColor(stagePanelPalette.muted)
	if i, changed := choice(c, key, label, labels, slices.Index(ids, *value)); changed {
		*value = ids[i]
	}
}
