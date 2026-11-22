package app

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/music"
	"mygo-music/internal/sharecard"
)

var cardTemplates = []struct{ id, label string }{
	{"", N("Minimal")}, {"cinema", N("Cinematic")}, {"vinyl", N("Vinyl")}, {"polaroid", N("Polaroid")}, {"ticket", N("Ticket")},
}

func (a *app) shareDialog(c *ui.Context) {
	draft := a.sharing
	if draft == nil || !draft.open {
		return
	}
	if draft.backgroundTask != nil && !draft.onlinePublishing {
		a.openBackgroundShare(draft)
		draft = a.sharing
	}
	width, height := c.Size()
	palette := a.pal
	// 面板可见性与任务生命周期分离，Escape 或遮罩只关闭编辑页。
	open := draft.open
	ui.DialogBase(c.Key("share-card"), &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.5))
		panel.Width(min(1040, width-48)).Padding(20, 24).Radius(20).Gap(14).Background(palette.bg).Border(1, palette.hairline)
		ui.Row(c).AlignItems(ui.Center).Gap(16).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, T("Share Card")).FontSize(20).FontWeight(700)
				trackLabel := draft.track.Name
				if artists := draft.track.ArtistNames(); artists != "" {
					trackLabel += " · " + artists
				}
				ui.Text(c, trackLabel).FontSize(13).TextColor(palette.muted).SingleLine()
			})
			if iconButton(c.Key("close-share"), iconX, 20, palette.muted, T("Close Share Card")).Clicked() {
				draft.open = false
			}
		})
		a.shareControls(c, draft)
		// 两排设置占用固定高度，试听和结果状态需要另外预留空间。
		linesHeight := height - 296
		if draft.aggregateView != nil && len(uniqueAggregateSources(draft.track.AggregateSources)) > 1 {
			linesHeight -= 42
		}
		if draft.status != "" {
			linesHeight -= 20
		}
		if draft.onlineMode == "clip" {
			if start, end, ok := draft.clipRange(); ok {
				ui.Column(c).Disabled(draft.onlinePublishing).Children(func() {
					a.shareClipControls(c, draft, start, end)
				})
				linesHeight -= 96
			}
		}
		ui.Row(c).Height(max(128, min(470, linesHeight))).Gap(24).AlignItems(ui.Stretch).Children(func() {
			ui.Scroll(c.Key("share-lines")).Grow(1).Basis(0).MinWidth(0).Children(func() {
				ui.Column(c).Gap(6).Children(func() {
					switch {
					case draft.loading:
						ui.Text(c, T("Loading lyrics…")).TextColor(palette.muted)
					case draft.loadError != nil:
						ui.Text(c, T("Could not load lyrics")).TextColor(palette.muted)
						if ui.Button(c, T("Retry Lyrics")).Clicked() {
							a.loadShareLyrics(draft)
						}
					case len(draft.lines) == 0:
						ui.Text(c, T("Share the song without lyrics")).TextColor(palette.muted)
					default:
						for index, line := range a.shareLines(draft) {
							row := ui.ButtonBase(c.Key(index)).Column().AlignItems(ui.Start).Padding(12, 14).Gap(6).Radius(10).
								Disabled(draft.onlinePublishing || draft.exporting).Label(Tf("Select lyric %d", index+1))
							if draft.selected[index] {
								row.Background(palette.accent.Alpha(0.12)).Border(1, palette.accent.Alpha(0.35))
							}
							row.Children(func() {
								ui.Text(c, line.Text).FontSize(16).FontWeight(500).TextColor(palette.text)
								if line.Translation != "" {
									ui.Text(c, line.Translation).FontSize(13).TextColor(palette.muted)
								}
							})
							if row.Clicked() {
								a.stopSharePreview(draft)
								draft.clipStart, draft.clipEnd, draft.clipHead = 0, 0, 0
								if c.Modifiers()&ui.Shift != 0 && draft.hasSelectionAnchor {
									for selected := min(draft.selectionAnchor, index); selected <= max(draft.selectionAnchor, index); selected++ {
										draft.selected[selected] = true
									}
								} else {
									draft.selected[index] = !draft.selected[index]
									draft.selectionAnchor, draft.hasSelectionAnchor = index, true
								}
								draft.onlineRevision++
								a.requestShareRender()
							}
						}
					}
				})
			})
			ui.Column(c.Key("preview")).Grow(1).Basis(0).MinWidth(0).Center().Gap(10).Background(palette.hover).Radius(12).Padding(16).Children(func() {
				switch {
				case draft.preview != nil:
					ui.Image(c, draft.preview).Grow(1).FillWidth().Fit(ui.Contain).Label(T("Card Preview"))
				case errors.Is(draft.renderError, sharecard.ErrCapacity):
					ui.Text(c, T("Too much text. Select fewer lines or change the template or size.")).FontSize(14).TextColor(palette.muted)
				case draft.renderError != nil:
					ui.Text(c, T("Could not create the card")).TextColor(palette.muted)
					if ui.Button(c, T("Retry Preview")).Clicked() {
						a.requestShareRender()
					}
				default:
					ui.Text(c, T("Preparing preview…")).TextColor(palette.muted)
				}
				if draft.coverError != nil {
					ui.Text(c, T("Cover unavailable; using a plain background")).FontSize(11).TextColor(palette.muted)
					if ui.Button(c, T("Retry Cover")).Clicked() {
						a.loadShareCover(draft)
						a.requestShareRender()
					}
				}
				if !draft.cardOnline && sharecard.PageURL(draft.track.URL) == "" {
					ui.Text(c, T("No song link; QR code omitted")).FontSize(11).TextColor(palette.muted)
				}
				if draft.cardOnline && a.onlineLinkMatchesDraft(draft) {
					ui.Text(c, T("QR code uses the online listening link")).FontSize(11).TextColor(palette.muted)
				} else if draft.cardOnline && draft.onlineURL != "" {
					ui.Text(c, T("Online link is for a previous snapshot")).FontSize(11).TextColor(palette.muted)
				}
			})
		})
		ui.Column(c).Gap(6).Children(func() {
			ui.Box(c).FillWidth().Height(1).Background(palette.hairline)
			if draft.status != "" {
				ui.Text(c, draft.status).FontSize(12).TextColor(palette.muted).SingleLine().Tooltip(draft.status)
			}
			ui.Row(c).Wrap().Justify(ui.SpaceBetween).Gap(8).Children(func() {
				ui.Row(c.Key("share-exports")).Gap(4).Children(func() {
					imageDisabled := len(draft.png) == 0 || draft.exporting || draft.onlinePublishing
					if a.shareAction(c.Key("copy-lyrics"), T("Copy Lyrics"), nil, len(a.shareFragment(draft)) == 0, false).Clicked() {
						a.copyShareLyrics(draft)
					}
					if a.shareAction(c.Key("copy-image"), T("Copy Image"), nil, imageDisabled, false).Clicked() {
						a.copyShareImage(draft)
					}
					if a.shareAction(c.Key("save-png"), T("Save PNG"), nil, imageDisabled, false).Clicked() {
						a.saveShareImage(draft)
					}
				})
				ui.Row(c.Key("share-link-actions")).Gap(6).Children(func() {
					linkDisabled := draft.onlinePublishing || draft.loading || draft.coverLoading || draft.exporting
					if a.shareAction(c.Key("copy-online-link"), T("Copy Listen Link"), iconCopy, linkDisabled, false).Clicked() {
						a.copyOnlineShareLink(draft)
					}
					if a.shareAction(c.Key("create-online-link"), T("Create Listen Link"), iconLink, linkDisabled || draft.rendering, true).Clicked() {
						a.publishOnlineShare(draft)
					}
				})
			})
		})
	})
	if !open {
		draft.open = false
	}
	if !draft.open {
		a.stopSharePreview(draft)
		if a.sharing == draft && draft.backgroundTask == nil {
			a.sharing = nil
		}
	}
}

func (a *app) shareClipControls(c *ui.Context, draft *shareDraft, start, end time.Duration) {
	if draft.clipPreviewActive {
		if draft.clipPreview.Playing() {
			draft.clipHead = draft.clipPreview.Position()
			c.After(50 * time.Millisecond)
		} else {
			draft.clipPreviewActive, draft.clipHead = false, end
		}
	}
	duration := draft.track.Length()
	low, high := start.Seconds(), end.Seconds()
	head := min(max(draft.clipHead, start), end).Seconds()
	ui.Column(c.Key("clip-timeline")).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, fmt.Sprintf("%s %s – %s", T("Clip Range:"), shareClipTime(start), shareClipTime(end))).FontSize(12).TextColor(a.pal.muted)
			ui.Spacer(c)
			if draft.clipPreviewLoading {
				a.shareAction(c.Key("stop-clip-preview"), T("Stop Preview"), nil, draft.onlinePublishing, false).OnClick(func() {
					a.stopSharePreview(draft)
				})
			} else if draft.clipPreview != nil {
				toggleLabel := T("Play Preview")
				if draft.clipPreviewActive && draft.clipPreview.Playing() {
					toggleLabel = T("Pause Preview")
				}
				toggleIcon := iconPlay
				if draft.clipPreviewActive && draft.clipPreview.Playing() {
					toggleIcon = iconPause
				}
				a.shareAction(c.Key("toggle-clip-preview"), toggleLabel, toggleIcon, draft.onlinePublishing, false).OnClick(func() {
					if draft.clipPreview.Playing() {
						draft.clipPreview.Pause()
						draft.clipPreviewActive = false
						draft.clipHead = draft.clipPreview.Position()
						return
					}
					a.playSharePreview(draft)
				})
				a.shareAction(c.Key("stop-clip-preview"), T("Stop Preview"), nil, draft.onlinePublishing, false).OnClick(func() {
					draft.clipPreview.Pause()
					draft.clipPreviewActive = false
					draft.clipHead = start
					draft.clipPreview.Seek(start)
				})
			} else {
				a.shareAction(c.Key("preview-clip"), T("Preview Clip"), iconPlay, draft.onlinePublishing, false).OnClick(func() { a.previewShareClip(draft) })
			}
		})
		ui.RangeSlider(c.Key("clip-range"), &low, &high, 0, duration.Seconds(), 0).
			FillWidth().Label(T("Clip Range:")).OnChange(func() {
			newStart, newEnd := clipSliderTime(low), clipSliderTime(high)
			// 两端相遇时仍给被拖动的一端留出可试听的区间。
			minLength := min(100*time.Millisecond, end-start)
			if newStart != start {
				newStart = min(newStart, newEnd-minLength)
			} else {
				newEnd = max(newEnd, newStart+minLength)
			}
			if newStart == start && newEnd == end {
				return
			}
			draft.clipStart, draft.clipEnd = newStart, newEnd
			if draft.clipHead < newStart || draft.clipHead >= newEnd {
				draft.clipHead = newStart
			}
			if draft.clipPreview != nil {
				draft.clipPreview.SetRange(newStart, newEnd, draft.clipHead)
			}
			draft.onlineRevision++
			if draft.cardOnline {
				a.requestShareRender()
			}
		})
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, fmt.Sprintf("%s %s / %s", T("Preview Playhead"), shareClipTime(secs(head)), shareClipTime(end))).
				FontSize(12).TextColor(a.pal.muted)
			ui.Slider(c.Key("clip-playhead"), &head, start.Seconds(), end.Seconds()).Grow(1).Label(T("Preview Playhead")).
				OnChange(func() {
					draft.clipHead = clipSliderTime(head)
					if draft.clipPreview != nil {
						draft.clipPreview.Seek(draft.clipHead)
					}
				})
		})
	})
}

func clipSliderTime(seconds float64) time.Duration {
	return time.Duration(math.Round(seconds*1000)) * time.Millisecond
}

func shareClipTime(at time.Duration) string {
	seconds := int(at / time.Second)
	return fmt.Sprintf("%d:%02d.%03d", seconds/60, seconds%60, at.Milliseconds()%1000)
}

func (a *app) shareAction(c *ui.Context, label string, icon *ui.SVG, disabled, primary bool) ui.Element {
	palette := a.pal
	button := ui.ButtonBase(c).Label(label).Height(34).PaddingX(12).Gap(7).Radius(8).
		Disabled(disabled).FocusRingColor(palette.accent)
	base, hover, pressed, text := ui.Transparent, palette.hover, palette.press, palette.text
	if primary {
		// 稍压暗主题强调色，让两种主题下的主操作白字都清晰可读。
		base, text = palette.accent.MixOklab(ui.RGB(0, 0, 0), 0.14), palette.onAccent
		hover, pressed = base.MixOklab(palette.bg, 0.08), base.MixOklab(ui.RGB(0, 0, 0), 0.15)
	}
	shareButtonFace(button, base, hover, pressed)
	button.TextColor(text).Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(15, 15)
		}
		ui.Text(c, label).FontSize(13).FontWeight(600).SingleLine()
	})
	return button
}

func shareButtonFace(button ui.Element, base, hover, pressed ui.Color) {
	fill := base
	if !button.IsDisabled() {
		if button.Pressed() {
			fill = pressed
		} else if button.Hovered() {
			fill = hover
		}
	}
	button.Background(button.AnimateColor("fill", fill, ui.Animation{Duration: 120 * time.Millisecond}))
}

func (a *app) shareField(c *ui.Context, title, value string, disabled bool, checked *bool) ui.Element {
	palette := a.pal
	button := ui.ButtonBase(c).Grow(1).Basis(0).MinWidth(0).Height(46).PaddingX(10).Gap(6).
		Radius(8).Label(title).Description(value).Disabled(disabled).FocusRingColor(palette.accent)
	shareButtonFace(button, ui.Transparent, palette.hover, palette.press)
	button.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, title).FontSize(11).TextColor(palette.muted).SingleLine()
			ui.Text(c, value).FontSize(13).FontWeight(600).TextColor(palette.text).SingleLine()
		})
		if checked == nil {
			ui.Icon(c, iconDown).Size(14, 14).Shrink(0).TextColor(palette.muted)
			return
		}
		on := *checked
		fill, align := palette.press, ui.Start
		if on {
			fill, align = palette.accent, ui.End
		}
		ui.Row(c).Size(24, 14).Shrink(0).Padding(2).Justify(align).AlignItems(ui.Center).Radius(7).Background(fill).
			Children(func() { ui.Box(c).Size(10, 10).Radius(5).Background(palette.onAccent) })
	})
	if checked != nil {
		button.Role(ui.RoleSwitch).Checked(*checked)
	}
	return button
}

func (a *app) shareMenu(c *ui.Context, title, value string, disabled bool, build func(*ui.Menu)) ui.Element {
	return a.shareField(c, title, value, disabled, nil).PopoverMenuButton(build, ui.PopoverMenuButtonOptions{
		Panel: func(panel ui.Element) { panel.Background(a.pal.bg).Border(1, a.pal.hairline).Radius(10) },
	})
}

func (a *app) shareControls(c *ui.Context, draft *shareDraft) {
	presentation := a.sharePresentation(draft)
	disabled := draft.onlinePublishing || draft.exporting
	ui.Column(c).Padding(4).Gap(3).Radius(12).Background(a.pal.hover).Children(func() {
		if draft.aggregateView != nil && len(uniqueAggregateSources(draft.track.AggregateSources)) > 1 {
			ui.Row(c).Padding(3, 10).Gap(12).AlignItems(ui.Center).Disabled(disabled).Children(func() {
				ui.Text(c, T("Recording Source")).FontSize(11).TextColor(a.pal.muted)
				a.shareRecordingSource(c, draft)
			})
		}
		ui.Row(c).Gap(4).Children(func() {
			a.shareAudioControls(c, draft, disabled)
			a.shareLinkControls(c, draft, disabled)
		})
		ui.Box(c).Height(1).MarginX(8).Background(a.pal.hairline)
		ui.Row(c).Gap(4).Children(func() { a.shareCardControls(c, draft, presentation, disabled) })
	})
}

func (a *app) shareRecordingSource(c *ui.Context, draft *shareDraft) {
	source := aggregateViewSource(c, draft.aggregateView, &draft.track)
	if queued, err := aggregateQueueTracks([]music.Track{draft.track}, draft.aggregateView.sourceChoices); err != nil {
		draft.status = err.Error()
	} else if queued[0].ID != draft.track.ID {
		if draft.pendingAggregateTrackID != queued[0].ID {
			draft.pendingAggregateTrackID = queued[0].ID
			selectedTrack, selectedSource := queued[0], source
			a.post(func() {
				if a.sharing != draft || !draft.open {
					return
				}
				draft.track = selectedTrack
				a.stopSharePreview(draft)
				draft.clipStart, draft.clipEnd, draft.clipHead = 0, 0, 0
				draft.recordingSource = &selectedSource
				draft.pendingAggregateTrackID = ""
				draft.lines = nil
				draft.selected = make(map[int]bool)
				draft.hasSelectionAnchor = false
				draft.timingReliable = false
				draft.display = lyricDisplayCache{}
				draft.onlineRevision++
				a.loadShareLyrics(draft)
				a.requestShareRender()
			})
		}
	} else {
		draft.recordingSource = &source
	}
}

func (a *app) shareAudioControls(c *ui.Context, draft *shareDraft, disabled bool) {
	_, _, clipAvailable := shareClipRange(draft.lines, draft.selected, draft.track.Length(), draft.timingReliable)
	modeLabel := T("Full Song")
	if draft.onlineMode == "clip" {
		modeLabel = T("Selected Clip")
	}
	mode := a.shareMenu(c.Key("online-mode"), T("Listen Mode"), modeLabel, disabled, func(menu *ui.Menu) {
		if menu.Item(T("Full Song")).Radio(draft.onlineMode == "full").Chosen() {
			a.stopSharePreview(draft)
			draft.onlineMode = "full"
			draft.onlineError = nil
			draft.onlineRevision++
			a.requestShareRender()
		}
		if menu.Item(T("Selected Clip")).Radio(draft.onlineMode == "clip").
			Disabled(!clipAvailable || draft.onlinePublishing).Chosen() {
			draft.onlineMode = "clip"
			draft.onlineError = nil
			draft.onlineRevision++
			a.requestShareRender()
		}
	})
	if !clipAvailable {
		mode.Tooltip(T("Clip mode requires selected lyrics with reliable timing"))
	}
	qualityLabel := qualityLevels[1].label
	for _, quality := range qualityLevels {
		if quality.level == draft.onlineQuality {
			qualityLabel = quality.label
		}
	}
	a.shareMenu(c.Key("online-quality"), T("Audio Quality"), T(qualityLabel), disabled, func(menu *ui.Menu) {
		for _, quality := range qualityLevels {
			if menu.Item(T(quality.label)).Radio(draft.onlineQuality == quality.level).Chosen() {
				a.stopSharePreview(draft)
				draft.onlineQuality = quality.level
				draft.onlineRevision++
				a.requestShareRender()
			}
		}
	})
}

func (a *app) shareCardControls(c *ui.Context, draft *shareDraft, presentation *sharePresentation, disabled bool) {
	label := cardTemplates[0].label
	for _, template := range cardTemplates {
		if template.id == presentation.template {
			label = template.label
		}
	}
	a.shareMenu(c.Key("card-template"), T("Card Template"), T(label), disabled, func(menu *ui.Menu) {
		for _, template := range cardTemplates {
			if menu.Item(T(template.label)).Radio(presentation.template == template.id).Chosen() {
				presentation.template = template.id
				a.settings.CardTemplate = template.id
				draft.onlineRevision++
				a.saveLyricChoices()
			}
		}
	})
	size := T("Portrait (3:4)")
	if presentation.square {
		size = T("Square (1:1)")
	}
	a.shareMenu(c.Key("card-size"), T("Card Size"), size, disabled, func(menu *ui.Menu) {
		if menu.Item(T("Portrait (3:4)")).Radio(!presentation.square).Chosen() {
			presentation.square = false
			a.settings.CardSquare = false
			draft.onlineRevision++
			a.saveLyricChoices()
		}
		if menu.Item(T("Square (1:1)")).Radio(presentation.square).Chosen() {
			presentation.square = true
			a.settings.CardSquare = true
			draft.onlineRevision++
			a.saveLyricChoices()
		}
	})
	// 开关也控制未选中的歌词列表，因此按整首歌判断是否有译文。
	available := hasTranslation(draft.lines)
	toggleTranslation := func() {
		presentation.hideTranslation = !presentation.hideTranslation
		a.settings.HideCardTranslation = presentation.hideTranslation
		draft.onlineRevision++
		a.saveLyricChoices()
	}
	showTranslation := available && !presentation.hideTranslation
	translationLabel := T("Off")
	if showTranslation {
		translationLabel = T("On")
	}
	if !available {
		translationLabel = T("No translation available")
	}
	translation := a.shareField(c.Key("card-translation"), T("Translation"), translationLabel, !available || disabled, &showTranslation).
		Label(T("Show Translation"))
	if !available {
		translation.Tooltip(T("No translation available"))
	}
	if translation.Clicked() {
		toggleTranslation()
	}
	scriptLabel := T("Follow Interface")
	for _, option := range lyricScripts {
		if a.settings.LyricsScript == option.id {
			scriptLabel = T(option.label)
		}
	}
	a.shareMenu(c.Key("card-language"), T("Lyrics Display"), scriptLabel, disabled, func(menu *ui.Menu) {
		if menu.Item(T("Show Translation")).Checked(!presentation.hideTranslation).Disabled(!available).Chosen() {
			toggleTranslation()
		}
		menu.Separator()
		script := a.settings.LyricsScript
		original := a.settings.ChineseLyrics[a.lyricKey(&draft.track)]
		a.lyricScriptItems(menu, &draft.track)
		if script != a.settings.LyricsScript || original != a.settings.ChineseLyrics[a.lyricKey(&draft.track)] {
			presentation.lyrics = a.lyricOptions(&draft.track, presentation.hideTranslation)
			draft.onlineRevision++
			a.requestShareRender()
		}
	})
}

func (a *app) shareLinkControls(c *ui.Context, draft *shareDraft, disabled bool) {
	label := "Original Song Page"
	hint := "QR code links to the original song page"
	if draft.cardOnline {
		label = "Online Listening Link"
		hint = "Copying or saving reuses an active share or creates a listening link"
		if a.onlineLinkMatchesDraft(draft) {
			hint = "QR code uses the online listening link"
		}
	}
	a.shareMenu(c.Key("card-link-target"), T("Card Link"), T(label), disabled, func(menu *ui.Menu) {
		if menu.Item(T("Online Listening Link")).Radio(draft.cardOnline).Chosen() && !draft.cardOnline {
			draft.cardOnline = true
			a.requestShareRender()
		}
		if menu.Item(T("Original Song Page")).Radio(!draft.cardOnline).Chosen() && draft.cardOnline {
			draft.cardOnline = false
			a.requestShareRender()
		}
	}).Tooltip(T(hint))
	viewLabel := "Lyrics"
	if draft.onlineDefaultView == "stage" {
		viewLabel = "Stage"
	}
	a.shareMenu(c.Key("share-default-view"), T("Default View"), T(viewLabel), disabled, func(menu *ui.Menu) {
		for _, option := range []struct{ value, label string }{{"", "Lyrics"}, {"stage", "Stage"}} {
			if menu.Item(T(option.label)).Radio(draft.onlineDefaultView == option.value).Chosen() && draft.onlineDefaultView != option.value {
				draft.onlineDefaultView = option.value
				// 打开视图属于分享快照，切换后不能复用旧链接的二维码。
				draft.onlineRevision++
				a.requestShareRender()
			}
		}
	})
}
