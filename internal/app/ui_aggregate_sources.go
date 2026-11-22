package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 指针是否存在决定旧插件兼容路径；active=false 不是“不支持音源管理”。
type aggregateCustomSources struct {
	Version int                     `json:"version"`
	Active  bool                    `json:"active"`
	State   string                  `json:"state"`
	Items   []aggregateCustomSource `json:"items"`
}

type aggregateCustomSource struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Platforms   []string `json:"platforms"`
	Enabled     bool     `json:"enabled"`
	State       string   `json:"state"`
	Reason      string   `json:"reason"`
	UpdateState string   `json:"updateState"`
	UpdateError string   `json:"updateError"`
}

func (a *app) aggregateSourceSettings(c *ui.Context, lib *library, e *aggregateEdit, p palette) {
	// 只观察启动更新，最多重取 30 次；离开页面不再调度，错误后由用户手动重取。
	if e.sources.State == "updating" && !e.busy && e.err == "" && e.sourcePolls < 30 {
		if e.sourceNextPoll.IsZero() {
			e.sourceNextPoll = c.Now().Add(2 * time.Second)
		}
		if !c.Now().Before(e.sourceNextPoll) {
			e.sourceNextPoll = c.Now().Add(2 * time.Second)
			e.sourcePolls++
			a.aggregateCommand(lib, e, "source-status", nil)
		} else {
			c.After(e.sourceNextPoll.Sub(c.Now()))
		}
	}
	ui.Row(c).Wrap().Gap(10).MinHeight(40).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, iconPlug).Size(16, 16).TextColor(p.muted)
		ui.Text(c, T("Playback Sources")).FontSize(15).FontWeight(500).Grow(1)
		if !e.sourceInput && ui.PrimaryButton(c.Key("add-source"), T("Add Source")).Disabled(e.busy).Clicked() {
			e.sourceInput = true
		}
		if ui.Button(c.Key("refresh-sources"), T("Update All Sources")).Disabled(e.busy).Clicked() {
			a.aggregateCommand(lib, e, "refreshSources", nil)
		}
		if e.busy || e.sources.State == "updating" {
			ui.Text(c, T("Updating sources…")).FontSize(11.5).TextColor(p.muted)
		}
	})
	if e.sourceInput {
		ui.Column(c.Key("source-input")).Gap(6).Padding(10).Background(p.hover).Radius(6).Children(func() {
			ui.Text(c, T("Source Link")).FontSize(12.5).FontWeight(500)
			ui.TextInput(c.Key("source-link"), &e.sourceLink).Label(T("Source Link")).Password().WidthPercent(100).Disabled(e.busy)
			ui.Row(c).Wrap().Gap(8).Children(func() {
				label := "Confirm Add Source"
				if e.sourceTarget != "" {
					label = "Replace Source"
				}
				if ui.PrimaryButton(c.Key("submit-source"), T(label)).Disabled(e.busy || strings.TrimSpace(e.sourceLink) == "").Clicked() {
					action := "addSource"
					input := map[string]any{"url": e.sourceLink}
					if e.sourceTarget != "" {
						action, input["id"] = "replaceSource", e.sourceTarget
					}
					a.aggregateCommand(lib, e, action, input)
				}
				if ui.Button(c.Key("cancel-source"), T("Cancel Source Input")).Disabled(e.busy).Clicked() {
					// 取消必须清掉敏感草稿，下一次展开不复用之前的链接。
					e.sourceLink, e.sourceTarget, e.sourceInput = "", "", false
				}
			})
			if e.err != "" {
				ui.Text(c, e.err).FontSize(12).TextColor(p.accent)
			}
		})
	}
	if e.sourceTarget != "" {
		ui.Row(c).Wrap().Gap(12).AlignItems(ui.Center).Children(func() {
			name := e.sourceTarget
			for _, source := range e.sources.Items {
				if source.ID == e.sourceTarget {
					name = source.Name
				}
			}
			ui.Text(c, fmt.Sprintf(T("Replacing: %s"), name)).FontSize(12).TextColor(p.muted)
			if ui.Button(c.Key("cancel-replace"), T("Cancel Replacement")).Disabled(e.busy).Clicked() {
				e.sourceLink, e.sourceTarget, e.sourceInput = "", "", false
			}
		})
	}
	if !e.sources.Active {
		ui.Text(c, T("Legacy playback remains until the first successful import.")).FontSize(11.5).TextColor(p.muted)
	} else if len(e.sources.Items) == 0 {
		ui.Text(c, T("No sources. Add a link to resume new playback; legacy services remain off.")).FontSize(11.5).TextColor(p.muted)
	}
	ui.Text(c, T("Available means configuration is usable, not verified playback.")).FontSize(11.5).TextColor(p.muted)
	for i, source := range e.sources.Items {
		// 按内容最小宽度自然折行，窄窗先保留名称空间，不压缩文字来给操作让位。
		ui.Row(c.Key("source-" + source.ID)).Wrap().Gap(16).MinHeight(52).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Grow(1).MinWidth(210).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprint(i+1)).FontSize(11).Width(18).Shrink(0).TextColor(p.muted)
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, source.Name).FontSize(13).FontWeight(500)
					platforms := make([]string, 0, len(source.Platforms))
					for _, platform := range source.Platforms {
						platforms = append(platforms, aggregatePlatformName(platform))
					}
					ui.Text(c, strings.Join(platforms, " / ")).FontSize(11).TextColor(p.muted)
				})
			})
			ui.Row(c).Grow(1).MinWidth(248).Gap(16).AlignItems(ui.Center).Children(func() {
				state := map[string]string{"ready": T("Source Available"), "disabled": T("Source Disabled"), "blocked": T("Source Needs Retry"), "unavailable": T("Source Unavailable")}[source.State]
				if state == "" {
					state = T("Source Unavailable")
				}
				if !source.Enabled {
					state = T("Source Disabled")
				}
				ui.Text(c, state).FontSize(12).Grow(1).MinWidth(100).TextColor(p.muted)
				enabled := source.Enabled
				if ui.Switch(c.Key("enabled"), &enabled).Label(T("Enable Source") + " " + source.Name).Disabled(e.busy).Changed() {
					a.aggregateCommand(lib, e, "setSourceEnabled", map[string]any{"id": source.ID, "enabled": enabled})
				}
				// 保留操作槽宽度，故障重试出现时不把其他行的状态和开关挤歪。
				ui.Row(c).Width(32).Shrink(0).AlignItems(ui.Center).Children(func() {
					if source.Enabled && source.State == "blocked" && iconButton(c.Key("retry"), iconRefresh, 16, p.muted, T("Retry Source")).Disabled(e.busy).Clicked() {
						a.aggregateCommand(lib, e, "retrySource", map[string]any{"id": source.ID})
					}
				})
				button := iconButton(c.Key("actions"), iconMore, 16, p.muted, T("Source Actions")+" "+source.Name)
				button.Disabled(e.busy).Menu(func(m *ui.Menu) {
					if m.Item(T("Update Source")).Disabled(e.busy || source.UpdateState == "updating").Chosen() {
						a.aggregateCommand(lib, e, "updateSource", map[string]any{"id": source.ID})
					}
					if m.Item(T("Replace Source Link")).Disabled(e.busy).Chosen() {
						// 替换沿用同一空白草稿，绝不取回已保存链接。
						e.sourceTarget, e.sourceLink, e.sourceInput = source.ID, "", true
					}
					m.Separator()
					if m.Item(T("Move Source Up")).Disabled(e.busy || i == 0).Chosen() {
						a.aggregateCommand(lib, e, "moveSource", map[string]any{"id": source.ID, "direction": "up"})
					}
					if m.Item(T("Move Source Down")).Disabled(e.busy || i == len(e.sources.Items)-1).Chosen() {
						a.aggregateCommand(lib, e, "moveSource", map[string]any{"id": source.ID, "direction": "down"})
					}
					m.Separator()
					if m.Item(T("Remove Source")).Disabled(e.busy).Chosen() {
						a.aggregateCommand(lib, e, "removeSource", map[string]any{"id": source.ID})
					}
				})
			})
		})
		if source.Reason != "" {
			ui.Text(c, source.Reason).FontSize(11.5).TextColor(p.muted)
		}
		if source.UpdateState == "updating" {
			ui.Text(c, T("Updating sources…")).FontSize(11.5).TextColor(p.muted)
		} else if source.UpdateError != "" {
			ui.Text(c, source.UpdateError).FontSize(11.5).TextColor(p.muted)
		}
	}
}
