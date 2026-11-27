package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"mygo-music/internal/music"
	"mygo-music/internal/player"
)

var showDownloadedFile = mygo.Shell.ShowItemInFolder

type downloadCenter struct {
	pending []*downloadTask
	active  *downloadTask
	attempt *downloadAttempt
	tasks   []*downloadTask
	history bool
	tab     int               // 下载详情页筛选分段（0=全部 1=进行中 2=已完成 3=失败）。
	epochs  map[string]uint64 // 保存对话框跨越账号或启停变化后，旧结果不再有效。
}

type downloadTask struct {
	card       *backgroundTask
	track      music.Track
	artists    string
	quality    string
	fileType   string
	path       string
	dir        string // 批量与弹窗目录下载在解析音频格式前只持有已校验的目标文件夹。
	customName string // 用户在下载确认弹窗中编辑的基础文件名（不含扩展名）。
	accountID  string
	tempPart   string
}

func (d *downloadTask) targetBaseName() string {
	if d.customName != "" {
		return sanitizeDownloadBaseName(d.customName)
	}
	return songFileBaseName(&d.track)
}

var downloadHistoryPath = func() string { return userDataDir("downloads.json") }

type downloadRecord struct {
	TrackID    string              `json:"trackId"`
	Source     string              `json:"source"`
	Name       string              `json:"name"`
	Artists    string              `json:"artists,omitempty"`
	PlaybackID string              `json:"playbackId,omitempty"`
	AccountID  string              `json:"accountId,omitempty"`
	Quality    string              `json:"quality"`
	FileType   string              `json:"fileType,omitempty"`
	Path       string              `json:"path,omitempty"`
	Dir        string              `json:"dir,omitempty"`
	CustomName string              `json:"customName,omitempty"`
	TempPart   string              `json:"tempPart,omitempty"`
	State      backgroundTaskState `json:"state"`
}

func (a *app) saveDownloads() {
	if !a.downloads.history {
		return
	}
	records := make([]downloadRecord, 0, len(a.downloads.tasks))
	for _, d := range a.downloads.tasks {
		if d.card.removed {
			continue
		}
		records = append(records, downloadRecord{TrackID: d.track.ID, Source: d.track.Source, Name: d.track.Name, Artists: d.artists, PlaybackID: d.track.PlaybackID, AccountID: d.accountID, Quality: d.quality, FileType: d.fileType, Path: d.path, Dir: d.dir, CustomName: d.customName, TempPart: d.tempPart, State: d.card.state})
	}
	data, err := json.Marshal(records)
	if err != nil {
		a.toast = T("Couldn't save download history")
		return
	}
	path := downloadHistoryPath()
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		a.toast = T("Couldn't save download history")
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".downloads-*.tmp")
	if err != nil {
		a.toast = T("Couldn't save download history")
		return
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		a.toast = T("Couldn't save download history")
	}
}

func (a *app) restoreDownloads() {
	a.downloads.history = true
	data, err := os.ReadFile(downloadHistoryPath())
	if err != nil {
		return
	}
	var records []downloadRecord
	if json.Unmarshal(data, &records) != nil {
		return
	}
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.TrackID == "" || r.Source == "" || (r.Path == "" && r.Dir == "") {
			continue
		}
		d := &downloadTask{track: music.Track{ID: r.TrackID, Source: r.Source, Name: r.Name, PlaybackID: r.PlaybackID}, artists: r.Artists, quality: r.Quality, fileType: r.FileType, path: r.Path, dir: r.Dir, customName: r.CustomName, accountID: r.AccountID}
		a.attachDownload(d)
		d.card.state = r.State
		switch r.State {
		case backgroundTaskSucceeded:
			d.card.status = T("Completed")
		case backgroundTaskCanceled:
			d.card.status = T("Canceled")
		case backgroundTaskFailed:
			d.card.status = T("Couldn't download this song")
		default:
			cleanOwnedDownloadTemp(r.Path, r.Dir, r.TempPart)
			d.card.state, d.card.status = backgroundTaskFailed, T("Interrupted")
		}
	}
	if len(records) > 0 {
		a.backgroundTasksOpen = false
	}
}

// cleanOwnedDownloadTemp 只清理历史明确登记过的应用临时文件，绝不碰用户原有文件或符号链接。
func cleanOwnedDownloadTemp(targetPath, dir, tempPart string) {
	if !strings.HasPrefix(tempPart, ".mygo-download-") || !strings.HasSuffix(tempPart, ".part") || strings.ContainsAny(tempPart, `/\`) {
		return
	}
	baseDir := dir
	if targetPath != "" {
		baseDir = filepath.Dir(targetPath)
	}
	if baseDir == "" {
		return
	}
	candidate := filepath.Join(baseDir, tempPart)
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if targetPath != "" {
		if targetInfo, err := os.Lstat(targetPath); err == nil && os.SameFile(info, targetInfo) {
			return
		}
	}
	_ = os.Remove(candidate)
}

func checkNewDownloadTarget(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return errDownloadKept
	case os.IsNotExist(err):
		return nil
	default:
		return err
	}
}

func downloadLocationFeedback(err error) string {
	if errors.Is(err, errDownloadKept) || errors.Is(err, os.ErrExist) {
		return T("File already exists; choose a different name")
	}
	return T("Check the download location")
}

var (
	errDownloadLocation = errors.New("download location changed or unavailable")
	errDownloadKept     = errors.New("existing download target kept")
	errDownloadType     = errors.New("download audio format changed")
)

type downloadAttempt struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex // 取消与最终发布按获取同一把锁的顺序生效。
	saved  bool
}

func (at *downloadAttempt) stop() bool {
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.saved {
		return false
	}
	at.cancel()
	return true
}

func downloadPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// 解析父目录别名，但不跟随目标文件自身的符号链接，以免把写入转向别处。
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// downloadDir 校验批量下载文件夹存在且可作为目录解析真实路径。
func downloadDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errDownloadLocation
	}
	return resolved, nil
}

func sameDownloadPath(x, y string) bool {
	if x == "" || y == "" {
		return false
	}
	if x == y || (runtime.GOOS == "windows" || runtime.GOOS == "darwin") && strings.EqualFold(x, y) {
		return true
	}
	// 文件身份覆盖已存在目标的短路径及其他文件系统别名。
	fi, e1 := os.Lstat(x)
	fj, e2 := os.Lstat(y)
	return e1 == nil && e2 == nil && os.SameFile(fi, fj)
}

func (a *app) downloadTargetInUse(d *downloadTask, path string) bool {
	if path == "" {
		return false
	}
	if active := a.downloads.active; active != nil && active != d && sameDownloadPath(active.path, path) {
		return true
	}
	for _, other := range a.downloads.pending {
		if other != d && other.card.waiting && sameDownloadPath(other.path, path) {
			return true
		}
	}
	return false
}

func sameDownloadBaseName(x, y string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(x, y)
	}
	return x == y
}

func (a *app) batchDownloadInUse(dir, baseName string) bool {
	sameBatch := func(other *downloadTask) bool {
		if other.path != "" {
			otherBase := strings.TrimSuffix(filepath.Base(other.path), filepath.Ext(other.path))
			return sameDownloadPath(filepath.Dir(other.path), dir) && sameDownloadBaseName(otherBase, baseName)
		}
		if other.dir != "" {
			return sameDownloadPath(other.dir, dir) && sameDownloadBaseName(other.targetBaseName(), baseName)
		}
		return false
	}
	if active := a.downloads.active; active != nil && sameBatch(active) {
		return true
	}
	for _, other := range a.downloads.pending {
		if other.card.waiting && sameBatch(other) {
			return true
		}
	}
	return false
}

type batchAddResult uint8

const (
	batchAddQueued batchAddResult = iota
	batchAddDuplicate
	batchAddSkipped
)

func (a *app) addDownload(track music.Track, lib *library, quality, fileType, path, accountID string) {
	if a.quitting {
		return
	}
	if a.downloadTargetInUse(nil, path) {
		a.toast = T("Another download is using this file")
		return
	}
	track.Source = lib.id
	d := &downloadTask{track: track, artists: track.ArtistNames(), quality: quality, fileType: fileType, path: path, accountID: accountID}
	a.attachDownload(d)
	a.enqueueDownload(d)
}

func (a *app) addDirDownload(track music.Track, lib *library, quality, dir, customName, accountID string) batchAddResult {
	if a.quitting || lib == nil || dir == "" {
		return batchAddSkipped
	}
	track.Source = lib.id
	cleanCustom := ""
	if strings.TrimSpace(customName) != "" {
		cleanCustom = sanitizeDownloadBaseName(customName)
	}
	baseName := cleanCustom
	if baseName == "" {
		baseName = songFileBaseName(&track)
	}
	if a.batchDownloadInUse(dir, baseName) {
		return batchAddDuplicate
	}
	d := &downloadTask{track: track, artists: track.ArtistNames(), quality: quality, dir: dir, customName: cleanCustom, accountID: accountID}
	a.attachDownload(d)
	a.enqueueDownload(d)
	return batchAddQueued
}

func (a *app) addBatchDownload(track music.Track, lib *library, quality, dir, accountID string) batchAddResult {
	return a.addDirDownload(track, lib, quality, dir, "", accountID)
}

func (a *app) attachDownload(d *downloadTask) {
	summary := T("Download")
	if d.artists != "" {
		summary += " · " + d.artists
	}
	d.card = a.newBackgroundTask(d.track.Name, summary)
	d.card.kind = backgroundTaskDownload
	d.card.retryLabel = Tf("Retry download: %s", d.track.Name)
	d.card.open = func() { a.open(route{page: pageDownloads}) }
	// 从后台任务层新建的下载若正在详情页展示，不展开右下角重复浮窗。
	if a.route.page == pageDownloads && !a.nowPlaying {
		a.backgroundTasksOpen = false
	}
	a.downloads.tasks = append(a.downloads.tasks, d)
	d.card.retry = func() { a.enqueueDownload(d) }
	d.card.actions = []backgroundTaskAction{{label: T("Show in Folder"), run: func() {
		if d.path == "" {
			d.card.status = T("Downloaded file is missing")
			return
		}
		if _, err := os.Stat(d.path); err != nil {
			d.card.status = T("Downloaded file is missing")
			return
		}
		showDownloadedFile(d.path)
	}}}
}

// 入队和真正执行都核对固定来源，排队期间不能因当前页面变化而换源。
func (a *app) downloadLibrary(d *downloadTask) (*library, string, *backgroundTaskAction) {
	lib := a.libOf(d.track.Source)
	if lib == nil || !lib.can("stream") || lib.src.Info().Playback != music.PlaybackStream {
		return nil, T("Download source is unavailable"), &backgroundTaskAction{label: T("Open Plugins"), run: a.openPlugins}
	}
	if g, ok := lib.src.(guarded); ok && len(g.Permissions().Process) > 0 && !g.Granted() || lib.needsSetup {
		return nil, T("Source needs setup or permission"), &backgroundTaskAction{label: T("Open Plugins"), run: a.openPlugins}
	}
	if lib.accountUnknown() {
		a.loadAccount(lib)
		return nil, T("Checking account status; try again"), nil
	}
	if d.accountID != "" && (lib.account == nil || lib.account.ID != d.accountID) {
		var guide *backgroundTaskAction
		if lib.can("signIn") {
			guide = &backgroundTaskAction{label: T("Sign In"), run: func() { a.promptSignIn(lib) }}
		}
		return nil, T("Sign in to the original account to retry"), guide
	}
	return lib, "", nil
}

func (a *app) downloadStartFailed(lib *library, err error) {
	switch {
	case errors.Is(err, music.ErrSignIn):
		a.promptSignIn(lib)
	case errors.Is(err, music.ErrSetup):
		lib.needsSetup, lib.setupReason = true, music.Reason(err)
		a.openPlugins()
	case errors.Is(err, music.ErrUnavailable):
		a.toast = T("Song unavailable from this source")
	case errors.Is(err, context.DeadlineExceeded):
		a.toast = T("Download timed out; check your network and retry")
	default:
		a.toast = T("Couldn't download this song")
	}
}

func (a *app) downloadFailureDetails(d *downloadTask, err error) (string, *backgroundTaskAction) {
	lib := a.libOf(d.track.Source)
	switch {
	case errors.Is(err, music.ErrSignIn):
		var guide *backgroundTaskAction
		if lib != nil && lib.can("signIn") {
			guide = &backgroundTaskAction{label: T("Sign In"), run: func() { a.promptSignIn(lib) }}
		}
		return T("Sign in required to download"), guide
	case errors.Is(err, music.ErrSetup):
		if lib != nil {
			lib.needsSetup, lib.setupReason = true, music.Reason(err)
		}
		return T("Source needs setup or permission"), &backgroundTaskAction{label: T("Open Plugins"), run: a.openPlugins}
	case errors.Is(err, music.ErrUnavailable):
		return T("Song unavailable from this source"), nil
	case errors.Is(err, context.DeadlineExceeded):
		return T("Download timed out; check your network and retry"), nil
	case errors.Is(err, errDownloadType):
		return T("Audio format changed; choose Download again"), nil
	case errors.Is(err, errDownloadLocation):
		return downloadLocationFeedback(err), nil
	default:
		return T("Couldn't download this song"), nil
	}
}

func (a *app) enqueueDownload(d *downloadTask) {
	card := d.card
	if card.removed || card.waiting || a.downloads.active == d {
		return
	}
	if _, status, guide := a.downloadLibrary(d); status != "" {
		card.state, card.status, card.failureAction = backgroundTaskFailed, status, guide
		a.saveDownloads()
		return
	}
	if a.downloadTargetInUse(d, d.path) {
		a.toast = T("Another download is using this file")
		return
	}
	card.generation++
	card.state, card.status, card.waiting, card.failureAction = backgroundTaskRunning, T("Queued"), true, nil
	card.cancelLabel = Tf("Cancel download: %s", d.track.Name)
	card.retry = func() { a.enqueueDownload(d) }
	card.cancel = func() {
		// 移除旧队列项，避免重试后旧位置让任务插队。
		for i, pending := range a.downloads.pending {
			if pending == d {
				a.downloads.pending = append(a.downloads.pending[:i], a.downloads.pending[i+1:]...)
				break
			}
		}
		card.waiting = false
		card.update(card.generation, backgroundTaskCanceled, T("Canceled"))
		a.saveDownloads()
	}
	a.downloads.pending = append(a.downloads.pending, d)
	a.saveDownloads()
	a.startNextDownload()
}

func (a *app) writeDownload(at *downloadAttempt, d *downloadTask, generation uint64, tempPart, targetPath, expectedType string, song *player.Song) error {
	ctx := at.ctx
	defer song.Close()
	if expectedType != "" && song.Type != expectedType {
		// 类型改变后原扩展名已不可信，必须重新走保存对话框。
		return errDownloadType
	}
	if err := checkNewDownloadTarget(targetPath); err != nil {
		return errors.Join(errDownloadLocation, err)
	}
	partPath := filepath.Join(filepath.Dir(targetPath), tempPart)
	part, err := os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return errors.Join(errDownloadLocation, err)
	}
	defer os.Remove(part.Name())
	var last time.Time
	err = song.WriteDownload(ctx, part, func(written, total int64) {
		if time.Since(last) < 150*time.Millisecond {
			return
		}
		last = time.Now()
		if total <= 0 {
			a.post(func() {
				if d.card.update(generation, backgroundTaskRunning, T("Downloading (size unknown)…")) {
					d.card.progress = nil
				}
			})
			return
		}
		progress := min(0.99, float64(written)/float64(total))
		a.post(func() {
			if d.card.update(generation, backgroundTaskRunning, T("Downloading…")+fmt.Sprintf(" %d%%", int(progress*100))) {
				d.card.progress = &progress
			}
		})
	})
	if closeErr := part.Close(); err == nil && closeErr != nil {
		err = errors.Join(errDownloadLocation, closeErr)
	}
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && pathErr.Path == part.Name() {
			return errors.Join(errDownloadLocation, err)
		}
		return err
	}
	at.mu.Lock()
	defer at.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// shortcut: 暂不替换已有目标，待有按文件身份原子替换的平台能力再开放。
	// 硬链接直接提交完整文件，晚到的同名文件也不会被覆盖。
	if err := os.Link(part.Name(), targetPath); err != nil {
		return errors.Join(errDownloadLocation, err)
	}
	at.saved = true
	return nil
}

// cancelAllDownloads 先清空排队再中止活动任务，避免取消当前项时自动拉起下一首。
func (a *app) cancelAllDownloads() {
	for i := len(a.downloads.pending) - 1; i >= 0; i-- {
		d := a.downloads.pending[i]
		if d.card.cancel != nil {
			d.card.cancel()
		}
	}
	if d := a.downloads.active; d != nil && d.card.cancel != nil {
		d.card.cancel()
	}
}

// retryFailedDownloads 仅重试当前处于失败/中断状态的下载记录。
func (a *app) retryFailedDownloads() {
	snapshot := append([]*downloadTask(nil), a.downloads.tasks...)
	for _, d := range snapshot {
		if d.card.state == backgroundTaskFailed && d.card.retry != nil {
			d.card.retry()
		}
	}
}

// clearFinishedDownloads 只删除非进行中任务的列表与历史记录，绝不删除磁盘音频文件。
func (a *app) clearFinishedDownloads() {
	snapshot := append([]*downloadTask(nil), a.downloads.tasks...)
	for _, d := range snapshot {
		if d.card.state != backgroundTaskRunning {
			a.removeBackgroundTask(d.card)
		}
	}
}

// 来源身份或启用状态变化后，阻止旧尝试继续提交。
func (a *app) invalidateDownloads(source string) {
	if a.downloads.epochs == nil {
		a.downloads.epochs = make(map[string]uint64)
	}
	a.downloads.epochs[source]++
	// 先取消排队项，避免释放活动任务时启动同一失效来源的下一项。
	for i := len(a.downloads.pending) - 1; i >= 0; i-- {
		d := a.downloads.pending[i]
		if d.track.Source == source && d.card.cancel != nil {
			d.card.cancel()
		}
	}
	if d := a.downloads.active; d != nil && d.track.Source == source && d.card.cancel != nil {
		d.card.cancel()
	}
}

func (a *app) startNextDownload() {
	if a.downloads.active != nil || a.quitting {
		return
	}
	for len(a.downloads.pending) > 0 {
		d := a.downloads.pending[0]
		a.downloads.pending[0] = nil
		a.downloads.pending = a.downloads.pending[1:]
		card := d.card
		if !card.waiting {
			continue
		}
		card.waiting = false
		card.status = T("Downloading…")
		deleteGeneration := card.generation
		lib, status, guide := a.downloadLibrary(d)
		if status != "" {
			card.failureAction = guide
			card.update(deleteGeneration, backgroundTaskFailed, status)
			a.saveDownloads()
			continue
		}
		src := lib.src
		ctx, cancel := context.WithCancel(context.Background())
		attempt := &downloadAttempt{ctx: ctx, cancel: cancel}
		card.retryLabel = Tf("Retry download: %s", d.track.Name)
		card.cancelLabel = Tf("Cancel download: %s", d.track.Name)
		a.downloads.active = d
		a.downloads.attempt = attempt
		tempPart := fmt.Sprintf(".mygo-download-%d-%d-%d.part", os.Getpid(), d.card.id, time.Now().UnixNano())
		d.tempPart = tempPart
		a.saveDownloads()
		card.cancel = func() {
			if attempt.stop() {
				d.tempPart = ""
				card.update(deleteGeneration, backgroundTaskCanceled, T("Canceled"))
				a.downloads.active, a.downloads.attempt = nil, nil
				a.saveDownloads()
				a.startNextDownload()
			}
		}
		targetPath, expectedType, targetDir, customName := d.path, d.fileType, d.dir, d.customName
		go func() {
			defer cancel()
			song, err := a.pl.FindSongContext(ctx, src, d.track, d.quality)
			var resolvedPath, resolvedType, resolvedPlaybackID string
			if err == nil {
				resolvedType = song.Type
				resolvedPlaybackID = song.PlaybackID
				if targetPath != "" {
					resolvedPath = targetPath
				} else if targetDir != "" {
					fileName := songFileName(&d.track, song.Type)
					if customName != "" {
						fileName = sanitizeDownloadBaseName(customName) + "." + song.Type
					}
					resolvedPath = filepath.Join(targetDir, fileName)
				}
				if resolvedPath == "" {
					song.Close()
					err = errDownloadLocation
				} else {
					err = a.writeDownload(attempt, d, deleteGeneration, tempPart, resolvedPath, expectedType, song)
				}
			}
			a.post(func() {
				// 已取消的旧尝试可能晚于新任务返回，不能释放新任务的执行槽。
				if a.downloads.attempt != attempt {
					return
				}
				a.downloads.active, a.downloads.attempt = nil, nil
				d.tempPart = ""
				if resolvedPath != "" && d.path == "" {
					d.path = resolvedPath
				}
				if resolvedType != "" && d.fileType == "" {
					d.fileType = resolvedType
				}
				if resolvedPlaybackID != "" && d.track.PlaybackID == "" {
					d.track.PlaybackID = resolvedPlaybackID
				}
				switch {
				case err == nil:
					if card.update(deleteGeneration, backgroundTaskSucceeded, T("Completed")) {
						a.toast = Tf("Downloaded %s", d.track.Name)
					}
				case ctx.Err() != nil && errors.Is(err, context.Canceled):
					card.update(deleteGeneration, backgroundTaskCanceled, T("Canceled"))
				default:
					status, guide := a.downloadFailureDetails(d, err)
					card.failureAction = guide
					card.update(deleteGeneration, backgroundTaskFailed, status)
				}
				a.saveDownloads()
				a.startNextDownload()
			})
		}()
		return
	}
}
