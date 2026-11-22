package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type sleepMode int

const (
	sleepModeAtDeadline sleepMode = iota
	sleepModeEndOfTrack
)

const (
	minSleepMinutes = 1
	maxSleepMinutes = 720
)

var sleepPresetMinutes = []int{15, 30, 45, 60, 90}

// sleepTimerState 保存当前会话的睡眠定时状态；应用退出后不持久化。
type sleepTimerState struct {
	active        bool
	mode          sleepMode
	deadline      time.Time // 使用 Round(0) 剥离单调时钟，确保系统休眠期间按实际墙钟时间流逝
	waitingForEnd bool      // 倒计时已结束，正在等待当前播放曲目播完
	open          bool
	anchor        string
	customMinutes string
	customError   string
	timer         *time.Timer
	gen           uint64
}

func (a *app) currentTime() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *app) sleepRemaining() time.Duration {
	if !a.sleep.active || a.sleep.waitingForEnd {
		return 0
	}
	rem := a.sleep.deadline.Round(0).Sub(a.currentTime().Round(0))
	if rem < 0 {
		return 0
	}
	return rem
}

func (a *app) startSleepTimer(minutes int, mode sleepMode) {
	if minutes < minSleepMinutes || minutes > maxSleepMinutes {
		return
	}
	if a.pl != nil && a.pl.Remote() {
		mode = sleepModeAtDeadline
	}
	if a.pl != nil {
		a.pl.SetStopAfterTrack(false)
	}
	a.sleep.gen++
	a.sleep.active = true
	a.sleep.mode = mode
	a.sleep.waitingForEnd = false
	// 剥离 monotonic clock，避免电脑休眠时单调时钟停走导致唤醒后继续计时。
	a.sleep.deadline = a.currentTime().Add(time.Duration(minutes) * time.Minute).Round(0)
	a.sleep.customError = ""
	a.scheduleSleepTimer()
}

func (a *app) setSleepMode(mode sleepMode) {
	if a.pl != nil && a.pl.Remote() && mode == sleepModeEndOfTrack {
		return
	}
	a.sleep.mode = mode
	if a.sleep.active && a.sleep.waitingForEnd && mode == sleepModeAtDeadline {
		// 等待曲尾阶段改回“到点暂停”时，倒计时已过期，应立即暂停并结束定时。
		a.triggerSleepPause()
	}
}

func (a *app) cancelSleepTimer() {
	if a.pl != nil {
		a.pl.SetStopAfterTrack(false)
	}
	a.clearSleepTimer()
}

func (a *app) clearSleepTimer() {
	if a.sleep.timer != nil {
		a.sleep.timer.Stop()
		a.sleep.timer = nil
	}
	a.sleep.gen++
	a.sleep.active = false
	a.sleep.waitingForEnd = false
	a.sleep.deadline = time.Time{}
}

func (a *app) scheduleSleepTimer() {
	if a.sleep.timer != nil {
		a.sleep.timer.Stop()
		a.sleep.timer = nil
	}
	if !a.sleep.active || a.sleep.waitingForEnd {
		return
	}
	// 仅在存在主窗口事件循环时挂载后台定时器，无窗口单测由时钟注入驱动。
	if a.win == nil {
		return
	}
	rem := a.sleepRemaining()
	// 最多每 15 秒校对一次墙钟，保证窗口隐藏在托盘期间系统休眠唤醒后仍能尽快收敛。
	wait := min(rem, 15*time.Second)
	if wait <= 0 {
		wait = time.Millisecond
	}
	gen := a.sleep.gen
	a.sleep.timer = time.AfterFunc(wait, func() {
		a.post(func() {
			if gen != a.sleep.gen {
				return
			}
			a.checkSleepTimer()
		})
	})
}

func (a *app) checkSleepTimer() {
	if !a.sleep.active || a.sleep.waitingForEnd {
		return
	}
	now := a.currentTime().Round(0)
	if now.Before(a.sleep.deadline.Round(0)) {
		a.scheduleSleepTimer()
		return
	}
	if a.sleep.mode == sleepModeEndOfTrack && a.pl != nil && !a.pl.Remote() {
		if !a.pl.Playing() {
			// 到点时已处于手动暂停状态，保持暂停并结束定时，不影响后续手动续播。
			a.clearSleepTimer()
			return
		}
		if a.sleep.timer != nil {
			a.sleep.timer.Stop()
			a.sleep.timer = nil
		}
		a.sleep.waitingForEnd = true
		a.pl.SetStopAfterTrack(true)
		return
	}
	a.triggerSleepPause()
}

func (a *app) triggerSleepPause() {
	a.clearSleepTimer()
	if a.pl == nil || !a.pl.Playing() {
		return
	}
	a.pl.PauseForSleep(func(error) {
		a.toast = T("Could not pause remote playback for sleep timer")
		a.redraw()
	})
}

func (a *app) syncSleepTimerPlayback() {
	if a.sleep.active && a.pl != nil && a.pl.Remote() && a.sleep.mode == sleepModeEndOfTrack {
		a.sleep.mode = sleepModeAtDeadline
	}
	a.checkSleepTimer()
}

func parseSleepMinutes(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < minSleepMinutes || v > maxSleepMinutes {
		return 0, false
	}
	return v, true
}

func formatSleepClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	// 向上取整到整秒，避免刚设置 15 分钟瞬间显示 14:59。
	secs := int((d + time.Second - time.Nanosecond) / time.Second)
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func (a *app) sleepStatusText() string {
	if !a.sleep.active {
		return ""
	}
	if a.sleep.waitingForEnd {
		return T("Track end")
	}
	return formatSleepClock(a.sleepRemaining())
}

func (a *app) tickSleepTimer(c *ui.Context) {
	if !a.sleep.active || a.sleep.waitingForEnd {
		return
	}
	rem := a.sleepRemaining()
	if rem <= 0 {
		c.After(time.Millisecond)
		return
	}
	step := rem % time.Second
	if step == 0 {
		step = time.Second
	}
	c.After(step + 10*time.Millisecond)
}

func (a *app) sleepTimerButton(c *ui.Context, kind string, color ui.Color, dark, centerAnchor bool) {
	a.tickSleepTimer(c)
	active := a.sleep.active
	accent := a.pal.accent
	status := a.sleepStatusText()
	var button ui.Element
	if !active {
		button = iconButton(c.Key("sleep-timer"), iconSleep, 16, color, T("Sleep Timer")).Tooltip(T("Sleep Timer"))
	} else {
		button = ui.ButtonBase(c.Key("sleep-timer")).Height(28).PaddingX(9).Gap(5).Radius(999).
			AlignItems(ui.Center).Label(T("Sleep Timer")).Tooltip(status).TextColor(accent)
		bg := accent.Alpha(0.14)
		if button.Hovered() {
			bg = accent.Alpha(0.22)
		}
		button.Background(button.AnimateColor("bg", bg, ui.Animation{Duration: 120 * time.Millisecond}))
		pose := ui.Pose{}
		if button.Pressed() {
			pose.Scale = 0.94
		}
		button.AnimatePose(pose, ui.Snappy)
		button.Children(func() {
			ui.Icon(c, iconSleep).Size(14, 14)
			ui.Text(c, status).FontSize(11.5).FontWeight(650).FontFeatures("tnum").SingleLine()
		})
	}
	if button.Clicked() {
		if a.sleep.open && a.sleep.anchor == kind {
			a.sleep.open = false
		} else {
			a.sleep.open = true
			a.sleep.anchor = kind
			a.sleep.customError = ""
		}
	}
	open := a.sleep.open && a.sleep.anchor == kind
	wasOpen := open
	ui.PopoverBase(c.Key("sleep-timer-popover"), button, &open, func(panel ui.Element) {
		a.sleepTimerPopover(c, panel, button, dark, centerAnchor)
	})
	if wasOpen && !open {
		a.sleep.open = false
	}
}

type sleepPopoverTheme struct {
	pop                      popover
	ink, muted, hover, press ui.Color
}

func (a *app) sleepTheme(dark bool) sleepPopoverTheme {
	th := sleepPopoverTheme{
		pop:   popoverColors(dark),
		ink:   a.pal.text,
		muted: a.pal.muted,
		hover: a.pal.hover,
		press: a.pal.press,
	}
	if dark && !a.pal.dark {
		// 全屏播放页在浅色系统主题下也使用深色背景，面板配色需保持高对比浅字。
		th.ink = ui.Hex("#f5f5f7")
		th.muted = ui.RGBA(235, 235, 245, 0.56)
		th.hover = ui.RGBA(255, 255, 255, 0.08)
		th.press = ui.RGBA(255, 255, 255, 0.14)
	}
	return th
}

func (a *app) sleepTimerPopover(c *ui.Context, panel, anchor ui.Element, dark, centerAnchor bool) {
	th := a.sleepTheme(dark)
	pop, ink, muted, hover, press := th.pop, th.ink, th.muted, th.hover, th.press
	if centerAnchor {
		panel.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom)
	} else {
		panel.AttachTo(anchor, ui.AnchorTopEnd, ui.AnchorBottomEnd)
	}
	panel.Margin(0, 0, 10, 0).Width(288).Padding(14).Gap(12).Radius(14).
		Background(pop.fill).Border(0.5, pop.edge).
		Shadow(0, 8, 28, -4, ui.RGBA(0, 0, 0, 0.24)).
		TextColor(ink).Label(T("Sleep Timer"))

	// 标题行
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, iconSleep).Size(16, 16).TextColor(a.pal.accent)
		ui.Text(c, T("Sleep Timer")).FontSize(14).FontWeight(650).Grow(1)
		if a.sleep.active {
			ui.Text(c, a.sleepStatusText()).FontSize(12).FontWeight(650).FontFeatures("tnum").TextColor(a.pal.accent)
		}
	})

	// 启用状态卡
	if a.sleep.active {
		ui.Column(c).Padding(9, 11).Gap(2).Radius(10).Background(a.pal.accent.Alpha(0.12)).Children(func() {
			if a.sleep.waitingForEnd {
				ui.Text(c, T("Pausing after current track")).FontSize(13).FontWeight(600).TextColor(ink)
			} else {
				ui.Text(c, formatSleepClock(a.sleepRemaining())).FontSize(18).FontWeight(700).FontFeatures("tnum").TextColor(ink)
				modeLabel := T("Pausing when timer ends")
				if a.sleep.mode == sleepModeEndOfTrack {
					modeLabel = T("Waiting for track to finish after timer")
				}
				ui.Text(c, modeLabel).FontSize(11.5).TextColor(muted)
			}
		})
	}

	// 暂停方式选择
	remote := a.pl != nil && a.pl.Remote()
	if remote && a.sleep.mode == sleepModeEndOfTrack {
		a.sleep.mode = sleepModeAtDeadline
	}
	ui.Column(c).Gap(5).Children(func() {
		ui.Row(c).Padding(3).Gap(3).Radius(9).Background(hover).Children(func() {
			modes := []struct {
				mode     sleepMode
				label    string
				disabled bool
			}{
				{sleepModeAtDeadline, T("At timer end"), false},
				{sleepModeEndOfTrack, T("After current track"), remote},
			}
			for _, item := range modes {
				selected := a.sleep.mode == item.mode
				btn := ui.ButtonBase(c.Key(item.mode)).Grow(1).Basis(0).Height(28).PaddingX(6).Radius(7).
					Center().Disabled(item.disabled).Label(item.label).FocusRing(false)
				bg := ui.Transparent
				fg := muted
				if selected {
					bg = a.pal.accent
					fg = a.pal.onAccent
				} else if !item.disabled && btn.Hovered() {
					bg = press
					fg = ink
				}
				if item.disabled {
					fg = muted.Alpha(0.45)
				}
				btn.Background(bg).TextColor(fg).Children(func() {
					ui.Text(c, item.label).FontSize(12).FontWeight(600).SingleLine()
				})
				if btn.Clicked() {
					a.setSleepMode(item.mode)
				}
			}
		})
		if remote {
			ui.Text(c, T("Remote playback only supports pausing at timer end")).FontSize(11).TextColor(muted)
		}
	})

	// 快捷时长按钮
	ui.Row(c).Gap(6).Children(func() {
		for _, mins := range sleepPresetMinutes {
			label := Tf("%dm", mins)
			btn := ui.ButtonBase(c.Key(mins)).Grow(1).Basis(0).Height(30).Radius(8).
				Center().Label(label).FocusRing(false)
			bg, fg := hover, ink
			if btn.Pressed() {
				bg = press
			} else if btn.Hovered() {
				bg = press
			}
			btn.Background(bg).TextColor(fg).Children(func() {
				ui.Text(c, label).FontSize(12).FontWeight(600).FontFeatures("tnum").SingleLine()
			})
			if btn.Clicked() {
				a.startSleepTimer(mins, a.sleep.mode)
			}
		}
	})

	// 自定义整分钟输入（1–720）
	ui.Column(c).Gap(5).Children(func() {
		submitCustom := func() {
			mins, ok := parseSleepMinutes(a.sleep.customMinutes)
			if !ok {
				a.sleep.customError = T("Enter a whole number from 1 to 720")
				return
			}
			a.sleep.customError = ""
			a.startSleepTimer(mins, a.sleep.mode)
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c.Key("sleep-custom-minutes"), &a.sleep.customMinutes).
				Placeholder(T("1–720 min")).Label(T("Custom minutes")).FontSize(12.5).Grow(1).MinWidth(0)
			if in.Changed() && a.sleep.customError != "" {
				a.sleep.customError = ""
			}
			setBtn := ui.ButtonBase(c.Key("sleep-set")).Height(30).PaddingX(12).Radius(8).
				Center().Label(T("Set")).Background(a.pal.accent).TextColor(a.pal.onAccent).FocusRing(false)
			setBtn.Children(func() {
				ui.Text(c, T("Set")).FontSize(12).FontWeight(600).SingleLine()
			})
			if setBtn.Clicked() || in.Submitted() {
				submitCustom()
			}
		})
		if a.sleep.customError != "" {
			ui.Text(c, a.sleep.customError).FontSize(11).TextColor(ui.RGB(218, 72, 72))
		}
	})

	if a.sleep.active {
		cancelBtn := ui.ButtonBase(c.Key("sleep-cancel")).Height(32).Radius(8).Center().
			Label(T("Cancel Timer")).FocusRing(false)
		bg := hover
		if cancelBtn.Hovered() || cancelBtn.Pressed() {
			bg = press
		}
		cancelBtn.Background(bg).TextColor(ink).Children(func() {
			ui.Text(c, T("Cancel Timer")).FontSize(12.5).FontWeight(600).SingleLine()
		})
		if cancelBtn.Clicked() {
			a.cancelSleepTimer()
		}
	}
}
