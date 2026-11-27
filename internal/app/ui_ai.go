package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"mygo-music/internal/ai"
	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
	"mygo-music/internal/websearch"
)

const aiSecretKey = "ai-api-key"

var (
	getAISecret = func(key string) ([]byte, error) { return mygo.Secrets.Get(key) }
	setAISecret = func(key string, value []byte) error { return mygo.Secrets.Set(key, value) }
	newAIClient = ai.NewClient
	// newWebProvider 让无头测试换成桩搜索，不需要真实 API Key。
	newWebProvider = websearch.For
)

type aiSuggestionState struct {
	open   bool
	busy   bool
	failed bool
	track  music.Track
	lib    *library
	pick   ai.Suggestion
	name   string
	// 手动入口也把联网进度、实际搜索词和来源留在弹窗里，折叠后由任务卡接管。
	stage        aiBarStage
	query        string
	sources      []websearch.Result
	evidenceOpen bool
	task         *backgroundTask
	cancel       context.CancelFunc
}

// aiBarStage is where the auto suggestion is: searching the web, asking the
// model, or done. 阶段单独存，界面才能显示进度而不是只给一个结论。
type aiBarStage int

const (
	aiBarIdle aiBarStage = iota
	aiBarSearching
	aiBarThinking
	aiBarDone
)

// aiBarState is the suggestion after a like, which stays until the listener
// adds it, adjusts it, or closes it.
type aiBarState struct {
	open   bool
	busy   bool
	failed bool
	track  music.Track
	lib    *library
	pick   ai.Suggestion
	name   string
	// stage 是联网与判断的进度；sources 是搜到的网页，供"依据"面板核实。
	stage        aiBarStage
	sources      []websearch.Result
	evidenceOpen bool
	query        string
}

// aiRefineState is the conversation that talks a suggestion into another
// playlist: the turns so far, and the pick they arrive at.
type aiRefineState struct {
	open       bool
	busy       bool
	failed     bool
	track      music.Track
	lib        *library
	candidates []ai.Candidate
	history    []ai.Message
	draft      string
	pick       ai.Suggestion
	name       string
	prefs      []string
	// learned is the listener's reason for changing the playlist.
	learned string
}

// aiSearchQueryFor reports the query the app would search with, and whether
// web search is part of this suggestion at all.
func (a *app) aiSearchQueryFor(song ai.Song, learned bool) (string, bool) {
	if learned || a.aiWebProvider() == nil {
		return "", false
	}
	return aiSearchQuery(song), true
}

func aiStageStatus(stage aiBarStage) string {
	switch stage {
	case aiBarSearching:
		return T("Searching the web…")
	case aiBarThinking:
		return T("Thinking…")
	default:
		return T("Completed")
	}
}

// openAISuggestion asks the configured model which own playlist fits tr.
func (a *app) openAISuggestion(tr *music.Track) {
	lib := a.trackLib(tr)
	if lib == nil || !lib.can("addToPlaylist") {
		return
	}
	prefs, learned := a.aiPreferenceTexts(lib, tr.ID)
	// 已有这首歌的调整记录，模型直接复用曲目信息和调整原因。
	cfg := a.aiConfig(a.settings.AIWebProvider == "native" && !learned)
	song := ai.Song{Name: tr.Name, Artists: tr.ArtistNames(), Album: tr.Album.Name}
	query, web := a.aiSearchQueryFor(song, learned)
	stage := aiBarThinking
	if web {
		stage = aiBarSearching
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.aiSuggestion = aiSuggestionState{
		open: true, busy: true, track: *tr, lib: lib,
		stage: stage, query: query, cancel: cancel,
	}
	go func() {
		defer cancel()
		var results []websearch.Result
		if web {
			// 已有记录时不重复联网查同一首歌。
			webCtx, cancelWeb := context.WithTimeout(ctx, 20*time.Second)
			results = a.aiWebContext(webCtx, song)
			cancelWeb()
		}
		if len(results) > 5 {
			results = results[:5]
		}
		a.post(func() {
			d := &a.aiSuggestion
			if d.track.ID != tr.ID || (!d.open && d.task == nil) {
				return
			}
			d.stage, d.sources = aiBarThinking, results
			if d.task != nil && d.task.state == backgroundTaskRunning {
				d.task.status = aiStageStatus(aiBarThinking)
			}
			a.redraw()
		})
		candidates := a.aiCandidates(lib, tr, 100)
		if len(candidates) == 0 {
			a.post(func() { a.finishAISuggestion(tr.ID, ai.Suggestion{}, "", false) })
			return
		}
		// 联网的 reasoning 模型可能接近一分钟，手动入口给它更长的余地。
		aiCtx, cancelAI := context.WithTimeout(ctx, 120*time.Second)
		defer cancelAI()
		pick, err := ai.Suggest(aiCtx, newAIClient(cfg), ai.SuggestInput{
			Song:        song,
			Candidates:  candidates,
			Preferences: prefs,
			WebResults:  toWebResults(results),
		})
		name := pick.PlaylistID
		for _, c := range candidates {
			if c.ID == pick.PlaylistID {
				name = c.Name
				break
			}
		}
		a.post(func() {
			a.finishAISuggestion(tr.ID, pick, name, err == nil && pick.PlaylistID != "")
		})
	}()
}

// finishAISuggestion settles the manual dialog, and the folded task if the
// listener put it in the background while the model was working.
func (a *app) finishAISuggestion(id string, pick ai.Suggestion, name string, ok bool) {
	d := &a.aiSuggestion
	if d.track.ID != id {
		return
	}
	d.busy = false
	d.stage = aiBarDone
	if ok {
		d.pick, d.name, d.failed = pick, name, false
	} else {
		d.pick, d.failed = ai.Suggestion{}, true
	}
	if task := d.task; task != nil {
		task.cancel = nil
		if ok {
			task.state, task.status = backgroundTaskSucceeded, Tf("Suggests %s", name)
			task.actions = []backgroundTaskAction{{label: T("Add"), run: func() {
				d.open = false
				a.addTo(d.lib, pick.PlaylistID, &d.track)
				a.removeBackgroundTask(task)
				d.task = nil
				a.redraw()
			}}}
		} else {
			task.state, task.status = backgroundTaskFailed, T("Couldn't guess a playlist. Choose one yourself.")
		}
	}
	a.redraw()
}

// foldAISuggestion closes the dialog but keeps the work going in the task
// center, so a slow search does not block the rest of the app.
func (a *app) foldAISuggestion() {
	d := &a.aiSuggestion
	if d.stage == aiBarIdle {
		return
	}
	d.open = false
	d.evidenceOpen = false
	// 已折叠过：再次收起只是关掉重新打开的弹窗，任务卡继续留着。
	if d.task != nil {
		a.redraw()
		return
	}
	summary := T("AI Playlist Suggestion")
	if artists := d.track.ArtistNames(); artists != "" {
		summary += " · " + artists
	}
	task := a.newQuietBackgroundTask(d.track.Name, summary)
	task.kind = backgroundTaskAI
	task.status = aiStageStatus(d.stage)
	task.open = func() {
		d.open = true
		a.redraw()
	}
	task.cancel = func() {
		if d.cancel != nil {
			d.cancel()
		}
		task.state, task.status = backgroundTaskCanceled, T("Canceled")
		task.cancel, task.retry = nil, nil
		delete(a.aiAsked, d.track.ID)
		a.redraw()
	}
	d.task = task
	a.redraw()
}

// retryAISuggestion asks again for the same song after a failed manual
// request, dropping the failed card if the dialog was folded meanwhile.
func (a *app) retryAISuggestion() {
	d := &a.aiSuggestion
	if d.track.ID == "" {
		return
	}
	tr := d.track
	if d.cancel != nil {
		d.cancel()
	}
	if task := d.task; task != nil {
		a.removeBackgroundTask(task)
	}
	a.aiSuggestion = aiSuggestionState{}
	a.openAISuggestion(&tr)
}

// dropAISuggestion cancels the manual request and forgets a folded task.
func (a *app) dropAISuggestion() {
	d := &a.aiSuggestion
	if d.cancel != nil {
		d.cancel()
	}
	if task := d.task; task != nil {
		if task.state == backgroundTaskRunning {
			task.state, task.status = backgroundTaskCanceled, T("Canceled")
			task.cancel = nil
		}
		a.removeBackgroundTask(task)
	}
	a.aiSuggestion = aiSuggestionState{}
	a.aiBar = aiBarState{}
	a.aiRefine = aiRefineState{}
	a.aiSuggestion.open = false
	a.redraw()
}

func (a *app) aiConfig(web bool) ai.Config {
	key := ""
	if b, err := getAISecret(aiSecretKey); err == nil {
		key = string(b)
	}
	tool := "web_search"
	if strings.Contains(strings.ToLower(a.settings.AIBaseURL), "api.deepseek.com") {
		tool = "deepseek_web_search"
	}
	return ai.Config{
		BaseURL: a.settings.AIBaseURL, APIKey: key, Model: a.settings.AIModel,
		// 仅 native 模式由服务商在服务端联网；其他模式不带工具字段。
		WebSearch: web && a.settings.AIWebProvider == "native", WebSearchTool: tool,
	}
}

// aiWebProvider returns the configured search provider, or nil when the
// listener picked none or a native one.
func (a *app) aiWebProvider() websearch.Provider {
	name := a.settings.AIWebProvider
	if name == "" || name == "off" || name == "native" {
		return nil
	}
	key := ""
	if b, err := getAISecret("ai-web-key-" + name); err == nil {
		key = string(b)
	}
	return newWebProvider(name, key)
}

// aiWebContext searches the web for the song's identity before the
// suggestion, so the model can correct a mismatched name or a lost artist.
// 查询只含歌曲信息，不含用户的歌单名；失败返回 nil 按无联网继续。
// aiSearchQuery 只含歌曲信息，不含用户的歌单名；界面也用它展示实际搜了什么。
func aiSearchQuery(song ai.Song) string {
	query := song.Name
	if song.Artists != "" {
		query += " " + song.Artists
	}
	if song.Album != "" {
		query += " " + song.Album
	}
	return query
}

func (a *app) aiWebContext(ctx context.Context, song ai.Song) []websearch.Result {
	p := a.aiWebProvider()
	if p == nil {
		return nil
	}
	results, err := p.Search(ctx, aiSearchQuery(song))
	if err != nil || len(results) == 0 {
		return nil
	}
	return results
}

// toWebResults caps the search hits at five and trims each snippet, so the
// search content never crowds out the candidate playlists.
func toWebResults(results []websearch.Result) []ai.WebResult {
	out := make([]ai.WebResult, 0, min(len(results), 5))
	for _, r := range results[:min(len(results), 5)] {
		snippet := r.Snippet
		if runes := []rune(snippet); len(runes) > 400 {
			snippet = string(runes[:400]) + "…"
		}
		out = append(out, ai.WebResult{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	return out
}

func (a *app) aiSettings(c *ui.Context, p palette, s *settings, row settingsRow, block settingsBlock) {
	settingsHeading(c, block, T("AI Playlist Suggestions"), iconSparkles)
	block(T("AI Playlist Suggestions"), func() {
		row(T("Enable AI Suggestions"), T("Let a chat completion service choose one of your own playlists for a song."), func() {
			if ui.Switch(c.Key("ai-enabled"), &s.AIEnabled).Label(T("Enable AI Suggestions")).Changed() {
				a.changed()
			}
		})
		row(T("Base URL"), T("Any endpoint that answers the OpenAI chat completions protocol."), func() {
			in := ui.TextInput(c.Key("ai-base-url"), &s.AIBaseURL).Label(T("Base URL")).FontSize(13)
			if in.Changed() {
				a.changed()
			}
		})
		row(T("Model"), "", func() {
			in := ui.TextInput(c.Key("ai-model"), &s.AIModel).Label(T("Model")).FontSize(13)
			if in.Changed() {
				a.changed()
			}
		})
		row(T("API Key"), T("Stored in the operating system credential store, not settings.json."), func() {
			ui.Column(c).Width(320).MaxWidthPercent(100).Gap(6).Children(func() {
				ui.TextInput(c.Key("ai-key"), &a.aiKeyDraft).Label(T("API Key")).Password().FontSize(13)
				if ui.Button(c.Key("ai-save-key"), T("Save Key")).Disabled(strings.TrimSpace(a.aiKeyDraft) == "").Clicked() {
					a.saveAIKey()
				}
				if a.aiStatus != "" {
					ui.Text(c, a.aiStatus).FontSize(11.5).TextColor(p.muted)
				}
			})
		})
		row(T("Test Connection"), "", func() {
			if ui.Button(c.Key("ai-test"), T("Test Connection")).
				Disabled(a.aiTesting || s.AIBaseURL == "" || s.AIModel == "").Clicked() {
				a.testAIConnection()
			}
		})
		// 联网方式：off 关闭；native 让服务商在服务端联网；tavily /
		// anysearch 由应用先搜一次再拼进提示词。
		row(T("Web Search"), T("How the suggestion may look the song up: the service's own search, or an app-side search API. Searching sends the song's name, artists and album only."), func() {
			names := []string{T("Off"), T("Service's own"), "Tavily", "AnySearch"}
			values := []string{"off", "native", "tavily", "anysearch"}
			chosen := names[0]
			for index, value := range values {
				if value == s.AIWebProvider || (s.AIWebProvider == "" && value == "off") {
					chosen = names[index]
				}
			}
			if ui.Select(c.Key("ai-web-provider"), &chosen, names).Label(T("Web Search")).Width(170).Changed() {
				for index, name := range names {
					if name == chosen {
						s.AIWebProvider = values[index]
						break
					}
				}
				a.changed()
			}
		})
		// 应用侧搜索需要独立的搜索服务密钥；native 模式不需要。
		if s.AIWebProvider == "tavily" || s.AIWebProvider == "anysearch" {
			row(T("Search API Key"), T("Stored in the operating system credential store, not settings.json."), func() {
				ui.Column(c).Width(320).MaxWidthPercent(100).Gap(6).Children(func() {
					ui.TextInput(c.Key("ai-web-key"), &a.aiWebKeyDraft).Label(T("Search API Key")).Password().FontSize(13)
					if ui.Button(c.Key("ai-web-save-key"), T("Save Key")).
						Disabled(strings.TrimSpace(a.aiWebKeyDraft) == "").Clicked() {
						a.saveAIWebKey()
					}
					if a.aiWebStatus != "" {
						ui.Text(c, a.aiWebStatus).FontSize(11.5).TextColor(p.muted)
					}
				})
			})
		}
		row(T("Suggest After Like"), T("After a like, ask for a playlist in the background and show it only when the model is confident."), func() {
			on := !s.AIAutoSuggestDisabled
			if ui.Switch(c.Key("ai-auto-suggest"), &on).Label(T("Suggest After Like")).Changed() {
				s.AIAutoSuggestDisabled = !on
				a.changed()
			}
		})
		row(T("Add When Confident"), T("Add the song to the suggested playlist at once when the model is at least 85 percent sure. You can undo for 10 seconds."), func() {
			if ui.Switch(c.Key("ai-auto-add"), &s.AIAutoAdd).Label(T("Add When Confident")).Changed() {
				a.changed()
			}
		})
	})
	a.aiPreferencesSettings(c, p, block)
}

// aiPreferencesSettings shows what the AI has learned, one row per preference,
// each removable by hand, and a way to add one without adopting a suggestion.
// 偏好跟随当前激活音源与账号，切换来源后这里看到的是另一份列表。
func (a *app) aiPreferencesSettings(c *ui.Context, p palette, block settingsBlock) {
	if a.aiPrefs == nil || a.library == nil {
		return
	}
	settingsHeading(c, block, T("AI Learned Preferences"), iconSparkles)
	block(T("AI Learned Preferences"), func() {
		prefs := a.aiPrefs.forListener(a.library.id, a.library.listener())
		ui.Text(c.Key("ai-prefs-limit"), Tf("Keep up to %d preferences per source and account. When full, new entries replace the oldest.", preferenceLimit)).
			FontSize(11.5).TextColor(p.muted).LineHeight(1.4).Margin(8, 16, 8, 16)
		if len(prefs) == 0 {
			// 空态说明偏好从哪来，避免用户以为要在这里凭空配置。
			ui.Text(c.Key("ai-prefs-empty"), T("Preferences the AI learns when you adopt an adjusted suggestion show here. Add one to start.")).
				FontSize(12).TextColor(p.muted).LineHeight(1.4).Margin(8, 16, 8, 16)
		}
		for i, pref := range prefs {
			ui.Row(c.Key(fmt.Sprintf("ai-pref-%d", i))).Padding(10, 16).Gap(10).AlignItems(ui.Center).Children(func() {
				if pref.CoverURL != "" {
					a.cover(c, pref.CoverURL, 96, 40, 6).Shrink(0)
				}
				ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(3).Children(func() {
					title := pref.TrackName
					if title == "" {
						title = pref.Text
					}
					if title == "" {
						title = Tf("Preference %d", i+1)
					}
					ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine().Shrink(1).MinWidth(0)
					details := pref.Artists
					if pref.Album != "" {
						if details != "" {
							details += " · "
						}
						details += pref.Album
					}
					if details != "" {
						ui.Text(c, details).FontSize(11).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
					}
					if pref.TrackName != "" && pref.Text != "" {
						ui.Text(c, Tf("You asked: %s", pref.Text)).FontSize(12).LineHeight(1.35)
					}
					if pref.PlaylistName != "" || !pref.UpdatedAt.IsZero() {
						by := i18n.Ago(pref.UpdatedAt, time.Now())
						if pref.PlaylistName != "" {
							by = Tf("→ %s · %s", pref.PlaylistName, by)
						}
						ui.Text(c, by).FontSize(11).TextColor(p.muted).SingleLine()
					}
				})
				if iconButton(c.Key("ai-pref-remove"), iconTrash, 15, p.muted, T("Remove")).
					Size(28, 28).Radius(7).Clicked() {
					a.aiPrefs.remove(a.library.id, a.library.listener(), i)
					a.redraw()
				}
			})
		}
		// 手动添加默认收起，点开才占一行的位置。
		if !a.aiPrefOpen {
			ui.Row(c.Key("ai-pref-add-row")).Padding(8, 16, 12, 16).Children(func() {
				if ui.Button(c.Key("ai-pref-add"), T("Add a Preference")).Clicked() {
					a.aiPrefOpen = true
					a.redraw()
				}
			})
			return
		}
		ui.Row(c.Key("ai-pref-new")).Padding(8, 16, 12, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c.Key("ai-pref-draft"), &a.aiPrefDraft).
				Placeholder(T("How should the AI pick playlists?")).Label(T("Add a Preference")).
				FontSize(13).Grow(1).MinWidth(0)
			if in.Submitted() {
				a.addAIPreference()
			}
			if ui.Button(c.Key("ai-pref-save"), T("Save")).
				Disabled(strings.TrimSpace(a.aiPrefDraft) == "").Clicked() {
				a.addAIPreference()
			}
			if ui.Button(c.Key("ai-pref-cancel"), T("Cancel")).Clicked() {
				a.aiPrefDraft = ""
				a.aiPrefOpen = false
				a.redraw()
			}
		})
	})
}

// addAIPreference keeps what the listener typed by hand, bound to the active
// source and account like a learned one.
func (a *app) addAIPreference() {
	text := strings.TrimSpace(a.aiPrefDraft)
	if text == "" || a.aiPrefs == nil {
		return
	}
	a.aiPrefs.add(Preference{Source: a.library.id, Account: a.library.listener(), Text: text})
	a.aiPrefDraft = ""
	a.aiPrefOpen = false
	a.redraw()
}

func (a *app) saveAIKey() {
	key := strings.TrimSpace(a.aiKeyDraft)
	if key == "" {
		return
	}
	if err := setAISecret(aiSecretKey, []byte(key)); err != nil {
		a.aiStatus = T("Couldn't save the API key")
		return
	}
	a.aiKeyDraft = ""
	a.aiStatus = T("API key saved")
	a.redraw()
}

// saveAIWebKey stores the app-side search provider's key under its own
// secret name, so the AI service key and the search key never mix.
func (a *app) saveAIWebKey() {
	key := strings.TrimSpace(a.aiWebKeyDraft)
	if key == "" {
		return
	}
	name := "ai-web-key-" + a.settings.AIWebProvider
	if err := setAISecret(name, []byte(key)); err != nil {
		a.aiWebStatus = T("Couldn't save the API key")
		return
	}
	a.aiWebKeyDraft = ""
	a.aiWebStatus = T("API key saved")
	a.redraw()
}

func (a *app) testAIConnection() {
	a.aiTesting = true
	a.aiStatus = T("Testing…")
	cfg := a.aiConfig(false)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := newAIClient(cfg).Complete(ctx, []ai.Message{{Role: "user", Content: "Reply with OK."}})
		a.post(func() {
			a.aiTesting = false
			if err != nil {
				a.aiStatus = Tf("Connection failed: %s", err)
			} else {
				a.aiStatus = T("Connection works")
			}
			a.redraw()
		})
	}()
}

// settled reports whether the pick can be acted on: 进行中的搜索与判断不给
// 按钮，其余阶段（含未设阶段的旧状态）都算有结果。
func (b *aiBarState) settled() bool {
	return b.stage != aiBarSearching && b.stage != aiBarThinking
}

// aiBarMatches reports whether the bar still belongs to this song: a listener
// who closed it, or a newer like, must not be overwritten by a late result.
func (a *app) aiBarMatches(id string) bool {
	return a.aiBar.open && a.aiBar.track.ID == id
}

// closeAIBar hides the bar for this song, leaving a newer song's bar alone.
func (a *app) closeAIBar(id string) {
	if a.aiBarMatches(id) {
		a.aiBar = aiBarState{}
		a.redraw()
	}
}

// autoSuggestPlaylist asks for a suggestion after the source confirmed a like.
// 搜索与判断只放在任务队列里，模型有把握才亮出推荐条，没把握就安静收走。
func (a *app) autoSuggestPlaylist(lib *library, tr music.Track) {
	if !a.settings.AIEnabled || a.settings.AIAutoSuggestDisabled ||
		a.settings.AIBaseURL == "" || a.settings.AIModel == "" {
		return
	}
	if a.aiAsked == nil {
		a.aiAsked = map[string]bool{}
	}
	if a.aiAsked[tr.ID] {
		return
	}
	a.aiAsked[tr.ID] = true
	prefs, learned := a.aiPreferenceTexts(lib, tr.ID)
	cfg := a.aiConfig(a.settings.AIWebProvider == "native" && !learned)
	song := ai.Song{Name: tr.Name, Artists: tr.ArtistNames(), Album: tr.Album.Name}
	query, web := a.aiSearchQueryFor(song, learned)
	stage := aiBarThinking
	if web {
		stage = aiBarSearching
	}
	ctx, cancel := context.WithCancel(context.Background())
	summary := T("AI Playlist Suggestion")
	if artists := tr.ArtistNames(); artists != "" {
		summary += " · " + artists
	}
	task := a.newQuietBackgroundTask(tr.Name, summary)
	task.kind = backgroundTaskAI
	task.status = aiStageStatus(stage)
	task.cancel = func() {
		cancel()
		task.state, task.status = backgroundTaskCanceled, T("Canceled")
		task.cancel = nil
		delete(a.aiAsked, tr.ID)
		a.redraw()
	}
	a.redraw()
	go func() {
		defer cancel()
		candidates := a.aiCandidates(lib, &tr, 0)
		if len(candidates) == 0 {
			a.post(func() {
				a.dropAIBackgroundTask(task)
				delete(a.aiAsked, tr.ID)
				a.redraw()
			})
			return
		}
		// 应用侧搜索给独立子超时；失败或没结果就按无联网继续判断。
		var results []websearch.Result
		if web {
			webCtx, cancelWeb := context.WithTimeout(ctx, 20*time.Second)
			results = a.aiWebContext(webCtx, song)
			cancelWeb()
		}
		if len(results) > 5 {
			results = results[:5]
		}
		a.post(func() {
			if task.removed || task.state != backgroundTaskRunning {
				return
			}
			task.status = aiStageStatus(aiBarThinking)
			a.redraw()
		})
		aiCtx, cancelAI := context.WithTimeout(ctx, 30*time.Second)
		defer cancelAI()
		pick, err := ai.Suggest(aiCtx, newAIClient(cfg), ai.SuggestInput{
			Song:        song,
			Candidates:  candidates,
			Preferences: prefs,
			WebResults:  toWebResults(results),
		})
		if err != nil {
			a.post(func() {
				delete(a.aiAsked, tr.ID)
				a.dropAIBackgroundTask(task)
				// 失败只提示一次；提示里直接重试，不必先取消喜欢再点一次。
				a.aiToast = &ui.Toast{
					ID:       "ai-suggest-failed-" + tr.ID,
					Title:    T("Couldn't suggest a playlist"),
					Action:   T("Retry"),
					OnAction: func() { a.autoSuggestPlaylist(lib, tr) },
					Timeout:  10 * time.Second,
				}
				a.redraw()
			})
			return
		}
		if pick.PlaylistID == "" || pick.Confidence < 0.85 {
			a.post(func() {
				a.dropAIBackgroundTask(task)
				a.redraw()
			})
			return
		}
		name := pick.PlaylistID
		for _, c := range candidates {
			if c.ID == pick.PlaylistID {
				name = c.Name
				break
			}
		}
		a.post(func() {
			if task.removed {
				return
			}
			a.dropAIBackgroundTask(task)
			a.aiToast = nil
			a.aiBar = aiBarState{
				open: true, track: tr, lib: lib, pick: pick, name: name,
				stage: aiBarDone, sources: results, query: query,
			}
			if a.settings.AIAutoAdd && lib.can("removeFromPlaylist") {
				a.aiBar.busy = true
				a.addToThen(lib, pick.PlaylistID, &tr, func(err error) {
					a.aiBar.busy = false
					if err != nil {
						return
					}
					a.aiBar.open = false
					a.aiToast = &ui.Toast{
						ID:       "ai-added-" + tr.ID,
						Title:    Tf("Added to %s", name),
						Action:   T("Undo"),
						OnAction: func() { a.removeFrom(lib, pick.PlaylistID, &tr) },
						Timeout:  10 * time.Second,
					}
					a.redraw()
				})
				return
			}
			a.redraw()
		})
	}()
}

// dropAIBackgroundTask removes a quiet AI record even while it is still the
// running task; the caller has already stopped its work.
func (a *app) dropAIBackgroundTask(task *backgroundTask) {
	if task == nil || task.removed {
		return
	}
	if task.state == backgroundTaskRunning {
		task.state, task.status = backgroundTaskCanceled, T("Canceled")
		task.cancel = nil
	}
	a.removeBackgroundTask(task)
}

// addSuggested adds what the bar or the dialog suggests, and lets the
// suggestion go, including any record it left in the task center.
func (a *app) addSuggested(lib *library, tr *music.Track, pick ai.Suggestion) {
	if task := a.aiSuggestion.task; task != nil {
		a.dropAIBackgroundTask(task)
	}
	a.aiBar.open = false
	a.aiSuggestion.open = false
	a.aiRefine.open = false
	a.addTo(lib, pick.PlaylistID, tr)
}

// closeSuggestion hides the suggestion for this song for the rest of the
// session, as aiAsked already keeps it from being asked again.
func (a *app) closeSuggestion() {
	a.dropAISuggestion()
}

// aiSuggestOverlay draws the suggestion and its evidence above everything,
// including Now Playing, which hides the whole browse page. 封面页没有侧边栏
// 也没有播放条，两者都贴窗口边。
func (a *app) aiSuggestOverlay(c *ui.Context, onNowPlaying bool) {
	if !a.aiBar.open {
		return
	}
	left := float32(sidebarWidth + 16)
	bottom := float32(playerBarHeight + 4)
	if onNowPlaying {
		left, bottom = 16, 24
	}
	a.evidencePanel(c, a.pal, left, bottom)
	a.suggestionBar(c, a.pal, left, bottom)
}

// evidencePanel is the expandable source list above the bar: each hit is a
// link the listener can open to check what the model actually read.
func (a *app) evidencePanel(c *ui.Context, p palette, left, bottom float32) {
	b := &a.aiBar
	if !b.open || !b.evidenceOpen || !b.settled() {
		return
	}
	g := shellGlass(p.dark, 0.48, 0.52)
	panel := ui.Column(c.Key("ai-evidence")).Absolute().Left(left).Right(16).Bottom(bottom+64).
		Padding(14).Gap(6).Radius(16).
		// 依据面板压在最前台，玻璃调得更实，避免透出下面的播放条显脏。
		Material(glass.Glass{Style: glass.Regular, Clarity: 0.88, Tint: g.tint}).
		Border(1, g.border).Shadow(0, 12, 32, -6, g.shade)
	panel.Children(func() {
		ui.Text(c, T("Evidence")).FontSize(12).FontWeight(600).TextColor(p.accent)
		if len(b.sources) == 0 {
			ui.Text(c, T("No web results")).FontSize(11.5).TextColor(p.muted)
		}
		for i, src := range b.sources {
			if src.Title == "" || src.URL == "" {
				continue
			}
			link := ui.ButtonBase(c.Key(fmt.Sprint("ai-evidence-", i))).
				Height(24).Justify(ui.Start).Role(ui.RoleLink).Label(src.Title)
			link.Children(func() {
				ui.Text(c, src.Title).FontSize(11.5).TextColor(p.text).Shrink(1).MinWidth(0)
			})
			if link.Clicked() {
				mygo.Shell.OpenExternal(src.URL)
			}
		}
		if b.pick.Reason != "" {
			ui.Text(c, b.pick.Reason).FontSize(11.5).TextColor(p.muted)
		}
	})
}

// suggestionBar is the suggestion after a like: it stays above the player,
// with adding it, adjusting it, or closing it.
func (a *app) suggestionBar(c *ui.Context, p palette, left, bottom float32) {
	b := &a.aiBar
	if !b.open {
		return
	}
	// 与底部播放胶囊同款玻璃：推荐条悬浮在内容之上，实心底色会在封面氛围下突兀。
	g := shellGlass(p.dark, 0.48, 0.52)
	bar := ui.Row(c.Key("ai-suggestion-bar")).Absolute().Left(left).Right(16).Bottom(bottom).Height(56).
		PaddingX(16).Gap(12).AlignItems(ui.Center).Radius(20).
		// 与播放条同款玻璃，但更实：它在最前台，下面就是播放条。
		Material(glass.Glass{Style: glass.Regular, Clarity: 0.88, Tint: g.tint}).
		Border(1, g.border).Shadow(0, 12, 32, -6, g.shade)
	bar.Children(func() {
		// AI 标记：歌名前的小星形提示这是模型的判断，不是系统通知。
		ui.Icon(c, iconSparkles).Size(16, 16).Shrink(0).TextColor(p.accent)
		ui.Column(c).Grow(1).Basis(0).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			// 阶段文案让"正在搜 / 正在判断"可见，出结果后才显示歌单名。
			title := Tf("AI suggests %s", b.name)
			switch b.stage {
			case aiBarSearching:
				title = T("Searching the web…")
			case aiBarThinking:
				title = T("Thinking…")
			}
			switch {
			case b.busy:
				title = T("Adding…")
			case b.failed:
				title = T("Couldn't guess a playlist. Choose one yourself.")
			}
			ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine().Shrink(1).MinWidth(0)
			// 有联网时第二行优先展示实际搜索词，让听众知道结论从何而来。
			line := b.pick.Reason
			if b.query != "" {
				line = Tf("Search: %s", b.query)
			}
			if b.settled() && line != "" && !b.failed {
				ui.Text(c, line).FontSize(11.5).TextColor(p.muted).SingleLine().Shrink(1).MinWidth(0)
			}
		})
		if b.settled() && !b.busy && !b.failed {
			// 依据按钮：展开后能看到模型读过的网页。
			if len(b.sources) > 0 {
				label := Tf("Searched %d pages", len(b.sources))
				if pillButton(c.Key("ai-bar-evidence"), nil, label, p.hover.Over(p.bg), p.text).Clicked() {
					b.evidenceOpen = !b.evidenceOpen
				}
			}
			// 主操作用 accent，次操作用 hover 底，与对话框按钮的层级一致。
			if pillButton(c.Key("ai-bar-add"), nil, T("Add"), p.accent, p.onAccent).Clicked() {
				a.addSuggested(b.lib, &b.track, b.pick)
			}
			if pillButton(c.Key("ai-bar-adjust"), nil, T("Adjust"), p.hover.Over(p.bg), p.text).Clicked() {
				a.openAIRefine()
			}
		}
		if pillButton(c.Key("ai-bar-close"), nil, T("Close"), p.hover.Over(p.bg), p.text).Clicked() {
			a.closeSuggestion()
		}
	})
}

// aiCandidates are the listener's own playlists for the song's source, with
// at most samples of each. The model never sees saved or fixed playlists.
func (a *app) aiCandidates(lib *library, tr *music.Track, samples int) []ai.Candidate {
	var out []ai.Candidate
	for _, pl := range lib.mine {
		if len(out) >= 20 {
			break
		}
		if !pl.Mine || pl.Fixed {
			continue
		}
		c := ai.Candidate{ID: pl.ID, Name: pl.Name, Description: pl.Description, TrackCount: pl.TrackCount}
		if samples > 0 {
			if full, err := lib.src.Playlist(pl.ID); err == nil && full != nil {
				for _, t := range full.Tracks {
					if len(c.Samples) >= samples {
						break
					}
					line := t.Name
					if names := t.ArtistNames(); names != "" {
						line += " - " + names
					}
					c.Samples = append(c.Samples, line)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func (a *app) aiSuggestionDialog(c *ui.Context) {
	p := a.pal
	d := &a.aiSuggestion
	// 背景点击或 Esc 关闭时，进行中的工作折叠进任务中心，而不是白跑一趟。
	if d.busy && !d.open && d.task == nil && d.stage != aiBarIdle {
		a.foldAISuggestion()
	}
	ui.DialogBase(c.Key("ai-suggestion"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(420).MinHeight(240).MaxHeight(560).Padding(24).Radius(18).Gap(14).
			Background(p.bg).Border(1, p.hairline).Shadow(0, 24, 60, -12, ui.RGBA(0, 0, 0, 0.4)).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.94}, Exit: &ui.Motion{Scale: 0.96}})
		panel.Children(func() {
			ui.Column(c).Gap(12).Children(func() {
				ui.Text(c, T("AI Playlist Suggestion")).FontSize(17).FontWeight(700)
				switch {
				case d.busy:
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c).FontSize(14).TextColor(p.accent).Label(aiStageStatus(d.stage))
						ui.Text(c, aiStageStatus(d.stage)).FontSize(13).TextColor(p.muted)
					})
					if d.query != "" {
						ui.Text(c, Tf("Search: %s", d.query)).FontSize(12).TextColor(p.faint)
					}
				case d.failed:
					ui.Text(c, T("Couldn't guess a playlist. Choose one yourself.")).FontSize(13).TextColor(p.muted)
					if d.query != "" {
						ui.Text(c, Tf("Search: %s", d.query)).FontSize(12).TextColor(p.faint)
					}
				default:
					ui.Text(c, d.name).FontSize(15).FontWeight(600)
					ui.Text(c, d.pick.Reason).FontSize(13).TextColor(p.muted)
					ui.Text(c, Tf("%d percent confident", int(d.pick.Confidence*100))).FontSize(11).TextColor(p.faint)
					if d.query != "" || len(d.sources) > 0 {
						if pillButton(c.Key("ai-dialog-evidence"), nil,
							Tf("Evidence · %d pages", len(d.sources)), p.hover.Over(p.bg), p.text).Clicked() {
							d.evidenceOpen = !d.evidenceOpen
						}
						if d.evidenceOpen {
							a.aiSuggestionEvidence(c, p)
						}
					}
				}
				ui.Row(c).Width(372).Height(36).Gap(10).Justify(ui.End).Children(func() {
					if pillButton(c.Key("ai-cancel"), nil, T("Cancel"), p.hover.Over(p.bg), p.text).Clicked() {
						a.dropAISuggestion()
					}
					if d.busy {
						if pillButton(c.Key("ai-fold"), nil, T("Fold"), p.hover.Over(p.bg), p.text).Clicked() {
							a.foldAISuggestion()
						}
					} else if d.failed {
						// 失败不必关掉弹窗再从头点一遍，原地再问一次。
						if pillButton(c.Key("ai-suggestion-retry"), nil, T("Retry"), p.accent, p.onAccent).Clicked() {
							a.retryAISuggestion()
						}
					} else {
						if pillButton(c.Key("ai-suggestion-adjust"), nil, T("Adjust"), p.hover.Over(p.bg), p.text).Clicked() {
							a.openAIRefineFromSuggestion()
						}
						if pillButton(c.Key("ai-add"), nil, T("Add"), p.accent, p.onAccent).Clicked() {
							a.addSuggested(d.lib, &d.track, d.pick)
						}
					}
				})
			})
		})
	})
}

// aiSuggestionEvidence lists the actual query and the pages the model read,
// inside the dialog, so the listener can check the suggestion without leaving.
func (a *app) aiSuggestionEvidence(c *ui.Context, p palette) {
	d := &a.aiSuggestion
	ui.Column(c).Padding(12, 14).Gap(6).Radius(12).
		Background(p.hover).Border(1, p.hairline).Children(func() {
		if d.query != "" {
			ui.Text(c, Tf("Search: %s", d.query)).FontSize(11.5).TextColor(p.muted)
		}
		if len(d.sources) == 0 {
			ui.Text(c, T("No web results")).FontSize(11.5).TextColor(p.muted)
			return
		}
		for index, src := range d.sources {
			if src.Title == "" || src.URL == "" {
				continue
			}
			link := ui.ButtonBase(c.Key(fmt.Sprint("ai-dialog-source-", index))).
				Height(22).Justify(ui.Start).Role(ui.RoleLink).Label(src.Title)
			if link.Clicked() {
				mygo.Shell.OpenExternal(src.URL)
			}
			link.Children(func() {
				ui.Text(c, src.Title).FontSize(11.5).TextColor(p.text).SingleLine().Shrink(1).MinWidth(0)
			})
		}
	})
}

// openAIRefine starts the conversation about the suggestion on screen.
func (a *app) openAIRefine() {
	if a.aiBar.open {
		a.openAIRefineFrom(a.aiBar.track, a.aiBar.lib, a.aiBar.pick, a.aiBar.name)
	}
}

// openAIRefineFromSuggestion starts the same conversation from the manual
// dialog: 手动入口和喜欢后的推荐条用同一套调整流程。
func (a *app) openAIRefineFromSuggestion() {
	d := a.aiSuggestion
	if d.open {
		a.openAIRefineFrom(d.track, d.lib, d.pick, d.name)
	}
}

func (a *app) openAIRefineFrom(tr music.Track, lib *library, pick ai.Suggestion, name string) {
	if lib == nil {
		return
	}
	prefs, _ := a.aiPreferenceTexts(lib, tr.ID)
	a.aiRefine = aiRefineState{
		open:    true,
		track:   tr,
		lib:     lib,
		history: []ai.Message{{Role: "assistant", Content: suggestionJSON(pick)}},
		pick:    pick,
		name:    name,
		prefs:   prefs,
	}
	go func() {
		candidates := a.aiCandidates(lib, &tr, 100)
		a.post(func() {
			a.aiRefine.candidates = candidates
		})
	}()
}

// refineSuggestion sends what the listener said, and shows the pick it led
// to.
func (a *app) refineSuggestion() {
	d := &a.aiRefine
	text := strings.TrimSpace(d.draft)
	if text == "" || d.busy || len(d.candidates) == 0 {
		return
	}
	d.busy = true
	d.failed = false
	d.history = append(d.history, ai.Message{Role: "user", Content: text})
	d.draft = ""
	cfg := a.aiConfig(false)
	prefs := d.prefs
	history := slices.Clone(d.history)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		pick, err := ai.Refine(ctx, newAIClient(cfg), ai.RefineInput{
			SuggestInput: ai.SuggestInput{
				Song:        ai.Song{Name: d.track.Name, Artists: d.track.ArtistNames(), Album: d.track.Album.Name},
				Candidates:  d.candidates,
				Preferences: prefs,
			},
			History: history,
		})
		a.post(func() {
			d.busy = false
			if err != nil || pick.PlaylistID == "" {
				d.failed = true
				return
			}
			d.history = append(d.history, ai.Message{Role: "assistant", Content: suggestionJSON(pick)})
			d.pick = pick
			d.name = pick.PlaylistID
			for _, c := range d.candidates {
				if c.ID == pick.PlaylistID {
					d.name = c.Name
					break
				}
			}
			var adjustments []string
			for _, message := range d.history {
				if message.Role == "user" {
					adjustments = append(adjustments, message.Content)
				}
			}
			d.learned = strings.Join(adjustments, "\n")
			a.redraw()
		})
	}()
}

// adoptRefined adds the refined pick and learns the listener's words, only
// when they adopt it.
func (a *app) adoptRefined() {
	d := &a.aiRefine
	if d.pick.PlaylistID == "" || d.lib == nil {
		return
	}
	lib, tr, pick := d.lib, d.track, d.pick
	preference := Preference{
		Source:       lib.id,
		Account:      lib.listener(),
		Text:         d.learned,
		TrackID:      tr.ID,
		TrackName:    tr.Name,
		Artists:      tr.ArtistNames(),
		Album:        tr.Album.Name,
		CoverURL:     tr.Album.ImageURL,
		PlaylistID:   pick.PlaylistID,
		PlaylistName: d.name,
	}
	a.aiBar.open = false
	a.aiSuggestion.open = false
	a.aiRefine.open = false
	a.addToThen(lib, pick.PlaylistID, &tr, func(err error) {
		if err != nil {
			return
		}
		// 歌单确实加入成功才记录采纳，避免失败后把错误结果当作偏好。
		if a.aiPrefs != nil && preference.Text != "" {
			a.aiPrefs.add(preference)
		}
		a.toast = T("Added to the playlist")
	})
}

// aiPreferenceTexts returns the prompt notes and whether they cover this song.
func (a *app) aiPreferenceTexts(lib *library, trackID string) ([]string, bool) {
	if a.aiPrefs == nil {
		return nil, false
	}
	var out []string
	learned := false
	for _, p := range a.aiPrefs.forListener(lib.id, lib.listener()) {
		learned = learned || (trackID != "" && p.TrackID == trackID)
		var note strings.Builder
		if p.TrackName != "" {
			fmt.Fprintf(&note, "%q", p.TrackName)
			if p.Artists != "" {
				fmt.Fprintf(&note, " by %s", p.Artists)
			}
			if p.Album != "" {
				fmt.Fprintf(&note, " (album: %s)", p.Album)
			}
			note.WriteString(". ")
		}
		fmt.Fprintf(&note, "The listener asked for: %s.", p.Text)
		if p.PlaylistName != "" {
			fmt.Fprintf(&note, " They chose the %q playlist.", p.PlaylistName)
		}
		out = append(out, note.String())
	}
	return out, learned
}

func suggestionJSON(s ai.Suggestion) string {
	name := s.PlaylistID
	return `{"playlist_id":"` + name + `","confidence":` + strconv.FormatFloat(s.Confidence, 'f', 2, 64) + `,"reason":"` + s.Reason + `"}`
}

func (a *app) aiRefineDialog(c *ui.Context) {
	p := a.pal
	d := &a.aiRefine
	ui.DialogBase(c.Key("ai-refine"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(460).Padding(24).Radius(18).Gap(14).
			Background(p.bg).Border(1, p.hairline).Shadow(0, 24, 60, -12, ui.RGBA(0, 0, 0, 0.4)).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.94}, Exit: &ui.Motion{Scale: 0.96}})
		panel.Children(func() {
			ui.Column(c).Gap(14).Children(func() {
				ui.Text(c, T("Adjust the Suggestion")).FontSize(17).FontWeight(700)
				// 当前建议收进一张浅底卡片：歌曲、歌单与理由聚在一起，先看清再改。
				ui.Column(c.Key("ai-refine-now")).Fill().Padding(12, 14).Gap(4).Radius(12).
					Background(p.hover.Over(p.bg)).Children(func() {
					ui.Text(c, d.track.Name).FontSize(13).FontWeight(600).SingleLine().Shrink(1).MinWidth(0)
					ui.Text(c, Tf("Now: %s", d.name)).FontSize(12.5).SingleLine().Shrink(1).MinWidth(0).TextColor(p.text)
					if d.pick.Reason != "" {
						ui.Text(c, d.pick.Reason).FontSize(11.5).TextColor(p.muted).LineHeight(1.4)
					}
				})
				if d.failed {
					// 失败不清对话，用户可以直接换种说法重试。
					ui.Text(c.Key("ai-refine-failed"), T("Couldn't adjust it. Try saying it another way.")).
						Role(ui.RoleAlert).FontSize(12).TextColor(ui.RGB(184, 54, 54)).LineHeight(1.4)
				}
				in := ui.TextInput(c.Key("ai-refine-text"), &d.draft).Label(T("How should it change?")).Placeholder(T("Quieter, for the evening")).FontSize(13).AutoFocus()
				if in.Submitted() {
					a.refineSuggestion()
				}
				ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
					if pillButton(c.Key("ai-refine-cancel"), nil, T("Close"), p.hover.Over(p.bg), p.text).Clicked() {
						d.open = false
					}
					action := T("Send")
					if d.busy {
						action = T("Thinking…")
					}
					if pillButton(c.Key("ai-refine-send"), nil, action, p.accent, p.onAccent).Disabled(d.busy || strings.TrimSpace(d.draft) == "").Clicked() {
						a.refineSuggestion()
					}
					// 采用是新建议明确后才出现的主操作，按钮比 Send 更醒目。
					if !d.busy && d.learned != "" {
						if pillButton(c.Key("ai-refine-adopt"), nil, T("Use This"), p.accent, p.onAccent).Clicked() {
							a.adoptRefined()
						}
					}
				})
			})
		})
	})
}
