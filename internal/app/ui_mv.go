package app

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mygo-music/internal/music"
	"mygo-music/internal/mv"
)

// mvDialogState 是 MV 绑定面板的状态。搜索和写入绑定都在后台，
// 结果通过 post 回到主线程，避免网络请求卡住视图。
type mvDialogState struct {
	open       bool
	track      *music.Track
	query      string
	busy       bool
	searchErr  string
	bindErr    string
	hint       string
	candidates []mv.Candidate
	epoch      uint64
}

// mvSearchCandidates 是搜索的测试替换点；生产环境直接查 B 站。
var mvSearchCandidates = func(ctx context.Context, query, cookie string) ([]mv.Candidate, error) {
	client := mv.NewClient()
	client.Cookie = cookie
	return client.Search(ctx, query)
}

// mvButton 在 MV 页提供一个入口，已有绑定时把操作收在同一菜单里。
func (a *app) mvButton(c *ui.Context, tr *music.Track) {
	if tr == nil || tr.ShowID != "" {
		return
	}
	a.ensureMVStore()
	var binding mv.Binding
	var bound bool
	if a.mvStore != nil {
		binding, bound = a.mvStore.Binding(tr.Source, tr.ID)
	}
	label := T("Choose MV")
	accessLabel := label
	if bound {
		label = T("Manage MV")
		accessLabel = label
		if a.mvShown(c) && a.mv.hasPicture(tr) {
			info := a.mv.streamInfo()
			if resolution := info.Resolution(); resolution > 0 {
				label = Tf("%dp", resolution)
				if info.FPS > 0 {
					label = Tf("%dp · %d fps", resolution, info.FPS)
				}
				accessLabel = Tf("Manage MV: %s", label)
			}
		}
	}
	button := pillButton(c.Key("mv-open"), iconVideo, label, ui.RGBA(8, 11, 16, 0.45), white.Alpha(0.9)).Height(30).Label(accessLabel).Tooltip(accessLabel)
	if !bound {
		if button.Clicked() {
			a.openMVDialog(tr)
		}
		return
	}
	services := c.Services()
	button.Menu(func(menu *ui.Menu) {
		if menu.Item(T("Change")).Chosen() {
			a.openMVDialog(tr)
		}
		if menu.Item(T("Open source")).Chosen() {
			services.OpenURL("https://www.bilibili.com/video/" + url.PathEscape(binding.VideoID))
		}
		menu.Separator()
		menu.Item(Tf("Video offset: %+d ms", binding.OffsetMs)).Disabled(true)
		for _, offset := range []struct {
			label string
			delta int
		}{{"Delay video by 100 ms", -100}, {"Advance video by 100 ms", 100}} {
			if menu.Item(T(offset.label)).Chosen() {
				if err := a.mvStore.ChangeOffset(tr.Source, tr.ID, offset.delta); err != nil {
					a.toast = T("Couldn't save the MV.")
				} else {
					a.mv.forceSync = true
				}
			}
		}
		menu.Separator()
		if menu.Item(T("Remove")).Chosen() {
			if err := a.mvStore.DeleteBinding(tr.Source, tr.ID); err != nil {
				a.toast = T("Couldn't save the MV.")
			} else {
				a.closeMV()
			}
		}
		menu.Separator()
		if menu.Item(T("Copy MV diagnostics")).Chosen() {
			if err := copyText(a.mvDiagnostics()); err != nil {
				a.toast = T("Could not copy MV diagnostics")
			} else {
				a.toast = T("Copied MV diagnostics")
			}
		}
	})
}

func (a *app) openMVDialog(tr *music.Track) {
	if tr == nil {
		return
	}
	query := strings.TrimSpace(tr.Name + " " + tr.ArtistNames())
	track := *tr
	a.mvDialog = mvDialogState{open: true, track: &track, query: query}
	a.ensureMVStore()
	if a.mvStore != nil && a.mvStore.HasOtherSourceBinding(track.Source, track.Name, track.ArtistNames()) {
		a.mvDialog.hint = T("Another source has an MV for this song.")
	}
	a.searchMV()
}

func (a *app) searchMV() {
	d := &a.mvDialog
	query := strings.TrimSpace(d.query)
	if query == "" || d.track == nil {
		return
	}
	d.epoch++
	epoch := d.epoch
	d.busy, d.searchErr, d.bindErr = true, "", ""
	d.candidates = nil
	tr := *d.track
	cookie := a.mvCookie()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		found, err := mvSearchCandidates(ctx, query, cookie)
		if err != nil {
			a.post(func() { a.mvSearchDone(epoch, nil, err) })
			return
		}
		ranked := mv.Rank(found, tr.Name, tr.ArtistNames(), time.Duration(tr.Duration)*time.Millisecond, 8)
		a.post(func() { a.mvSearchDone(epoch, ranked, nil) })
	}()
}

func (a *app) mvSearchDone(epoch uint64, candidates []mv.Candidate, err error) {
	d := &a.mvDialog
	if epoch != d.epoch {
		return
	}
	d.busy = false
	if err != nil {
		d.searchErr = T("Couldn't search Bilibili.")
		return
	}
	d.candidates = candidates
}

func (a *app) bindMV(c mv.Candidate) {
	d := &a.mvDialog
	if d.track == nil {
		return
	}
	a.ensureMVStore()
	if a.mvStore == nil {
		d.bindErr = T("Couldn't save the MV.")
		return
	}
	err := a.mvStore.PutBinding(mv.Binding{
		Source:      d.track.Source,
		TrackID:     d.track.ID,
		TrackName:   d.track.Name,
		TrackArtist: d.track.ArtistNames(),
		VideoID:     c.VideoID,
		Title:       c.Title,
		Uploader:    c.Uploader,
	})
	if err != nil {
		d.bindErr = T("Couldn't save the MV.")
		return
	}
	d.open = false
	a.closeMV()
}

func (a *app) mvDialogView(c *ui.Context) {
	p := a.pal
	d := &a.mvDialog
	ui.DialogBase(c.Key("mv-binding"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(560).Height(460).Padding(20).Radius(18).Gap(12).
			Background(p.bg).Border(1, p.hairline).Shadow(0, 24, 60, -12, ui.RGBA(0, 0, 0, 0.4)).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.94}, Exit: &ui.Motion{Scale: 0.96}})
		panel.Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, T("MV Background")).FontSize(17).FontWeight(700)
					ui.Spacer(c)
					if a.mvCredential != "" {
						label := T("Bilibili signed in.")
						if a.mvAccount != "" {
							label = Tf("Signed in as %s", a.mvAccount)
						}
						ui.Text(c, label).FontSize(11).TextColor(p.muted).SingleLine()
						if pillButton(c.Key("mv-login-out"), nil, T("Sign Out"), p.hover.Over(p.bg), p.text).Clicked() {
							a.signOutMV()
						}
					} else if pillButton(c.Key("mv-login-open"), nil, T("Sign In"), p.hover.Over(p.bg), p.text).Clicked() {
						a.mvDialog.open = false
						a.openMVLogin()
					}
				})
				if d.track != nil {
					ui.Text(c, Tf("Choose an MV for %s", d.track.Name)).FontSize(13).TextColor(p.muted).SingleLine()
				}
				if d.hint != "" {
					ui.Text(c, d.hint).FontSize(12).TextColor(p.muted)
				}
				input := ui.TextInput(c.Key("mv-query"), &d.query).Placeholder(T("Song and artist")).FontSize(13)
				if input.Submitted() {
					a.searchMV()
				}
				if pillButton(c.Key("mv-search"), iconSearch, T("Search"), p.accent, p.onAccent).Clicked() {
					a.searchMV()
				}
				if d.searchErr != "" {
					ui.Text(c, d.searchErr).FontSize(12).TextColor(p.muted)
				}
				if d.bindErr != "" {
					ui.Text(c, d.bindErr).FontSize(12).TextColor(p.muted)
				}
				ui.Scroll(c.Key("mv-candidates")).Grow(1).Children(func() {
					if d.busy {
						ui.Text(c, T("Searching…")).FontSize(13).TextColor(p.muted)
						return
					}
					for i, candidate := range d.candidates {
						row := candidate
						item := ui.ButtonBase(c.Key(i)).Fill().Column().AlignItems(ui.Start).
							Padding(10, 12).Radius(10).Gap(3).FocusRing(false).Label(row.Title)
						if item.Hovered() {
							item.Background(p.hover.Over(p.bg))
						}
						item.Children(func() {
							ui.Text(c, row.Title).FontSize(13).FontWeight(600).SingleLine()
							if row.Uploader != "" {
								ui.Text(c, row.Uploader).FontSize(11).TextColor(p.muted).SingleLine()
							}
							if len(row.Reasons) > 0 {
								ui.Text(c, strings.Join(mvReasons(row.Reasons), " · ")).FontSize(11).TextColor(p.muted).SingleLine()
							}
						})
						if item.Clicked() {
							a.bindMV(row)
						}
					}
					if !d.busy && len(d.candidates) == 0 && d.searchErr == "" {
						ui.Text(c, T("No MV found.")).FontSize(13).TextColor(p.muted)
					}
				})
				ui.Row(c).Justify(ui.End).Children(func() {
					if pillButton(c.Key("mv-close"), nil, T("Close"), p.hover.Over(p.bg), p.text).Clicked() {
						d.open = false
					}
				})
			})
		})
	})
}

func mvReasons(reasons []string) []string {
	out := make([]string, len(reasons))
	for i, reason := range reasons {
		out[i] = T(reason)
	}
	return out
}
