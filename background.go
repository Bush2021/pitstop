package main

import (
	"context"
	_ "embed"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/gofrs/flock"

	"github.com/Bush2021/pitstop/internal/background"
	"github.com/Bush2021/pitstop/internal/chromeproc"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/pitstop"
	"github.com/Bush2021/pitstop/locales"
)

//go:embed assets/tray.png
var trayIcon []byte

const logLimit = 1 << 20

// runBackground runs the background mode until Chrome exits. Chrome++
// starts it with every chrome.exe it loads in, including the ones that only
// hand a link to the running browser, so all but the first quit at once.
func runBackground(engine *pitstop.Engine) {
	meta := install.Meta(engine.Root)
	if err := os.MkdirAll(meta, 0o755); err != nil {
		return
	}
	l := flock.New(filepath.Join(meta, "background.lock"))
	if ok, err := l.TryLock(); err != nil || !ok {
		return
	}
	defer l.Unlock()
	logPath := filepath.Join(meta, "background.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(logPath); err == nil && st.Size() > logLimit {
		flags |= os.O_TRUNC
	}
	if f, err := os.OpenFile(logPath, flags, 0o644); err == nil {
		defer f.Close()
		log.SetOutput(f)
	}
	log.Printf("started for %s", engine.Root)

	// The flyout is a window: closing it must not quit the app.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		n := &notifier{root: engine.Root}
		self := &selfUpdater{}
		n.r = &background.Runner{
			Engine:     engine,
			UI:         n,
			UpdateSelf: self.install,
			Conditions: func() pitstop.Conditions {
				return pitstop.Conditions{Now: time.Now(), Metered: mygo.Network.Status().Metered}
			},
			Wait:     chromeproc.Wait,
			Linger:   time.Minute,
			Postpone: background.Postpone,
		}
		go func() {
			if err := n.r.Run(context.Background()); err != nil {
				log.Print(err)
			}
			log.Print("done")
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Print(err)
	}
}

// notifier shows what the background mode has to say: a notification, and
// an icon in the notification area while it waits for an answer, whose
// flyout holds the buttons. Its fields belong to the main thread.
type notifier struct {
	root   string
	r      *background.Runner
	tray   *mygo.Tray
	flyout *mygo.Window
	asked  *background.Offer
	status string
	// start offers to start Chrome, which an install found closed.
	start bool
}

// post runs fn on the main thread, and redraws the flyout.
func (n *notifier) post(fn func()) {
	mygo.App.WhenReady(func() {
		fn()
		if n.flyout != nil {
			n.flyout.Update(func() {})
		}
	})
}

func (n *notifier) Ask(o background.Offer) {
	n.post(func() {
		n.asked, n.status, n.start = &o, "", false
		n.notify(locales.AskTitle(o.Name, o.Version), locales.AskBody(), n.openFlyout)
	})
}

func (n *notifier) Installed(what string) {
	n.post(func() {
		n.asked, n.status, n.start = nil, locales.InstalledTitle(what), true
		n.notify(n.status, locales.InstalledBody(), n.openFlyout)
	})
}

func (n *notifier) Failed(what string, err error) {
	n.post(func() {
		n.asked, n.status, n.start = nil, err.Error(), false
		n.notify(locales.BackgroundFailed(what), n.status, n.openFlyout)
	})
}

func (n *notifier) notify(title, body string, click func()) {
	if n.tray == nil {
		t, err := mygo.NewTray(mygo.TrayOptions{ID: "background", Icon: trayIcon, ToolTip: locales.AppTitle()})
		if err != nil {
			log.Print(err)
			return
		}
		t.OnClick(n.openFlyout)
		n.tray = t
	}
	// One ID, so that a new notification replaces the last.
	note := mygo.NewNotification(mygo.NotificationOptions{ID: "pitstop-background", Title: title, Body: body})
	note.OnClick(click)
	if err := note.Show(); err != nil {
		log.Print(err)
	}
}

func (n *notifier) openFlyout() {
	if n.tray == nil {
		return
	}
	if n.flyout != nil {
		n.flyout.Focus()
		return
	}
	n.flyout = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:      n.tray,
		Placement: mygo.PlacementTop,
		Width:     420,
		Height:    160,
		Focusable: true,
		Shadow:    true,
		Content:   ui.View(n.view),
	})
	n.flyout.OnClosed(func() { n.flyout = nil })
}

func (n *notifier) view(c *ui.Context) {
	ui.Column(c).Fill().Padding(16).Gap(10).Background(c.Theme().Background).Children(func() {
		if o := n.asked; o != nil {
			ui.Text(c, locales.AskTitle(o.Name, o.Version)).Bold()
			ui.Text(c, locales.AskDetail())
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, locales.RestartUpdateButton()).Clicked() {
					n.answer(*o, restartNow)
				}
				if ui.Button(c, locales.LaterButton()).Clicked() {
					n.answer(*o, later)
				}
				if ui.Button(c, locales.SkipVersionButton()).Clicked() {
					n.answer(*o, skip)
				}
			})
			return
		}
		// An error naming a path runs longer than the flyout is high.
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Text(c, n.status).Selectable()
		})
		ui.Row(c).Gap(8).Children(func() {
			if n.start && ui.PrimaryButton(c, locales.StartButton()).Clicked() {
				n.startChrome()
			}
			if ui.Button(c, locales.OpenPitstopButton()).Clicked() {
				n.openWindow()
			}
		})
	})
}

func (n *notifier) startChrome() {
	if err := n.r.Engine.StartChrome(); err != nil {
		log.Print(err)
	}
	n.hide()
}

type choice int

const (
	restartNow choice = iota
	later
	skip
)

// answer carries out the user's choice, and then the icon goes: there is
// nothing to say until the install.
func (n *notifier) answer(o background.Offer, c choice) {
	n.asked = nil
	if c != skip {
		n.status = locales.BackgroundBusy()
	}
	go func() {
		var err error
		switch c {
		case restartNow:
			err = n.r.Restart(context.Background(), o)
		case later:
			err = n.r.Approve(context.Background(), o)
		case skip:
			err = n.r.Skip(context.Background(), o)
		}
		if err != nil {
			log.Printf("answer %d for %s %s: %v", c, o.Name, o.Version, err)
			n.Failed(o.Name+" "+o.Version, err)
			return
		}
		log.Printf("answer %d for %s %s", c, o.Name, o.Version)
		n.post(func() {
			n.hide()
			if c == later {
				// Without the icon, Windows shows it beside the app's own.
				note := mygo.NewNotification(mygo.NotificationOptions{ID: "pitstop-background", Title: locales.AppTitle(), Body: locales.ApprovedBody(o.Name, o.Version)})
				if err := note.Show(); err != nil {
					log.Print(err)
				}
			}
		})
	}()
}

func (n *notifier) hide() {
	if n.flyout != nil {
		n.flyout.Close()
	}
	if n.tray != nil {
		n.tray.Destroy()
		n.tray = nil
	}
}

// openWindow starts Pitstop's window, where the user can switch back.
func (n *notifier) openWindow() {
	exe, err := os.Executable()
	if err == nil {
		err = exec.Command(exe, "-root", n.root).Start()
	}
	if err != nil {
		log.Print(err)
	}
	n.hide()
}
