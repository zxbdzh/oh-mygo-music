package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/local"
	"mygo-music/internal/music"
	"mygo-music/internal/plugin"
)

func downloadTestView(a *app, tracks ...music.Track) *ui.Tester {
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "Downloads", Tracks: tracks}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	return tt
}

func clickDownloadUI(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	tt.Frame()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func useDirectDownloadDir(a *app, dir string) {
	a.settings.PromptDownload = false
	a.settings.DownloadDirectory = dir
}

func chooseDownload(t *testing.T, tt *ui.Tester, title string) {
	t.Helper()
	if err := tt.RightClick(title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Download…"); err != nil {
		t.Fatal(err)
	}
}

// 关闭下载确认弹窗时，曲目菜单会按默认文件夹静默完成下载并保存完整音频。
func TestDownloadTaskCompletesFromSongMenu(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("complete audio"))
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "Artist - First song.mp3")
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song", "Artist"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return a.toast == "Downloaded First song" })
	tt.Frame()
	if _, ok := tt.Find("Open task: First song"); !ok {
		t.Fatal("completed download is missing from the task panel")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "complete audio" {
		t.Fatalf("saved %q, %v", data, err)
	}
}

// 重启后未完成的下载显示为中断，用户可以手动重新尝试。
func TestDownloadTaskRestoresInterrupted(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	dir := t.TempDir()
	history := downloadHistoryPath
	downloadHistoryPath = func() string { return filepath.Join(dir, "history.json") }
	defer func() { downloadHistoryPath = history }()
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	a.restoreDownloads()
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "download transfer")
	a.stopBackgroundTasks()
	b := newTestApp()
	b.restoreDownloads()
	view := downloadTestView(b, song("one", "First song"))
	clickDownloadUI(t, view, "Background Tasks (1)")
	if _, ok := view.Find("Interrupted"); !ok {
		t.Fatal("interrupted download was not restored")
	}
	if _, ok := view.Find("Retry"); !ok {
		t.Fatal("restored download cannot be retried")
	}
}

// 历史保存失败必须提示用户，但不能阻止音频文件本身安全保存。
func TestDownloadTaskReportsHistoryFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	history := downloadHistoryPath
	downloadHistoryPath = func() string { return filepath.Join(blocked, "history.json") }
	defer func() { downloadHistoryPath = history }()
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	a.restoreDownloads()
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool {
		return len(a.backgroundTasks) == 1 && a.backgroundTasks[0].state == backgroundTaskSucceeded
	})
	if a.toast != "Couldn't save download history" {
		t.Fatalf("history failure was hidden: %q", a.toast)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "audio" {
		t.Fatalf("history failure stopped the audio save: %q, %v", data, err)
	}
}

// 删除下载记录后重启不会恢复记录，也不会删除已保存的音乐。
func TestDownloadTaskRemoveHistoryKeepsFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	history := downloadHistoryPath
	downloadHistoryPath = func() string { return filepath.Join(dir, "history.json") }
	defer func() { downloadHistoryPath = history }()
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	a.restoreDownloads()
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return a.toast == "Downloaded First song" })
	clickDownloadUI(t, tt, "Remove")
	b := newTestApp()
	b.restoreDownloads()
	if len(b.backgroundTasks) != 0 {
		t.Fatal("removed download was restored")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "audio" {
		t.Fatalf("saved music was changed: %q, %v", data, err)
	}
}

// 失败的下载可从任务面板重试并保存音频。
func TestDownloadTaskRetriesFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 && a.backgroundTasks[0].state == backgroundTaskFailed })
	clickDownloadUI(t, tt, "Retry")
	until(t, ch, func() bool { return a.backgroundTasks[0].state == backgroundTaskSucceeded })
	if data, err := os.ReadFile(path); err != nil || string(data) != "audio" {
		t.Fatalf("retry saved %q, %v", data, err)
	}
}

// 注销账号时取消正在传输的下载，避免继续使用旧授权提交文件。
func TestDownloadTaskStopsOnSignOut(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
			canceled <- struct{}{}
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release) // 断言失败时也能释放测试服务，避免掩盖真正的错误。
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	a.account = &music.Account{ID: "account-one", Name: "Listener"}
	a.accountAsked = true
	var signedOut atomic.Bool
	fake(a).account = func() (*music.Account, error) {
		if signedOut.Load() {
			return nil, nil
		}
		return &music.Account{ID: "account-one", Name: "Listener"}, nil
	}
	fake(a).signOut = func() error { signedOut.Store(true); return nil }
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	if a.downloads.tasks[0].accountID != "account-one" {
		t.Fatalf("account was not captured: %q", a.downloads.tasks[0].accountID)
	}
	wait(t, started, "download transfer")
	defer a.stopBackgroundTasks()
	clickDownloadUI(t, tt, "Listener")
	clickDownloadUI(t, tt, "Sign Out")
	until(t, ch, func() bool {
		return a.downloads.active == nil && a.backgroundTasks[0].state != backgroundTaskRunning
	})
	wait(t, canceled, "the canceled download request")
	settled(tt)
	clickDownloadUI(t, tt, "Retry download: First song")
	tt.Frame()
	if a.backgroundTasks[0].status != "Sign in to the original account to retry" {
		t.Fatalf("missing original account feedback: %q, account=%+v, taskAccount=%q, active=%v", a.backgroundTasks[0].status, a.account, a.downloads.tasks[0].accountID, a.downloads.active != nil)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("signed-out download created a file: %v", err)
	}
}

// 在设置里停用来源会停止传输；重新启用也不会自动恢复旧任务。
func TestDownloadTaskStopsWhenSourceDisabled(t *testing.T) {
	started, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
			canceled <- struct{}{}
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	addFake(a, newFakeSource("other"))
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "download transfer")
	defer a.stopBackgroundTasks()
	settings := pluginsView(a, 1400)
	clickDownloadUI(t, settings, "Enable fake")
	until(t, ch, func() bool { return a.downloads.active == nil })
	wait(t, canceled, "disabled source request cancellation")
	if a.backgroundTasks[0].state != backgroundTaskCanceled {
		t.Fatal("disabled source download was not canceled")
	}
	clickDownloadUI(t, settings, "Enable fake")
	if a.downloads.active != nil || len(a.downloads.pending) != 0 {
		t.Fatal("enabling the source restarted the old download")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled source published audio: %v", err)
	}
}

// 确认弹窗浏览文件夹或等待确认期间注销账号，不能在旧账号下创建下载任务。
func TestDownloadTaskRejectsSignOutDuringSaveDialog(t *testing.T) {
	dir := t.TempDir()
	opened, release := make(chan struct{}), make(chan struct{})
	original := pickDownloadFolder
	pickDownloadFolder = func(*app) string {
		close(opened)
		<-release
		return dir
	}
	defer func() { pickDownloadFolder = original }()
	a := newTestApp()
	a.settings.DownloadDirectory = dir
	a.account = &music.Account{ID: "account-one", Name: "Listener"}
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: "http://127.0.0.1/unused", Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	tt.Frame()
	clickDownloadUI(t, tt, "Browse…")
	wait(t, opened, "folder picker")
	a.signOut(a.libOf("fake"))
	close(release)
	runPost(t, ch)
	clickDownloadUI(t, tt, "Download")
	if len(a.backgroundTasks) != 0 || !tt.HasText("Download source changed; choose Download again") {
		t.Fatalf("stale dialog created a task or missed warning: tasks=%d, texts=%q", len(a.backgroundTasks), tt.Texts())
	}
}

// 预绑定文件类型与执行时重解析结果不一致时，不得把不同格式的音频保存到旧扩展名。
func TestDownloadTaskRejectsChangedFileType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "different format")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "song.mp3")
	a := newTestApp()
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "flac"}, nil
	}
	a.addDownload(song("one", "First song"), a.libOf("fake"), "high", "mp3", path, "")
	until(t, ch, func() bool {
		return len(a.backgroundTasks) == 1 && a.backgroundTasks[0].state != backgroundTaskRunning
	})
	if a.backgroundTasks[0].status != "Audio format changed; choose Download again" {
		t.Fatalf("format change was not explained: %q", a.backgroundTasks[0].status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("changed format was published: %v", err)
	}
}

// 完成的下载可以定位文件，文件被移走时给出提示。
func TestDownloadTaskOpensSavedFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	show := showDownloadedFile
	opened := ""
	showDownloadedFile = func(path string) { opened = path }
	defer func() { showDownloadedFile = show }()
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return a.toast == "Downloaded First song" })
	clickDownloadUI(t, tt, "Show in Folder")
	if !sameDownloadPath(opened, path) {
		t.Fatalf("opened %q, want %q", opened, path)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clickDownloadUI(t, tt, "Show in Folder")
	if a.backgroundTasks[0].status != "Downloaded file is missing" {
		t.Fatalf("missing file feedback: %q", a.backgroundTasks[0].status)
	}
}

// 无法安全替换已有文件时，保留原文件并提示更换名称。
func TestDownloadTaskKeepsExistingTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "new audio")
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool {
		return len(a.backgroundTasks) == 1 && a.backgroundTasks[0].state == backgroundTaskFailed
	})
	if a.backgroundTasks[0].status != "File already exists; choose a different name" {
		t.Fatalf("existing target feedback: %q", a.backgroundTasks[0].status)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "original" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}
}

// 传输开始后新建的同名文件也不能被覆盖。
func TestDownloadTaskKeepsTargetCreatedDuringTransfer(t *testing.T) {
	release := make(chan struct{}, 1)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			_, _ = io.WriteString(w, "downloaded audio")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "the transfer after choosing an existing file")
	defer a.backgroundTasks[0].cancel()
	if err := os.WriteFile(path, []byte("user created the file"), 0o600); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	until(t, ch, func() bool { return a.backgroundTasks[0].state != backgroundTaskRunning })
	if status := a.backgroundTasks[0].status; status != "File already exists; choose a different name" {
		t.Fatalf("changed target feedback: %q", status)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "user created the file" {
		t.Fatalf("download overwrote a file created during transfer: %q, %v", data, err)
	}
}

// 已知长度的流显示按字节计算的进度，未知长度时仍能看见传输状态。
func TestDownloadTaskShowsTransferProgress(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "known", false: "unknown"}[known], func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if known {
					w.Header().Set("Content-Length", "8")
				}
				_, _ = io.WriteString(w, "half")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			dir := t.TempDir()
			a := newTestApp()
			useDirectDownloadDir(a, dir)
			ch := posts(a)
			fake(a).stream = func(_, _ string) (*music.Stream, error) {
				return &music.Stream{URL: server.URL, Type: "mp3"}, nil
			}
			tt := downloadTestView(a, song("one", "First song"))
			chooseDownload(t, tt, "First song")
			until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
			defer a.backgroundTasks[0].cancel()
			wait(t, started, "half of the stream")
			want := "Downloading…"
			if known {
				want = "Downloading… 50%"
				until(t, ch, func() bool { return a.backgroundTasks[0].status == want })
			}
			tt.Frame()
			if _, ok := tt.Find(want); !ok {
				t.Fatalf("the active download did not show %q", want)
			}
			clickDownloadUI(t, tt, "Cancel download: First song")
			until(t, ch, func() bool { return a.backgroundTasks[0].state != backgroundTaskRunning })
		})
	}
}

// 同一目标名称已被下载占用时，第二首在确认弹窗中改用相同目标名称会收到冲突反馈。
func TestDownloadTaskRejectsDuplicateTarget(t *testing.T) {
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	dir := t.TempDir()
	a := newTestApp()
	a.settings.DownloadDirectory = dir
	t.Cleanup(func() { a.stopBackgroundTasks() })
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"), song("two", "Second song"))
	chooseDownload(t, tt, "First song")
	tt.Frame()
	a.downloadPrompt.fileName = "song"
	clickDownloadUI(t, tt, "Download")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "the first transfer")
	defer a.backgroundTasks[0].cancel()
	tt.Frame()
	chooseDownload(t, tt, "Second song")
	tt.Frame()
	a.downloadPrompt.fileName = "song"
	clickDownloadUI(t, tt, "Download")
	if !tt.HasText("Another download is using this file") || len(a.backgroundTasks) != 1 {
		t.Fatalf("duplicate path was accepted: texts=%q, %d tasks", tt.Texts(), len(a.backgroundTasks))
	}
}

// 旧音源解析不响应取消时，下一任务仍可运行，迟到结果不能影响新任务。
func TestDownloadTaskCancelUnblocksQueue(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	secondStarted, finishSecond := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(secondStarted)
		<-finishSecond
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-finishSecond:
		default:
			close(finishSecond)
		}
	}()
	a := newTestApp()
	ch := posts(a)
	var calls atomic.Int32
	fake(a).stream = func(id, _ string) (*music.Stream, error) {
		if id == "one" && calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	dir := t.TempDir()
	lib := a.libOf("fake")
	a.addDownload(song("one", "First song"), lib, "", "mp3", filepath.Join(dir, "one.mp3"), "")
	wait(t, entered, "blocked source")
	a.addDownload(song("two", "Second song"), lib, "", "mp3", filepath.Join(dir, "two.mp3"), "")
	a.downloads.tasks[0].card.cancel()
	wait(t, secondStarted, "second transfer")
	close(release)
	runPost(t, ch)
	if a.downloads.active != a.downloads.tasks[1] {
		t.Fatal("late canceled attempt released the current task")
	}
	close(finishSecond)
	until(t, ch, func() bool { return a.downloads.tasks[1].card.state == backgroundTaskSucceeded })
	if _, err := os.Stat(filepath.Join(dir, "one.mp3")); !os.IsNotExist(err) {
		t.Fatalf("canceled download published a file: %v", err)
	}
}

// 第一个下载未结束时，取消第二条排队记录不会请求第二首的音频。
func TestDownloadTaskCancelsWaitingSong(t *testing.T) {
	first := make(chan struct{}, 1)
	second := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/one" {
			first <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		} else {
			second <- struct{}{}
		}
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	dir := t.TempDir()
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	fake(a).stream = func(id, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL + "/" + id, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song", "Artist"), song("two", "Second song", "Artist"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, first, "the first transfer")
	tt.Frame()
	chooseDownload(t, tt, "Second song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 2 })
	clickDownloadUI(t, tt, "Cancel download: Second song")
	close(release)
	until(t, ch, func() bool { return a.toast == "Downloaded First song" })
	tt.Frame()
	if _, ok := tt.Find("Retry"); !ok {
		t.Fatal("the canceled download cannot be retried")
	}
	select {
	case <-second:
		t.Fatal("the queued download started despite cancellation")
	default:
	}
	if _, err := os.Stat(filepath.Join(dir, "Artist - Second song.mp3")); !os.IsNotExist(err) {
		t.Fatalf("the canceled download left a target: %v", err)
	}
}

// 仅支持流式且可用的曲目提供下载入口，本地、远程与不可用曲目不展示下载。
func TestDownloadTaskEligibilityBySourceAndTrack(t *testing.T) {
	a := newTestApp()
	a.local = local.New(t.TempDir())
	localLib := newLibrary(a.local)
	remoteSrc := newFakeSource("remote")
	remoteSrc.info.Playback = music.PlaybackRemote
	remoteSrc.caps["stream"] = false
	remoteLib := addFake(a, remoteSrc)
	a.allLibs = append(a.allLibs, localLib)
	a.libs = append(a.libs, localLib)

	localTrack := song("loc1", "Local Song")
	localTrack.Source = local.ID
	remoteTrack := song("rem1", "Remote Song")
	remoteTrack.Source = remoteLib.id
	unavailTrack := song("un1", "Unavailable Song")
	unavailTrack.Source = "fake"
	unavailTrack.Unavailable = true
	okTrack := song("ok1", "Streamable Song")
	okTrack.Source = "fake"

	tt := downloadTestView(a, localTrack, remoteTrack, unavailTrack, okTrack)
	for _, hidden := range []string{"Local Song", "Remote Song", "Unavailable Song"} {
		if err := tt.RightClick(hidden); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("Download…"); err == nil {
			t.Fatalf("%s unexpectedly offered Download…", hidden)
		}
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
	}
	if err := tt.RightClick("Streamable Song"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Download…"); err != nil {
		t.Fatalf("streamable track did not offer Download…: %v", err)
	}
}

// 取消下载确认弹窗不会创建后台下载任务，也不发起音频解析请求。
func TestDownloadTaskSaveDialogCancelCreatesNoRecord(t *testing.T) {
	a := newTestApp()
	var calls atomic.Int32
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		calls.Add(1)
		return &music.Stream{URL: "http://127.0.0.1/unused", Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"))
	chooseDownload(t, tt, "First song")
	tt.Frame()
	if !a.downloadPrompt.open {
		t.Fatal("expected download prompt dialog to open")
	}
	clickDownloadUI(t, tt, "Cancel")
	if a.downloadPrompt.open {
		t.Fatal("Cancel did not close download prompt dialog")
	}
	if len(a.backgroundTasks) != 0 || calls.Load() != 0 {
		t.Fatalf("expected 0 tasks and 0 stream requests after canceling prompt, got tasks=%d calls=%d", len(a.backgroundTasks), calls.Load())
	}
}

// 未知总长度的分块下载先显示不定传输状态，收起面板并切歌后仍能完成下载。
func TestDownloadTaskUnknownLengthCompletesAcrossCollapseAndTrackSwitch(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Del("Content-Length")
		_, _ = io.WriteString(w, "chunk-1-")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		select {
		case <-finish:
			_, _ = io.WriteString(w, "chunk-2")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "First song.mp3")
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	fake(a).stream = func(id, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL + "/" + id, Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "First song"), song("two", "Other song"))
	chooseDownload(t, tt, "First song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "unknown-length chunked transfer")
	until(t, ch, func() bool { return a.backgroundTasks[0].status == "Downloading (size unknown)…" })
	if a.backgroundTasks[0].progress != nil {
		t.Fatal("unknown-length transfer should use indeterminate progress")
	}

	// 收起任务面板并切换到别的播放曲目，不应打断进行中的下载。
	clickDownloadUI(t, tt, "Background Tasks (1)")
	if a.backgroundTasksOpen {
		t.Fatal("background tasks panel did not collapse")
	}
	a.pl.PlayQueue([]music.Track{song("two", "Other song")}, 0)
	tt.Frame()

	close(finish)
	until(t, ch, func() bool { return a.backgroundTasks[0].state == backgroundTaskSucceeded })
	clickDownloadUI(t, tt, "Background Tasks (1)")
	if _, ok := tt.Find("Completed"); !ok {
		t.Fatal("completed status missing after reopening panel")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "chunk-1-chunk-2" {
		t.Fatalf("downloaded %q, %v", data, err)
	}
}

// 下载失败按分类呈现安全说明与直达恢复动作，且不暴露原始错误和令牌。
func TestDownloadTaskFailureCategoriesAndRecoveryActions(t *testing.T) {
	const secret = "secret-download-token-999"
	for _, tc := range []struct {
		name        string
		err         error
		wantStatus  string
		clickAction string
		checkAfter  func(t *testing.T, a *app)
	}{
		{
			name:        "sign-in",
			err:         music.ErrSignIn,
			wantStatus:  "Sign in required to download",
			clickAction: "Sign In",
			checkAfter: func(t *testing.T, a *app) {
				if !a.signIn.open || a.signIn.lib != a.library {
					t.Fatal("Sign In action did not open sign-in dialog")
				}
			},
		},
		{
			name:        "setup",
			err:         music.ErrSetup,
			wantStatus:  "Source needs setup or permission",
			clickAction: "Open Plugins",
			checkAfter: func(t *testing.T, a *app) {
				if a.route.page != pagePlugins {
					t.Fatalf("did not open the plugins page: %+v", a.route)
				}
			},
		},
		{
			name:       "unavailable",
			err:        music.ErrUnavailable,
			wantStatus: "Song unavailable from this source",
		},
		{
			name:       "timeout",
			err:        context.DeadlineExceeded,
			wantStatus: "Download timed out; check your network and retry",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a := newTestApp()
			useDirectDownloadDir(a, dir)
			ch := posts(a)
			fake(a).stream = func(_, _ string) (*music.Stream, error) {
				return nil, errors.Join(tc.err, errors.New(secret))
			}
			tt := downloadTestView(a, song("one", "First song"))
			chooseDownload(t, tt, "First song")
			until(t, ch, func() bool {
				return len(a.backgroundTasks) == 1 && a.backgroundTasks[0].state == backgroundTaskFailed
			})
			tt.Frame()
			if _, ok := tt.Find(secret); ok {
				t.Fatal("raw secret leaked into download task panel")
			}
			if _, ok := tt.Find(tc.wantStatus); !ok {
				t.Fatalf("missing status %q in %q", tc.wantStatus, tt.Texts())
			}
			if tc.clickAction != "" {
				clickDownloadUI(t, tt, tc.clickAction)
				tc.checkAfter(t, a)
			}
		})
	}
}

// 撤销程序授权立即停止进行中及排队下载，重新授权后才能手动重试成功。
func TestDownloadTaskStopsOnPermissionRevokeAndRetriesAfterRegrant(t *testing.T) {
	started, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
	release := make(chan struct{})
	var serverCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serverCalls.Add(1) == 1 {
			started <- struct{}{}
			select {
			case <-r.Context().Done():
				canceled <- struct{}{}
			case <-release:
			}
			return
		}
		_, _ = io.WriteString(w, "granted-audio")
	}))
	defer server.Close()
	defer close(release)

	dir := t.TempDir()
	path := filepath.Join(dir, "Guarded song.mp3")

	a := newTestApp()
	useDirectDownloadDir(a, dir)
	g := &guardedSource{fakeSource: newFakeSource("guarded"), granted: true}
	g.perms.Process = plugin.Programs{"helper"}
	g.stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL, Type: "mp3"}, nil
	}
	lib := addFake(a, g)
	a.library = lib
	ch := posts(a)

	a.playlists["42"] = &music.Playlist{ID: "42", Name: "Guarded", Tracks: []music.Track{{ID: "one", Name: "Guarded song", Source: lib.id}}}
	a.route = route{page: pagePlaylist, src: lib.id, id: "42"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()

	chooseDownload(t, tt, "Guarded song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "guarded transfer")

	settings := pluginsView(a, 1400)
	clickDownloadUI(t, settings, "Stop Allowing")
	until(t, ch, func() bool {
		return a.downloads.active == nil && a.backgroundTasks[0].state == backgroundTaskCanceled
	})
	wait(t, canceled, "revoked permission cancellation")

	clickDownloadUI(t, tt, "Retry download: Guarded song")
	tt.Frame()
	if a.backgroundTasks[0].status != "Source needs setup or permission" {
		t.Fatalf("unexpected status while permission revoked: %q", a.backgroundTasks[0].status)
	}

	clickDownloadUI(t, settings, "Allow Programs")
	if a.downloads.active != nil {
		t.Fatal("regranting permission auto-restarted download without manual retry")
	}
	clickDownloadUI(t, tt, "More actions: Guarded song")
	if err := tt.ChooseMenuItem("Retry"); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return a.backgroundTasks[0].state == backgroundTaskSucceeded })
	if data, err := os.ReadFile(path); err != nil || string(data) != "granted-audio" {
		t.Fatalf("regrant retry saved %q, %v", data, err)
	}
}

// 崩溃重启时只清理历史登记的自有 .mygo-download-*.part 文件，不误删用户文件，且历史不包含敏感字段。
func TestDownloadTaskHistorySanitizedAndCleansOnlyOwnedTempPart(t *testing.T) {
	dir := t.TempDir()
	historyFile := filepath.Join(dir, "downloads.json")
	target := filepath.Join(dir, "song.mp3")
	ownedPart := ".mygo-download-999-1-123.part"
	otherPart := ".mygo-download-other.part"
	userPart := "user-draft.part"
	for _, name := range []string{ownedPart, otherPart, userPart} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("temp"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(target, []byte("keep-target"), 0o600); err != nil {
		t.Fatal(err)
	}

	const secretURL = "https://cdn.example/audio?token=topsecret"
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	history := downloadHistoryPath
	downloadHistoryPath = func() string { return historyFile }
	defer func() { downloadHistoryPath = history }()

	// 先通过完整菜单下载与取消，验证写入的 downloads.json 不包含敏感链接或请求头。
	a := newTestApp()
	useDirectDownloadDir(a, dir)
	a.restoreDownloads()
	ch := posts(a)
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		return &music.Stream{
			URL:     server.URL + "?token=" + secretURL,
			Type:    "mp3",
			Headers: map[string]string{"Cookie": "session=secret", "Authorization": "Bearer secret"},
		}, nil
	}
	tt := downloadTestView(a, song("s1", "Clean Song"))
	chooseDownload(t, tt, "Clean Song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "transfer before cancel")
	clickDownloadUI(t, tt, "Cancel download: Clean Song")
	until(t, ch, func() bool { return a.backgroundTasks[0].state == backgroundTaskCanceled })
	rawHistory, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawHistory), secretURL) || strings.Contains(strings.ToLower(string(rawHistory)), "cookie") || strings.Contains(strings.ToLower(string(rawHistory)), "authorization") {
		t.Fatalf("downloads.json contains disallowed fields: %s", rawHistory)
	}

	// 再模拟中断后携带 TempPart 的历史文件，确认重启只清理归属临时文件。
	records := []downloadRecord{{
		TrackID:   "one",
		Source:    "fake",
		Name:      "Interrupted Song",
		Quality:   "high",
		FileType:  "mp3",
		Path:      target,
		TempPart:  ownedPart,
		State:     backgroundTaskRunning,
		AccountID: "acc-1",
	}}
	encoded, _ := json.Marshal(records)
	if err := os.WriteFile(historyFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	b := newTestApp()
	b.restoreDownloads()
	if _, err := os.Stat(filepath.Join(dir, ownedPart)); !os.IsNotExist(err) {
		t.Fatalf("owned temp part was not cleaned: %v", err)
	}
	for _, kept := range []string{otherPart, userPart, "song.mp3"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Fatalf("non-owned file %s was unexpectedly touched: %v", kept, err)
		}
	}
	_ = time.Millisecond
}

// 键盘用户可通过 Tab 与 Enter 在后台任务面板取消和重试下载。
func TestDownloadTaskKeyboardControls(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		if r.URL.Path == "/cancel-first" {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "Key Song.mp3")

	a := newTestApp()
	useDirectDownloadDir(a, dir)
	ch := posts(a)
	var calls atomic.Int32
	fake(a).stream = func(_, _ string) (*music.Stream, error) {
		if calls.Add(1) == 1 {
			return &music.Stream{URL: server.URL + "/cancel-first", Type: "mp3"}, nil
		}
		return &music.Stream{URL: server.URL + "/ok", Type: "mp3"}, nil
	}
	tt := downloadTestView(a, song("one", "Key Song"))
	chooseDownload(t, tt, "Key Song")
	until(t, ch, func() bool { return len(a.backgroundTasks) == 1 })
	wait(t, started, "first download")
	settled(tt)

	canceledByKeyboard := false
	for range tt.AccessAll() {
		tt.Key(0, ui.KeyTab)
		if info, ok := tt.Access("Cancel download: Key Song"); ok && info.Focused {
			tt.Key(0, ui.KeyEnter)
			canceledByKeyboard = true
			break
		}
	}
	if !canceledByKeyboard {
		t.Fatal("could not focus and activate Cancel download via keyboard")
	}
	until(t, ch, func() bool { return a.backgroundTasks[0].state == backgroundTaskCanceled })
	settled(tt)

	retriedByKeyboard := false
	for range tt.AccessAll() {
		tt.Key(0, ui.KeyTab)
		if info, ok := tt.Access("Retry download: Key Song"); ok && info.Focused {
			tt.Key(0, ui.KeyEnter)
			retriedByKeyboard = true
			break
		}
	}
	if !retriedByKeyboard {
		t.Fatal("could not focus and activate Retry download via keyboard")
	}
	until(t, ch, func() bool { return a.backgroundTasks[0].state == backgroundTaskSucceeded })
	if data, err := os.ReadFile(path); err != nil || string(data) != "ok" {
		t.Fatalf("keyboard retry did not save %s: %q, %v", path, data, err)
	}
}

// 设置 MM_FRAMES 后，生成下载任务中心多状态在窄窗口与明暗主题下的截图。
func TestDownloadTasksFrames(t *testing.T) {
	dir := framesDir(t)
	for _, tc := range []struct {
		name string
		w, h int
		dark bool
	}{
		{"p1-download-tasks-narrow-light.png", 680, 640, false},
		{"p1-download-tasks-dark.png", 1180, 760, true},
	} {
		a := newTestApp()
		prog := 0.42
		running := &downloadTask{track: song("1", "Aurora Drift"), path: filepath.Join(dir, "1.mp3")}
		a.attachDownload(running)
		running.card.state = backgroundTaskRunning
		running.card.status = "Downloading… 42%"
		running.card.progress = &prog
		running.card.cancel = func() {}
		running.card.cancelLabel = "Cancel download: Aurora Drift"

		failed := &downloadTask{track: song("2", "Starlight Echo"), path: filepath.Join(dir, "2.mp3")}
		a.attachDownload(failed)
		failed.card.state = backgroundTaskFailed
		failed.card.status = "Sign in required to download"
		failed.card.failureAction = &backgroundTaskAction{label: "Sign In", run: func() {}}

		done := &downloadTask{track: song("3", "Morning Dew"), path: filepath.Join(dir, "3.mp3")}
		a.attachDownload(done)
		done.card.state = backgroundTaskSucceeded
		done.card.status = "Completed"

		a.playlists["42"] = &music.Playlist{ID: "42", Name: "Downloads", Tracks: []music.Track{running.track, failed.track, done.track}}
		a.route = route{page: pagePlaylist, src: "fake", id: "42"}
		a.backgroundTasksOpen = true
		tt := ui.NewTester(a.view, tc.w, tc.h)
		tt.SetDark(tc.dark)
		settled(tt)
		saveFrame(t, dir, tc.name, tt.Image())
	}
}

// 下载详情页支持侧边栏与悬浮入口直达、按状态筛选、批量取消/重试及仅清除记录。
func TestDownloadsPageNavigationFiltersAndBatchControls(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var blockActive atomic.Bool
	blockActive.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" && blockActive.Load() {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
		}
		_, _ = io.WriteString(w, "audio-bytes")
	}))
	defer server.Close()
	defer close(release)

	dir := t.TempDir()
	history := downloadHistoryPath
	downloadHistoryPath = func() string { return filepath.Join(dir, "downloads.json") }
	defer func() { downloadHistoryPath = history }()

	a := newTestApp()
	a.restoreDownloads()
	a.settings.Quality = "lossless"
	ch := posts(a)
	var failSecond atomic.Bool
	failSecond.Store(true)
	fake(a).stream = func(id, _ string) (*music.Stream, error) {
		switch id {
		case "s1":
			return &music.Stream{URL: server.URL + "/slow", Type: "mp3"}, nil
		case "s2":
			if failSecond.Load() {
				return nil, music.ErrUnavailable
			}
			return &music.Stream{URL: server.URL + "/ok", Type: "flac"}, nil
		default:
			return &music.Stream{URL: server.URL + "/ok", Type: "mp3"}, nil
		}
	}

	s1 := song("s1", "First Slow", "Artist One")
	s2 := song("s2", "Second Fail", "Artist Two")
	s3 := song("s3", "Third Done", "Artist Three")
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "Batch List", Tracks: []music.Track{s1, s2, s3}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}

	p3 := filepath.Join(dir, "done.mp3")
	a.addDownload(s3, a.library, "lossless", "mp3", p3, "")
	until(t, ch, func() bool { return a.downloads.tasks[0].card.state == backgroundTaskSucceeded })

	p2 := filepath.Join(dir, "fail.flac")
	a.addDownload(s2, a.library, "lossless", "flac", p2, "")
	until(t, ch, func() bool { return a.downloads.tasks[1].card.state == backgroundTaskFailed })

	p1 := filepath.Join(dir, "slow.mp3")
	a.addDownload(s1, a.library, "lossless", "mp3", p1, "")
	wait(t, started, "slow active transfer")

	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	// 从悬浮窗按钮直接跳转到下载详情页，浮窗随即隐藏不再遮挡。
	clickDownloadUI(t, tt, "Open Downloads")
	settled(tt)
	if a.route.page != pageDownloads {
		t.Fatalf("expected pageDownloads, got %+v", a.route)
	}
	if _, ok := tt.Find("Background Tasks (3)"); ok {
		t.Fatal("floating background panel remained visible on downloads detail page")
	}
	if _, ok := tt.Find("Artist Three"); !ok {
		t.Fatal("missing artist metadata on downloads detail page")
	}

	// 切换状态筛选。
	clickDownloadUI(t, tt, "Completed")
	if _, ok := tt.Find("Third Done"); !ok {
		t.Fatal("completed filter did not keep completed download")
	}
	if _, ok := tt.Find("First Slow"); ok {
		t.Fatal("completed filter leaked active download")
	}
	clickDownloadUI(t, tt, "Failed")
	if _, ok := tt.Find("Second Fail"); !ok {
		t.Fatal("failed filter did not keep failed download")
	}
	if _, ok := tt.Find("Third Done"); ok {
		t.Fatal("failed filter leaked completed download")
	}
	clickDownloadUI(t, tt, "In progress")
	if _, ok := tt.Find("First Slow"); !ok {
		t.Fatal("in-progress filter did not keep active download")
	}
	clickDownloadUI(t, tt, "All")

	// 批量取消：停止活动传输；再恢复并批量重试失败项。
	clickDownloadUI(t, tt, "Cancel All")
	until(t, ch, func() bool { return a.downloads.tasks[2].card.state == backgroundTaskCanceled })
	blockActive.Store(false)
	failSecond.Store(false)
	clickDownloadUI(t, tt, "Retry Failed")
	until(t, ch, func() bool { return a.downloads.tasks[1].card.state == backgroundTaskSucceeded })

	// 清除已结束任务只移出历史记录，不能删除已经保存的本地音频文件。
	clickDownloadUI(t, tt, "Clear Finished")
	settled(tt)
	if len(a.downloads.tasks) != 0 || len(a.backgroundTasks) != 0 {
		t.Fatalf("expected all finished records cleared, got tasks=%d bg=%d", len(a.downloads.tasks), len(a.backgroundTasks))
	}
	if data, err := os.ReadFile(p3); err != nil || string(data) != "audio-bytes" {
		t.Fatalf("clearing history damaged downloaded file: %q, %v", data, err)
	}
	// 切到别的页面再从侧边栏“Downloads”回到详情页仍可正常进入。
	a.open(route{page: pageHome, src: "fake"})
	settled(tt)
	clickDownloadUI(t, tt, "Downloads")
	if a.route.page != pageDownloads {
		t.Fatalf("sidebar Downloads did not navigate to pageDownloads: %+v", a.route)
	}
	if _, ok := tt.Find("No download tasks"); !ok {
		t.Fatal("empty downloads detail page did not show empty notice")
	}
}

// 批量下载跳过不可用曲目、串行保存新文件、保护目录内已有文件不被覆盖，并拒绝来源变更后的迟到入队。
func TestBatchDownloadAllTracksAndSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "stream:"+strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer server.Close()

	dir := t.TempDir()
	existingPath := filepath.Join(dir, "Artist C - Song C.mp3")
	if err := os.WriteFile(existingPath, []byte("keep existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	folderReady := make(chan struct{}, 2)
	blockFolder := make(chan struct{})
	var useSlowFolder atomic.Bool
	oldPick := pickDownloadFolder
	pickDownloadFolder = func(*app) string {
		if useSlowFolder.Load() {
			folderReady <- struct{}{}
			<-blockFolder
		}
		return dir
	}
	defer func() { pickDownloadFolder = oldPick }()

	a := newTestApp()
	a.settings.DownloadDirectory = dir
	ch := posts(a)
	fake(a).stream = func(id, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL + "/" + id, Type: "mp3"}, nil
	}
	t1 := song("a", "Song A", "Artist A")
	t2 := song("b", "Song B", "Artist B")
	t2.Unavailable = true
	t3 := song("c", "Song C", "Artist C")
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "Batch List", Tracks: []music.Track{t1, t2, t3}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}

	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	clickDownloadUI(t, tt, "Batch Download")
	tt.Frame()
	if !a.downloadPrompt.open || !a.downloadPrompt.batch {
		t.Fatalf("expected batch download prompt dialog to open, got open=%v batch=%v", a.downloadPrompt.open, a.downloadPrompt.batch)
	}
	clickDownloadUI(t, tt, "Download")
	until(t, ch, func() bool {
		return len(a.downloads.tasks) == 2 &&
			a.downloads.tasks[0].card.state == backgroundTaskSucceeded &&
			a.downloads.tasks[1].card.state == backgroundTaskFailed
	})

	if data, err := os.ReadFile(filepath.Join(dir, "Artist A - Song A.mp3")); err != nil || string(data) != "stream:a" {
		t.Fatalf("unexpected Song A file: %q, %v", data, err)
	}
	if data, err := os.ReadFile(existingPath); err != nil || string(data) != "keep existing" {
		t.Fatalf("existing file was overwritten: %q, %v", data, err)
	}
	if a.downloads.tasks[1].card.status != "File already exists; choose a different name" {
		t.Fatalf("unexpected conflict status: %q", a.downloads.tasks[1].card.status)
	}

	// 删除阻塞文件后在详情页重试失败项可成功补齐。
	if err := os.Remove(existingPath); err != nil {
		t.Fatal(err)
	}
	clickDownloadUI(t, tt, "Downloads")
	settled(tt)
	clickDownloadUI(t, tt, "Retry Failed")
	until(t, ch, func() bool { return a.downloads.tasks[1].card.state == backgroundTaskSucceeded })
	if data, err := os.ReadFile(existingPath); err != nil || string(data) != "stream:c" {
		t.Fatalf("retried Song C file mismatch: %q, %v", data, err)
	}

	// 弹窗开启并挑选文件夹期间停用来源，确认下载时不能把旧来源曲目入队。
	a.open(route{page: pagePlaylist, src: "fake", id: "42"})
	settled(tt)
	useSlowFolder.Store(true)
	other := newFakeSource("Other")
	addFake(a, other)
	clickDownloadUI(t, tt, "Batch Download")
	tt.Frame()
	clickDownloadUI(t, tt, "Browse…")
	wait(t, folderReady, "slow folder picker")
	a.setPluginEnabled(a.libOf("fake"), false)
	close(blockFolder)
	runPost(t, ch)
	clickDownloadUI(t, tt, "Download")
	if !tt.HasText("Download source changed; choose Download again") {
		t.Fatalf("missing source changed toast after stale prompt confirm: %q", tt.Texts())
	}
	if len(a.downloads.tasks) != 2 {
		t.Fatalf("disabled source created stale batch tasks: %d", len(a.downloads.tasks))
	}
}

// 多选模式只勾选并下载可下载曲目，切页时自动退出选择状态。
func TestTrackSelectionModeBatchDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "selected:"+strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer server.Close()

	dir := t.TempDir()
	a := newTestApp()
	a.settings.DownloadDirectory = dir
	ch := posts(a)
	fake(a).stream = func(id, _ string) (*music.Stream, error) {
		return &music.Stream{URL: server.URL + "/" + id, Type: "flac"}, nil
	}
	s1 := song("1", "Song One", "Artist")
	s2 := song("2", "Song Two", "Artist")
	s3 := song("3", "Song Three", "Artist")
	s3.Unavailable = true
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "Batch List", Tracks: []music.Track{s1, s2, s3}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}

	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)
	clickDownloadUI(t, tt, "Select")
	settled(tt)
	// 不可用曲目点击后不会被选中。
	clickDownloadUI(t, tt, "Song Three")
	if _, ok := tt.Find("0 selected"); !ok {
		t.Fatal("unavailable song should not be selectable")
	}
	clickDownloadUI(t, tt, "Song One")
	if _, ok := tt.Find("1 selected"); !ok {
		t.Fatal("selecting Song One did not update selection count")
	}
	clickDownloadUI(t, tt, "Select All")
	if _, ok := tt.Find("2 selected"); !ok {
		t.Fatal("Select All should select only the 2 downloadable tracks")
	}
	clickDownloadUI(t, tt, "Clear Selection")
	if _, ok := tt.Find("0 selected"); !ok {
		t.Fatal("Clear Selection did not reset selection")
	}
	clickDownloadUI(t, tt, "Song Two")
	clickDownloadUI(t, tt, "Download Selected")
	tt.Frame()
	clickDownloadUI(t, tt, "Download")
	until(t, ch, func() bool {
		return len(a.downloads.tasks) == 1 && a.downloads.tasks[0].card.state == backgroundTaskSucceeded
	})
	if a.trackSelectActive() {
		t.Fatal("track selection mode remained active after batch enqueue")
	}
	if _, err := os.Stat(filepath.Join(dir, "Artist - Song Two.flac")); err != nil {
		t.Fatalf("missing selected file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Artist - Song One.flac")); !os.IsNotExist(err) {
		t.Fatalf("unselected track was downloaded unexpectedly: %v", err)
	}

	// 进入选择模式后切换页面必须自动退出选择。
	clickDownloadUI(t, tt, "Select")
	if !a.trackSelectActive() {
		t.Fatal("expected selection mode active")
	}
	clickDownloadUI(t, tt, "Downloads")
	if a.trackSelectActive() {
		t.Fatal("navigating away did not exit selection mode")
	}
}

// 设置 MM_FRAMES 后，生成下载任务独立详情页、曲目多选、下载确认弹窗与下载设置截图。
func TestDownloadsPageAndBatchFrames(t *testing.T) {
	dir := framesDir(t)
	for _, tc := range []struct {
		name string
		w, h int
		dark bool
	}{
		{"p2-downloads-page-light.png", 1160, 740, false},
		{"p2-downloads-page-dark.png", 1160, 740, true},
		{"p2-downloads-page-narrow.png", 680, 560, false},
	} {
		a := newTestApp()
		a.settings.Quality = "lossless"
		prog := 0.64

		done := &downloadTask{track: song("1", "Morning Dew", "Haruhikage"), artists: "Haruhikage", quality: "lossless", fileType: "flac", path: filepath.Join(dir, "Haruhikage - Morning Dew.flac")}
		done.track.Source = "fake"
		a.attachDownload(done)
		done.card.state, done.card.status = backgroundTaskSucceeded, "Completed"

		failed := &downloadTask{track: song("2", "Starlight Echo", "Crychic"), artists: "Crychic", quality: "lossless", dir: dir}
		failed.track.Source = "fake"
		a.attachDownload(failed)
		failed.card.state, failed.card.status = backgroundTaskFailed, "Sign in required to download"
		failed.card.failureAction = &backgroundTaskAction{label: "Sign In", run: func() {}}

		queued := &downloadTask{track: song("3", "Silhouette", "Ave Mujica"), artists: "Ave Mujica", quality: "320k", dir: dir}
		queued.track.Source = "fake"
		a.attachDownload(queued)
		queued.card.state, queued.card.status, queued.card.waiting = backgroundTaskRunning, "Queued", true
		queued.card.cancel = func() {}

		active := &downloadTask{track: song("4", "Aurora Drift", "MyGO!!!!!"), artists: "MyGO!!!!!", quality: "lossless", fileType: "mp3", path: filepath.Join(dir, "MyGO!!!!! - Aurora Drift.mp3")}
		active.track.Source = "fake"
		a.attachDownload(active)
		active.card.state, active.card.status, active.card.progress = backgroundTaskRunning, "Downloading… 64%", &prog
		active.card.cancel = func() {}

		a.route = route{page: pageDownloads, src: "fake"}
		tt := ui.NewTester(a.view, tc.w, tc.h)
		tt.SetDark(tc.dark)
		settled(tt)
		saveFrame(t, dir, tc.name, tt.Image())
	}

	a := newTestApp()
	s1 := song("1", "迷星叫", "MyGO!!!!!")
	s2 := song("2", "碧天伴走", "MyGO!!!!!")
	s3 := song("3", "诗超绊", "MyGO!!!!!")
	s3.Unavailable = true
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "Batch List", Tracks: []music.Track{s1, s2, s3}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}
	a.enterTrackSelect()
	a.toggleTrackSelected(&s1)
	tt := ui.NewTester(a.view, 1160, 740)
	settled(tt)
	saveFrame(t, dir, "p2-batch-select-mode.png", tt.Image())

	// 单曲确认弹窗截图（亮色）
	singlePromptApp := newTestApp()
	singlePromptApp.settings.DownloadDirectory = dir
	singlePromptApp.settings.DownloadQuality = "lossless"
	singlePromptApp.playlists["42"] = &music.Playlist{ID: "42", Name: "Batch List", Tracks: []music.Track{s1, s2, s3}}
	singlePromptApp.route = route{page: pagePlaylist, src: "fake", id: "42"}
	singlePromptApp.download(s1)
	singlePromptTT := ui.NewTester(singlePromptApp.view, 1160, 740)
	settled(singlePromptTT)
	saveFrame(t, dir, "p2-download-prompt-single.png", singlePromptTT.Image())

	// 批量确认弹窗截图（暗色、含不可用曲目提示）
	batchPromptApp := newTestApp()
	batchPromptApp.settings.DownloadDirectory = dir
	batchPromptApp.settings.DownloadQuality = "high"
	batchPromptApp.playlists["42"] = &music.Playlist{ID: "42", Name: "Batch List", Tracks: []music.Track{s1, s2, s3}}
	batchPromptApp.route = route{page: pagePlaylist, src: "fake", id: "42"}
	batchPromptApp.downloadBatch([]music.Track{s1, s2, s3})
	batchPromptApp.downloadPrompt.saveAsDefault = true
	batchPromptTT := ui.NewTester(batchPromptApp.view, 1160, 740)
	batchPromptTT.SetDark(true)
	settled(batchPromptTT)
	saveFrame(t, dir, "p2-download-prompt-batch-dark.png", batchPromptTT.Image())

	settingsApp := newTestApp()
	settingsApp.settingsPage = "playback"
	settingsTester := ui.NewTester(settingsApp.settingsView, 780, 700)
	settled(settingsTester)
	saveFrame(t, dir, "p2-settings-download-quality.png", settingsTester.Image())
}

// 设置中的默认下载音质可以独立于播放音质持久化，并在单曲与批量下载时生效。
func TestSettingsDefaultDownloadQuality(t *testing.T) {
	a := newTestApp()
	post := posts(a)
	tempDir := t.TempDir()
	a.settings.path = filepath.Join(tempDir, "settings.json")
	a.settings.Quality = "standard"

	audioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("audio-bytes"))
	}))
	defer audioSrv.Close()

	requestedQualities := make(chan string, 8)
	fake(a).stream = func(id, quality string) (*music.Stream, error) {
		requestedQualities <- quality
		return &music.Stream{URL: audioSrv.URL, Type: "flac", Quality: quality}, nil
	}

	settingsTT := ui.NewTester(a.settingsView, 780, 700)
	settingsTT.Frame()
	if err := settingsTT.Click("Playback"); err != nil {
		t.Fatal(err)
	}
	settled(settingsTT)
	clickSettingRole(t, settingsTT, ui.RolePopUpButton, "Download Quality")
	if err := settingsTT.Click("Lossless"); err != nil {
		t.Fatal(err)
	}
	settled(settingsTT)

	if a.settings.DownloadQuality != "lossless" || a.settings.Quality != "standard" {
		t.Fatalf("unexpected qualities after change: download=%q playback=%q", a.settings.DownloadQuality, a.settings.Quality)
	}

	savedBytes, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatalf("read persisted settings: %v", err)
	}
	var reloaded settings
	if err := json.Unmarshal(savedBytes, &reloaded); err != nil {
		t.Fatalf("unmarshal persisted settings: %v", err)
	}
	if reloaded.DownloadQuality != "lossless" || reloaded.Quality != "standard" {
		t.Fatalf("persisted qualities mismatch: %+v", reloaded)
	}

	// 关闭确认弹窗后，单曲与批量下载都应直接按默认文件夹和默认下载音质发起请求。
	batchDir := filepath.Join(tempDir, "batch")
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	useDirectDownloadDir(a, batchDir)

	s1 := song("dq-1", "Single Song", "MyGO!!!!!")
	s2 := song("dq-2", "Batch Song", "MyGO!!!!!")
	a.download(s1)
	until(t, post, func() bool {
		return len(a.downloads.tasks) == 1 && a.downloads.tasks[0].card.state == backgroundTaskSucceeded
	})
	a.downloadBatch([]music.Track{s2})
	until(t, post, func() bool {
		return len(a.downloads.tasks) == 2 && a.downloads.tasks[1].card.state == backgroundTaskSucceeded
	})

	for i := 0; i < 2; i++ {
		if q := <-requestedQualities; q != "lossless" {
			t.Fatalf("download %d requested quality %q, want lossless", i, q)
		}
	}

	// 切回跟随播放音质后，应使用当前的播放音质，非法配置也要安全回退。
	clickSettingRole(t, settingsTT, ui.RolePopUpButton, "Download Quality")
	if err := settingsTT.Click("Follow Playback"); err != nil {
		t.Fatal(err)
	}
	settled(settingsTT)
	if a.settings.DownloadQuality != "" {
		t.Fatalf("expected empty DownloadQuality for Follow Playback, got %q", a.settings.DownloadQuality)
	}
	if got := a.downloadQuality(); got != "standard" {
		t.Fatalf("expected Follow Playback to resolve to standard, got %q", got)
	}

	a.settings.DownloadQuality = "invalid-tier"
	if got := a.downloadQuality(); got != "standard" {
		t.Fatalf("expected invalid DownloadQuality to fall back to standard, got %q", got)
	}
	a.settings.Quality = "bogus"
	if got := a.downloadQuality(); got != "high" {
		t.Fatalf("expected double-invalid qualities to fall back to high, got %q", got)
	}
}

// 下载确认弹窗复用设置默认值，支持单曲改名与临时改目录/音质不污染设置，并支持快捷设为默认与不再弹窗直下。
func TestDownloadPromptSingleAndBatchDefaultsAndCustomize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "audio:"+strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer srv.Close()

	root := t.TempDir()
	dir1 := filepath.Join(root, "dir1")
	dir2 := filepath.Join(root, "dir2")
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}

	pickedFolder := dir2
	oldPick := pickDownloadFolder
	pickDownloadFolder = func(*app) string { return pickedFolder }
	defer func() { pickDownloadFolder = oldPick }()

	a := newTestApp()
	a.settings.path = filepath.Join(root, "settings.json")
	a.settings.Quality = "standard"
	a.settings.DownloadQuality = "lossless"
	a.settings.DownloadDirectory = dir1
	a.settings.PromptDownload = true
	ch := posts(a)

	type reqInfo struct{ id, quality string }
	reqs := make(chan reqInfo, 16)
	fake(a).stream = func(id, quality string) (*music.Stream, error) {
		reqs <- reqInfo{id: id, quality: quality}
		return &music.Stream{URL: srv.URL + "/" + id, Type: "flac"}, nil
	}

	s1 := song("p1", "My/Song", "Artist")
	s2 := song("p2", "Track Two", "Artist")
	s3 := song("p3", "Track Three", "Artist")
	s3.Unavailable = true
	s4 := song("p4", "Track Four", "Artist")
	a.playlists["42"] = &music.Playlist{ID: "42", Name: "List", Tracks: []music.Track{s1, s2, s3, s4}}
	a.route = route{page: pagePlaylist, src: "fake", id: "42"}

	tt := ui.NewTester(a.view, 1200, 800)
	settled(tt)

	// 1. 单曲右键打开弹窗：预填设置默认目录、音质与清洗后的基础文件名。
	chooseDownload(t, tt, "My/Song")
	tt.Frame()
	if !a.downloadPrompt.open || a.downloadPrompt.batch {
		t.Fatalf("expected single download prompt, got open=%v batch=%v", a.downloadPrompt.open, a.downloadPrompt.batch)
	}
	if !sameDownloadPath(a.downloadPrompt.folder, dir1) || a.downloadPrompt.quality != "lossless" || a.downloadPrompt.fileName != "Artist - My_Song" {
		t.Fatalf("unexpected prompt defaults: %+v", a.downloadPrompt)
	}

	// 弹窗开启时按空格不应误触发播放/暂停快捷键。
	tt.Key(' ', 0)
	if a.pl.Playing() {
		t.Fatal("Space inside download prompt unexpectedly started playback")
	}

	// 临时改目录、改音质、改文件名（含非法路径字符），不勾默认选项。
	clickDownloadUI(t, tt, "Browse…")
	until(t, ch, func() bool { return sameDownloadPath(a.downloadPrompt.folder, dir2) })
	clickSettingRole(t, tt, ui.RolePopUpButton, "Download Quality")
	clickDownloadUI(t, tt, "Standard")
	a.downloadPrompt.fileName = "custom/name"
	clickDownloadUI(t, tt, "Download")

	until(t, ch, func() bool {
		return len(a.downloads.tasks) == 1 && a.downloads.tasks[0].card.state == backgroundTaskSucceeded
	})
	if r := <-reqs; r.id != "p1" || r.quality != "standard" {
		t.Fatalf("unexpected stream request for customized single download: %+v", r)
	}
	if data, err := os.ReadFile(filepath.Join(dir2, "custom_name.flac")); err != nil || string(data) != "audio:p1" {
		t.Fatalf("missing customized single file: %q, %v", data, err)
	}
	// 未勾选“设为默认”与“不再弹窗”时，设置保持原样。
	if !sameDownloadPath(a.settings.DownloadDirectory, dir1) || a.settings.DownloadQuality != "lossless" || !a.settings.PromptDownload {
		t.Fatalf("temporary prompt overrides mutated settings: %+v", a.settings)
	}

	// 2. 同一首曲目在默认目录另存时改用不同基础文件名允许再次下载。
	chooseDownload(t, tt, "My/Song")
	tt.Frame()
	a.downloadPrompt.fileName = "alt-copy"
	clickDownloadUI(t, tt, "Download")
	until(t, ch, func() bool {
		return len(a.downloads.tasks) == 2 && a.downloads.tasks[1].card.state == backgroundTaskSucceeded
	})
	_ = <-reqs
	if data, err := os.ReadFile(filepath.Join(dir1, "alt-copy.flac")); err != nil || string(data) != "audio:p1" {
		t.Fatalf("missing alt-copy file: %q, %v", data, err)
	}

	// 3. 多选批量弹窗：只选 s1/s2 与不可用 s3（跳过 1 首），无文件名输入，勾选“设为默认”和“不再弹窗”。
	a.downloadBatch([]music.Track{s1, s2, s3})
	tt.Frame()
	if !a.downloadPrompt.open || !a.downloadPrompt.batch || a.downloadPrompt.skipped != 1 {
		t.Fatalf("unexpected batch prompt state: %+v", a.downloadPrompt)
	}
	if _, ok := tt.Find("File Name"); ok {
		t.Fatal("batch prompt should not render single-track File Name field")
	}
	if _, ok := tt.Find("2 songs ready to download (1 unavailable skipped)"); !ok {
		t.Fatal("batch prompt missing ready/skipped summary")
	}
	clickDownloadUI(t, tt, "Browse…")
	until(t, ch, func() bool { return sameDownloadPath(a.downloadPrompt.folder, dir2) })
	clickSettingRole(t, tt, ui.RolePopUpButton, "Download Quality")
	clickDownloadUI(t, tt, "Standard")
	clickDownloadUI(t, tt, "Save folder and quality as default")
	clickDownloadUI(t, tt, "Don't ask again before downloading")
	clickDownloadUI(t, tt, "Download")

	until(t, ch, func() bool {
		return len(a.downloads.tasks) == 4 &&
			a.downloads.tasks[2].card.state == backgroundTaskSucceeded &&
			a.downloads.tasks[3].card.state == backgroundTaskSucceeded
	})
	_ = <-reqs
	_ = <-reqs

	// 验证设置已持久化写入 settings.json。
	raw, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatalf("read persisted settings: %v", err)
	}
	reloaded := defaultSettings()
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if !sameDownloadPath(reloaded.DownloadDirectory, dir2) || reloaded.DownloadQuality != "standard" || reloaded.PromptDownload {
		t.Fatalf("persisted defaults mismatch: dir=%q quality=%q prompt=%v", reloaded.DownloadDirectory, reloaded.DownloadQuality, reloaded.PromptDownload)
	}

	// 4. 下次再点下载时不再弹窗，直接按 dir2 与 standard 静默入队。
	chooseDownload(t, tt, "Track Four")
	tt.Frame()
	if a.downloadPrompt.open {
		t.Fatal("download prompt opened even though PromptDownload was disabled")
	}
	until(t, ch, func() bool {
		return len(a.downloads.tasks) == 5 && a.downloads.tasks[4].card.state == backgroundTaskSucceeded
	})
	if r := <-reqs; r.id != "p4" || r.quality != "standard" {
		t.Fatalf("direct download after disabling prompt used wrong params: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir2, "Artist - Track Four.flac")); err != nil {
		t.Fatalf("direct download did not save into persisted dir2: %v", err)
	}
}

// 设置页支持配置与重置默认下载文件夹，并切换“下载前确认弹窗”开关且可跨重启保留。
func TestSettingsDownloadFolderAndPromptToggle(t *testing.T) {
	root := t.TempDir()
	sysDir := filepath.Join(root, "sys-downloads")
	customDir := filepath.Join(root, "custom-downloads")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	oldSys := defaultSystemDownloadDir
	defaultSystemDownloadDir = func() string { return sysDir }
	defer func() { defaultSystemDownloadDir = oldSys }()

	oldPick := pickDownloadFolder
	pickDownloadFolder = func(*app) string { return customDir }
	defer func() { pickDownloadFolder = oldPick }()

	a := newTestApp()
	a.settings.path = filepath.Join(root, "settings.json")
	ch := posts(a)

	settingsTT := ui.NewTester(a.settingsView, 780, 720)
	settingsTT.Frame()
	if err := settingsTT.Click("Playback"); err != nil {
		t.Fatal(err)
	}
	settled(settingsTT)

	// 初始回退系统默认下载文件夹，且确认弹窗默认开启。
	if a.downloadFolder() != sysDir || !a.settings.PromptDownload {
		t.Fatalf("unexpected initial download settings: folder=%q prompt=%v", a.downloadFolder(), a.settings.PromptDownload)
	}

	// 点击 Browse… 选择自定义默认目录。
	if err := settingsTT.Click("Browse…"); err != nil {
		t.Fatal(err)
	}
	until(t, ch, func() bool { return sameDownloadPath(a.settings.DownloadDirectory, customDir) })
	settled(settingsTT)

	// 关闭“下载前询问”开关，并验证磁盘读取时不会回退为 true。
	clickSettingRole(t, settingsTT, ui.RoleSwitch, "Ask Before Downloading")
	settled(settingsTT)
	if a.settings.PromptDownload {
		t.Fatal("Ask Before Downloading switch did not turn off PromptDownload")
	}

	raw, err := os.ReadFile(a.settings.path)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	loaded := defaultSettings()
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("unmarshal settings.json: %v", err)
	}
	if !sameDownloadPath(loaded.DownloadDirectory, customDir) || loaded.PromptDownload {
		t.Fatalf("reloaded settings mismatch: dir=%q prompt=%v", loaded.DownloadDirectory, loaded.PromptDownload)
	}

	// 点击 Reset 恢复默认下载文件夹，再重新开启询问开关。
	if err := settingsTT.Click("Reset"); err != nil {
		t.Fatal(err)
	}
	settled(settingsTT)
	if a.settings.DownloadDirectory != "" || a.downloadFolder() != sysDir {
		t.Fatalf("Reset did not restore system download folder: %q / %q", a.settings.DownloadDirectory, a.downloadFolder())
	}
	clickSettingRole(t, settingsTT, ui.RoleSwitch, "Ask Before Downloading")
	settled(settingsTT)
	if !a.settings.PromptDownload {
		t.Fatal("Ask Before Downloading switch did not turn PromptDownload back on")
	}
}
