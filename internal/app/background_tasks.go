package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

type backgroundTaskState uint8

const (
	backgroundTaskRunning backgroundTaskState = iota
	backgroundTaskSucceeded
	backgroundTaskFailed
	backgroundTaskCanceled
)

type backgroundTaskKind uint8

const (
	backgroundTaskOther backgroundTaskKind = iota
	backgroundTaskDownload
	backgroundTaskShare
	backgroundTaskAI
)

type backgroundTask struct {
	id            uint64
	key           string
	title         string
	summary       string
	status        string
	state         backgroundTaskState
	kind          backgroundTaskKind
	hidden        bool
	progress      *float64
	waiting       bool // 排队仍可取消，但尚未占用传输资源。
	cancelLabel   string
	generation    uint64
	removed       bool
	open          func()
	cancel        func()
	retry         func()
	retryLabel    string // 为同屏多个重试按钮提供稳定的无头测试和辅助技术名称。
	failureAction *backgroundTaskAction
	actions       []backgroundTaskAction
}

type backgroundTaskAction struct {
	label    string
	icon     *ui.SVG
	run      func()
	disabled func() bool
}

// 任务数据只在主线程更新，工作协程通过 app.post 发布，组件不持有业务草稿或 UI Context。
func (a *app) newBackgroundTask(title, summary string) *backgroundTask {
	task := a.addBackgroundTask(title, summary)
	// 下载、分享这类用户主动发起的传输才自动展开；手动收起后不再抢回视线。
	if !a.backgroundTasksPinned {
		a.backgroundTasksOpen = true
	}
	return task
}

// newQuietBackgroundTask 加入任务但不展开面板，AI 建议在后台安静进行。
func (a *app) newQuietBackgroundTask(title, summary string) *backgroundTask {
	return a.addBackgroundTask(title, summary)
}

func (a *app) addBackgroundTask(title, summary string) *backgroundTask {
	a.nextBackgroundTask++
	task := &backgroundTask{
		id:         a.nextBackgroundTask,
		title:      title,
		summary:    summary,
		state:      backgroundTaskRunning,
		generation: 1,
	}
	a.backgroundTasks = append([]*backgroundTask{task}, a.backgroundTasks...)
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
	if state == backgroundTaskSucceeded || state == backgroundTaskCanceled {
		task.failureAction = nil
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
	for index, d := range a.downloads.tasks {
		if d.card == task {
			copy(a.downloads.tasks[index:], a.downloads.tasks[index+1:])
			a.downloads.tasks[len(a.downloads.tasks)-1] = nil
			a.downloads.tasks = a.downloads.tasks[:len(a.downloads.tasks)-1]
			break
		}
	}
	a.saveDownloads()
	if len(a.backgroundTasks) == 0 {
		a.backgroundTasksOpen = false
	}
}

func (a *app) stopBackgroundTasks() {
	// 先保留运行中的快照；取消后的迟到回调不得把退出记录清成空列表。
	a.saveDownloads()
	a.downloads.history = false
	a.downloads.pending = nil
	for _, task := range a.backgroundTasks {
		task.removed = true
		if task.cancel != nil {
			task.cancel()
		}
	}
	a.backgroundTasks = nil
	a.backgroundTasksOpen = false
}

// 任务按状态分组：出错的最先看到，其次进行中、已完成、已取消。
var backgroundTaskGroupOrder = [...]backgroundTaskState{
	backgroundTaskFailed,
	backgroundTaskRunning,
	backgroundTaskSucceeded,
	backgroundTaskCanceled,
}

type backgroundTaskGroup struct {
	state backgroundTaskState
	tasks []*backgroundTask
}

func (a *app) backgroundTaskGroups() []backgroundTaskGroup {
	groups := make([]backgroundTaskGroup, 0, len(backgroundTaskGroupOrder))
	for _, state := range backgroundTaskGroupOrder {
		var tasks []*backgroundTask
		for _, task := range a.backgroundTasks {
			if task.state == state {
				tasks = append(tasks, task)
			}
		}
		if len(tasks) > 0 {
			groups = append(groups, backgroundTaskGroup{state: state, tasks: tasks})
		}
	}
	return groups
}

func backgroundTaskStateLabel(state backgroundTaskState) string {
	switch state {
	case backgroundTaskFailed:
		return T("Failed")
	case backgroundTaskRunning:
		return T("In progress")
	case backgroundTaskSucceeded:
		return T("Completed")
	default:
		return T("Canceled")
	}
}

func (a *app) backgroundTaskCounts() [4]int {
	var counts [4]int
	for _, task := range a.backgroundTasks {
		counts[task.state]++
	}
	return counts
}

// backgroundTaskCountSummary 是收起胶囊第二行：只列出现的状态，避免四个零。
func backgroundTaskCountSummary(counts [4]int) string {
	parts := make([]string, 0, 4)
	if counts[backgroundTaskFailed] > 0 {
		parts = append(parts, Tf("%d failed", counts[backgroundTaskFailed]))
	}
	if counts[backgroundTaskRunning] > 0 {
		parts = append(parts, Tf("%d in progress", counts[backgroundTaskRunning]))
	}
	if counts[backgroundTaskSucceeded] > 0 {
		parts = append(parts, Tf("%d completed", counts[backgroundTaskSucceeded]))
	}
	if counts[backgroundTaskCanceled] > 0 {
		parts = append(parts, Tf("%d canceled", counts[backgroundTaskCanceled]))
	}
	return strings.Join(parts, " · ")
}

// backgroundTaskRep 挑一个最能说明情况的任务放进收起胶囊。
func (a *app) backgroundTaskRep() *backgroundTask {
	for _, state := range backgroundTaskGroupOrder {
		for _, task := range a.backgroundTasks {
			if task.state == state {
				return task
			}
		}
	}
	return nil
}

func (a *app) backgroundTasksLayer(c *ui.Context) {
	if len(a.backgroundTasks) == 0 {
		return
	}
	// 在下载详情页中隐藏仅含下载任务的浮窗，避免与详情列表重复或遮挡页面底部操作。
	if a.route.page == pageDownloads && !a.nowPlaying {
		hasOther := false
		for _, task := range a.backgroundTasks {
			if task.kind != backgroundTaskDownload {
				hasOther = true
				break
			}
		}
		if !hasOther {
			return
		}
	}
	width, height := c.Size()
	panelWidth := min(float32(360), max(1, width-32))
	if !a.backgroundTasksOpen {
		panelWidth = min(float32(288), max(1, width-32))
	}
	bottom := float32(playerBarHeight + 18)
	if a.nowPlaying {
		// 播放页的浮动控制栏含内外留白，需要比普通页面多留出垂直空间。
		bottom += 36
	}
	available := max(float32(44), height-bottom-topBar(c)-14)
	label := fmt.Sprintf("%s (%d)", T("Background Tasks"), len(a.backgroundTasks))
	if !a.backgroundTasksOpen {
		panelHeight := min(float32(60), available)
		ui.ButtonBase(c.Key("background-task-toggle")).Absolute().Right(18).Bottom(bottom).
			Width(panelWidth).Height(panelHeight).Padding(9, 14).Column().Justify(ui.Center).
			Gap(4).AlignItems(ui.Stretch).Label(label).Radius(20).
			Background(a.pal.sidebar).Border(1, a.pal.hairline).OnClick(func() {
			a.backgroundTasksOpen = true
			a.backgroundTasksPinned = false
		}).Children(func() {
			a.backgroundTaskCollapsedPill(c)
		})
		return
	}

	groups := a.backgroundTaskGroups()
	contentHeight := float32(0)
	for _, group := range groups {
		contentHeight += 20
		for range group.tasks {
			contentHeight += 76
		}
	}
	panelHeight := min(float32(480), max(float32(120), contentHeight+86))
	panelHeight = min(panelHeight, available)
	ui.Column(c.Key("background-tasks")).Absolute().Right(18).Bottom(bottom).
		Width(panelWidth).Height(panelHeight).AlignItems(ui.Stretch).
		Radius(16).Background(a.pal.sidebar).Border(1, a.pal.hairline).Clip().Children(func() {
		ui.ButtonBase(c.Key("background-task-toggle")).Height(46).PaddingX(16).
			AlignItems(ui.Center).Gap(10).Label(label).OnClick(func() {
			a.backgroundTasksOpen = false
			a.backgroundTasksPinned = true
		}).Children(func() {
			ui.Icon(c, iconQueue).Size(15, 15).TextColor(a.pal.muted)
			ui.Text(c, T("Background Tasks")).FontSize(14).FontWeight(600).TextColor(a.pal.text)
			ui.Box(c).Size(22, 22).Radius(11).Background(a.pal.hover).Center().Children(func() {
				ui.Text(c, fmt.Sprint(len(a.backgroundTasks))).FontSize(11).TextColor(a.pal.muted)
			})
			ui.Box(c).Grow(1)
			ui.Icon(c, iconDown).Size(14, 14).TextColor(a.pal.muted)
		})
		ui.Box(c).Height(1).Background(a.pal.hairline)
		ui.Scroll(c.Key("background-task-list")).Height(max(0, panelHeight-86)).Children(func() {
			ui.Column(c).Children(func() {
				for groupIndex, group := range groups {
					if groupIndex > 0 {
						ui.Box(c).Height(1).Background(a.pal.hairline).Margin(0, 14)
					}
					ui.Row(c).Height(20).PaddingX(14).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Text(c, backgroundTaskStateLabel(group.state)).FontSize(10.5).
							FontWeight(600).TextColor(a.pal.muted)
						ui.Text(c, fmt.Sprint(len(group.tasks))).FontSize(10.5).TextColor(a.pal.faint)
					})
					for index, task := range group.tasks {
						if index > 0 {
							ui.Box(c).Height(1).Background(a.pal.hairline).Margin(0, 14)
						}
						a.backgroundTaskCard(c, task)
					}
				}
			})
		})
		ui.Box(c).Height(1).Background(a.pal.hairline)
		a.backgroundTaskFooter(c)
	})
}

// 收起时用两行说明"哪件事最要紧 + 还剩多少"，点整块区域展开。
func (a *app) backgroundTaskCollapsedPill(c *ui.Context) {
	counts := a.backgroundTaskCounts()
	rep := a.backgroundTaskRep()
	ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
		if rep != nil {
			a.backgroundTaskIcon(c, rep, 14)
			line := backgroundTaskStateLabel(rep.state)
			if rep.title != "" {
				line += " · " + rep.title
			}
			ui.Text(c, line).FontSize(13).FontWeight(600).TextColor(a.pal.text).
				SingleLine().Grow(1).MinWidth(0)
		} else {
			ui.Box(c).Grow(1)
		}
		ui.Box(c).Size(22, 22).Radius(11).Background(a.pal.hover).Center().Children(func() {
			ui.Text(c, fmt.Sprint(len(a.backgroundTasks))).FontSize(11).TextColor(a.pal.muted)
		})
		ui.Icon(c, iconDown).Size(13, 13).Rotate(180).TextColor(a.pal.muted)
	})
	if summary := backgroundTaskCountSummary(counts); summary != "" {
		ui.Text(c, summary).FontSize(11.5).TextColor(a.pal.muted).SingleLine().
			PaddingStart(22).MinWidth(0)
	}
}

func (a *app) backgroundTaskFooter(c *ui.Context) {
	counts := a.backgroundTaskCounts()
	terminal := counts[backgroundTaskSucceeded] + counts[backgroundTaskCanceled]
	ui.Row(c).Height(40).PaddingX(16).AlignItems(ui.Center).Children(func() {
		if terminal > 0 {
			ui.ButtonBase(c.Key("clear-completed")).
				Label(Tf("Clear completed (%d)", terminal)).Padding(0).OnClick(func() {
				a.clearCompletedTasks()
			}).Children(func() {
				ui.Text(c, Tf("Clear completed (%d)", terminal)).FontSize(12).
					FontWeight(500).TextColor(a.pal.text)
			})
		}
		ui.Box(c).Grow(1)
		if len(a.downloads.tasks) > 0 {
			iconButton(c.Key("open-downloads"), iconDownload, 15, a.pal.muted, T("Open Downloads")).
				Tooltip(T("Open Downloads")).OnClick(func() {
				a.open(route{page: pageDownloads})
			})
		}
	})
}

// 清除已完成/已取消只删任务记录，运行中的工作保持不动。
func (a *app) clearCompletedTasks() {
	snapshot := append([]*backgroundTask(nil), a.backgroundTasks...)
	for _, task := range snapshot {
		if task.state != backgroundTaskRunning {
			a.removeBackgroundTask(task)
		}
	}
}

func (a *app) backgroundTaskIcon(c *ui.Context, task *backgroundTask, size float32) {
	switch {
	case task.waiting:
		ui.Icon(c, iconQueue).Size(size, size).TextColor(a.pal.muted)
	case task.state == backgroundTaskRunning:
		ui.Spinner(c).FontSize(size).TextColor(a.pal.accent).Label(task.status)
	case task.state == backgroundTaskSucceeded:
		ui.Icon(c, iconCheck).Size(size, size).TextColor(a.pal.accent)
	case task.state == backgroundTaskFailed:
		ui.Icon(c, iconX).Size(size, size).TextColor(a.pal.accent)
	default:
		ui.Icon(c, iconX).Size(size, size).TextColor(a.pal.muted)
	}
}

func (a *app) backgroundTaskCard(c *ui.Context, task *backgroundTask) {
	ui.Column(c.Key(fmt.Sprintf("background-task-%d", task.id))).
		Padding(8, 14).Gap(3).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
			ui.Box(c).Size(28, 28).Radius(8).Background(a.pal.hover).Center().Shrink(0).Children(func() {
				a.backgroundTaskIcon(c, task, 15)
			})
			ui.ButtonBase(c.Key("open")).Column().Gap(1).Padding(0).AlignItems(ui.Start).
				Label(Tf("Open task: %s", task.title)).Grow(1).MinWidth(0).
				Disabled(task.open == nil).OnClick(task.open).Children(func() {
				ui.Text(c, task.title).FontSize(12.5).FontWeight(600).TextColor(a.pal.text).
					SingleLine().Shrink(1).MinWidth(0)
				ui.Text(c, task.summary).FontSize(11.5).TextColor(a.pal.muted).
					SingleLine().Shrink(1).MinWidth(0)
			})
			if task.state != backgroundTaskRunning {
				ui.ButtonBase(c.Key("remove")).Size(20, 20).Center().Radius(6).
					Label(T("Remove")).OnClick(func() {
					a.removeBackgroundTask(task)
				}).Children(func() {
					ui.Icon(c, iconX).Size(11, 11).TextColor(a.pal.faint)
				})
			}
		})
		ui.Row(c).Height(25).Padding(0, 38).AlignItems(ui.Center).Gap(6).Children(func() {
			color := a.pal.muted
			if task.state == backgroundTaskFailed {
				color = a.pal.accent
			}
			ui.Text(c, task.status).FontSize(11.5).TextColor(color).SingleLine().
				Grow(1).MinWidth(0)
			a.backgroundTaskCardActions(c, task)
		})
		if task.progress != nil && task.state == backgroundTaskRunning {
			ui.Progress(c, max(0, min(1, *task.progress))).Label(task.status)
		}
	})
}

func (a *app) backgroundTaskCardActions(c *ui.Context, task *backgroundTask) {
	primary := (*backgroundTaskAction)(nil)
	primaryLabel := ""
	var more []backgroundTaskAction
	switch task.state {
	case backgroundTaskRunning:
		if task.cancel != nil {
			button := ui.Button(c.Key("cancel"), T("Cancel")).FontSize(12).Padding(4, 10).
				OnClick(task.cancel)
			if task.cancelLabel != "" {
				button.Label(task.cancelLabel)
			}
		}
		// 运行中只有取消，不提供会误删记录的入口。
		return
	case backgroundTaskFailed:
		primary = task.failureAction
		if task.retry != nil {
			retry := backgroundTaskAction{label: T("Retry"), run: task.retry}
			if primary == nil {
				primary, primaryLabel = &retry, task.retryLabel
			} else {
				more = append(more, retry)
			}
		}
	case backgroundTaskSucceeded:
		if len(task.actions) > 0 {
			primary = &task.actions[0]
			more = append(more, task.actions[1:]...)
		}
	case backgroundTaskCanceled:
		if task.retry != nil {
			primary = &backgroundTaskAction{label: T("Retry"), run: task.retry}
			primaryLabel = task.retryLabel
		}
	}
	if primary != nil {
		button := ui.Button(c.Key("primary"), primary.label).FontSize(12).Padding(4, 10)
		if primaryLabel == "" && primary.label == T("Retry") {
			// 与分享编辑页等处的“Retry”区分，辅助技术和无头测试都能定位到这张卡。
			primaryLabel = Tf("Retry task: %s", task.title)
		}
		if primaryLabel != "" {
			button.Label(primaryLabel)
		}
		button.Disabled(primary.disabled != nil && primary.disabled()).OnClick(primary.run)
	}
	if len(more) > 0 {
		a.backgroundTaskMore(c, task, more)
	}
}

func (a *app) backgroundTaskMore(c *ui.Context, task *backgroundTask, actions []backgroundTaskAction) {
	more := ui.ButtonBase(c.Key("more")).Size(23, 23).Center().Radius(7).
		Label(Tf("More actions: %s", task.title)).TextColor(a.pal.muted)
	more.Children(func() {
		ui.Text(c, "⋯").FontSize(15).TextColor(a.pal.muted)
	})
	more.Menu(func(menu *ui.Menu) {
		for _, action := range actions {
			item := menu.Item(action.label)
			if action.disabled != nil && action.disabled() {
				item.Disabled(true)
			}
			if item.Chosen() {
				action.run()
			}
		}
	})
}
