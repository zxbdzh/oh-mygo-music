package app

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/music"
)

// 各平台回执只归自己的行；不能用通用 library.account 承载三个并行身份。
type aggregatePlatformSource interface {
	AccountPlatform(string) (*music.Account, error)
	SignInPlatform(string) (*music.Account, error)
	SignOutPlatform(string) error
}

type aggregateAccountEdit struct {
	loaded, busy bool
	account      *music.Account
	err          string
}

func aggregatePlatformName(platform string) string {
	switch platform {
	case "wy":
		return T("NetEase Music")
	case "tx":
		return T("QQ Music")
	case "qs":
		return T("Qishui Music")
	case "kg":
		return T("Kugou")
	case "kw":
		return T("Kuwo")
	case "mg":
		return T("Migu")
	}
	return platform
}

func aggregateSection(c *ui.Context, p palette, icon *ui.SVG, title string) {
	ui.Box(c).Height(1).Background(p.hairline).Margin(16, 0, 8, 0)
	ui.Row(c).Gap(10).MinHeight(28).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icon).Size(16, 16).TextColor(p.muted)
		ui.Text(c, T(title)).FontSize(15).FontWeight(500)
	})
}

func (e *aggregateEdit) dirty() bool {
	if e.saved == nil {
		return false
	}
	var saved aggregateEdit
	saved.apply(*e.saved)
	return e.target != saved.target || e.minimum != saved.minimum || !reflect.DeepEqual(e.order, saved.order) || !reflect.DeepEqual(e.values, saved.values)
}

func (a *app) aggregateSettingsLayout(c *ui.Context, lib *library, e *aggregateEdit, p palette) {
	a.aggregateSourceSettings(c.Key("sources"), lib, e, p)
	if len(e.accountPlatforms) > 0 {
		aggregateSection(c, p, iconUser, "Platform Accounts")
		a.aggregateAccountRows(c.Key("accounts"), lib, e, p)
		// 会话可以并存，但歌单与喜欢仍按一个平台读取，不能随登录动作暗中切换。
		a.aggregatePlatformSetting(c.Key("personal"), lib, "accountPlatform", "Personal Content Platform", p)
		ui.Text(c, T("Choose the platform for your playlists and likes; sign-in actions stay independent.")).FontSize(11.5).TextColor(p.muted)
	}
	aggregateSection(c, p, iconSearch, "Search Platform")
	a.aggregatePlatformSetting(c.Key("catalog"), lib, "catalogPlatform", "Search Platform", p)
	aggregateSection(c, p, iconVolume, "Playback Settings")
	qualities := []string{"flac", "320k", "256k", "128k", "64k", "32k"}
	targets := append([]string(nil), qualities...)
	for _, target := range e.qualityTargets {
		targets = append(targets, target.Quality)
	}
	aggregateField(c, T("Target Quality"), func() {
		ui.Select(c.Key("target"), &e.target, targets).Label(T("Target Quality")).Width(180).Disabled(e.busy)
	})
	aggregateField(c, T("Minimum Quality"), func() {
		ui.Select(c.Key("minimum"), &e.minimum, qualities).Label(T("Minimum Quality")).Width(180).Disabled(e.busy)
	})
	aggregateField(c, T("Platform Order"), func() {
		ui.Column(c).Gap(4).Children(func() {
			for i, platform := range e.order {
				ui.Row(c).Gap(12).MinHeight(36).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprint(i+1)).FontSize(11).Width(18).TextColor(p.muted)
					ui.Text(c, aggregatePlatformName(platform)).FontSize(13).Width(110)
					up := roundButton(c.Key("up-"+platform), 16, p.muted, T("Move Up")+" "+aggregatePlatformName(platform))
					up.Children(func() { ui.Icon(c, iconDown).Size(16, 16).Rotate(180) })
					if up.Disabled(e.busy || i == 0).Clicked() {
						e.order[i-1], e.order[i] = e.order[i], e.order[i-1]
					}
					if iconButton(c.Key("down-"+platform), iconDown, 16, p.muted, T("Move Down")+" "+aggregatePlatformName(platform)).Disabled(e.busy || i == len(e.order)-1).Clicked() {
						e.order[i+1], e.order[i] = e.order[i], e.order[i+1]
					}
				})
			}
		})
	})
	advanced := ui.ButtonBase(c.Key("advanced")).AlignSelf(ui.Start).Height(36).PaddingX(8).Gap(8).AlignItems(ui.Center).Label(T("Advanced Settings"))
	advanced.Children(func() {
		angle := float32(-90)
		if e.advanced {
			angle = 0
		}
		ui.Icon(c, iconDown).Size(16, 16).Rotate(angle).TextColor(p.muted)
		ui.Text(c, T("Advanced Settings")).FontSize(13).TextColor(p.muted)
	})
	if advanced.Hovered() {
		advanced.Background(p.hover).Radius(6)
	}
	if advanced.Clicked() {
		e.advanced = !e.advanced
	}
	if e.advanced {
		for _, field := range aggregateTimingFields {
			aggregateField(c, T(field.label), func() {
				value := e.values[field.key]
				ui.TextInput(c.Key(field.key), &value).Label(T(field.label)).Width(180).Disabled(e.busy)
				e.values[field.key] = value
			})
		}
		ui.Text(c, T("Stall detection must be below the recovery budget; changes apply to new playback flows.")).FontSize(11.5).TextColor(p.muted)
	}
	if e.dirty() {
		ui.Row(c).Wrap().Gap(8).Children(func() {
			if ui.PrimaryButton(c.Key("save"), T("Save Aggregate Settings")).Disabled(e.busy).Clicked() {
				input := map[string]any{"targetQuality": e.target, "minimumQuality": e.minimum, "platformOrder": e.order}
				valid := true
				for _, field := range aggregateTimingFields {
					n, err := strconv.Atoi(e.values[field.key])
					if err != nil || n <= 0 {
						e.err = fmt.Sprintf(T("Enter a positive integer for %s"), T(field.label))
						valid = false
						break
					}
					input[field.key] = n
				}
				if valid {
					a.aggregateCommand(lib, e, "configure", input)
				}
			}
			if ui.Button(c.Key("cancel-settings"), T("Discard changes")).Disabled(e.busy).Clicked() {
				e.apply(*e.saved)
				e.err = ""
			}
		})
	}
	ui.Row(c).Wrap().Gap(8).Children(func() {
		if ui.Button(c.Key("reload"), T("Reload aggregate status")).Disabled(e.busy).Clicked() {
			a.aggregateCommand(lib, e, "status", nil)
		}
		if current := a.pl.Current(); current != nil && current.Source == lib.id {
			if ui.Button(c.Key("reload-track"), T("Reload Current Track")).Disabled(e.busy).Clicked() {
				a.pl.Reload()
			}
		}
	})
	if e.err != "" && !e.sourceInput {
		ui.Text(c, e.err).FontSize(12).TextColor(p.accent).LineHeight(1.4)
	}
}

// 空设置的生效值是网易；未知的非空旧值保持可见，不能偷偷改写。
func (a *app) aggregatePlatformSetting(c *ui.Context, lib *library, setting, label string, p palette) {
	key := lib.id + "\x00" + setting
	if a.settingEdits == nil {
		a.settingEdits = map[string]*settingEdit{}
	}
	source := lib.src.Setting(setting)
	draft := a.settingEdits[key]
	if draft == nil || !draft.saving && !draft.failed && draft.value == draft.source && source != draft.source {
		draft = &settingEdit{source: source, value: source}
		a.settingEdits[key] = draft
	}
	values, names := []string{"wy", "tx", "qs"}, []string{T("NetEase Music"), T("QQ Music"), T("Qishui Music")}
	if setting == "catalogPlatform" {
		names[2] += T(" (Account playlists only)")
	}
	selected := names[0]
	effective := draft.value
	if effective == "" {
		effective = "wy"
	}
	found := false
	for i, value := range values {
		if value == effective {
			selected = names[i]
			found = true
		}
	}
	if !found {
		selected = fmt.Sprintf("%s [%s]", T("Current value"), effective)
		names = append(names, selected)
		values = append(values, effective)
	}
	aggregateField(c, T(label), func() {
		if ui.Select(c.Key("value"), &selected, names).Label(T(label)).Disabled(draft.saving).Changed() {
			for i, name := range names {
				if selected == name {
					draft.value = values[i]
					a.keepTypedSetting(lib, setting, draft)
					break
				}
			}
		}
	})
	if draft.failed {
		ui.Text(c, T("Couldn't save platform. Retry or discard the change.")).FontSize(12).TextColor(p.accent)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c.Key("retry"), T("Retry")).Clicked() {
				a.keepTypedSetting(lib, setting, draft)
			}
			if ui.Button(c.Key("discard"), T("Discard changes")).Clicked() {
				draft.value = draft.source
				a.keepTypedSetting(lib, setting, draft)
			}
		})
	}
}

func (a *app) aggregateHasPlatformAccounts(lib *library) bool {
	e := a.aggregateEdits[lib]
	return e != nil && len(e.accountPlatforms) > 0
}

func (a *app) loadAggregateAccount(lib *library, e *aggregateEdit, platform string) {
	src, ok := lib.src.(aggregatePlatformSource)
	if !ok {
		return
	}
	if e.accounts == nil {
		e.accounts = map[string]*aggregateAccountEdit{}
	}
	row := e.accounts[platform]
	if row == nil {
		row = &aggregateAccountEdit{}
		e.accounts[platform] = row
	}
	if row.busy {
		return
	}
	row.busy = true
	go func() {
		account, err := src.AccountPlatform(platform)
		a.post(func() {
			if a.aggregateEdits[lib] != e || e.accounts[platform] != row {
				return
			}
			row.loaded, row.busy = true, false
			if err != nil {
				row.err = err.Error()
				return
			}
			row.account, row.err = account, ""
		})
	}()
}

func (a *app) aggregateAccountRows(c *ui.Context, lib *library, e *aggregateEdit, p palette) {
	if _, ok := lib.src.(aggregatePlatformSource); !ok {
		return
	}
	for _, platform := range e.accountPlatforms {
		if e.accounts[platform] == nil {
			a.loadAggregateAccount(lib, e, platform)
		}
		row := e.accounts[platform]
		if row == nil {
			continue
		}
		name := aggregatePlatformName(platform)
		ui.Column(c.Key(platform)).Gap(4).Children(func() {
			// 操作区放不下时整体换行，平台和昵称共享文字行，长昵称允许继续折行。
			ui.Row(c).Wrap().Gap(16).MinHeight(44).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Grow(1).MinWidth(230).Gap(16).AlignItems(ui.Center).Children(func() {
					ui.Text(c, name).FontSize(13).Width(130).Shrink(0)
					state := T("Not signed in")
					if !row.loaded || row.busy {
						state = T("Loading…")
					} else if row.err != "" {
						state = T("Account unavailable")
					} else if row.account != nil {
						state = row.account.Name
					}
					ui.Text(c, state).FontSize(13).TextColor(p.muted).Grow(1).Shrink(1).MinWidth(0)
				})
				ui.Row(c).Wrap().Gap(8).Width(164).Shrink(0).Justify(ui.End).Children(func() {
					if row.account == nil {
						if ui.Button(c.Key("sign-in"), T("Sign In")).Label(T("Sign In") + " " + name).Width(164).Disabled(row.busy || a.signIn.waiting).Clicked() {
							a.signIn = signInDialog{open: true, lib: lib, here: true, platform: platform, gen: a.signIn.gen + 1}
						}
					} else if ui.Button(c.Key("sign-out"), T("Sign Out")).Label(T("Sign Out") + " " + name).Width(164).Disabled(row.busy || a.signIn.waiting).Clicked() {
						a.signOutAggregateAccount(lib, e, platform, row)
					}
					if row.err != "" && ui.Button(c.Key("retry"), T("Retry")+" "+name).Disabled(row.busy || a.signIn.waiting).Clicked() {
						a.loadAggregateAccount(lib, e, platform)
					}
				})
			})
			if row.err != "" {
				ui.Text(c, row.err).FontSize(11.5).TextColor(p.accent)
			}
		})
	}
}

func (a *app) signOutAggregateAccount(lib *library, e *aggregateEdit, platform string, row *aggregateAccountEdit) {
	src, ok := lib.src.(aggregatePlatformSource)
	if !ok || row.busy {
		return
	}
	row.busy = true
	go func() {
		err := src.SignOutPlatform(platform)
		a.post(func() {
			if a.aggregateEdits[lib] != e || e.accounts[platform] != row {
				return
			}
			row.busy = false
			if err != nil {
				row.err = err.Error()
				a.loadAggregateAccount(lib, e, platform)
				return
			}
			row.account, row.err = nil, ""
			// 退出改变聚合快照，但默认账号仍单独查询，不拿本行身份覆盖它。
			a.afterAccount(lib)
			a.loadAccount(lib)
		})
	}()
}
