package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

type backgroundTaskState uint8

const (
	backgroundTaskRunning backgroundTaskState = iota
	backgroundTaskSucceeded
	backgroundTaskFailed
	backgroundTaskCanceled
)

type backgroundTask struct {
	id         uint64
	key        string
	title      string
	summary    string
	status     string
	state      backgroundTaskState
	hidden     bool
	progress   *float64
	generation uint64
	removed    bool
	open       func()
	cancel     func()
	retry      func()
	actions    []backgroundTaskAction
}

type backgroundTaskAction struct {
	label    string
	icon     *ui.SVG
	run      func()
	disabled func() bool
}

// 任务数据只在主线程更新，工作协程通过 app.post 发布，组件不持有业务草稿或 UI Context。
func (a *app) newBackgroundTask(title, summary string) *backgroundTask {
	a.nextBackgroundTask++
	task := &backgroundTask{
		id:         a.nextBackgroundTask,
		title:      title,
		summary:    summary,
		state:      backgroundTaskRunning,
		generation: 1,
	}
	a.backgroundTasks = append([]*backgroundTask{task}, a.backgroundTasks...)
	a.backgroundTasksOpen = true
	return task
}

func (task *backgroundTask) update(generation uint64, state backgroundTaskState, status string) bool {
	if task.removed || task.generation != generation || task.state != backgroundTaskRunning {
		return false
	}
	task.state, task.status = state, status
	if state != backgroundTaskRunning {
		task.cancel = nil
		task.progress = nil
	}
	return true
}

func (a *app) hideBackgroundTask(task *backgroundTask) {
	if task == nil {
		return
	}
	task.hidden = true
	a.backgroundTasksOpen = false
}

func (a *app) removeBackgroundTask(task *backgroundTask) {
	for index, candidate := range a.backgroundTasks {
		if candidate != task {
			continue
		}
		if task.state == backgroundTaskRunning {
			return
		}
		task.removed = true
		copy(a.backgroundTasks[index:], a.backgroundTasks[index+1:])
		a.backgroundTasks[len(a.backgroundTasks)-1] = nil
		a.backgroundTasks = a.backgroundTasks[:len(a.backgroundTasks)-1]
		break
	}
	if len(a.backgroundTasks) == 0 {
		a.backgroundTasksOpen = false
	}
}

func (a *app) stopBackgroundTasks() {
	for _, task := range a.backgroundTasks {
		task.removed = true
		if task.cancel != nil {
			task.cancel()
		}
	}
	a.backgroundTasks = nil
	a.backgroundTasksOpen = false
}

func (a *app) backgroundTasksLayer(c *ui.Context) {
	if len(a.backgroundTasks) == 0 {
		return
	}
	width, height := c.Size()
	panelWidth := min(360, max(1, width-32))
	if !a.backgroundTasksOpen {
		panelWidth = min(190, max(1, width-32))
	}
	bottom := float32(playerBarHeight + 18)
	if a.nowPlaying {
		// 播放页的浮动控制栏含内外留白，需要比普通页面多留出垂直空间。
		bottom += 36
	}
	panelHeight := max(44, height-bottom-topBar(c)-14)
	ui.Column(c.Key("background-tasks")).Absolute().Right(18).Bottom(bottom).
		Width(panelWidth).MaxHeight(panelHeight).AlignItems(ui.Stretch).
		Radius(12).Background(a.pal.sidebar).Border(1, a.pal.hairline).Children(func() {
		label := fmt.Sprintf("%s (%d)", T("Background Tasks"), len(a.backgroundTasks))
		counts := [4]int{}
		for _, task := range a.backgroundTasks {
			counts[task.state]++
		}
		state, status := backgroundTaskCanceled, T("Canceled")
		// 出错优先提示，避免并行任务仍在运行时掩盖失败。
		if counts[backgroundTaskFailed] > 0 {
			state, status = backgroundTaskFailed, T("Failed")
		} else if counts[backgroundTaskRunning] > 0 {
			state, status = backgroundTaskRunning, T("In progress")
		} else if counts[backgroundTaskSucceeded] > 0 {
			state, status = backgroundTaskSucceeded, T("Completed")
		}
		summary := fmt.Sprintf("%s %d · %s %d · %s %d · %s %d",
			T("In progress"), counts[backgroundTaskRunning], T("Completed"), counts[backgroundTaskSucceeded],
			T("Failed"), counts[backgroundTaskFailed], T("Canceled"), counts[backgroundTaskCanceled])
		ui.ButtonBase(c.Key("background-task-toggle")).Label(label).
			Tooltip(summary).Padding(14, 16).AlignItems(ui.Center).Gap(10).OnClick(func() {
			a.backgroundTasksOpen = !a.backgroundTasksOpen
		}).Children(func() {
			if a.backgroundTasksOpen {
				ui.Icon(c, iconQueue).Size(16, 16).TextColor(a.pal.muted)
				ui.Text(c, T("Background Tasks")).FontSize(14).FontWeight(600).TextColor(a.pal.text)
				ui.Box(c).Size(22, 22).Radius(11).Background(a.pal.hover).Center().Children(func() {
					ui.Text(c, fmt.Sprint(len(a.backgroundTasks))).FontSize(11).TextColor(a.pal.muted)
				})
				ui.Box(c).Grow(1)
				ui.Icon(c, iconX).Size(14, 14).TextColor(a.pal.muted)
			} else {
				switch state {
				case backgroundTaskRunning:
					ui.Spinner(c).FontSize(15).TextColor(a.pal.accent).Label(status)
				case backgroundTaskSucceeded:
					ui.Icon(c, iconCheck).Size(16, 16).TextColor(a.pal.accent)
				default:
					ui.Icon(c, iconX).Size(16, 16).TextColor(a.pal.accent)
				}
				ui.Text(c, status).FontSize(13).FontWeight(600).TextColor(a.pal.text).SingleLine()
				ui.Text(c, fmt.Sprint(counts[state])).FontSize(12).TextColor(a.pal.muted).Grow(1)
				ui.Icon(c, iconForward).Size(14, 14).TextColor(a.pal.muted)
			}
		})
		if !a.backgroundTasksOpen {
			return
		}
		contentHeight := float32(0)
		for _, task := range a.backgroundTasks {
			contentHeight += 114
			if task.state == backgroundTaskSucceeded && len(task.actions) > 0 {
				contentHeight += 28
			}
			if task.progress != nil && task.state == backgroundTaskRunning {
				contentHeight += 20
			}
		}
		if contentHeight > 0 {
			ui.Box(c).Height(1).Background(a.pal.hairline)
			ui.Scroll(c.Key("background-task-list")).Height(min(contentHeight, max(0, panelHeight-54))).Children(func() {
				ui.Column(c).Children(func() {
					for index, task := range a.backgroundTasks {
						if index > 0 {
							ui.Box(c).Height(1).Background(a.pal.hairline).Margin(0, 16)
						}
						a.backgroundTaskCard(c, task)
					}
				})
			})
		}
	})
}

func (a *app) backgroundTaskCard(c *ui.Context, task *backgroundTask) {
	ui.Column(c.Key(fmt.Sprintf("background-task-%d", task.id))).
		Padding(14, 16).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Start).Gap(12).Children(func() {
			ui.Box(c).Size(34, 34).Radius(10).Background(a.pal.hover).Center().Shrink(0).Children(func() {
				if task.state == backgroundTaskRunning {
					ui.Spinner(c).FontSize(16).TextColor(a.pal.accent)
				} else if task.state == backgroundTaskSucceeded {
					ui.Icon(c, iconCheck).Size(17, 17).TextColor(a.pal.accent)
				} else {
					ui.Icon(c, iconX).Size(16, 16).TextColor(a.pal.muted)
				}
			})
			ui.ButtonBase(c.Key("open")).Column().Gap(4).Padding(0).AlignItems(ui.Start).
				Label(Tf("Open task: %s", task.summary)).Grow(1).MinWidth(0).
				Disabled(task.open == nil).OnClick(task.open).Children(func() {
				ui.Text(c, task.title).FontSize(13).FontWeight(600).TextColor(a.pal.text).SingleLine()
				ui.Text(c, task.summary).FontSize(12).TextColor(a.pal.muted).SingleLine()
				ui.Text(c, task.status).FontSize(12).TextColor(a.pal.muted).SingleLine()
			})
			iconButton(c.Key("hide"), iconX, 16, a.pal.muted, T("Close Background Task")).OnClick(func() {
				a.hideBackgroundTask(task)
			})
		})
		if task.progress != nil && task.state == backgroundTaskRunning {
			ui.Progress(c, max(0, min(1, *task.progress))).Label(task.status)
		}
		if task.state == backgroundTaskRunning && task.cancel != nil {
			ui.Row(c).Padding(0, 46).Children(func() {
				ui.Button(c.Key("cancel"), T("Cancel")).FontSize(12).Padding(5, 10).OnClick(task.cancel)
			})
		}
		if task.state == backgroundTaskSucceeded {
			ui.Row(c).Padding(0, 46).Wrap().Gap(4).Children(func() {
				for index, action := range task.actions {
					disabled := action.disabled != nil && action.disabled()
					if action.icon != nil {
						iconButton(c.Key(index), action.icon, 16, a.pal.text, action.label).
							Tooltip(action.label).Disabled(disabled).OnClick(action.run)
					} else {
						ui.Button(c.Key(index), action.label).FontSize(12).Padding(5, 8).Disabled(disabled).OnClick(action.run)
					}
				}
				iconButton(c.Key("remove"), iconTrash, 16, a.pal.muted, T("Remove")).Tooltip(T("Remove")).OnClick(func() {
					a.removeBackgroundTask(task)
				})
			})
		}
		if task.state == backgroundTaskFailed || task.state == backgroundTaskCanceled {
			ui.Row(c).Padding(0, 46).Gap(6).Children(func() {
				if task.retry != nil {
					ui.Button(c.Key("retry"), T("Retry")).FontSize(12).Padding(5, 8).OnClick(task.retry)
				}
				ui.Button(c.Key("remove"), T("Remove")).FontSize(12).Padding(5, 8).OnClick(func() {
					a.removeBackgroundTask(task)
				})
			})
		}
	})
}
