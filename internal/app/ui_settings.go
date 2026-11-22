package app

import (
	"log"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
	"mygo-music/internal/local"
	"mygo-music/internal/music"
	"mygo-music/internal/plugin"
)

// openSettings shows the settings window, opening it the first time.
func (a *app) openSettings() {
	if a.win == nil {
		return // no app to show a window in, as in tests
	}
	if a.settingsWin != nil {
		a.settingsWin.Show()
		a.settingsWin.Focus()
		a.measureCache()
		return
	}
	a.measureCache()
	a.settingsWin = mygo.NewWindow(mygo.WindowOptions{
		Title:     T("Settings"),
		Width:     780,
		Height:    640,
		MinWidth:  640,
		MinHeight: 420,
		// Its sidebar reaches up under the window buttons, as the main window's does.
		TitleBarStyle:        mygo.TitleBarHidden,
		TitleBarHeight:       buttonsHeight,
		TrafficLightPosition: &mygo.Point{X: 20, Y: 20},
		// A new key: the size kept for the old, narrower window doesn't fit this.
		StateKey:        "settings-pages",
		DisableMaximize: true,
		DisableMinimize: true,
		// The app's menu has nothing for this window: on Linux and
		// Windows it stays out of sight until Alt shows it.
		AutoHideMenuBar: true,
		BackgroundColor: "light-dark(#f4f4f6, #1c1c1f)",
		Content:         ui.View(a.settingsView),
	})
	a.settingsWin.OnClosed(func() {
		a.endShortcutRecording()
		a.shortcutEdit.confirm = false
		a.settingsWin = nil
		// Its sign-in dialog goes with it; a sign-in on its way still counts.
		if a.signIn.here {
			a.signIn.open = false
		}
		a.syncDock()
	})
	a.settingsWin.OnBlur(a.endShortcutRecording)
	a.syncDock()
}

// openSourceSettings shows the page of the settings where plugins are set up.
func (a *app) openSourceSettings() {
	a.settingsPage, a.settingsQuery = "plugins", ""
	a.openSettings()
}

// changed applies the settings everywhere and keeps them.
func (a *app) changed() {
	a.settings.save()
	a.settings.applyAppearance()
	if a.notch != nil {
		a.notch.setEnabled(a.settings.Notch)
	}
	a.redraw()
}

// systemLanguages are the languages the system prefers; tests replace it.
var systemLanguages = func() []string { return mygo.App.PreferredLanguages() }

// 自启由系统持有状态，避免本地设置覆盖用户在系统设置里的选择。
var (
	openAtLogin = func() bool {
		// 无头视图没有原生事件循环，应用就绪后才能同步读取系统状态。
		return mygo.App.IsReady() && mygo.App.OpenAtLogin()
	}
	setOpenAtLogin = mygo.App.SetOpenAtLogin
)

// setLanguage speaks another language from now on: the views say it as they
// redraw; the menus, and what the sources wrote out in it, are made anew.
func (a *app) setLanguage(code string) {
	a.settings.Language = code
	i18n.SetLanguage(i18n.Resolve(code, systemLanguages()))
	a.changed()
	if a.win != nil {
		a.setMenu()
		a.refreshTray()
	}
	if a.settingsWin != nil {
		a.settingsWin.SetTitle(T("Settings"))
	}
	if lib := a.libOf(local.ID); lib != nil {
		a.libraryChanged(lib)
	}
	// A plugin's pages are in the language it was asked in.
	if lib := a.library; pluginOf(lib) != nil {
		a.libraryChanged(lib)
	}
}

// choice is a select of labels, to translate, reporting the index of the one
// chosen once it changes.
func choice(c *ui.Context, key, label string, labels []string, at int) (int, bool) {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = T(l)
	}
	chosen := names[max(at, 0)]
	if ui.Select(c.Key(key), &chosen, names).Label(label).Width(170).Changed() {
		for i, n := range names {
			if n == chosen {
				return i, true
			}
		}
	}
	return at, false
}

// settingsRow is a setting of the settings window: what it is, what it means,
// and its control. It shows if a search finds it.
type settingsRow func(label, desc string, control func())

// settingsBlock is anything else in the settings, which a search finds by text.
type settingsBlock func(text string, draw func())

// settingsSection is a page of the settings. Its build only says what it has
// through row and block, for a search to go through them without drawing.
type settingsSection struct {
	id, title string
	icon      *ui.SVG
	group     int
	build     func(row settingsRow, block settingsBlock)
}

// settingsMatch reports whether a search, lower case, finds any of the texts.
func settingsMatch(query string, texts ...string) bool {
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), query) {
			return true
		}
	}
	return false
}

func (a *app) settingsView(c *ui.Context) {
	t := *c.Theme()
	p := paletteFor(&t)
	useAccent(&t, p)
	c.SetTheme(&t)
	c.Root().Background(p.bg)
	if a.shortcutEdit.recording != "" && (!c.WindowActive() || a.shortcutEdit.hadFocus && !a.shortcutEdit.handle.Focused(c)) {
		a.endShortcutRecording()
	}
	if a.shortcutEdit.recording == "" {
		c.OnShortcut(ui.Cmd, ui.KeyW, func() {
			if a.settingsWin != nil {
				a.settingsWin.Close()
			}
		})
		c.OnShortcut(ui.Cmd, ui.KeyF, func() { a.settingsSearchBox.Focus(c) })
	}

	sections := a.settingsSections(c, p)
	current := sections[0]
	for _, sec := range sections {
		if sec.id == a.settingsPage {
			current = sec
		}
	}
	query := strings.ToLower(strings.TrimSpace(a.settingsQuery))

	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		// Under the window buttons where they sit over the sidebar. Where nothing
		// takes the press, it moves the window, as the title bar it reaches into would.
		top := float32(14)
		if c.TitleBar().Left > 0 {
			top = topBar(c) + 4
		}
		ui.Column(c).Width(settingsSidebarWidth).Shrink(0).Padding(top, 12, 14, 12).Gap(2).Background(p.sidebar).DragWindow().Children(func() {
			a.settingsSearch(c, p)
			ui.Scroll(c.Key("settings-navigation")).Grow(1).MinHeight(0).Gap(2).Children(func() {
				groups := []string{T("Listening Experience"), T("Sources and Sharing"), T("Application")}
				previous := -1
				for _, sec := range sections {
					if sec.group != previous {
						spacing := float32(14)
						if previous < 0 {
							spacing = 0
						}
						ui.Text(c, groups[sec.group]).FontSize(11).FontWeight(600).TextColor(p.muted).Margin(spacing, 9, 6, 9)
						previous = sec.group
					}
					a.settingsNavItem(c, p, sec, query == "" && sec.id == current.id)
				}
			})
		})
		ui.Box(c).Width(1).Shrink(0).Background(p.hairline)
		// The page's title is in the header, the strip the window buttons leave. What
		// scrolls goes under a hard edge there, showing only once there's something under it.
		pad := max(24, topBar(c)+6)
		ui.Box(c).Grow(1).MinWidth(0).Children(func() {
			ui.Scroll(c.Key(current.id+"\x00"+query)).Fill().Padding(pad, 28, 32, 28).Gap(8).Children(func() {
				mark := ui.Box(c.Key("start-mark")).Height(0).Bounds()
				if mark.W > 0 {
					a.settingsUnderTop = mark.Y < pad-0.5
				}
				if query == "" {
					a.settingsGroup(c, p, current, "", "")
					return
				}
				found := false
				for _, sec := range sections {
					if a.settingsFinds(sec, query) {
						found = true
						a.settingsGroup(c, p, sec, sec.title, query)
					}
				}
				if !found {
					ui.Text(c, T("No settings found")).FontSize(13.5).TextColor(p.muted).Margin(8, 4, 0, 4)
				}
			})
			edge := ui.Box(c.Key("top-edge")).Absolute().Top(0).Left(0).Right(0).Height(topBar(c)).DragWindow().
				Material(glass.ScrollEdge{Hard: true, Background: p.bg})
			title := current.title
			if query != "" {
				title = T("Search Results")
			}
			// The + keeps clear of the window buttons where they sit at the
			// right (Linux, Windows).
			right := max(32, c.TitleBar().Right+12)
			header := ui.Row(c.Key("header")).Absolute().Top(0).Left(0).Right(0).Height(topBar(c)).Padding(0, right, 0, 32).Gap(8).AlignItems(ui.Center).DragWindow()
			header.Children(func() {
				glyph := current.icon
				if query != "" {
					glyph = iconSearch
				}
				ui.Icon(c, glyph).Size(16, 16).TextColor(p.accent).IgnoreInput(true)
				ui.Text(c, title).FontSize(15).FontWeight(700).SingleLine().Shrink(1).MinWidth(0)
				if query == "" && current.id == "plugins" {
					ui.Box(c).Grow(1)
					a.installMenu(c, p)
				}
			})
			// The pointer over it brings the edge up too, as AppKit's does.
			edge.Opacity(edgeShown(edge, a.settingsUnderTop || header.Hovered()))
		})
	})
	a.installDialog(c, p)
	a.signInDialog(c, p, true)
}

// settingsSidebarWidth is the width of the list of the settings' pages.
const settingsSidebarWidth = 200

// settingsSearch is the field that finds settings across all the pages.
func (a *app) settingsSearch(c *ui.Context, p palette) {
	field := ui.Row(c).Height(30).Radius(8).AlignItems(ui.Stretch).Margin(0, 0, 10, 0)
	if c.Focused(a.settingsSearchBox) {
		field.Background(p.bg).Shadow(0, 0, 0, 3, p.accent.Alpha(0.25)).Border(1, p.accent)
	} else {
		field.Background(p.hover).Border(1, p.hairline)
	}
	field.Children(func() {
		// The input fills the field, under its icon, so a click anywhere on
		// the field focuses it.
		ui.TextInputBase(c.Key("search"), &a.settingsQuery).Bind(&a.settingsSearchBox).Grow(1).MinWidth(0).PaddingStart(29).PaddingEnd(9).
			Placeholder(T("Search")).FontSize(13).Label(T("Search Settings"))
		ui.Icon(c, iconSearch).Size(13, 13).TextColor(p.muted).Absolute().Left(9).Top(7.5).IgnoreInput(true)
	})
}

// settingsNavItem opens a page of the settings, leaving a search.
func (a *app) settingsNavItem(c *ui.Context, p palette, sec settingsSection, selected bool) {
	b := ui.ButtonBase(c.Key(sec.id)).Height(32).PaddingX(9).Gap(9).Radius(8).AlignItems(ui.Center).Justify(ui.Start).
		FocusRing(false).Label(sec.title)
	switch {
	case selected:
		b.Background(p.press)
	case b.Hovered():
		b.Background(p.hover)
	}
	color := p.muted
	if selected {
		color = p.accent
	}
	b.Children(func() {
		ui.Icon(c, sec.icon).Size(15, 15).TextColor(color)
		ui.Text(c, sec.title).FontSize(13).FontWeight(500).SingleLine().Shrink(1).MinWidth(0)
	})
	if b.Clicked() {
		a.endShortcutRecording()
		a.shortcutEdit.confirm = false
		a.settingsPage, a.settingsQuery = sec.id, ""
	}
}

// settingsFinds reports whether a search finds anything in a section: all of it,
// if it's what the section is called.
func (a *app) settingsFinds(sec settingsSection, query string) bool {
	if settingsMatch(query, sec.title) {
		return true
	}
	found := false
	sec.build(func(label, desc string, _ func()) {
		found = found || settingsMatch(query, label, desc)
	}, func(text string, _ func()) {
		found = found || settingsMatch(query, text)
	})
	return found
}

// settingsGroup draws a section's rows, those a search finds if there is one,
// under its title if it has one.
func (a *app) settingsGroup(c *ui.Context, p palette, sec settingsSection, title, query string) {
	if title != "" {
		ui.Row(c).Gap(7).AlignItems(ui.Center).Margin(14, 4, 4, 4).Children(func() {
			ui.Icon(c, sec.icon).Size(14, 14).TextColor(p.muted).IgnoreInput(true)
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(p.muted)
		})
	}
	all := query == "" || settingsMatch(query, sec.title)
	ui.Column(c.Key(sec.id)).Radius(12).Background(p.bg).Border(1, p.hairline).Dividers(1, p.hairline).Children(func() {
		sec.build(func(label, desc string, control func()) {
			if !all && !settingsMatch(query, label, desc) {
				return
			}
			ui.Row(c).Padding(12, 16).Gap(16).AlignItems(ui.Center).MinHeight(48).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, label).FontSize(13.5).FontWeight(500)
					if desc != "" {
						ui.Text(c, desc).FontSize(12).TextColor(p.muted).LineHeight(1.4)
					}
				})
				control()
			})
		}, func(text string, draw func()) {
			if all || settingsMatch(query, text) {
				draw()
			}
		})
	})
}

func settingsHeading(c *ui.Context, block settingsBlock, title string, glyph *ui.SVG) {
	block(title, func() {
		theme := *c.Theme()
		colors := paletteFor(&theme)
		ui.Row(c).Padding(10, 16).Gap(8).AlignItems(ui.Center).Background(colors.hover).Children(func() {
			ui.Icon(c, glyph).Size(16, 16).TextColor(colors.muted).IgnoreInput(true)
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(colors.muted)
		})
	})
}

// settingsSections are the pages of the settings, those that apply here.
func (a *app) settingsSections(c *ui.Context, p palette) []settingsSection {
	s := &a.settings
	var sections []settingsSection
	add := func(id, title string, icon *ui.SVG, build func(row settingsRow, block settingsBlock)) {
		group := 0
		switch id {
		case "plugins", "shares", "podcasts":
			group = 1
		case "cache", "experiments", "updates":
			group = 2
		}
		sections = append(sections, settingsSection{id: id, title: title, icon: icon, group: group, build: build})
	}

	add("general", T("General"), iconSettings, func(row settingsRow, block settingsBlock) {
		settingsHeading(c, block, T("Language and Appearance"), iconSettings)
		row(T("Language"), "", func() {
			follow := T("Follow System")
			names := []string{follow}
			chosen := follow
			for _, l := range i18n.Languages {
				names = append(names, l.Name)
				if l.Code == s.Language {
					chosen = l.Name
				}
			}
			if ui.Select(c.Key("language"), &chosen, names).Label(T("Language")).Width(170).Changed() {
				code := i18n.System
				for _, l := range i18n.Languages {
					if l.Name == chosen {
						code = l.Code
					}
				}
				a.setLanguage(code)
			}
		})
		row(T("Appearance"), "", func() {
			modes := []string{"system", "light", "dark"}
			i := 0
			for k, m := range modes {
				if m == s.Appearance {
					i = k
				}
			}
			if segmented(c.Key("appearance"), &i, T("System"), T("Light"), T("Dark")) {
				s.Appearance = modes[i]
				a.changed()
			}
		})
		settingsHeading(c, block, T("Playback Display"), iconLyrics)
		row(T("Chinese Lyrics"), T("Use the same text in lyrics, visualizers and share cards"), func() {
			labels, selected := make([]string, len(lyricScripts)), 0
			for index, option := range lyricScripts {
				labels[index] = option.label
				if s.LyricsScript == option.id {
					selected = index
				}
			}
			if index, changed := choice(c, "lyrics-script", T("Chinese Lyrics"), labels, selected); changed {
				s.LyricsScript = lyricScripts[index].id
				a.saveLyricChoices()
			}
		})
		row(T("Now Playing Style"), T("Show the song playing as its cover, or as a record or a cassette turning as it plays"), func() {
			labels := make([]string, len(playerStyles))
			at := 0
			for i, ps := range playerStyles {
				labels[i] = ps.label
				if ps.id == s.PlayerStyle {
					at = i
				}
			}
			if i, ok := choice(c, "player-style", T("Now Playing Style"), labels, at); ok {
				s.PlayerStyle = playerStyles[i].id
				a.changed()
				go retroBitmaps() // rendered before Now Playing shows them
			}
		})
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			settingsHeading(c, block, T("Desktop"), iconLaptop)
			label := T("Launch at login")
			startupEnabled := openAtLogin()
			row(label, "", func() {
				enabled := startupEnabled
				ui.Switch(c.Key("open-at-login"), &enabled).Label(label).OnClick(func() {
					err := setOpenAtLogin(enabled)
					a.startupFailed = err != nil
					if err != nil {
						log.Printf("launch at login: %v", err)
					}
					a.redraw()
				})
			})
			if a.startupFailed {
				// 搜索结果按开关名称保留提示，且不会依赖系统错误的语言。
				block(label, func() {
					message := T("Could not change launch at login. Check Settings > Apps > Startup.")
					if runtime.GOOS == "darwin" {
						message = T("Could not change launch at login. Check System Settings > General > Login Items.")
					}
					ui.Text(c.Key("startup-error"), message).Role(ui.RoleAlert).FontSize(12).TextColor(ui.RGB(184, 54, 54)).Margin(8, 16, 8, 16)
				})
			}
			row(T("Start in background at login"), T("Do not open the main window when the app starts at login"), func() {
				ui.Switch(c.Key("open-at-login-background"), &s.LaunchAtLoginBackground).
					Label(T("Start in background at login")).Disabled(!startupEnabled).OnClick(func() {
						err := s.save()
						a.startupBackgroundFailed = err != nil
						if err != nil {
							s.LaunchAtLoginBackground = !s.LaunchAtLoginBackground
							log.Printf("save background startup setting: %v", err)
						}
						a.redraw()
					})
			})
			if a.startupBackgroundFailed {
				block(T("Start in background at login"), func() {
					ui.Text(c.Key("startup-background-error"), T("Could not save background startup setting. Try again.")).
						Role(ui.RoleAlert).FontSize(12).TextColor(ui.RGB(184, 54, 54)).Margin(8, 16, 8, 16)
				})
			}
		}
		// Only on macOS: elsewhere the icon is always in the notification
		// area.
		if runtime.GOOS == "darwin" {
			row(T("App Icon"), T("Shown in the menu bar, it opens the window again after you close it"), func() {
				labels := make([]string, len(appIcons))
				at := 0
				for i, ic := range appIcons {
					labels[i] = ic.label
					if ic.id == s.AppIcon {
						at = i
					}
				}
				if i, ok := choice(c, "app-icon", T("App Icon"), labels, at); ok {
					s.AppIcon = appIcons[i].id
					a.changed()
					a.changeAppIcon()
				}
			})
		}
	})

	add("plugins", T("Plugins"), iconPlug, func(row settingsRow, block settingsBlock) { a.sourceSettings(c, row, block, p) })
	if (runtime.GOOS == "windows" || runtime.GOOS == "darwin") && a.globalShortcuts != nil {
		add("shortcuts", T("Shortcuts"), iconKeyboard, func(row settingsRow, block settingsBlock) {
			a.shortcutSettings(c, p, row, block)
		})
	}

	add("shares", T("Online Sharing"), iconLink, func(row settingsRow, block settingsBlock) {
		a.onlineShareSettings(c, p, row, block)
	})
	// Only on macOS, the one with a notch player.
	if a.notch != nil {
		add("notch", T("Notch Player"), iconLaptop, func(row settingsRow, block settingsBlock) {
			row(T("Show the Notch Player"), T("A small player at the notch on top of the screen, which opens as the pointer rests on it"), func() {
				if ui.Switch(c.Key("notch"), &s.Notch).Label(T("Show the Notch Player")).Changed() {
					a.changed()
				}
			})
			row(T("Show Activity While Playing"), T("The cover and levels beside the notch, which can cover menu bar icons there"), func() {
				if ui.Switch(c.Key("activity"), &s.NotchActivity).Label(T("Show Activity While Playing")).Disabled(!s.Notch).Changed() {
					a.changed()
				}
			})
			row(T("Keep It Showing Without a Notch"), T("When off, move the pointer to the top center of the screen to bring it up, without covering the menu bar"), func() {
				if ui.Switch(c.Key("virtual"), &s.NotchVirtual).Label(T("Keep It Showing Without a Notch")).Disabled(!s.Notch).Changed() {
					a.changed()
				}
			})
		})
	}

	add("playback", T("Playback"), iconVolume, func(row settingsRow, block settingsBlock) {
		settingsHeading(c, block, T("Sound and Quality"), iconVolume)
		row(T("Quality"), T("What's asked of plugins that have a choice, from the next song on"), func() {
			labels := make([]string, len(qualityLevels))
			at := 1
			for i, q := range qualityLevels {
				labels[i] = q.label
				if q.level == s.Quality {
					at = i
				}
			}
			if i, ok := choice(c, "quality", T("Quality"), labels, at); ok {
				s.Quality = qualityLevels[i].level
				a.changed()
			}
		})
		row(T("Even Out Loudness"), T("Adjust each song's volume by what its plugin measured of it, for them to sound as loud as each other"), func() {
			if ui.Switch(c.Key("normalize"), &s.Normalize).Label(T("Even Out Loudness")).Changed() {
				a.setPlayerVolume(a.volume)
				a.changed()
			}
		})
		a.playbackTransitionSettings(c, row, block)
		betterLyricsDescription := T("Share the current track, position and source lyrics on this computer")
		if a.betterLyricsStatus != "" {
			betterLyricsDescription = a.betterLyricsStatus
		}
		row(T("Share with BetterLyrics"), betterLyricsDescription, func() {
			if ui.Switch(c.Key("betterlyrics"), &s.BetterLyricsIntegration).Label(T("Share with BetterLyrics")).Changed() {
				a.changed()
				a.setBetterLyricsIntegration()
			}
		})
		row(T("Allow BetterLyrics to Change Lyrics"), T("Save manually selected lyrics on this computer"), func() {
			if ui.Switch(c.Key("betterlyrics-write"), &s.BetterLyricsLyricsWrite).
				Label(T("Allow BetterLyrics to Change Lyrics")).Disabled(!s.BetterLyricsIntegration).Changed() {
				a.changed()
				a.setBetterLyricsIntegration()
			}
		})
		row(T("Restore Current Song Lyrics"), T("Clear the manual choice and use the original source"), func() {
			hasOverride := a.betterLyricsOverrides != nil && a.pl != nil && a.pl.Current() != nil &&
				a.betterLyricsOverrides.Has(a.betterLyricsSongKey(*a.pl.Current()))
			if ui.Button(c.Key("betterlyrics-clear"), T("Restore Current Song Lyrics")).Disabled(!hasOverride).Clicked() {
				a.clearBetterLyricsOverride()
			}
		})
		if a.anyCan("comments") {
			settingsHeading(c, block, T("Playback Content"), iconComment)
			row(T("Top Comments in Quiet Parts"), T("In intros, breaks and outros, a song's top comments show in place of the lyrics"), func() {
				if ui.Switch(c.Key("hot-comments"), &s.HotComments).Label(T("Top Comments in Quiet Parts")).Changed() {
					a.changed()
				}
			})
		}
	})

	add("cache", T("Cache"), iconDatabase, func(row settingsRow, block settingsBlock) {
		row(T("Song Cache"), Tf("Songs from elsewhere are kept here to play again at once, without the network. %s", a.cacheUsage()), func() {
			labels := make([]string, len(cacheLimits))
			at := 2
			for i, l := range cacheLimits {
				labels[i] = l.label
				if l.mb == s.CacheMB {
					at = i
				}
			}
			if i, ok := choice(c, "cache", T("Song Cache"), labels, at); ok {
				s.CacheMB = cacheLimits[i].mb
				a.changed()
				a.applyCacheLimit()
			}
		})
		row(T("Clear the Cache"), T("Delete all the songs kept here"), func() {
			if ui.Button(c.Key("clear-cache"), T("Clear")).Disabled(a.cacheUsed <= 0).Clicked() {
				a.clearCache()
			}
		})
	})

	if a.captionsShown() && a.anyCan("shows") {
		add("podcasts", T("Podcasts"), iconPodcast, func(row settingsRow, block settingsBlock) { a.captionSettings(row, c) })
	}

	add("experiments", T("Experiments"), iconFlask, func(row settingsRow, block settingsBlock) {
		row(T("Smoke on the Progress Bar"), T("The progress bar turns into a light trailing smoke, moving with the music. Uses a little more power."), func() {
			if ui.Switch(c.Key("smoke"), &s.Smoke).Label(T("Smoke on the Progress Bar")).Changed() {
				a.changed()
			}
		})
		row(T("Fog Backdrop"), T("Now Playing's backdrop turns into fog in the cover's colors, rising with the bass. Still being polished; uses a little more power."), func() {
			if ui.Switch(c.Key("fog"), &s.Fog).Label(T("Fog Backdrop")).Changed() {
				a.changed()
			}
		})
		row(T("Grow From the Cover"), T("Now Playing opens out of the cover in the player bar and shrinks back into it, rather than sliding up from the bottom"), func() {
			if ui.Switch(c.Key("grow-from-cover"), &s.GrowFromCover).Label(T("Grow From the Cover")).Changed() {
				a.changed()
			}
		})
	})

	if a.updates {
		add("updates", T("Updates"), iconRefresh, func(row settingsRow, block settingsBlock) {
			desc := T("Check for a new version once a day")
			if t := lastUpdateCheck(); !t.IsZero() {
				desc = Tf("Check for a new version once a day. Last checked: %s", i18n.Ago(t, time.Now()))
			}
			row(T("Check for Updates Automatically"), desc, func() {
				on := updateChecks()
				if ui.Switch(c.Key("update-checks"), &on).Label(T("Check for Updates Automatically")).Changed() {
					setUpdateChecks(on)
				}
			})
			row(T("Download and Install Automatically"), T("Download new versions in the background, to take effect the next time the app opens"), func() {
				on := updateDownloads()
				if ui.Switch(c.Key("update-downloads"), &on).Label(T("Download and Install Automatically")).Disabled(!updateChecks()).Changed() {
					setUpdateDownloads(on)
				}
			})
			row(Tf("Current Version %s", mygo.App.Version()), "", func() {
				if ui.Button(c.Key("check-updates"), T("Check for Updates")).Clicked() {
					checkForUpdates()
				}
			})
		})
	}
	// 分类只重排导航，页面和控件的稳定键保持不变，搜索结果也沿用同一顺序。
	slices.SortStableFunc(sections, func(first, second settingsSection) int { return first.group - second.group })
	return sections
}

// anyCan reports whether any source does what's named.
func (a *app) anyCan(what string) bool {
	for _, lib := range a.libs {
		if lib.can(what) {
			return true
		}
	}
	return false
}

// sourceSettings lists the sources, each with what it asks of the listener,
// and where more of them come from.
func (a *app) sourceSettings(c *ui.Context, row settingsRow, block settingsBlock, p palette) {
	for _, lib := range a.allLibs {
		if lib.id == local.ID {
			a.localSettings(c, lib, row, block, p)
			continue
		}
		text := lib.name()
		for _, st := range lib.src.Info().Settings {
			text += " " + st.Label + " " + st.Help
		}
		block(text, func() { a.pluginSettings(c.Key(lib.id), lib, p) })
	}
}

// revealFolder shows a folder in the file manager, making it if need be;
// tests replace it.
var revealFolder = func(dir string) {
	os.MkdirAll(dir, 0o755)
	mygo.Shell.OpenPath(dir)
}

// localSettings are the folders of the built-in source.
func (a *app) localSettings(c *ui.Context, lib *library, row settingsRow, block settingsBlock, p palette) {
	if a.local == nil {
		return
	}
	folders := a.local.Folders()
	desc := T("Add the folders where your music lives")
	if a.local.Scanning() {
		desc = T("Looking for music…")
	} else if len(folders) > 0 {
		desc = Tf("%d songs found", a.local.TrackCount())
	}
	enabled := a.pluginEnabled(lib)
	desc = T("Built-in") + " · " + desc
	row(T("Local Files"), desc, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if enabled && len(folders) > 0 && ui.Button(c.Key("rescan"), T("Rescan")).Disabled(a.local.Scanning()).Clicked() {
				a.local.Rescan()
			}
			if enabled && ui.Button(c.Key("add-folder"), T("Add Folder…")).Clicked() {
				a.addMusicFolder()
			}
			a.pluginSwitch(c, lib)
		})
	})
	if !enabled {
		return
	}
	for _, dir := range folders {
		block(dir, func() {
			ui.Row(c.Key(dir)).Padding(8, 16).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconFolder).Size(15, 15).TextColor(p.muted)
				ui.Text(c, dir).FontSize(12.5).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if ui.Button(c.Key("remove"), T("Remove")).Clicked() {
					a.local.RemoveFolder(dir)
				}
			})
		})
	}
}

// pluginSettings is a plugin: who's signed in, and what it asks of the listener.
func (a *app) pluginSettings(c *ui.Context, lib *library, p palette) {
	info := lib.src.Info()
	enabled := a.pluginEnabled(lib)
	ui.Column(c).Padding(12, 16).Gap(10).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).MinHeight(24).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
				name := lib.name()
				if info.Version != "" {
					name += " " + info.Version
				}
				ui.Text(c, name).FontSize(13.5).FontWeight(500)
				// Where it came from, a link to its repository, and who's signed
				// in, on one line.
				from, url := pluginFrom(lib)
				signedIn := ""
				if enabled && !lib.needsSetup && lib.account != nil {
					signedIn = Tf("Signed in as %s", lib.account.Name)
				}
				if from != "" || signedIn != "" {
					ui.Row(c).MinWidth(0).Children(func() {
						if url != "" {
							ui.Link(c, from, url).FontSize(12).TextColor(p.muted).SingleLine().Shrink(0).Tooltip(T("Open on GitHub"))
						} else if from != "" {
							ui.Text(c, from).FontSize(12).TextColor(p.muted).SingleLine().Shrink(0)
						}
						if signedIn != "" {
							if from != "" {
								signedIn = " · " + signedIn
							}
							ui.Text(c, signedIn).FontSize(12).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
						}
					})
				}
				if enabled && lib.needsSetup {
					ui.Text(c, setupNotice(lib, info)).FontSize(12).TextColor(p.accent).LineHeight(1.4)
				}
				a.pluginUpdateError(c, lib, p)
			})
			a.pluginUpdateButton(c, lib)
			switch {
			case !enabled || a.aggregateHasPlatformAccounts(lib):
			case lib.account == nil && lib.accountUnknown():
				// A source not yet used hasn't said who's signed in: asked now, its
				// row offers no sign-in to someone who already is.
				a.loadAccount(lib)
			case lib.account != nil && lib.can("signOut"):
				if ui.Button(c.Key("sign-out"), T("Sign Out")).Clicked() {
					a.signOut(lib)
				}
			case lib.account == nil && lib.can("signIn"):
				if ui.PrimaryButton(c.Key("sign-in"), T("Sign In")).Clicked() {
					a.promptSignInInSettings(lib)
				}
			}
			a.pluginMenu(c, lib, p)
			a.pluginSwitch(c, lib)
		})
		if !enabled {
			return
		}
		if g, ok := lib.src.(guarded); ok {
			a.pluginPermissions(c.Key("permissions"), lib, g, p)
		}
		for _, st := range info.Settings {
			if edit := a.aggregateEdits[lib]; edit != nil && edit.sources != nil && (st.Key == "catalogPlatform" || len(edit.accountPlatforms) > 0 && st.Key == "accountPlatform") {
				continue
			}
			a.sourceSetting(c.Key(st.Key), lib, st, p)
		}
		a.aggregateSettings(c.Key("aggregate"), lib, p)
	})
}

// setupNotice says what a plugin that needs setting up wants: what it said, or
// else the settings still empty, or else only that it does. A plugin may need
// something no field holds, such as a program installed or allowed, so the
// fields alone aren't the answer.
func setupNotice(lib *library, info music.Info) string {
	if lib.setupReason != "" {
		return lib.setupReason
	}
	var empty []string
	for _, st := range info.Settings {
		if strings.TrimSpace(lib.src.Setting(st.Key)) == "" {
			empty = append(empty, st.Label)
		}
	}
	if len(empty) > 0 {
		return Tf("Fill in %s for it to work", strings.Join(empty, ", "))
	}
	return Tf("%s needs setting up first", lib.name())
}

// pluginEnabled reports whether a plugin is on.
func (a *app) pluginEnabled(lib *library) bool { return slices.Contains(a.libs, lib) }

// pluginSwitch turns a plugin on or off; the last one on stays.
func (a *app) pluginSwitch(c *ui.Context, lib *library) {
	on := a.pluginEnabled(lib)
	last := on && len(a.libs) == 1
	if ui.Switch(c.Key("enabled"), &on).Label(Tf("Enable %s", lib.name())).Disabled(last).Changed() {
		a.setPluginEnabled(lib, on)
	}
}

// guarded is a source with permissions: a plugin.
type guarded interface {
	Permissions() plugin.Permissions
	Granted() bool
	Grant(on bool) error
}

// pluginPermissions says what a plugin can reach, and asks before it may start
// programs, which can do anything the listener can. It may not until they say.
func (a *app) pluginPermissions(c *ui.Context, lib *library, g guarded, p palette) {
	perms := g.Permissions()
	if len(perms.Network) > 0 {
		reach := Tf("Can reach %s", strings.Join(perms.Network, ", "))
		if slices.Contains(perms.Network, "*") {
			reach = T("Can reach any website")
		}
		ui.Text(c, reach).FontSize(12).TextColor(p.muted).LineHeight(1.4)
	}
	if len(perms.Process) == 0 {
		return
	}
	granted := g.Granted()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, programsWarning(perms.Process)).
			FontSize(12).TextColor(p.muted).LineHeight(1.4).Grow(1).Shrink(1).MinWidth(0)
		label := T("Allow Programs")
		if granted {
			label = T("Stop Allowing")
		}
		if ui.Button(c.Key("grant"), label).Clicked() {
			a.grantPrograms(lib, g, !granted)
		}
	})
}

// programsWarning says which programs a plugin wants to start, before the
// listener lets it.
func programsWarning(programs plugin.Programs) string {
	if programs.Any() {
		return T("This plugin wants to start programs, which can do anything you can. Only allow it if you trust it.")
	}
	return Tf("This plugin wants to start %s, which can do anything you can. Only allow it if you trust it.", strings.Join(programs, ", "))
}

// grantPrograms changes the choice off the main thread, as the plugin starts
// over for it, and has the source load again with what it can do now.
func (a *app) grantPrograms(lib *library, g guarded, on bool) {
	go func() {
		err := g.Grant(on)
		a.post(func() {
			if err != nil {
				a.actionFailed(lib, err, T("Couldn't change that"))
				return
			}
			// Starting programs may be what it needed setting up for.
			lib.needsSetup, lib.askedSetup, lib.setupReason = false, false, ""
			a.libraryChanged(lib)
		})
	}()
}

// sourceSetting draws the control a source asks for, falling back to a text
// field saved on submission or blur for legacy or unsupported metadata.
func (a *app) sourceSetting(c *ui.Context, lib *library, st music.Setting, p palette) {
	if a.typedSourceSetting(c, lib, st, p) {
		return
	}
	key := lib.id + "\x00" + st.Key
	text := a.settingText[key]
	if text == nil {
		v := lib.src.Setting(st.Key)
		text = &v
		a.settingText[key] = text
	}
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, st.Label).FontSize(12.5).FontWeight(500)
		in := ui.TextInput(c.Key("value"), text).AlignSelf(ui.Start).Width(320).MaxWidthPercent(100).Label(st.Label).FontSize(13)
		if in.Submitted() || !in.Focused() && *text != a.pendingSetting(lib, st.Key) {
			a.keepSetting(lib, st.Key, *text)
		}
		if st.Help != "" {
			ui.Text(c, st.Help).FontSize(11.5).TextColor(p.muted).LineHeight(1.4)
		}
		if st.Link != "" {
			ui.RichText(c).FontSize(11.5).Children(func() { ui.Link(c, st.Link, st.Link) })
		}
	})
}

// keepSetting gives a source a setting, off the main thread, and loads anew
// what it may now be able to show.
func (a *app) keepSetting(lib *library, key, value string) {
	a.saveSetting(lib, key, value, nil)
}

type settingWrite struct{ value string }

// pendingSetting includes a queued edit, even before the source starts saving it.
func (a *app) pendingSetting(lib *library, key string) string {
	if pending := a.settingPending[lib.id+"\x00"+key]; pending != nil {
		return pending.value
	}
	return lib.src.Setting(key)
}

// saveSetting runs completion on the main thread. Typed controls suppress
// duplicate writes themselves and must be able to retry a failed persistence
// or plugin callback even if SetSetting already changed the in-memory value.
func (a *app) saveSetting(lib *library, key, value string, done func(error)) {
	if done == nil && a.pendingSetting(lib, key) == value {
		return
	}
	if a.settingPending == nil {
		a.settingPending = map[string]*settingWrite{}
	}
	pendingKey := lib.id + "\x00" + key
	pending := &settingWrite{value: value}
	a.settingPending[pendingKey] = pending
	// SetSetting persists the whole source map. Keep UI writes in initiation
	// order so controls cannot race the same settings.json temporary file.
	if a.settingSaves == nil {
		a.settingSaves = map[*library]<-chan struct{}{}
	}
	previous := a.settingSaves[lib]
	finished := make(chan struct{})
	a.settingSaves[lib] = finished
	go func() {
		if previous != nil {
			<-previous
		}
		err := lib.src.SetSetting(key, value)
		close(finished)
		a.post(func() {
			if a.settingSaves[lib] == finished {
				delete(a.settingSaves, lib)
			}
			if a.settingPending[pendingKey] == pending {
				delete(a.settingPending, pendingKey)
			}
			if done != nil {
				done(err)
			}
			if err != nil {
				a.toast = Tf("Couldn't keep the setting: %s", err.Error())
				return
			}
			lib.needsSetup, lib.askedSetup, lib.setupReason = false, false, ""
			a.libraryChanged(lib)
			a.loadAccount(lib)
		})
	}()
}
