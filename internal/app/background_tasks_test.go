package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"
	"mygo-music/internal/i18n"
	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
)

// 收起任务中心不停止任务，再次展开后仍可操作任务结果。
func TestBackgroundTaskCenterCollapseAndReopen(t *testing.T) {
	a := newTestApp()
	task := a.newBackgroundTask("Library scan", "Local music")
	task.update(task.generation, backgroundTaskSucceeded, "Scan complete")
	exported := 0
	task.actions = []backgroundTaskAction{{label: "Export index", run: func() { exported++ }}}
	tester := ui.NewTester(func(context *ui.Context) {
		a.pal = paletteFor(context.Theme())
		a.backgroundTasksLayer(context)
	}, 800, 600)
	tester.Frame()
	if err := tester.Click("Background Tasks (1)"); err != nil {
		t.Fatal(err)
	}
	if _, exists := tester.Find("Export index"); exists {
		t.Fatal("collapsed task center still exposes task actions")
	}
	if err := tester.Click("Background Tasks (1)"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("Export index"); err != nil || exported != 1 {
		t.Fatalf("reopened task action: exported=%d, error=%v", exported, err)
	}
}

// 折叠入口随任务完成、失败和重试更新状态，并能展开任务中心。
func TestBackgroundTaskCollapsedStatus(t *testing.T) {
	a := newTestApp()
	running := a.newBackgroundTask("Scan", "First")
	done := a.newBackgroundTask("Scan", "Second")
	done.update(done.generation, backgroundTaskSucceeded, "Complete")
	a.backgroundTasksOpen = false
	tester := ui.NewTester(func(context *ui.Context) {
		a.pal = paletteFor(context.Theme())
		a.backgroundTasksLayer(context)
	}, 800, 600)
	tester.Frame()
	if _, ok := tester.Find("In progress"); !ok {
		t.Fatal("running task was not shown in the collapsed center")
	}
	running.update(running.generation, backgroundTaskFailed, "Failed")
	tester.Frame()
	if _, ok := tester.Find("Failed"); !ok {
		t.Fatal("failure was not shown in the collapsed center")
	}
	running.state = backgroundTaskSucceeded
	tester.Frame()
	if _, ok := tester.Find("Completed"); !ok {
		t.Fatal("completion was not shown in the collapsed center")
	}
	if err := tester.Click("Background Tasks (2)"); err != nil || !a.backgroundTasksOpen {
		t.Fatalf("collapsed center did not open: %v", err)
	}
}

// 多个任务超出面板时，滚动后仍能打开较早的任务。
func TestBackgroundTaskCenterScrollsToOlderTask(t *testing.T) {
	a := newTestApp()
	opened := false
	for index := range 8 {
		task := a.newBackgroundTask("Scan", fmt.Sprintf("Library %d", index))
		if index == 0 {
			task.open = func() { opened = true }
		}
	}
	tester := ui.NewTester(func(context *ui.Context) {
		a.pal = paletteFor(context.Theme())
		a.backgroundTasksLayer(context)
	}, 800, 600)
	tester.Frame()
	tester.Scroll(700, 350, 0, 2000)
	tester.Frame()
	if err := tester.Click("Open task: Library 0"); err != nil || !opened {
		t.Fatalf("older task was unreachable after scrolling: opened=%v, error=%v", opened, err)
	}
}

// 非分享业务也能接入任务卡，隐藏不取消，入口能找回并执行业务动作。
func TestBackgroundTaskHooksHideReopenAndRemove(t *testing.T) {
	a := newTestApp()
	opened, canceled, exported := 0, 0, 0
	task := a.newBackgroundTask("Library scan", "Local music")
	task.open = func() { opened++ }
	task.cancel = func() { canceled++ }
	tester := ui.NewTester(func(context *ui.Context) {
		a.pal = paletteFor(context.Theme())
		a.backgroundTasksLayer(context)
	}, 800, 600)
	tester.Frame()
	if err := tester.Click("Close Background Task"); err != nil {
		t.Fatal(err)
	}
	if canceled != 0 {
		t.Fatal("hiding a task canceled its work")
	}
	if err := tester.Click("Background Tasks (1)"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("Library scan"); err != nil || opened != 1 {
		t.Fatalf("hidden task could not reopen: opened=%d, error=%v", opened, err)
	}
	task.update(task.generation, backgroundTaskSucceeded, "Scan complete")
	task.actions = []backgroundTaskAction{{label: "Export index", run: func() { exported++ }}}
	tester.Frame()
	if err := tester.Click("Export index"); err != nil || exported != 1 {
		t.Fatalf("result action: exported=%d, error=%v", exported, err)
	}
	if err := tester.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	if _, exists := tester.Find("Background Tasks (0)"); exists || len(a.backgroundTasks) != 0 {
		t.Fatal("removed task left its entry behind")
	}
}

// 两份分享乱序完成时，各自保留提交的模板和结果，隐藏卡片不会取消任务。
func TestBackgroundSharesCompleteOutOfOrderWithFrozenCards(t *testing.T) {
	started := make(chan string, 2)
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	releaseFirst := func() { firstOnce.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { secondOnce.Do(func() { close(secondRelease) }) }
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var metadata shareMetadata
		if request.URL.Path != "/api/shares/reuse" {
			t.Errorf("unexpected request: %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&metadata); err != nil {
			t.Error(err)
			return
		}
		started <- metadata.TrackID
		release := firstRelease
		if metadata.TrackID == "second" {
			release = secondRelease
		}
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		link := "https://share.example/s/listening-1"
		if metadata.TrackID == "second" {
			link = "https://share.example/s/listening-2"
		}
		json.NewEncoder(writer).Encode(shareResult{ID: metadata.TrackID, Status: "ready", URL: link})
	}))
	defer server.Close()
	defer releaseFirst()
	defer releaseSecond()
	previousSecret := getShareServiceSecret
	getShareServiceSecret = func(string) ([]byte, error) { return []byte("test-token"), nil }
	defer func() { getShareServiceSecret = previousSecret }()
	a := newTestApp()
	a.settings.ShareServerURL = server.URL
	fake(a).lyrics = func(string) (*music.Lyrics, error) { return &music.Lyrics{}, nil }
	posted := posts(a)
	tester := ui.NewTester(func(context *ui.Context) {
		a.pal = paletteFor(context.Theme())
		a.shareDialog(context)
		a.backgroundTasksLayer(context)
	}, 1200, 900)
	a.openShare(music.Track{ID: "first", Source: "fake", Name: "First", Duration: 12000})
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Create Listen Link"); err != nil {
		t.Fatal(err)
	}
	first := a.sharing
	if wait(t, started, "first share") != "first" {
		t.Fatal("wrong first share")
	}
	a.settings.CardSquare = true
	a.openShare(music.Track{ID: "second", Source: "fake", Name: "Second", Duration: 12000})
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Create Listen Link"); err != nil {
		t.Fatal(err)
	}
	second := a.sharing
	if wait(t, started, "second share") != "second" {
		t.Fatal("wrong second share")
	}
	if err := tester.Click("Close Background Task"); err != nil {
		t.Fatal(err)
	}
	releaseSecond()
	until(t, posted, func() bool {
		tester.Frame()
		return second.backgroundTask.state == backgroundTaskSucceeded && len(second.png) > 0
	})
	releaseFirst()
	until(t, posted, func() bool {
		tester.Frame()
		return first.backgroundTask.state == backgroundTaskSucceeded && len(first.png) > 0
	})
	if first.onlineURL != "https://share.example/s/listening-1" || second.onlineURL != "https://share.example/s/listening-2" {
		t.Fatalf("share results crossed: first=%s, second=%s", first.onlineURL, second.onlineURL)
	}
	assertCardListeningQR(t, first.png, first.onlineURL)
	assertCardListeningQR(t, second.png, second.onlineURL)
	firstImage, firstErr := png.DecodeConfig(bytes.NewReader(first.png))
	secondImage, secondErr := png.DecodeConfig(bytes.NewReader(second.png))
	if firstErr != nil || secondErr != nil || firstImage.Height != 1600 || secondImage.Height != 1200 {
		t.Fatalf("later settings changed a submitted card: first=%v, second=%v", firstImage, secondImage)
	}
	if err := tester.Click("Background Tasks (2)"); err != nil {
		t.Fatal(err)
	}
	a.settings.CardSquare = false
	if err := tester.Click("Open task: Second · "); err != nil {
		t.Fatal(err)
	}
	if a.sharing == first || a.sharing.track.ID != "second" || !a.sharing.open {
		t.Fatal("task did not reopen its own share editor")
	}
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 && !a.sharing.rendering })
	reopenedImage, err := png.DecodeConfig(bytes.NewReader(a.sharing.png))
	if err != nil || reopenedImage.Height != 1200 {
		t.Fatalf("reopened editor lost the submitted square format: image=%v, error=%v", reopenedImage, err)
	}
}

// 取消后立即重试仍沿用请求身份，上一轮迟到的成功结果不能替换新结果。
func TestBackgroundShareCancelRetryRejectsLateResult(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	a, tester, posted, _ := onlineCardOutputApp(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/api/shares/reuse" {
			return false
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			json.NewEncoder(writer).Encode(shareResult{ID: "retry", Status: "ready", URL: "https://share.example/s/listening-2"})
		case <-request.Context().Done():
		}
		return true
	})
	defer unblock()
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Create Listen Link"); err != nil {
		t.Fatal(err)
	}
	draft := a.sharing
	wait(t, started, "share lookup")
	generation := draft.backgroundTask.generation
	if err := tester.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	a.finishOnlineShare(draft, &shareResult{ID: "old", URL: "https://share.example/s/old"}, nil, generation, "old")
	unblock()
	awaitShareSucceeded(t, a, tester, posted)
	if draft.onlineURL != "https://share.example/s/listening-2" || len(a.backgroundTasks) != 1 {
		t.Fatalf("late completion replaced retry: %s", draft.onlineURL)
	}
}

// 真正退出才停止本机工作并清空列表，清理后任务回调不能复活记录。
func TestBackgroundTasksStopAtApplicationExit(t *testing.T) {
	a := newTestApp()
	task := a.newBackgroundTask("Scan", "Music")
	ctx, cancel := context.WithCancel(context.Background())
	task.cancel = cancel
	a.stopBackgroundTasks()
	if ctx.Err() != context.Canceled || len(a.backgroundTasks) != 0 {
		t.Fatal("application exit left background work running")
	}
	if task.update(task.generation, backgroundTaskSucceeded, "late result") {
		t.Fatal("a disposed task accepted a late result")
	}
}

// 重复提交同一份分享复用正在运行的任务，完成后仍需明确领取结果。
func TestBackgroundShareRepeatedSubmissionUsesOneTask(t *testing.T) {
	a, tester, posted, _ := onlineCardOutputApp(t)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	stream := fake(a).stream
	fake(a).stream = func(id, quality string) (*music.Stream, error) {
		started <- struct{}{}
		<-release
		return stream(id, quality)
	}
	track := a.sharing.track
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Create Listen Link"); err != nil {
		t.Fatal(err)
	}
	wait(t, started, "first audio request")
	a.openShare(track)
	until(t, posted, func() bool { tester.Frame(); return len(a.sharing.png) > 0 })
	if err := tester.Click("Create Listen Link"); err != nil {
		t.Fatal(err)
	}
	if len(a.backgroundTasks) != 1 {
		t.Fatalf("duplicate share created %d tasks", len(a.backgroundTasks))
	}
	unblock()
	awaitShareSucceeded(t, a, tester, posted)
	select {
	case <-started:
		t.Fatal("duplicate submission prepared the audio twice")
	default:
	}
}

// 主界面的播放与导航快捷键不会因已经收起的分享页而失效。
func TestBackgroundSharingLeavesPlayerShortcutsAvailable(t *testing.T) {
	a := newTestApp()
	a.pl.Cue([]music.Track{song("background-keyboard", "Track")})
	out := audio.NewPlayer(&silence{})
	out.SetVolume(0)
	out.Play()
	a.pl.Attach(out)
	defer out.Close()
	a.history = []route{{page: pageHome}}
	a.route = route{page: pageLiked}
	a.sharing = &shareDraft{open: false, onlinePublishing: true}
	task := a.newBackgroundTask("Preparing share", "Track")
	task.hidden = true
	tester := ui.NewTester(a.view, 1200, 800)
	tester.Frame()
	tester.Key(0, ui.KeySpace)
	if a.pl.Playing() {
		t.Fatal("Space did not pause while sharing in the background")
	}
	tester.Key(0, ui.KeyBack)
	if a.route.page != pageHome {
		t.Fatal("back navigation stayed blocked by a hidden share editor")
	}
}

// 右下角任务卡避开播放栏，成功结果显示手动复制和保存动作。
func TestBackgroundTaskFrame(t *testing.T) {
	dir := os.Getenv("MM_FRAMES")
	if dir == "" {
		t.Skip("set MM_FRAMES for visual acceptance")
	}
	previousLanguage := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(previousLanguage)
	a := newTestApp()
	a.account = &music.Account{ID: "test", Name: "Oh MyGo Music"}
	tracks := framesTracks(a)
	a.home = &music.Browse{Sections: []music.Section{{Title: "精选歌曲", Tracks: tracks}}}
	a.pl.Cue(tracks)
	a.pl.SetLyrics(lyrics.Build("[00:01]让风慢慢经过\n[00:08]这一刻，唱给你听\n[00:18]我们一起出发", ""))
	a.pl.Hold(10 * time.Second)
	task := a.newBackgroundTask(T("Online Sharing"), "北方的夜 · The Polar Lights")
	task.open = func() {}
	task.state = backgroundTaskSucceeded
	task.status = T("Online link ready")
	task.actions = []backgroundTaskAction{
		{label: T("Copy Listen Link"), icon: iconLink, run: func() {}},
		{label: T("Copy Image"), icon: iconCopyImage, run: func() {}},
		{label: T("Save PNG"), icon: iconDownload, run: func() {}},
	}
	for _, size := range []struct {
		width, height int
		name          string
		nowPlaying    bool
		dark          bool
	}{
		{1200, 800, "background-task-desktop.png", false, true},
		{760, 640, "background-task-compact.png", false, true},
		{1200, 800, "background-task-light.png", false, false},
		{1200, 800, "background-task-playing.png", true, true},
	} {
		a.nowPlaying, a.npPane = size.nowPlaying, paneStage
		tester := ui.NewTester(a.view, size.width, size.height)
		tester.SetDark(size.dark)
		settled(tester)
		saveFrame(t, dir, size.name, tester.Image())
	}
	running := a.newBackgroundTask(T("Online Sharing"), "Slow Tide · The Polar Lights")
	running.status = T("Uploading online share…")
	running.open, running.cancel = func() {}, func() {}
	tester := ui.NewTester(a.view, 1200, 800)
	tester.SetDark(true)
	tester.Advance(2 * time.Second)
	tester.Frame()
	saveFrame(t, dir, "background-task-multiple-playing.png", tester.Image())
	if err := tester.Click(fmt.Sprintf("%s (2)", T("Background Tasks"))); err != nil {
		t.Fatal(err)
	}
	settled(tester)
	saveFrame(t, dir, "background-task-collapsed-playing.png", tester.Image())
	running.update(running.generation, backgroundTaskSucceeded, T("Online link ready"))
	tester.Frame()
	saveFrame(t, dir, "background-task-collapsed-completed.png", tester.Image())
	running.state = backgroundTaskFailed
	tester.Frame()
	saveFrame(t, dir, "background-task-collapsed-failed.png", tester.Image())
	for index := range 4 {
		extra := a.newBackgroundTask(T("Share Card"), fmt.Sprintf("分享任务 %d · The Polar Lights", index+3))
		extra.update(extra.generation, backgroundTaskSucceeded, T("Online link ready"))
		extra.open = func() {}
	}
	tester = ui.NewTester(a.view, 760, 640)
	tester.SetDark(true)
	tester.Advance(2 * time.Second)
	tester.Frame()
	saveFrame(t, dir, "background-task-overflow.png", tester.Image())
}
