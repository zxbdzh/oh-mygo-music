package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/i18n"
)

// 分享者选择律动后，实际发布的快照必须携带该选项。
func TestShareDefaultViewReachesPublishedSnapshot(t *testing.T) {
	previousLanguage := i18n.Language()
	i18n.SetLanguage(i18n.Chinese)
	defer i18n.SetLanguage(previousLanguage)
	snapshots := make(chan map[string]json.RawMessage, 2)
	a, tester, posted, _ := onlineCardOutputApp(t, func(_ http.ResponseWriter, request *http.Request) bool {
		if request.Method == http.MethodPost && request.URL.Path == "/api/shares" {
			data, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
			}
			request.Body = io.NopCloser(bytes.NewReader(data))
			var snapshot map[string]json.RawMessage
			if err := json.Unmarshal(data, &snapshot); err != nil {
				t.Error(err)
			}
			snapshots <- snapshot
		}
		return false
	})
	draft := a.sharing
	until(t, posted, func() bool { tester.Frame(); return len(draft.png) > 0 })
	if err := tester.Click(T("Default View")); err != nil {
		t.Fatal(err)
	}
	tester.Key(0, ui.KeyEscape)
	if len(tester.Menu()) != 0 || !tester.Focused(T("Default View")) || !draft.open {
		t.Fatal("closing the menu should return focus without dismissing the share card")
	}
	tester.Key(0, ui.KeyEnter)
	tester.Key(0, ui.KeyEnd)
	tester.Key(0, ui.KeyEnter)
	until(t, posted, func() bool { tester.Frame(); return len(draft.png) > 0 && !draft.rendering })
	if directory := os.Getenv("MM_FRAMES"); directory != "" {
		settled(tester)
		saveFrame(t, directory, "share-default-stage-desktop.png", tester.Image())
	}
	if err := tester.Click(T("Create Listen Link")); err != nil {
		t.Fatal(err)
	}
	until(t, posted, func() bool { tester.Frame(); return !draft.onlinePublishing })
	snapshot := wait(t, snapshots, "published share snapshot")
	if string(snapshot["defaultView"]) != `"stage"` || draft.onlineURL == "" {
		t.Fatalf("opening view was not published: defaultView=%s, error=%v", snapshot["defaultView"], draft.onlineError)
	}
}
