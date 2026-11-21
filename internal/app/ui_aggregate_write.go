package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
)

// aggregateWrite 记录一次待确认的云端写入；确认框关闭或成功后才释放。
type aggregateWrite struct {
	open, busy       bool
	generation       int64
	generationLoaded bool
	lib              *library
	src              aggregateSource
	view             *aggregateView
	source           music.AggregateSource
	platform         string
	accountID        string
	trackName        string
	operation        string
	listID           string // 歌单写入的目标；喜欢操作留空
	listName         string
	name             string // 创建/重命名的草稿，仅当前对话框持有
	position         string
	prevLiked        bool // 失败回滚到确认前的本地状态，不假装写入成功
	err              string
}

type aggregateWriteCapabilityReply struct {
	Version   int `json:"version"`
	Platforms []struct {
		Platform   string   `json:"platform"`
		Available  bool     `json:"available"`
		Operations []string `json:"operations"`
	} `json:"platforms"`
}

type aggregateWriteReply struct {
	Version         int    `json:"version"`
	OK              bool   `json:"ok"`
	Code            string `json:"code"`
	Message         string `json:"message"`
	Platform        string `json:"platform"`
	Operation       string `json:"operation"`
	TrackID         string `json:"trackID"`
	ListID          string `json:"listID"`
	Name            string `json:"name"`
	RefreshRequired bool   `json:"refreshRequired"`
}

// 能力只取插件公开接口；旧插件不认识该动作时隐藏入口，不回退到猜测。
func (a *app) aggregateWriteLoadCapabilities(lib *library, src aggregateSource, view *aggregateView) {
	if view.writeCapsLoaded || view.loadingWriteCaps {
		return
	}
	view.loadingWriteCaps = true
	go func() {
		raw, err := src.Aggregate("writeCapabilities", nil)
		var reply aggregateWriteCapabilityReply
		if err == nil {
			err = json.Unmarshal(raw, &reply)
		}
		caps := map[string]map[string]bool{}
		if err == nil {
			for _, platform := range reply.Platforms {
				operations := map[string]bool{}
				if platform.Available {
					for _, operation := range platform.Operations {
						operations[operation] = true
					}
				}
				caps[platform.Platform] = operations
			}
		}
		a.post(func() {
			// 离页或换来源后，旧能力回执不能出现在另一份浏览视图里。
			if !a.aggregateLibraryCurrent(lib, view) {
				return
			}
			view.loadingWriteCaps = false
			view.writeCaps = caps
			view.writeCapsLoaded = true
		})
	}()
}

func (a *app) aggregateWriteLoadGenerations(lib *library, src aggregateSource, view *aggregateView) {
	if !view.listsLoaded || view.writeGenerationsLoaded || view.loadingWriteGenerations {
		return
	}
	view.loadingWriteGenerations = true
	revision := view.writeGenerationRevision
	accounts := make([]aggregateWriteAccount, 0, len(view.lists))
	seen := map[aggregateWriteAccount]bool{}
	for _, list := range view.lists {
		account := aggregateWriteAccount{platform: list.Platform, accountID: list.AccountID}
		if account.platform == "" || account.accountID == "" || seen[account] {
			continue
		}
		seen[account] = true
		accounts = append(accounts, account)
	}
	go func() {
		generations := make(map[aggregateWriteAccount]int64, len(accounts))
		var firstErr error
		for _, account := range accounts {
			generation, err := aggregateWriteGeneration(src, account.platform, account.accountID)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			generations[account] = generation
		}
		a.post(func() {
			if !a.aggregateLibraryCurrent(lib, view) || view.writeGenerationRevision != revision {
				return
			}
			view.loadingWriteGenerations = false
			view.writeGenerations = generations
			view.writeGenerationsLoaded = true
			view.writeGenerationErr = ""
			if firstErr != nil {
				view.writeGenerationErr = T("Unable to verify cloud account state: ") + firstErr.Error()
			}
		})
	}()
}

func aggregateWriteResetGenerations(view *aggregateView) {
	view.writeGenerationRevision++
	view.loadingWriteGenerations = false
	view.writeGenerationsLoaded = false
	view.writeGenerations = nil
	view.writeGenerationErr = ""
}

func aggregateWriteGenerationKnown(view *aggregateView, account aggregateWriteAccount) bool {
	if view == nil || !view.writeGenerationsLoaded {
		return false
	}
	_, ok := view.writeGenerations[account]
	return ok
}

// 曲目来源行上的入口；菜单项按平台能力过滤，确认前不发出任何写入。
func (a *app) aggregateWriteButton(c *ui.Context, src aggregateSource, view *aggregateView, track *music.Track, source music.AggregateSource) {
	if !view.writeCapsLoaded || !aggregateWriteGenerationKnown(view, aggregateWriteAccount{
		platform: source.Platform, accountID: source.AccountID,
	}) {
		return
	}
	operations := view.writeCaps[source.Platform]
	key := aggregateSourceKey(source)
	liked := view.writeLiked[key]
	if !operations["like"] && !operations["unlike"] && !operations["playlistAddTrack"] {
		return
	}
	// 细节操作收进溢出菜单，保留完整无障碍名称，避免每行被长按钮撑满。
	button := iconButton(c.Key("cloud-actions-"+track.ID), iconMore, 16, a.pal.muted, T("Cloud Actions")+" "+track.Name).Disabled(view.busy)
	button.Menu(func(m *ui.Menu) {
		if liked {
			if m.Item(T("Unlike")).Disabled(!operations["unlike"]).Chosen() {
				a.aggregateWriteStart(src, view, source, track.Name, "unlike")
			}
		} else if m.Item(T("Like")).Disabled(!operations["like"]).Chosen() {
			a.aggregateWriteStart(src, view, source, track.Name, "like")
		}
		if operations["playlistAddTrack"] {
			// 只列同一平台同一账号的可写歌单，避免把曲目加到别的账号。
			playlists := aggregateWritePlaylists(view, source)
			if len(playlists) > 0 {
				item := m.Submenu(T("Add to Playlist"), func(sub *ui.Menu) {
					for _, list := range playlists {
						list := list
						if sub.Item(list.ListName).Chosen() {
							a.aggregateWriteStartPlaylist(src, view, source, track.Name, list)
						}
					}
				})
				item.Disabled(view.busy)
			}
		}
		playlists := aggregateWriteTrackPlaylists(view, track, source)
		if operations["playlistRemoveTrack"] && len(playlists) == 1 {
			list := playlists[0]
			if m.Item(T("Remove from Playlist")).Chosen() {
				a.aggregateWriteStartRemoveTrack(src, view, source, track.Name, list)
			}
		} else if operations["playlistRemoveTrack"] && len(playlists) > 1 {
			item := m.Submenu(T("Remove from Playlist"), func(sub *ui.Menu) {
				for _, list := range playlists {
					list := list
					if sub.Item(list.ListName).Chosen() {
						a.aggregateWriteStartRemoveTrack(src, view, source, track.Name, list)
					}
				}
			})
			item.Disabled(view.busy)
		}
		if operations["playlistMoveTrack"] && len(playlists) > 0 {
			item := m.Submenu(T("Move in Playlist"), func(sub *ui.Menu) {
				for _, list := range playlists {
					list := list
					if sub.Item(list.ListName).Chosen() {
						a.aggregateWriteStartMove(src, view, source, track.Name, list)
					}
				}
			})
			item.Disabled(view.busy)
		}
	})
}

// 只允许操作当前曲目来源快照明确列出的同账号普通歌单。
func aggregateWriteTrackPlaylists(view *aggregateView, track *music.Track, source music.AggregateSource) []aggregateList {
	memberships := map[string]bool{}
	for _, member := range track.AggregateSources {
		if sameAggregateSource(member, source) && member.ListID != "" && member.ListID != "liked" {
			memberships[member.ListID] = true
		}
	}
	lists := make([]aggregateList, 0, len(memberships))
	for _, list := range view.lists {
		if list.Platform == source.Platform && list.AccountID == source.AccountID && memberships[list.ListID] {
			lists = append(lists, list)
		}
	}
	return lists
}

// 当前曲目的移除目标直接采用 UI 已选中的完整歌单身份。
func (a *app) aggregateWriteStartRemoveTrack(src aggregateSource, view *aggregateView, source music.AggregateSource, trackName string, list aggregateList) {
	a.aggregateWriteStart(src, view, source, trackName, "playlistRemoveTrack")
	a.aggregateWrite.listID, a.aggregateWrite.listName = list.ListID, list.ListName
}

// 当前歌单和“喜欢”列表不作为添加目标，重复添加只会得到 unchanged。
func aggregateWritePlaylists(view *aggregateView, source music.AggregateSource) []aggregateList {
	lists := make([]aggregateList, 0, len(view.lists))
	for _, list := range view.lists {
		if list.Platform != source.Platform || list.AccountID != source.AccountID {
			continue
		}
		if list.ListID == "liked" || list.ListID == source.ListID {
			continue
		}
		lists = append(lists, list)
	}
	return lists
}

func (a *app) aggregateWriteStart(src aggregateSource, view *aggregateView, source music.AggregateSource, trackName, operation string) {
	account := aggregateWriteAccount{platform: source.Platform, accountID: source.AccountID}
	generation, generationLoaded := view.writeGenerations[account]
	a.aggregateWrite = aggregateWrite{
		open:             true,
		generation:       generation,
		generationLoaded: generationLoaded,
		src:              src,
		view:             view,
		source:           source,
		platform:         source.Platform,
		accountID:        source.AccountID,
		trackName:        trackName,
		operation:        operation,
		prevLiked:        view.writeLiked[aggregateSourceKey(source)],
	}
}

type aggregateWriteAccount struct {
	platform, accountID string
}

// 只列出插件声明支持创建歌单的账号，避免用户点进去才看到不支持。
func aggregateWriteAccounts(view *aggregateView) []aggregateWriteAccount {
	accounts := make([]aggregateWriteAccount, 0, len(view.lists))
	seen := map[aggregateWriteAccount]bool{}
	for _, list := range view.lists {
		account := aggregateWriteAccount{platform: list.Platform, accountID: list.AccountID}
		if seen[account] || account.platform == "" || account.accountID == "" ||
			!view.writeCaps[account.platform]["playlistCreate"] || !aggregateWriteGenerationKnown(view, account) {
			continue
		}
		seen[account] = true
		accounts = append(accounts, account)
	}
	return accounts
}

func (a *app) aggregateWriteStartCreate(src aggregateSource, view *aggregateView, account aggregateWriteAccount) {
	generation, generationLoaded := view.writeGenerations[account]
	a.aggregateWrite = aggregateWrite{
		open:             true,
		generation:       generation,
		generationLoaded: generationLoaded,
		src:              src,
		view:             view,
		platform:         account.platform,
		accountID:        account.accountID,
		operation:        "playlistCreate",
	}
}

func (a *app) aggregateWriteStartList(src aggregateSource, view *aggregateView, list aggregateList, operation string) {
	account := aggregateWriteAccount{platform: list.Platform, accountID: list.AccountID}
	generation, generationLoaded := view.writeGenerations[account]
	a.aggregateWrite = aggregateWrite{
		open:             true,
		generation:       generation,
		generationLoaded: generationLoaded,
		src:              src,
		view:             view,
		platform:         list.Platform,
		accountID:        list.AccountID,
		operation:        operation,
		listID:           list.ListID,
		listName:         list.ListName,
	}
}

func (a *app) aggregateWriteStartPlaylist(src aggregateSource, view *aggregateView, source music.AggregateSource, trackName string, list aggregateList) {
	a.aggregateWriteStart(src, view, source, trackName, "playlistAddTrack")
	a.aggregateWrite.listID, a.aggregateWrite.listName = list.ListID, list.ListName
}

func (a *app) aggregateWriteStartMove(src aggregateSource, view *aggregateView, source music.AggregateSource, trackName string, list aggregateList) {
	a.aggregateWriteStart(src, view, source, trackName, "playlistMoveTrack")
	a.aggregateWrite.listID, a.aggregateWrite.listName = list.ListID, list.ListName
}

func aggregateWriteActionName(operation string) string {
	switch operation {
	case "like":
		return T("Like")
	case "unlike":
		return T("Unlike")
	case "playlistAddTrack":
		return T("Add")
	case "playlistCreate":
		return T("Create")
	case "playlistRename":
		return T("Rename")
	case "playlistDelete":
		return T("Delete")
	case "playlistRemoveTrack":
		return T("Remove")
	case "playlistMoveTrack":
		return T("Move")
	default:
		return T("Confirm")
	}
}

// 使用打开确认框时捕获的账号世代；重新登录后旧确认不能复用新会话。
func (a *app) aggregateWriteConfirm() {
	w := &a.aggregateWrite
	if !w.open || w.busy {
		return
	}
	w.busy, w.err = true, ""
	if !w.generationLoaded {
		w.busy = false
		w.err = T("Unable to verify cloud account state. Refresh the cached lists and try again.")
		return
	}
	view, source, operation := w.view, w.source, w.operation
	platform, accountID := w.platform, w.accountID
	generation := w.generation
	listID, listName := w.listID, w.listName
	name := strings.TrimSpace(w.name)
	toIndex := 0
	if operation == "playlistCreate" || operation == "playlistRename" {
		if name == "" {
			w.busy = false
			w.err = T("Enter a playlist name.")
			return
		}
	}
	if operation == "playlistMoveTrack" {
		position, err := strconv.Atoi(strings.TrimSpace(w.position))
		if err != nil || position < 1 {
			w.busy = false
			w.err = T("Enter a position from 1.")
			return
		}
		// 界面按 1 开始计数，插件契约的 toIndex 从 0 开始。
		toIndex = position - 1
	}
	key := aggregateSourceKey(source)
	switch operation {
	case "like":
		aggregateWriteSetLiked(view, key, true)
	case "unlike":
		aggregateWriteSetLiked(view, key, false)
	}
	previous := w.prevLiked
	src := a.aggregateWriteSource()
	go func() {
		var err error
		var reply aggregateWriteReply
		if err == nil {
			input := map[string]any{
				"version":    1,
				"platform":   platform,
				"accountID":  accountID,
				"operation":  operation,
				"generation": generation,
			}
			if source.TrackID != "" {
				// 这里必须传原始平台身份；聚合曲目 ID 只用于浏览和播放。
				input["source"] = map[string]any{
					"platform":  source.Platform,
					"accountID": source.AccountID,
					"trackID":   source.TrackID,
				}
			}
			if listID != "" {
				input["listID"] = listID
			}
			switch operation {
			case "playlistCreate", "playlistRename":
				input["name"] = name
			case "playlistDelete":
				input["confirm"] = true
			case "playlistMoveTrack":
				input["toIndex"] = toIndex
			}
			var raw json.RawMessage
			raw, err = src.Aggregate("write", input)
			if err == nil {
				err = json.Unmarshal(raw, &reply)
			}
		}
		a.post(func() {
			current := &a.aggregateWrite
			// 只接受同一个曲目、同一个操作的旧回执，避免覆盖随后发起的新写入。
			if !current.open || current.platform != platform || current.accountID != accountID ||
				current.operation != operation || current.listID != listID || current.source.TrackID != source.TrackID {
				return
			}
			current.busy = false
			if err == nil && reply.OK && (reply.Code == "" || reply.Code == "done" || reply.Code == "unchanged") {
				current.open = false
				switch operation {
				case "like":
					aggregateWriteSetLiked(view, key, true)
				case "unlike":
					aggregateWriteSetLiked(view, key, false)
				case "playlistCreate":
					if reply.ListID == "" || reply.RefreshRequired {
						a.toast = T("Playlist created; refresh cloud lists before using it.")
					} else {
						aggregateWriteUpsertList(view, reply.ListID, name, platform, accountID)
						a.toast = T("Created") + " " + name
					}
				case "playlistRename":
					aggregateWriteRenameList(view, platform, accountID, listID, name)
					a.toast = T("Renamed") + " " + name
				case "playlistDelete":
					aggregateWriteRemoveList(view, platform, accountID, listID)
					a.toast = T("Deleted") + " " + listName
				case "playlistAddTrack":
					a.toast = T("Added") + " " + listName
				case "playlistRemoveTrack":
					a.toast = T("Removed") + " " + listName
				case "playlistMoveTrack":
					a.toast = T("Moved") + " " + listName
				}
				return
			}
			// 失败保留确认框和旧本地状态，让用户能直接重试或取消。
			if aggregateWriteTouchesLiked(operation) {
				aggregateWriteSetLiked(view, key, previous)
			}
			code := reply.Code
			if err != nil && code == "" {
				code = "error"
			}
			current.err = aggregateWriteFailure(code, reply.Message, err)
		})
	}()
}

// 测试和真实页面都只经过同一个来源接口；这里不缓存跨页面来源，避免写入错账号。
func (a *app) aggregateWriteSource() aggregateSource {
	if w := &a.aggregateWrite; w.src != nil {
		return w.src
	}
	return nil
}

func aggregateWriteGeneration(src aggregateSource, platform, accountID string) (int64, error) {
	if src == nil {
		return 0, fmt.Errorf("aggregate source unavailable")
	}
	raw, err := src.Aggregate("snapshotGeneration", map[string]any{
		"platform":  platform,
		"accountID": accountID,
	})
	if err != nil {
		return 0, err
	}
	var reply struct {
		Generation int64 `json:"generation"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return 0, err
	}
	return reply.Generation, nil
}

// 只有喜欢状态需要乐观更新和回滚；歌单写入不能连带改喜欢状态。
func aggregateWriteTouchesLiked(operation string) bool {
	return operation == "like" || operation == "unlike"
}

func aggregateWriteSetLiked(view *aggregateView, key string, liked bool) {
	if view == nil {
		return
	}
	if view.writeLiked == nil {
		view.writeLiked = map[string]bool{}
	}
	view.writeLiked[key] = liked
}

func (a *app) aggregateWriteSyncLiked(view *aggregateView, home *music.Browse) {
	liked := map[string]bool{}
	if home != nil {
		for _, section := range home.Sections {
			for _, track := range section.Tracks {
				for _, source := range track.AggregateSources {
					key := aggregateSourceKey(source)
					if source.ListID == "liked" {
						liked[key] = true
					} else if _, exists := liked[key]; !exists {
						liked[key] = false
					}
				}
			}
		}
	}
	if write := &a.aggregateWrite; write.view == view && write.busy && aggregateWriteTouchesLiked(write.operation) {
		key := aggregateSourceKey(write.source)
		if current, exists := view.writeLiked[key]; exists {
			liked[key] = current
		}
	}
	view.writeLikedHome, view.writeLiked = home, liked
}

// 歌单变更只在远端确认成功后落本地，失败时旧列表保持不变。
func aggregateWriteUpsertList(view *aggregateView, listID, name, platform, accountID string) {
	if view == nil || listID == "" {
		return
	}
	for i := range view.lists {
		list := &view.lists[i]
		if list.Platform == platform && list.AccountID == accountID && list.ListID == listID {
			list.ListName = name
			return
		}
	}
	view.lists = append(view.lists, aggregateList{
		Platform: platform, AccountID: accountID, ListID: listID, ListName: name,
	})
}

func aggregateWriteRenameList(view *aggregateView, platform, accountID, listID, name string) {
	if view == nil {
		return
	}
	for i := range view.lists {
		list := &view.lists[i]
		if list.Platform == platform && list.AccountID == accountID && list.ListID == listID {
			list.ListName = name
			return
		}
	}
}

func aggregateWriteRemoveList(view *aggregateView, platform, accountID, listID string) {
	if view == nil {
		return
	}
	kept := view.lists[:0]
	for _, list := range view.lists {
		if list.Platform == platform && list.AccountID == accountID && list.ListID == listID {
			continue
		}
		kept = append(kept, list)
	}
	view.lists = kept
}

func aggregateWriteFailure(code, message string, cause error) string {
	switch code {
	case "accountInvalid":
		return T("Account signed out. Sign in again, then retry.")
	case "unsupported":
		return T("This platform does not support that action.")
	case "conflict":
		return T("The cloud changed while you were confirming. Refresh and try again.")
	case "refreshRequired":
		return T("The cached lists are out of date. Refresh cloud lists and try again.")
	case "confirmationRequired":
		return T("The platform needs another confirmation. Review the refreshed state and try again.")
	case "trackNotWritable":
		return T("This track can't be changed in your account.")
	case "invalidInput":
		return T("The platform rejected this request.")
	}
	if cause != nil {
		return T("The write failed. You can retry: ") + cause.Error()
	}
	if strings.TrimSpace(message) != "" {
		return T("The write failed. You can retry: ") + message
	}
	return T("The write failed. You can retry.")
}

// 操作前显示平台、账号和曲目；写入中禁用重复提交，失败结果留在框内。
func (a *app) aggregateWriteDialog(c *ui.Context) {
	w := &a.aggregateWrite
	p := a.pal
	ui.DialogBase(c.Key("aggregate-write"), &w.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(420).Padding(24).Radius(18).Gap(10).
			Background(p.bg).Border(1, p.hairline).Shadow(0, 24, 60, -12, ui.RGBA(0, 0, 0, 0.4)).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.94}, Exit: &ui.Motion{Scale: 0.96}})
		// 标题固定，避免与确认按钮同名导致辅助技术/测试命中错误元素。
		ui.Text(c, T("Confirm Cloud Action")).FontSize(17).FontWeight(700)
		ui.Text(c, aggregatePlatformName(w.platform)+" · "+w.accountID).FontSize(13).TextColor(p.muted)
		if w.trackName != "" {
			ui.Text(c, w.trackName).FontSize(14).FontWeight(500)
		}
		if w.listName != "" {
			ui.Text(c, w.listName).FontSize(14).FontWeight(500)
		}
		// 名称和位置只在对应操作出现，避免无关输入框诱导用户修改别的字段。
		if w.operation == "playlistCreate" || w.operation == "playlistRename" {
			ui.TextInput(c.Key("playlist-name"), &w.name).Label(T("Playlist Name")).Width(280).Disabled(w.busy)
		}
		if w.operation == "playlistMoveTrack" {
			ui.TextInput(c.Key("playlist-position"), &w.position).Label(T("Position")).Width(160).Disabled(w.busy)
		}
		if w.busy {
			ui.Text(c, T("Writing to your account…")).FontSize(12).TextColor(p.muted)
		}
		if w.err != "" {
			ui.Text(c, w.err).FontSize(12).TextColor(p.accent).LineHeight(1.4)
		}
		ui.Row(c).Gap(10).Justify(ui.End).Margin(8, 0, 0, 0).Children(func() {
			if pillButton(c.Key("cancel"), nil, T("Cancel"), p.hover.Over(p.bg), p.text).Disabled(w.busy).Clicked() {
				// 取消也要回滚尚未确认成功的乐观状态。
				if aggregateWriteTouchesLiked(w.operation) {
					aggregateWriteSetLiked(w.view, aggregateSourceKey(w.source), w.prevLiked)
				}
				w.open = false
			}
			label := aggregateWriteActionName(w.operation)
			if w.busy {
				label = T("Writing…")
			}
			if pillButton(c.Key("ok"), nil, label, p.accent, p.onAccent).Disabled(w.busy).Clicked() {
				a.aggregateWriteConfirm()
			}
		})
	})
}
