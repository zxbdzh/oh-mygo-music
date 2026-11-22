package app

import (
	"context"
	"image"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/lyrics"
	"mygo-music/internal/music"
	"mygo-music/internal/player"
	"mygo-music/internal/sharecard"
)

type shareDraft struct {
	open                    bool
	track                   music.Track
	position                time.Duration
	current                 bool
	loading                 bool
	loadError               error
	lines                   []lyrics.Line
	selected                map[int]bool
	selectionAnchor         int
	hasSelectionAnchor      bool
	loadSerial              int
	cover                   image.Image
	coverLoading            bool
	coverError              error
	timingReliable          bool
	aggregateView           *aggregateView
	recordingSource         *music.AggregateSource
	pendingAggregateTrackID string
	display                 lyricDisplayCache
	renderSerial            int
	rendering               bool
	renderError             error
	preview                 *ui.Bitmap
	png                     []byte
	pngURL                  string
	exporting               bool
	status                  string
	onlineMode              string
	onlineDefaultView       string
	cardOnline              bool
	onlineQuality           string
	onlineURL               string
	onlineID                string
	onlineRequestKey        string
	onlineRequestSnapshot   string
	onlinePublishedSnapshot string
	onlineRevision          uint64
	onlinePublishedRevision uint64
	onlinePublishing        bool
	onlineError             error
	backgroundTask          *backgroundTask
	metadata                *shareMetadata
	shareClient             *shareClient
	cardSnapshot            *sharecard.Card
	presentation            *sharePresentation
	shareCover              []byte
	clipStart, clipEnd      time.Duration
	clipHead                time.Duration
	clipPreview             *player.ClipPreview
	clipPreviewCancel       context.CancelFunc
	clipPreviewLoading      bool
	clipPreviewActive       bool
	clipPreviewSerial       int
}

type sharePresentation struct {
	template        string
	square          bool
	hideTranslation bool
	lyrics          lyrics.DisplayOptions
}

func (a *app) sharePresentation(draft *shareDraft) *sharePresentation {
	if draft.presentation == nil {
		draft.presentation = &sharePresentation{
			template: a.settings.CardTemplate, square: a.settings.CardSquare,
			hideTranslation: a.settings.HideCardTranslation,
			lyrics:          a.lyricOptions(&draft.track, a.settings.HideCardTranslation),
		}
	}
	return draft.presentation
}

func (draft *shareDraft) clipRange() (time.Duration, time.Duration, bool) {
	if draft.clipEnd > draft.clipStart {
		return draft.clipStart, draft.clipEnd, true
	}
	return shareClipRange(draft.lines, draft.selected, draft.track.Length(), draft.timingReliable)
}

func (a *app) openShare(track music.Track) {
	if track.Source == "" && a.library != nil {
		track.Source = a.library.id
	}
	track.Artists = slices.Clone(track.Artists)
	if a.sharing != nil {
		a.stopSharePreview(a.sharing)
		a.sharing.open = false
	}
	draft := &shareDraft{
		open: true, track: track, selected: make(map[int]bool),
		onlineMode: "full", onlineQuality: a.settings.Quality,
		cardOnline: strings.TrimSpace(a.settings.ShareServerURL) != "",
	}
	if len(uniqueAggregateSources(track.AggregateSources)) > 1 {
		draft.aggregateView = &aggregateView{}
	}
	if current := a.pl.Current(); current != nil && a.lyricKey(current) == a.lyricKey(&track) {
		draft.current, draft.position = true, a.pl.Position()
		// 当前队列项已经核实的录音身份必须跟随分享，不能退回目录默认版本。
		if current.PlaybackID != "" {
			draft.track.PlaybackID = current.PlaybackID
		}
	}
	a.sharing = draft
	a.loadShareLyrics(draft)
	a.loadShareCover(draft)
	a.requestShareRender()
}

func (a *app) loadShareLyrics(draft *shareDraft) {
	lib := a.libOf(draft.track.Source)
	if lib == nil {
		draft.loadError = music.ErrUnavailable
		return
	}
	if !lib.can("lyrics") {
		return
	}
	draft.loading, draft.loadError = true, nil
	draft.loadSerial++
	serial, id := draft.loadSerial, draft.track.ID
	go func() {
		answer, err := lib.src.Lyrics(id)
		lines := lyrics.FromSource(answer)
		a.post(func() {
			// 同一来源同 ID 的重试也必须匹配代次，关闭的草稿不接收旧结果。
			if a.sharing != draft || !draft.open || draft.loadSerial != serial {
				return
			}
			draft.loading, draft.loadError = false, err
			if err == nil {
				draft.lines = lines
				draft.timingReliable = reliableShareTiming(answer, lines)
				if len(draft.selected) == 0 {
					if index := initialShareLine(lines, draft.position, draft.current); index >= 0 {
						draft.selected[index] = true
					}
				}
			}
			a.requestShareRender()
		})
	}()
}

func initialShareLine(lines []lyrics.Line, position time.Duration, current bool) int {
	var valid []int
	for index, line := range lines {
		if strings.TrimSpace(line.Text) != "" && !lyrics.IsCredit(line.Text) {
			valid = append(valid, index)
		}
	}
	if len(valid) == 0 {
		return -1
	}
	if !current || lines[valid[len(valid)-1]].At <= lines[valid[0]].At {
		return valid[0]
	}
	for _, index := range valid {
		if position < lines[index].At || position < lines[index].End {
			return index
		}
	}
	return valid[len(valid)-1]
}

func (a *app) shareLines(draft *shareDraft) []lyrics.Line {
	if len(draft.lines) == 0 {
		return nil
	}
	options := a.lyricOptions(&draft.track, a.settings.HideCardTranslation)
	if draft.presentation != nil {
		options = draft.presentation.lyrics
		options.HideTranslation = draft.presentation.hideTranslation
	}
	cache := &draft.display
	if cache.source != &draft.lines[0] || cache.options != options || cache.count != len(draft.lines) {
		*cache = lyricDisplayCache{source: &draft.lines[0], count: len(draft.lines), options: options, shown: lyrics.Display(draft.lines, options)}
	}
	return cache.shown
}

func (a *app) shareFragment(draft *shareDraft) []sharecard.Line {
	return sharecard.Select(a.shareLines(draft), draft.selected, !a.sharePresentation(draft).hideTranslation)
}

func (a *app) copyShareLyrics(draft *shareDraft) {
	text := sharecard.Text(a.shareFragment(draft))
	if text == "" {
		return
	}
	if err := copyText(text); err != nil {
		a.toast = T("Could not copy lyrics")
		return
	}
	a.toast = T("Lyrics copied")
}

func (a *app) loadShareCover(draft *shareDraft) {
	if draft.track.Album.ImageURL == "" {
		return
	}
	key := sized(draft.track.Album.ImageURL, 1200)
	load := a.art.hold(key)
	draft.coverLoading, draft.coverError = true, nil
	go func() {
		a.art.load(key, load)
		a.post(func() {
			a.art.letGo(key, load)
			if a.sharing != draft || !draft.open {
				return
			}
			draft.coverLoading, draft.coverError, draft.cover = false, load.err, load.img
			a.requestShareRender()
		})
	}()
}

func (a *app) requestShareRender() {
	draft := a.sharing
	a.requestShareRenderFor(draft)
}

func (a *app) requestShareRenderFor(draft *shareDraft) {
	if draft == nil || (!draft.open && draft.backgroundTask == nil) {
		return
	}
	draft.renderSerial++
	draft.png, draft.preview, draft.renderError, draft.pngURL = nil, nil, nil, ""
	if draft.loading || draft.coverLoading || draft.rendering {
		return
	}
	serial := draft.renderSerial
	revision := draft.onlineRevision
	draft.rendering = true
	cardURL := a.shareCardURL(draft)
	presentation := a.sharePresentation(draft)
	card := sharecard.Card{Title: draft.track.Name, Artist: draft.track.ArtistNames(), URL: cardURL,
		Template: presentation.template, Square: presentation.square, Cover: draft.cover, Lines: a.shareFragment(draft)}
	if draft.cardSnapshot != nil {
		card = *draft.cardSnapshot
		card.URL = cardURL
	}
	go func() {
		data, err := sharecard.Render(card)
		a.post(func() {
			if a.sharing != draft && draft.backgroundTask == nil {
				return
			}
			draft.rendering = false
			// 连续操作最多保留一个渲染任务，过期结果只触发最新快照，不回写旧图片。
			if serial != draft.renderSerial || revision != draft.onlineRevision {
				a.requestShareRenderFor(draft)
				return
			}
			draft.renderError = err
			if err == nil {
				draft.preview, draft.renderError = ui.DecodeBitmap(data)
				if draft.renderError == nil {
					draft.png = data
					draft.pngURL = cardURL
				}
			}
			if draft.renderError != nil && draft.backgroundTask != nil {
				draft.backgroundTask.status = T("Online link ready; image unavailable. Open to edit.")
			}
		})
	}()
}

func (a *app) openBackgroundShare(draft *shareDraft) {
	if a.sharing != nil && a.sharing != draft {
		a.stopSharePreview(a.sharing)
		a.sharing.open = false
	}
	// 已提交任务保留独立快照，重新编辑不能反过来改变它的结果或重试参数。
	if !draft.onlinePublishing {
		editor := *draft
		editor.selected = maps.Clone(draft.selected)
		if draft.presentation != nil {
			presentation := *draft.presentation
			editor.presentation = &presentation
		}
		editor.backgroundTask, editor.metadata, editor.shareClient, editor.cardSnapshot = nil, nil, nil, nil
		editor.shareCover = nil
		editor.rendering, editor.exporting = false, false
		draft = &editor
	}
	draft.open = true
	a.sharing = draft
	a.requestShareRenderFor(draft)
}

func (a *app) shareCardURL(draft *shareDraft) string {
	if !draft.cardOnline {
		return draft.track.URL
	}
	if a.onlineLinkMatchesDraft(draft) {
		return draft.onlineURL
	}
	// 在线卡片尚未发布时不回退到原曲，避免预览和最终导出含义不一致。
	return ""
}
