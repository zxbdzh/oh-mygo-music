package app

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"rsc.io/qr"

	"mygo-music/internal/credential"
	"mygo-music/internal/mv"
)

type mvLoginState struct {
	open    bool
	busy    bool
	status  string
	err     string
	cookie  string
	account string
	qrCode  *qr.Code
	epoch   uint64
	cancel  context.CancelFunc
}

func (a *app) openMVLogin() {
	d := &a.mvLogin
	d.open, d.busy, d.err, d.cookie = true, false, "", ""
	if a.mvCredential != "" {
		d.account = a.mvAccount
		if d.account == "" {
			a.refreshMVAccount()
		}
		return
	}
	a.startMVQR()
}

// refreshMVAccount 只用来把已保存的 Cookie 换成显示名；失败不影响播放。
func (a *app) refreshMVAccount() {
	cookie := a.mvCredential
	if cookie == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		client := mv.NewClient()
		client.Cookie = cookie
		account, err := client.Account(ctx)
		if err != nil {
			return
		}
		a.post(func() {
			if a.mvCredential == cookie {
				a.mvAccount = account.Name
				a.mvLogin.account = account.Name
			}
		})
	}()
}

func (a *app) startMVQR() {
	d := &a.mvLogin
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.epoch++
	epoch := d.epoch
	d.qrCode, d.status, d.err, d.busy = nil, T("Getting a QR code…"), "", true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		login, err := mv.NewClient().LoginQRCode(ctx)
		a.post(func() {
			if epoch != d.epoch || !d.open {
				return
			}
			d.busy = false
			if err != nil {
				d.err = T("Couldn't get a QR code.")
				return
			}
			code, err := qr.Encode(login.URL, qr.M)
			if err != nil {
				d.err = T("Couldn't get a QR code.")
				return
			}
			d.qrCode = code
			d.status = T("Scan with Bilibili.")
			a.pollMVQR(epoch, login.Key)
		})
	}()
}

func (a *app) pollMVQR(epoch uint64, key string) {
	d := &a.mvLogin
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			pollCtx, pollCancel := context.WithTimeout(ctx, 20*time.Second)
			result, err := mv.NewClient().PollLoginQRCode(pollCtx, key)
			pollCancel()
			if err != nil {
				a.post(func() {
					if epoch == d.epoch && d.open {
						d.status = T("Couldn't check the QR code.")
					}
				})
				return
			}
			done := result.State == mv.QRExpired || result.State == mv.QRDone
			a.post(func() {
				if epoch != d.epoch || !d.open {
					return
				}
				switch result.State {
				case mv.QRWaiting:
					d.status = T("Waiting for you to scan…")
				case mv.QRScanned:
					d.status = T("Scanned. Confirm in Bilibili.")
				case mv.QRExpired:
					d.status = T("QR code expired.")
				case mv.QRDone:
					a.finishMVLogin(result.Cookie)
				}
			})
			if done {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *app) finishMVLogin(cookie string) {
	d := &a.mvLogin
	cookie = mv.NormalizeCookie(cookie)
	if cookie == "" {
		d.err = T("Enter a Bilibili cookie.")
		return
	}
	d.busy, d.err, d.status = true, "", T("Checking the account…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		client := mv.NewClient()
		client.Cookie = cookie
		account, err := client.Account(ctx)
		a.post(func() {
			if !d.open {
				return
			}
			d.busy = false
			if err != nil {
				d.err = T("That Bilibili cookie is not signed in.")
				return
			}
			a.saveMVLogin(cookie, account)
		})
	}()
}

func (a *app) saveMVLogin(cookie string, account mv.Account) {
	a.mvCredential = cookie
	a.mvAccount = account.Name
	a.mvLogin.open = false
	if err := credential.Set(mv.CookieTarget, cookie); err != nil {
		a.toast = T("Signed in for this session only.")
	} else if account.Name != "" {
		a.toast = Tf("Signed in as %s", account.Name)
	}
	a.closeMV()
	if a.nowPlaying {
		a.openMV()
	}
}

func (a *app) signOutMV() {
	if a.mvLogin.cancel != nil {
		a.mvLogin.cancel()
	}
	a.mvLogin = mvLoginState{}
	a.mvCredential, a.mvAccount = "", ""
	err := credential.Remove(mv.CookieTarget)
	a.closeMV()
	if a.nowPlaying {
		a.openMV()
	}
	if err != nil {
		a.toast = T("Signed out for this session; the saved login could not be removed.")
	} else {
		a.toast = T("Signed out of Bilibili.")
	}
}

func (a *app) mvLoginDialog(c *ui.Context) {
	p := a.pal
	d := &a.mvLogin
	ui.DialogBase(c.Key("mv-login"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(420).Padding(20).Radius(18).Gap(12).
			Background(p.bg).Border(1, p.hairline).Shadow(0, 24, 60, -12, ui.RGBA(0, 0, 0, 0.4)).
			Transition(ui.ElementTransition{Animation: &ui.Snappy, Enter: &ui.Motion{Scale: 0.94}, Exit: &ui.Motion{Scale: 0.96}})
		panel.Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				ui.Text(c, T("Bilibili Account")).FontSize(17).FontWeight(700)
				if a.mvCredential != "" {
					name := a.mvAccount
					if name == "" {
						ui.Text(c, T("Bilibili signed in.")).FontSize(13).TextColor(p.muted)
					} else {
						ui.Text(c, Tf("Signed in as %s", name)).FontSize(13).TextColor(p.muted)
					}
					ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
						if pillButton(c.Key("mv-login-out"), nil, T("Sign Out"), p.hover.Over(p.bg), p.text).Clicked() {
							a.signOutMV()
						}
						if pillButton(c.Key("mv-login-close"), nil, T("Close"), p.hover.Over(p.bg), p.text).Clicked() {
							d.open = false
						}
					})
					return
				}
				if d.err != "" {
					ui.Text(c, d.err).FontSize(12).TextColor(p.muted)
				} else if d.status != "" {
					ui.Text(c, d.status).FontSize(12).TextColor(p.muted)
				}
				if code := d.qrCode; code != nil {
					ui.Row(c).Justify(ui.Center).Children(func() {
						ui.Box(c).Size(192, 192).Radius(12).Background(ui.Hex("#ffffff")).
							Draw(func(pt *ui.Painter, r ui.Rect) { drawQR(pt, r, code, 12) })
					})
				}
				ui.Row(c).Justify(ui.Center).Children(func() {
					if pillButton(c.Key("mv-login-refresh"), nil, T("Refresh QR"), p.hover.Over(p.bg), p.text).Clicked() {
						a.startMVQR()
					}
				})
				ui.Box(c).Height(1).Background(p.hairline)
				input := ui.TextInput(c.Key("mv-login-cookie"), &d.cookie).
					Password().Label(T("Bilibili Cookie")).Placeholder(T("SESSDATA=...")).FontSize(12)
				if input.Submitted() {
					a.finishMVLogin(d.cookie)
				}
				ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
					if pillButton(c.Key("mv-login-cookie-ok"), nil, T("Use Cookie"), p.accent, p.onAccent).Clicked() {
						a.finishMVLogin(d.cookie)
					}
					if pillButton(c.Key("mv-login-cancel"), nil, T("Close"), p.hover.Over(p.bg), p.text).Clicked() {
						if d.cancel != nil {
							d.cancel()
						}
						d.open = false
					}
				})
			})
		})
	})
}
