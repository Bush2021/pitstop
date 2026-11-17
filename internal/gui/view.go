package gui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Bush2021/mygo-kit"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/pitstop/assets"
	"github.com/Bush2021/pitstop/internal/ghrelease"
	"github.com/Bush2021/pitstop/internal/ini"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/options"
	"github.com/Bush2021/pitstop/internal/pitstop"
	"github.com/Bush2021/pitstop/locales"
)

type navItem struct {
	id    string
	icon  *ui.SVG
	label func() string
}

var (
	appIcon      = ui.MustParseSVG(assets.Icon)
	overviewItem = navItem{pageOverview, kit.Icon(`<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>`), locales.PageOverview}
	// plusItems edit chrome++.ini, which takes a save and a Chrome restart.
	plusItems = []navItem{
		{pageTabs, kit.Icon(`<path d="M3 20V7a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v3h6a2 2 0 0 1 2 2v8"/><path d="M2 20h20"/>`), locales.PageTabs},
		{pageKeyboard, kit.Icon(`<rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M7 14h10"/>`), locales.PageKeyboard},
		{pageStartup, kit.Icon(`<path d="M12 3v9"/><path d="M6.3 6.3a8 8 0 1 0 11.4 0"/>`), locales.PageStartup},
		{pageAdvanced, kit.Icon(`<path d="M4 7h10M18 7h2M4 17h2M10 17h10"/><circle cx="16" cy="7" r="2"/><circle cx="8" cy="17" r="2"/>`), locales.PageAdvanced},
	}
	// ownItems are Pitstop's own, whose settings apply at once.
	ownItems = []navItem{
		{pageSettings, kit.Icon(`<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>`), locales.PageSettings},
		{pageAbout, kit.Icon(`<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>`), locales.PageAbout},
	}
)

func (a *App) View(c *ui.Context) {
	kit.Apply(c)
	if a.toast != "" {
		c.Toast(a.toast)
		a.toast = ""
	}
	if !a.Recording() && c.Shortcut(ui.Cmd, ui.KeyS) {
		a.saveKey()
	}
	kit.Shell(c, func() { a.sidebar(c) }, func() {
		switch {
		case a.raw != nil:
			a.rawView(c)
		case a.review != nil:
			a.reviewView(c)
		default:
			ui.Column(c.Key(a.page)).Grow(1).MinHeight(0).Transition(kit.PageEnter).Children(func() {
				kit.NarrowPage(c.Key(a.page+"/"+a.query), func() { a.content(c) })
			})
			a.pendingBar(c)
		}
	})
	a.dialogs(c)
	a.referenceDialog(c)
}

func (a *App) sidebar(c *ui.Context) {
	kit.SidebarHeader(c, locales.AppTitle(), a.version)
	search := ui.SearchField(c.Key("search"), &a.query).Label(locales.SearchPlaceholder()).Margin(0, 10)
	// The text editor and the review find in what they show.
	if a.raw == nil && a.review == nil && !a.Recording() && c.Shortcut(ui.Cmd, ui.KeyF) {
		search.Focus()
	}
	selected := a.page
	item := func(n navItem) { kit.NavItem(c, n.id, n.icon, n.label(), n.id == selected, nil) }
	if ui.Sidebar(c, &selected, func() {
		item(overviewItem)
		ui.SidebarSection(c, "Chrome++ Next", nil, func() {
			for _, n := range plusItems {
				item(n)
			}
		})
		ui.SidebarSection(c, locales.AppTitle(), nil, func() {
			for _, n := range ownItems {
				item(n)
			}
		})
	}).Grow(1).Background(ui.Transparent).Label(locales.AppTitle()).Changed() {
		if a.raw != nil && !a.applyRaw() {
			return
		}
		a.page, a.query, a.raw = selected, "", nil
	}
}

// sections shows items in panels, one for each run of items of a group,
// under the group's name if it has one.
func sections[T any](c *ui.Context, items []T, group func(T) string, row func(T)) {
	for len(items) > 0 {
		name := group(items[0])
		n := 1
		for n < len(items) && group(items[n]) == name {
			n++
		}
		run := items[:n]
		items = items[n:]
		if name != "" {
			kit.Heading(c.Key("heading/"+name), name)
		}
		kit.Panel(c.Key("panel/"+name), func() {
			for _, item := range run {
				row(item)
			}
		})
	}
}

// groupName is what a heading says over o, "" for none.
func groupName(o options.Option, msgs locales.Messages) string {
	if o.Group == nil {
		return ""
	}
	return o.Group(msgs)
}

// settingRow is a setting as Windows' Settings show one: what it does on
// the left, its control on the right, anything long below them.
func settingRow(c *ui.Context, key any, body func()) ui.Element {
	return ui.Column(c.Key(key)).Gap(8).Padding(12, 16).Transition(kit.RowMotion).Children(body)
}

func rowHead(c *ui.Context, label, help string, control func()) {
	ui.Row(c).Wrap().Gap(16).AlignItems(ui.Center).Children(func() {
		text := ui.Column(c).Grow(1).Gap(2).MinWidth(0)
		if help != "" {
			// A control too wide to leave the help this much goes below it.
			text.Basis(260)
		}
		text.Children(func() {
			ui.Text(c, label)
			if help != "" {
				kit.Muted(c, help)
			}
		})
		control()
	})
}

func (a *App) content(c *ui.Context) {
	if strings.TrimSpace(a.query) != "" {
		a.searchResults(c)
		return
	}
	switch a.page {
	case pageOverview:
		a.overview(c)
	case pageTabs:
		a.settings(c, locales.PageTabs(), options.Tabs)
	case pageKeyboard:
		a.settings(c, locales.PageKeyboard(), options.Keyboard)
	case pageStartup:
		a.settings(c, locales.PageStartup(), options.Startup)
	case pageAdvanced:
		a.settings(c, locales.PageAdvanced(), options.Advanced)
	case pageSettings:
		a.ownSettingsPage(c)
	case pageAbout:
		a.about(c)
	}
}

func channelLabel(ch omaha.Channel) string {
	switch ch {
	case omaha.Beta:
		return locales.ChannelBeta()
	case omaha.Dev:
		return locales.ChannelDev()
	case omaha.Canary:
		return locales.ChannelCanary()
	}
	return locales.ChannelStable()
}

func policyLabel(p install.Policy) string {
	switch p {
	case install.Automatic:
		return locales.PolicyAutomatic()
	case install.Ask:
		return locales.PolicyAsk()
	}
	return locales.PolicyOff()
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }

func installedToast(name, version string) string { return locales.DoneInstalled(name, version) }

// updatedToast says where an update of name to version ended up.
func updatedToast(name, version string, outcome pitstop.Outcome) string {
	switch outcome {
	case pitstop.Staged:
		return locales.DoneStaged(version)
	case pitstop.Pending:
		return locales.DonePending(name, version)
	}
	return installedToast(name, version)
}

func savedToast(running bool) string {
	if running {
		return locales.SavedToast()
	}
	return locales.SavedToastClosed()
}

func (a *App) overview(c *ui.Context) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		kit.Title(c, locales.PageOverview()).Grow(1)
		if a.in != nil && !a.busy && a.chromeUpdate == nil && a.plusUpdate == nil {
			var btn ui.Element
			if a.checked {
				btn = ui.Button(c, locales.CheckButton())
			} else {
				btn = ui.PrimaryButton(c.Key("check"), locales.CheckButton())
			}
			if btn.Clicked() {
				a.check()
			}
		}
	})
	a.operation(c)
	if a.in == nil {
		if a.openErr != nil && !errors.Is(a.openErr, install.ErrNotInstalled) {
			kit.Card(c, func() {
				ui.Text(c, locales.ErrorTitle()).Bold()
				ui.Text(c, a.openErr.Error()).Selectable()
			})
		}
		a.setupCard(c)
		return
	}
	in := a.in
	chromeReady, plusReady := a.engine.Downloaded(in)
	kit.Card(c, func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, locales.ChromeTitle(in.Chrome)).FontSize(16).FontWeight(600)
				kit.Muted(c, locales.ChromeDetail(channelLabel(in.State.Channel), in.Arch))
			})
			a.startButton(c)
		})
		if in.Unsettled {
			ui.Text(c, locales.StatusUnsettled()).TextColor(c.Theme().Warning).Margin(4, 0, 0)
		}
		if in.Staged != "" && (a.chromeUpdate == nil || a.chromeUpdate.Version == in.Staged) {
			ui.Row(c.Key("staged")).Gap(12).AlignItems(ui.Center).Margin(4, 0, 0).Children(func() {
				ui.Text(c, locales.StatusStaged(in.Staged)).TextColor(c.Theme().Success).Grow(1)
				if a.running {
					a.restartUpdateButton(c)
				}
			})
			return
		}
		if v := in.State.Approved; v != "" && install.CompareVersions(v, in.Chrome) > 0 && (a.chromeUpdate == nil || a.chromeUpdate.Version == v) {
			a.waiting(c, v, chromeReady, a.installApproved)
			return
		}
		a.status(c, a.chromeUpdate != nil, func() {
			u := a.chromeUpdate
			ui.Text(c, locales.StatusAvailable(u.Version, mb(u.DownloadSize()))).Grow(1)
			if ui.PrimaryButton(c.Key("update-chrome"), locales.UpdateButton(u.Version)).Disabled(a.busy).Clicked() {
				a.updateChrome()
			}
		})
	})
	kit.Card(c, func() {
		ui.Column(c).Gap(2).Children(func() {
			if in.ChromePlus == "" {
				ui.Text(c, locales.PlusNotInstalled()).FontSize(16).FontWeight(600)
				return
			}
			ui.Text(c, locales.PlusTitle(in.ChromePlus)).FontSize(16).FontWeight(600)
			if in.Injection == install.SetDLL {
				kit.Muted(c, locales.PlusSetDLL(in.ChromePlusDLL))
			} else {
				kit.Muted(c, locales.PlusVersionDLL())
			}
		})
		if v := in.State.ApprovedPlus; v != "" && (a.plusUpdate == nil || a.plusUpdate.Release.Tag == v) {
			a.waiting(c, v, plusReady, a.installApprovedPlus)
			return
		}
		a.status(c, a.plusUpdate != nil, func() {
			u := a.plusUpdate
			ui.Text(c, locales.StatusAvailable(u.Release.Tag, mb(u.Release.Archive.Size))).Grow(1)
			label := locales.UpdateButton(u.Release.Tag)
			if in.ChromePlus == "" {
				label = locales.InstallPlusButton(u.Release.Tag)
			}
			if ui.PrimaryButton(c.Key("update-plus"), label).Disabled(a.busy).Clicked() {
				a.updateChromePlus()
			}
		})
	})
	kit.Panel(c.Key("folder"), func() {
		settingRow(c, "folder", func() {
			rowHead(c, locales.InstallFolder(), a.engine.Root, func() {
				if ui.Button(c.Key("open"), locales.OpenButton()).Clicked() {
					a.OpenFolder(a.engine.Root)
				}
			})
		})
	})
	a.keptCard(c)
	a.activity(c)
}

func (a *App) startButton(c *ui.Context) {
	if a.running {
		if ui.Button(c.Key("restart"), locales.RestartButton()).Disabled(a.busy || a.elsewhere).Clicked() {
			a.restartOpen = true
		}
		return
	}
	if ui.Button(c.Key("start"), locales.StartButton()).Disabled(a.busy).Clicked() {
		a.startChrome()
	}
}

func (a *App) status(c *ui.Context, found bool, update func()) {
	if !a.checked {
		return
	}
	ui.Row(c).Gap(12).AlignItems(ui.Center).Margin(4, 0, 0).Children(func() {
		if found {
			update()
			return
		}
		ui.Text(c, locales.StatusUpToDate()).TextColor(c.Theme().Success)
	})
}

func (a *App) operation(c *ui.Context) {
	if f := a.failure(); f != nil {
		kit.Card(c, func() {
			ui.Row(c).Gap(12).Children(func() {
				ui.Text(c, locales.BackgroundFailed(f.What)).Bold().TextColor(c.Theme().Danger).Grow(1)
				kit.Muted(c, i18n.FormatDate(c.Locale(), f.Time.Local(), i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle}))
			})
			if body := troubleBody(pitstop.Trouble(f.Trouble), f.Host); body != "" {
				ui.Text(c, body)
				kit.Muted(c, f.Error).Selectable()
			} else {
				ui.Text(c, f.Error).Selectable()
			}
			ui.Row(c).Children(func() {
				if ui.Button(c.Key("dismiss-failure"), locales.DismissButton()).Clicked() {
					a.dismissFailure()
				}
			})
		})
	}
	switch {
	case a.busy:
		kit.Card(c, func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, phaseLabel(a.step.Phase)).Bold().Grow(1)
				if ui.Button(c, locales.CancelButton()).Disabled(a.step.Phase == pitstop.Applying).Clicked() && a.cancel != nil {
					a.cancel()
				}
			})
			v := -1.0
			if a.step.Total > 0 {
				v = float64(a.step.Done) / float64(a.step.Total)
			}
			ui.Progress(c, v).Label(phaseLabel(a.step.Phase))
			if a.step.Total > 0 {
				line := locales.ProgressBytes(mb(a.step.Done), mb(a.step.Total))
				if a.speed > 0 {
					line = locales.ProgressSpeed(mb(a.step.Done), mb(a.step.Total), mb(int64(a.speed)))
				}
				kit.Muted(c, line)
			}
		})
	case a.elsewhere:
		kit.Card(c, func() {
			ui.Text(c, locales.BackgroundBusy()).Bold()
			ui.Progress(c, -1).Label(locales.BackgroundBusy())
		})
	case errors.Is(a.opErr, pitstop.ErrChromeRunning):
		kit.Card(c, func() {
			ui.Text(c, locales.RunningTitle()).Bold()
			if r, ok := errors.AsType[*pitstop.RunningError](a.opErr); ok && r.Elevated {
				ui.Text(c, locales.RunningElevatedBody())
			} else {
				ui.Text(c, locales.RunningBody())
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, locales.TryAgainButton()).Clicked() && a.retry != nil {
					a.retry()
				}
				if ui.Button(c, locales.DismissButton()).Clicked() {
					a.opErr, a.retry = nil, nil
				}
			})
		})
	case explained(a.opErr):
		kit.Card(c, func() {
			trouble, host := pitstop.Explain(a.opErr)
			ui.Text(c, troubleTitle(trouble)).Bold().TextColor(c.Theme().Danger)
			ui.Text(c, troubleBody(trouble, host))
			kit.Muted(c, a.opErr.Error()).Selectable()
			ui.Row(c).Gap(8).Children(func() {
				if a.retry != nil && ui.PrimaryButton(c, locales.TryAgainButton()).Clicked() {
					a.retry()
				}
				if ui.Button(c, locales.DismissButton()).Clicked() {
					a.opErr, a.retry = nil, nil
				}
			})
		})
	case a.opErr != nil:
		kit.Card(c, func() {
			ui.Text(c, locales.ErrorTitle()).Bold().TextColor(c.Theme().Danger)
			msg := a.opErr.Error()
			switch {
			case errors.Is(a.opErr, pitstop.ErrBusy):
				msg = locales.BusyBody()
			case errors.Is(a.opErr, pitstop.ErrPatched):
				msg = locales.PatchedBody()
			}
			ui.Text(c, msg).Selectable()
			ui.Row(c).Children(func() {
				if ui.Button(c, locales.DismissButton()).Clicked() {
					a.opErr, a.retry = nil, nil
				}
			})
		})
	}
}

// explained reports whether err is one the user is told about in plain
// words, which retrying may mend.
func explained(err error) bool {
	t, _ := pitstop.Explain(err)
	return t != ""
}

func troubleTitle(t pitstop.Trouble) string {
	if t == pitstop.ServerTrouble {
		return locales.ServerTitle()
	}
	return locales.NetworkTitle()
}

// troubleBody is what to tell the user instead of the error, or "" when
// the error says it best.
func troubleBody(t pitstop.Trouble, host string) string {
	switch {
	case t == pitstop.NetworkTrouble && host != "":
		return locales.NetworkBody(host)
	case t == pitstop.NetworkTrouble:
		return locales.NetworkBodyNoHost()
	case t == pitstop.ServerTrouble && host != "":
		return locales.ServerBody(host)
	case t == pitstop.ServerTrouble:
		return locales.ServerBodyNoHost()
	}
	return ""
}

func (a *App) failure() *install.Failure {
	if a.in == nil || a.busy {
		return nil
	}
	return a.in.State.Failure
}

func phaseLabel(p pitstop.Phase) string {
	switch p {
	case pitstop.Downloading:
		return locales.PhaseDownload()
	case pitstop.Decoding:
		return locales.PhaseDecode()
	case pitstop.Unpacking:
		return locales.PhaseUnpack()
	case pitstop.Verifying:
		return locales.PhaseVerify()
	case pitstop.Diffing:
		return locales.PhaseDiff()
	case pitstop.Applying:
		return locales.PhaseApply()
	case pitstop.Closing:
		return locales.PhaseClose()
	case pitstop.Moving:
		return locales.PhaseMove()
	case pitstop.Deleting:
		return locales.PhaseDelete()
	}
	return locales.PhaseCheck()
}

// restartUpdateButton restarts Chrome, which installs what waits for that.
func (a *App) restartUpdateButton(c *ui.Context) {
	if ui.PrimaryButton(c.Key("restart-update"), locales.RestartUpdateButton()).Disabled(a.busy || a.elsewhere).Clicked() {
		a.restartOpen = true
	}
}

// waiting shows an update that goes in once Chrome closes, which a restart
// brings about when its download is there. With Chrome closed, whoever was
// to install it is done or failed, so the user can.
func (a *App) waiting(c *ui.Context, version string, downloaded bool, apply func()) {
	ui.Row(c.Key(version)).Gap(12).AlignItems(ui.Center).Margin(4, 0, 0).Children(func() {
		if a.running {
			ui.Text(c, locales.StatusWaiting(version)).Grow(1)
			if downloaded {
				a.restartUpdateButton(c)
			}
		} else {
			ui.Text(c, locales.StatusNotApplied(version)).Grow(1)
			if ui.PrimaryButton(c.Key("install"), locales.UpdateNowButton()).Disabled(a.busy || a.elsewhere).Clicked() {
				apply()
			}
		}
		if ui.Button(c.Key("skip"), locales.SkipVersionButton()).Disabled(a.busy).Clicked() {
			a.setState(func(s *install.State) { s.Skip(version) })
		}
	})
}

func (a *App) setupCard(c *ui.Context) {
	kit.Card(c, func() {
		ui.Text(c, locales.SetupTitle()).FontSize(16).Bold()
		ui.Text(c, locales.SetupBody(a.engine.Root))
		channels := make([]string, len(omaha.Channels))
		channel := 0
		for i, ch := range omaha.Channels {
			channels[i] = channelLabel(ch)
			if string(ch) == a.setupChannel {
				channel = i
			}
		}
		rowHead(c, locales.SetupChannel(), "", func() {
			if kit.Segmented(c, "setup-channel", &channel, channels...).Label(locales.SetupChannel()).Changed() {
				a.setupChannel = string(omaha.Channels[channel])
			}
		})
		arches := []string{"x64", "x86", "arm64"}
		arch := max(slices.Index(arches, a.setupArch), 0)
		rowHead(c, locales.SetupArch(), "", func() {
			if kit.Segmented(c, "setup-arch", &arch, arches...).Label(locales.SetupArch()).Changed() {
				a.setupArch = arches[arch]
			}
		})
		rowHead(c, locales.UpdatesPolicy(), locales.UpdatesPolicyHelp(), func() {})
		policyRadios(c, &a.setupPolicy, false, func(p install.Policy) { a.setupPolicy = p })
		ui.Row(c).Children(func() {
			if ui.PrimaryButton(c.Key("setup"), locales.SetupButton()).Disabled(a.busy).Clicked() {
				a.setup()
			}
		})
	})
}

func (a *App) keptCard(c *ui.Context) {
	kit.Heading(c, locales.RollbackTitle())
	chrome, okChrome := a.engine.ChromeRollback()
	plus, okPlus := a.engine.ChromePlusRollback()
	kit.Card(c, func() {
		if !okChrome && !okPlus {
			if a.in.State.DiscardReplaced {
				kit.Muted(c, locales.RollbackOff())
			} else {
				kit.Muted(c, locales.RollbackNone())
			}
			return
		}
		row := func(label, version, what string, size int64) {
			ui.Row(c.Key(what)).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, label).Grow(1)
				if size > 0 {
					kit.Muted(c, mb(size))
				}
				if ui.Button(c, locales.SwitchButton(version)).Disabled(a.busy).Clicked() {
					a.switching, a.switchOpen = what, true
				}
				if ui.Button(c, locales.DeleteButton()).Disabled(a.busy).Clicked() {
					a.deleting, a.deleteOpen = what, true
				}
			})
		}
		if okChrome {
			row(locales.RollbackChrome(chrome), chrome, "chrome", a.kept.Chrome)
		}
		if okPlus {
			row(locales.RollbackPlus(plus), plus, "chromeplus", a.kept.Plus)
		}
	})
}

func (a *App) activity(c *ui.Context) {
	ui.Row(c).Gap(12).AlignItems(ui.End).Children(func() {
		kit.Heading(c, locales.ActivityTitle()).Grow(1)
		if len(a.in.State.History) > 0 && ui.Button(c.Key("clear-activity"), locales.ActivityClear()).Disabled(a.busy).Clicked() {
			a.clearOpen = true
		}
	})
	kit.Card(c, func() {
		if len(a.in.State.History) == 0 {
			kit.Muted(c, locales.ActivityNone())
			return
		}
		for i, e := range a.in.State.History {
			if i == 8 {
				break
			}
			ui.Row(c.Key(i)).Gap(12).Children(func() {
				ui.Text(c, describe(e)).Grow(1)
				kit.Muted(c, i18n.FormatDate(c.Locale(), e.Time.Local(), i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle}))
			})
		}
	})
}

func describe(e install.Event) string {
	switch e.Kind {
	case install.Installed:
		return locales.EventInstalled(e.To)
	case install.ChromeUpdated:
		return locales.EventChromeUpdated(e.From, e.To)
	case install.ChromeRolledBack:
		return locales.EventChromeSwitched(e.From, e.To)
	case install.PlusUpdated:
		if e.From == "" {
			return locales.EventPlusInstalled(e.To)
		}
		return locales.EventPlusUpdated(e.From, e.To)
	case install.PlusRolledBack:
		return locales.EventPlusSwitched(e.From, e.To)
	case install.KeptDeleted:
		return locales.EventKeptDeleted(e.To)
	case install.SettingsSaved:
		return locales.EventSettingsSaved()
	case install.Recovered:
		return locales.EventRecovered(e.From)
	}
	return string(e.Kind)
}

func (a *App) dialogs(c *ui.Context) {
	if a.switchOpen && a.in != nil {
		version, current := "", ""
		if a.switching == "chrome" {
			version, _ = a.engine.ChromeRollback()
			current = a.in.Chrome
		} else {
			version, _ = a.engine.ChromePlusRollback()
			current = a.in.ChromePlus
		}
		body := locales.SwitchBody(current)
		if a.switching == "chrome" && pitstop.Milestone(version) < pitstop.Milestone(current) {
			body = locales.SwitchMilestoneBody(version, current)
		}
		if ui.AlertDialog(c, &a.switchOpen, locales.SwitchTitle(version), body, locales.CancelButton(), locales.SwitchConfirm()) == 1 {
			a.switchTo(a.switching)
		}
	}
	if a.deleteOpen {
		name, body := "Chrome++ Next", locales.DeleteBody()
		version, _ := a.engine.ChromePlusRollback()
		if a.deleting == "chrome" {
			name = "Chrome"
			version, _ = a.engine.ChromeRollback()
		}
		if ui.AlertDialog(c, &a.deleteOpen, locales.DeleteTitle(name, version), body, locales.CancelButton(), locales.DeleteButton()) == 1 {
			a.deleteKept(a.deleting)
		}
	}
	if ui.AlertDialog(c, &a.restartOpen, locales.RestartTitle(), locales.RestartBody(), locales.CancelButton(), locales.RestartConfirm()) == 1 {
		a.restartChrome()
	}
	if ui.AlertDialog(c, &a.clearOpen, locales.ActivityClearTitle(), locales.ActivityClearBody(), locales.CancelButton(), locales.ActivityClearConfirm()) == 1 {
		a.clearActivity()
	}
	if a.moveOpen {
		body := locales.MoveBody(a.moveFrom, a.moveTo)
		if a.running {
			body = locales.MoveBodyRunning(a.moveFrom, a.moveTo)
		}
		switch ui.AlertDialog(c, &a.moveOpen, locales.MoveTitle(), body, locales.MoveSkip(), locales.MoveConfirm()) {
		case 0:
			a.write()
		case 1:
			a.moveData()
		}
	}
	if a.changedOpen {
		switch ui.AlertDialog(c, &a.changedOpen, locales.ChangedOnDiskTitle(), locales.ChangedOnDiskBody(), locales.ReloadButton(), locales.OverwriteButton()) {
		case 0:
			a.loadINI()
			a.raw, a.review = nil, nil
		case 1:
			a.save(true)
		}
	}
}

// ready shows why there is nothing to edit, if so.
func (a *App) ready(c *ui.Context) bool {
	switch {
	case a.in == nil:
		kit.Card(c, func() { ui.Text(c, locales.NoInstallBody()) })
		return false
	case a.iniErr != nil:
		kit.Card(c, func() { ui.Text(c, a.iniErr.Error()).Selectable() })
		return false
	case a.draft == nil:
		kit.Card(c, func() {
			ui.Text(c, locales.IniMissingTitle()).Bold()
			ui.Text(c, locales.IniMissingBody())
			ui.Row(c).Children(func() {
				if ui.PrimaryButton(c, locales.IniCreateButton()).Clicked() {
					a.createINI()
				}
			})
		})
		return false
	}
	return true
}

func (a *App) settings(c *ui.Context, name string, page options.Page) {
	kit.Title(c, name)
	if !a.ready(c) {
		return
	}
	msgs := locales.In(c.Locale())
	var shown []options.Option
	for _, o := range options.OnPage(page) {
		if a.applies(o) {
			shown = append(shown, o)
		}
	}
	sections(c, shown, func(o options.Option) string { return groupName(o, msgs) }, func(o options.Option) { a.optionRow(c, o) })
	switch page {
	case options.Keyboard:
		a.keymapping(c)
	case options.Advanced:
		ui.Row(c).Margin(8, 0, 0).Transition(kit.Flow).Children(func() {
			if ui.Button(c, locales.RawEditButton()).Clicked() {
				a.startRaw()
			}
		})
	}
}

func (a *App) applies(o options.Option) bool {
	return o.Applies == nil || o.Applies(func(key string) string {
		for _, p := range options.All {
			if p.Key == key {
				raw, present := a.draft.Raw(p.Section, key)
				return p.Effective(raw, present)
			}
		}
		return ""
	})
}

func (a *App) optionRow(c *ui.Context, o options.Option) {
	t := c.Theme()
	msgs := locales.In(c.Locale())
	label := o.Label(msgs)
	raw, _ := a.draft.Raw(o.Section, o.Key)
	before, _ := a.saved.Raw(o.Section, o.Key)
	id := o.Section + "/" + o.Key
	errText := a.fieldErr[id]
	if o.Kind == options.Hotkey && !options.ValidHotkey(ini.Unquote(raw)) {
		errText = locales.InvalidHotkey()
	}
	row := settingRow(c, id, func() {
		// A click on the label works the control, as in a ui.Field.
		var labelClicked bool
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Gap(2).MinWidth(0).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					if raw != before {
						ui.Box(c).Size(6, 6).Radius(3).Background(t.Accent)
					}
					labelClicked = ui.Text(c.Key("label"), label).Clicked()
				})
				if o.Help != nil {
					kit.Muted(c, o.Help(msgs))
				}
				if errText != "" {
					ui.Text(c, errText).FontSize(12).TextColor(t.Danger)
				} else if o.Kind == options.Hotkey {
					a.hotkeyNote(c, "option/"+o.Key, ini.Unquote(raw))
				}
			})
			a.optionControl(c, o, label, labelClicked)
		})
		a.optionBody(c, o, label, labelClicked)
	})
	if o.Applies != nil {
		row.Padding(12, 16, 12, 40)
	}
}

// optionControl builds what sits at the right of an option's row.
func (a *App) optionControl(c *ui.Context, o options.Option, label string, labelClicked bool) {
	msgs := locales.In(c.Locale())
	raw, present := a.draft.Raw(o.Section, o.Key)
	value := o.Effective(raw, present)
	var ctrl ui.Element
	switch o.Kind {
	case options.Switch:
		on := value == "1"
		ctrl = ui.Switch(c.Key("control"), &on).Label(label)
		if labelClicked {
			on = !on
		}
		if ctrl.Changed() || labelClicked {
			a.set(o.Section, o.Key, map[bool]string{true: "1", false: "0"}[on])
		}
		return
	case options.Select:
		labels := make([]string, len(o.Choices))
		chosen := 0
		for i, ch := range o.Choices {
			labels[i] = ch.Label(msgs)
			if ch.Value == value {
				chosen = i
			}
		}
		ctrl = kit.Segmented(c, o.Key, &chosen, labels...).Label(label)
		if ctrl.Changed() {
			a.set(o.Section, o.Key, o.Choices[chosen].Value)
		}
	case options.Number:
		n, _ := strconv.Atoi(value)
		v := float64(n)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ctrl = ui.NumberInput(c.Key("control"), &v, float64(o.Min), float64(o.Max), 50).Width(140).Label(label)
			kit.Muted(c, locales.Milliseconds())
		})
		if ctrl.Changed() {
			a.set(o.Section, o.Key, strconv.Itoa(int(v)))
		}
	case options.Hotkey:
		id := "option/" + o.Key
		v := ini.Unquote(raw)
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if v != "" && kit.IconButton(c, "clear", closeIcon, locales.HotkeyClear(), "").Clicked() {
				a.set(o.Section, o.Key, "")
			}
			a.hotkeyField(c, id, label, v, false, func(s string) { a.set(o.Section, o.Key, s) })
		})
		if labelClicked {
			a.startRecording(id)
		}
		return
	case options.Folder:
		mode := folderMode(o, value)
		ctrl = kit.Segmented(c, o.Key, &mode, locales.FolderPortable(), locales.FolderChrome(), locales.FolderCustom()).Label(label)
		if ctrl.Changed() {
			switch mode {
			case folderPortable:
				a.set(o.Section, o.Key, o.Default)
			case folderChrome:
				a.set(o.Section, o.Key, "none")
			case folderCustom:
				a.browseFolder(o)
			}
		}
	case options.SwitchList:
		ctrl = ui.Button(c.Key("control"), locales.AddSwitch())
		if ctrl.Clicked() {
			a.switchReference(func(v string) {
				raw, _ := a.draft.Raw(o.Section, o.Key)
				a.set(o.Section, o.Key, options.JoinSwitches(append(options.SplitSwitches(raw), v)))
				if strings.Contains(v, "=") {
					a.focusLine = o.Key
				}
			})
		}
	case options.CommandList:
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c.Key("program"), locales.AddProgram()).Clicked() {
				a.browse(func() string { return a.PickProgram(locales.ProgramPick(), locales.ProgramFilter()) }, func(path string) {
					a.addCommand(o, `"`+options.InstallPath(a.in.App(), path)+`"`)
				})
			}
			ctrl = ui.Button(c.Key("control"), locales.AddCommand())
			if ctrl.Clicked() {
				a.adding, a.focusLine = o.Key, o.Key
			}
		})
	default:
		return
	}
	if labelClicked {
		ctrl.Focus()
	}
}

// optionBody builds what an option shows below its label.
func (a *App) optionBody(c *ui.Context, o options.Option, label string, labelClicked bool) {
	raw, present := a.draft.Raw(o.Section, o.Key)
	switch o.Kind {
	case options.Folder:
		mode := folderMode(o, o.Effective(raw, present))
		if mode == folderCustom {
			ui.Row(c.Key("path")).Gap(8).AlignItems(ui.Center).Transition(kit.RowMotion).Children(func() {
				v := raw
				if ui.TextInput(c.Key("field"), &v).Grow(1).MinWidth(0).Label(label).Changed() {
					a.set(o.Section, o.Key, v)
				}
				if ui.Button(c.Key("browse"), locales.FolderBrowse()).Clicked() {
					a.browseFolder(o)
				}
			})
		}
		if o.Key == "data_dir" && mode != folderPortable {
			ui.Text(c.Key("note"), locales.OptDataDirNote()).FontSize(12).TextColor(c.Theme().Warning).Transition(kit.RowMotion)
		}
	case options.NameList:
		names := options.SplitNames(raw)
		field := ui.TokenField(c.Key("names"), &names, nil).Label(label).Placeholder(locales.AddTitle())
		if labelClicked {
			field.Focus()
		}
		if field.Changed() {
			a.set(o.Section, o.Key, options.JoinNames(names))
		}
	case options.SwitchList:
		a.lines(c, o.Key, label, "", options.SplitSwitches(raw), func(list []string) {
			a.set(o.Section, o.Key, options.JoinSwitches(list))
		})
	case options.CommandList:
		shown, hidden := a.commands(raw)
		a.lines(c, o.Key, label, locales.CommandPlaceholder(), shown, func(list []string) {
			a.set(o.Section, o.Key, options.JoinCommands(append(list, hidden...)))
		})
	}
}

// commands splits a launch_on_* value into the user's commands and
// Pitstop's own, which the update policy adds and takes out.
func (a *App) commands(raw string) (shown, hidden []string) {
	for _, cmd := range options.SplitCommands(raw) {
		if a.engine.IsBackground(strings.TrimSpace(cmd)) {
			hidden = append(hidden, cmd)
		} else {
			shown = append(shown, cmd)
		}
	}
	return shown, hidden
}

func (a *App) addCommand(o options.Option, cmd string) {
	raw, _ := a.draft.Raw(o.Section, o.Key)
	shown, hidden := a.commands(raw)
	a.set(o.Section, o.Key, options.JoinCommands(append(append(shown, cmd), hidden...)))
}

const (
	folderPortable = iota
	folderChrome
	folderCustom
)

func folderMode(o options.Option, value string) int {
	value = ini.Unquote(value)
	switch {
	case value == "none":
		return folderChrome
	case strings.EqualFold(value, o.Default):
		return folderPortable
	}
	return folderCustom
}

func (a *App) browseFolder(o options.Option) {
	a.browse(func() string { return a.PickFolder(locales.FolderPick(), a.engine.Root) }, func(path string) {
		a.set(o.Section, o.Key, options.InstallPath(a.in.App(), path))
	})
}

func (a *App) keymapping(c *ui.Context) {
	kit.Heading(c, locales.KeymapTitle())
	kit.Muted(c, locales.KeymapHelp())
	kvs := a.draft.Keys("keymapping")
	saved := len(kvs)
	if a.adding == mappingRow {
		kvs = append(kvs, ini.KeyValue{})
	}
	kit.Card(c, func() {
		if len(kvs) == 0 {
			kit.Muted(c, locales.KeymapNone())
		}
		for i, kv := range kvs {
			a.mapping(c, i, kv)
		}
		if e := a.fieldErr["keymapping"]; e != "" {
			ui.Text(c, e).FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.Row(c.Key("add-row")).Transition(kit.Flow).Children(func() {
			if ui.Button(c.Key("add"), locales.KeymapAdd()).Disabled(a.adding == mappingRow).Clicked() {
				a.adding = mappingRow
				a.startRecording(sourceField(saved))
			}
		})
	})
}

func sourceField(i int) string { return fmt.Sprintf("keymapping/%d/source", i) }

func targetField(i int) string { return fmt.Sprintf("keymapping/%d/target", i) }

func (a *App) writeMappings(kvs []ini.KeyValue) {
	a.fieldErr["keymapping"] = editError(a.draft.SetKeys("keymapping", kvs))
}

// mapping is a row of [keymapping]. A new row, not in the draft yet, is
// only its source field, and pressing a shortcut there adds it.
func (a *App) mapping(c *ui.Context, i int, kv ini.KeyValue) {
	t := c.Theme()
	msgs := locales.In(c.Locale())
	setSource := func(v string) {
		kvs := a.draft.Keys("keymapping")
		if i == len(kvs) {
			a.writeMappings(append(kvs, ini.KeyValue{Key: v}))
			a.startRecording(targetField(i))
			return
		}
		kvs[i].Key = v
		a.writeMappings(kvs)
	}
	setTarget := func(v string) {
		kvs := a.draft.Keys("keymapping")
		kvs[i].Value = v
		a.writeMappings(kvs)
	}
	chooseCommand := func() {
		a.commandReference(func(id string) { setTarget("command:" + id) })
	}
	ui.Column(c.Key(i)).Gap(4).Transition(kit.RowMotion).Children(func() {
		ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			a.hotkeyField(c, sourceField(i), locales.KeymapSource(), kv.Key, false, setSource)
			if kv == (ini.KeyValue{}) {
				return
			}
			ui.Text(c, "→").TextColor(t.TextMuted)
			command := strings.HasPrefix(kv.Value, "command:")
			kind := 0
			if command {
				kind = 1
			}
			if kit.Segmented(c, fmt.Sprintf("keymapping/%d/kind", i), &kind, locales.KeymapKindShortcut(), locales.KeymapKindCommand()).Label(locales.KeymapTarget()).Changed() {
				if kind == 1 {
					setTarget("command:")
					chooseCommand()
				} else {
					setTarget("")
					a.startRecording(targetField(i))
				}
			}
			if command {
				if ui.Button(c.Key("command"), commandLabel(kv.Value, msgs)).Tooltip(kv.Value).Clicked() {
					chooseCommand()
				}
			} else {
				a.hotkeyField(c, targetField(i), locales.KeymapTarget(), kv.Value, true, setTarget)
			}
			ui.Box(c).Grow(1)
			if kit.IconButton(c, "remove", trashIcon, locales.ListRemove(), "").Clicked() {
				a.writeMappings(slices.Delete(a.draft.Keys("keymapping"), i, i+1))
			}
		})
		switch {
		case kv == (ini.KeyValue{}):
		case !validSource(kv.Key):
			ui.Text(c, locales.InvalidHotkey()).FontSize(12).TextColor(t.Danger)
		case kv.Value == "" || kv.Value == "command:":
			kit.Muted(c, locales.InvalidTarget())
		case !options.ValidTarget(kv.Value):
			ui.Text(c, locales.InvalidTarget()).FontSize(12).TextColor(t.Danger)
		default:
			a.hotkeyNote(c, sourceField(i), kv.Key)
		}
	})
}

func commandLabel(target string, msgs locales.Messages) string {
	id := strings.TrimPrefix(target, "command:")
	if id == "" {
		return locales.KeymapChoose()
	}
	if label, ok := options.CommandLabel(id, msgs); ok {
		return label
	}
	return locales.KeymapCustomCommand(id)
}

// ownSetting is a setting of Pitstop itself, not of chrome++.ini. Its Option
// holds what a search and a heading read.
type ownSetting struct {
	options.Option
	row func(*ui.Context)
}

func (a *App) ownSettings() []ownSetting {
	list := []ownSetting{
		{options.Option{Key: "language", Label: locales.Messages.LanguageLabel}, a.languageRow},
		{options.Option{Key: "theme", Label: locales.Messages.ThemeLabel}, a.themeRow},
	}
	if a.in == nil {
		return list
	}
	updates := locales.Messages.GroupUpdates
	list = append(list, ownSetting{options.Option{Key: "channel", Group: updates, Label: locales.Messages.UpdatesChannel, Help: locales.Messages.UpdatesChannelHelp}, a.channelRow})
	list = append(list, ownSetting{options.Option{Key: "policy", Group: updates, Label: locales.Messages.UpdatesPolicy, Help: locales.Messages.UpdatesPolicyHelp}, a.policyRow})
	if a.in.State.Policy != install.Off {
		list = append(list, ownSetting{options.Option{Key: "interval", Group: updates, Label: locales.Messages.UpdatesInterval}, a.intervalRow})
	}
	list = append(list, ownSetting{options.Option{Key: "keep", Group: updates, Label: locales.Messages.UpdatesKeep, Help: locales.Messages.UpdatesKeepHelp}, a.keepRow})
	list = append(list, ownSetting{options.Option{Key: "diff", Group: updates, Label: locales.Messages.UpdatesDiff, Help: locales.Messages.UpdatesDiffHelp}, a.diffRow})
	return append(list, ownSetting{options.Option{Key: "host", Group: updates, Label: locales.Messages.UpdatesHost, Help: locales.Messages.UpdatesHostHelp}, a.hostRow})
}

func (a *App) ownSettingsPage(c *ui.Context) {
	kit.Title(c, locales.PageSettings())
	msgs := locales.In(c.Locale())
	sections(c, a.ownSettings(), func(s ownSetting) string { return groupName(s.Option, msgs) }, func(s ownSetting) { s.row(c) })
}

func (a *App) languageRow(c *ui.Context) {
	tags := append([]string{""}, locales.Catalog.Languages()...)
	labels := make([]string, len(tags))
	chosen := 0
	for i, tag := range tags {
		labels[i] = locales.LanguageSystem()
		if tag != "" {
			labels[i] = i18n.Autonym(tag)
		}
		if tag == a.Language {
			chosen = i
		}
	}
	label := locales.LanguageLabel()
	settingRow(c, "language", func() {
		rowHead(c, label, "", func() {
			if kit.Segmented(c, "language", &chosen, labels...).Label(label).Changed() {
				a.Language = tags[chosen]
				a.SetLanguage(a.Language)
			}
		})
	})
}

func (a *App) themeRow(c *ui.Context) {
	names := []string{"", ThemeLight, ThemeDark}
	chosen := max(slices.Index(names, a.Theme), 0)
	label := locales.ThemeLabel()
	settingRow(c, "theme", func() {
		rowHead(c, label, "", func() {
			if kit.Segmented(c, "theme", &chosen, locales.ThemeSystem(), locales.ThemeLight(), locales.ThemeDark()).Label(label).Changed() {
				a.Theme = names[chosen]
				a.SetTheme(a.Theme)
			}
		})
	})
}

func (a *App) keepRow(c *ui.Context) {
	label := locales.UpdatesKeep()
	settingRow(c, "keep", func() {
		rowHead(c, label, locales.UpdatesKeepHelp(), func() {
			on := !a.in.State.DiscardReplaced
			if ui.Switch(c.Key("control"), &on).Label(label).Disabled(a.busy).Changed() {
				a.setKeep(on)
			}
		})
	})
}

// diffRow drops the updates found so far: a check with the kept package
// can offer a differential update instead.
func (a *App) diffRow(c *ui.Context) {
	label := locales.UpdatesDiff()
	settingRow(c, "diff", func() {
		rowHead(c, label, locales.UpdatesDiffHelp(), func() {
			on := a.in.State.DiffUpdates
			if ui.Switch(c.Key("control"), &on).Label(label).Disabled(a.busy).Changed() {
				a.setState(func(s *install.State) { s.DiffUpdates = on })
			}
		})
	})
}

func (a *App) hostRow(c *ui.Context) {
	hosts := append([]string{""}, install.DownloadHosts...)
	labels := append([]string{locales.HostAutomatic()}, install.DownloadHosts...)
	chosen := max(slices.Index(hosts, a.in.State.DownloadHost), 0)
	label := locales.UpdatesHost()
	settingRow(c, "host", func() {
		rowHead(c, label, locales.UpdatesHostHelp(), func() {
			if kit.Segmented(c, "host", &chosen, labels...).Label(label).Changed() {
				a.setState(func(s *install.State) { s.DownloadHost = hosts[chosen] })
			}
		})
	})
}

func (a *App) channelRow(c *ui.Context) {
	labels := make([]string, len(omaha.Channels))
	chosen := 0
	for i, ch := range omaha.Channels {
		labels[i] = channelLabel(ch)
		if ch == a.in.State.Channel {
			chosen = i
		}
	}
	label := locales.UpdatesChannel()
	settingRow(c, "channel", func() {
		rowHead(c, label, locales.UpdatesChannelHelp(), func() {
			if kit.Segmented(c, "channel", &chosen, labels...).Label(label).Changed() {
				ch := omaha.Channels[chosen]
				a.setState(func(s *install.State) {
					s.Channel = ch
					s.Approved = ""
				})
			}
		})
	})
}

func (a *App) policyRow(c *ui.Context) {
	label := locales.UpdatesPolicy()
	settingRow(c, "policy", func() {
		rowHead(c, label, locales.UpdatesPolicyHelp(), func() {})
		if !a.in.ManagesUpdates {
			ui.Text(c, locales.UpdatesPolicyNeedsPlus()).TextColor(c.Theme().Warning)
		}
		chosen := a.in.State.Policy
		policyRadios(c, &chosen, !a.in.ManagesUpdates, a.setPolicy)
	})
}

func policyRadios(c *ui.Context, chosen *install.Policy, disabled bool, changed func(install.Policy)) {
	ui.RadioGroup(c.Key("control"), func() {
		for _, p := range install.Policies {
			if ui.Radio(c.Key(p), chosen, p, policyLabel(p)).Disabled(disabled).Changed() {
				changed(p)
			}
		}
	}).Gap(6).Label(locales.UpdatesPolicy())
}

func (a *App) intervalRow(c *ui.Context) {
	labels := []string{locales.IntervalHourly(), locales.IntervalThreeHours(), locales.IntervalDaily()}
	chosen := max(slices.Index(install.CheckHours, cmp.Or(a.in.State.CheckHours, install.DefaultCheckHours)), 0)
	label := locales.UpdatesInterval()
	settingRow(c, "interval", func() {
		rowHead(c, label, "", func() {
			if kit.Segmented(c, "interval", &chosen, labels...).Label(label).Changed() {
				a.setCheckHours(install.CheckHours[chosen])
			}
		})
	})
}

func (a *App) searchResults(c *ui.Context) {
	kit.Title(c, locales.SearchResults(a.query))
	en, zh := locales.In("en"), locales.In("zh-Hans")
	var own []ownSetting
	for _, s := range a.ownSettings() {
		if s.Matches(a.query, en, zh) {
			own = append(own, s)
		}
	}
	var found []options.Option
	for _, o := range options.All {
		if o.Matches(a.query, en, zh) {
			found = append(found, o)
		}
	}
	if len(own) > 0 {
		kit.Panel(c.Key("own"), func() {
			for _, s := range own {
				s.row(c)
			}
		})
	}
	if len(found) == 0 {
		if len(own) == 0 {
			kit.Muted(c, locales.SearchNone(a.query))
		}
		return
	}
	if !a.ready(c) {
		return
	}
	kit.Panel(c.Key("options"), func() {
		for _, o := range found {
			a.optionRow(c, o)
		}
	})
}

func (a *App) pendingBar(c *ui.Context) {
	n := a.pending()
	if n == 0 {
		return
	}
	t := c.Theme()
	ui.Column(c.Key("pending")).Background(t.Surface).Transition(kit.RowMotion).Children(func() {
		ui.Divider(c)
		ui.Column(c).Padding(12, kit.PageSide).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().MaxWidth(kit.ContentWidth).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, locales.PendingCount(n)).FontWeight(600)
					kit.Muted(c, locales.PendingHint())
				})
				if ui.Button(c, locales.ReviewButton()).Clicked() {
					a.startReview()
				}
				if ui.Button(c, locales.DiscardButton()).Disabled(a.busy).Clicked() {
					a.discard()
				}
				if ui.PrimaryButton(c.Key("save"), locales.SaveButton()).Tooltip("Ctrl+S").Disabled(a.invalid() || a.busy).Clicked() {
					a.save(false)
				}
			})
		})
	})
}

func (a *App) reviewView(c *ui.Context) {
	ui.Column(c).Padding(kit.Top(c), kit.PageSide, 8).Gap(4).Children(func() {
		kit.Title(c, locales.ReviewTitle())
		kit.Muted(c, locales.ReviewDetail(a.iniPath, a.draft.Encoding().String())).Selectable()
	})
	editor.DiffView(c, a.review).Grow(1).FillWidth()
	ui.Row(c).Gap(12).Padding(12, 32).Justify(ui.End).Children(func() {
		if ui.Button(c, locales.BackButton()).Clicked() {
			a.review = nil
		}
		if ui.PrimaryButton(c.Key("save"), locales.SaveButton()).Tooltip("Ctrl+S").Disabled(a.invalid()).Clicked() {
			a.save(false)
		}
	})
}

func (a *App) rawView(c *ui.Context) {
	raw := a.raw // Done ends the editing in this frame
	ui.Row(c).Padding(kit.Top(c), kit.PageSide, 8).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Children(func() {
			kit.Title(c, locales.RawTitle())
			kit.Muted(c, a.iniPath).Selectable()
		})
		if ui.PrimaryButton(c.Key("done"), locales.DoneButton()).Clicked() {
			a.endRaw()
		}
	})
	if a.rawErr != nil {
		ui.Text(c, a.rawErr.Error()).TextColor(c.Theme().Danger).Padding(0, 32)
	}
	editor.View(c, raw).Grow(1).FillWidth()
}

func (a *App) about(c *ui.Context) {
	kit.Title(c, locales.PageAbout())
	ui.Row(c).Gap(16).AlignItems(ui.Center).Padding(8, 0, 12).Children(func() {
		ui.Image(c, appIcon).Size(64, 64).Shrink(0)
		ui.Column(c).Gap(2).MinWidth(0).Children(func() {
			ui.Text(c, locales.AppTitle()).FontSize(20).FontWeight(600)
			ui.Text(c, locales.AboutBody())
			kit.Muted(c, locales.AboutVersion(a.version)).Selectable()
			if ui.Button(c.Key("self-update"), locales.CheckButton()).Disabled(a.busy).Clicked() {
				a.CheckForUpdates()
			}
		})
	})
	kit.Panel(c.Key("links"), func() {
		project := func(name, help, url string) {
			settingRow(c, name, func() {
				rowHead(c, name, help, func() { kit.Link(c, "GitHub", url) })
			})
		}
		project(locales.AppTitle(), locales.AboutPitstopHelp(), "https://github.com/Bush2021/pitstop")
		project("Chrome++ Next", locales.AboutPlusHelp(), "https://github.com/"+ghrelease.Repo)
	})
}
