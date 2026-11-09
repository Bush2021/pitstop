// Package gui is Pitstop's window: the install's status with its
// updates and rollbacks, and the settings of chrome++.ini, edited without
// losing a byte of the file.
package gui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/pitstop/internal/defaultini"
	"github.com/Bush2021/pitstop/internal/ini"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/options"
	"github.com/Bush2021/pitstop/internal/pitstop"
	"github.com/Bush2021/pitstop/locales"
)

// Host is what the window needs from the app around it. The package can't
// import mygo's root package itself: its init ties a Tester's locale to the
// system's.
type Host struct {
	// Post runs a function where the view runs, then draws a frame: the
	// window's Update.
	Post func(func())
	// Language is the language the user chose, "" for the system's.
	Language    string
	SetLanguage func(string)
	// Theme is the look the user chose, ThemeLight, ThemeDark or "" for the
	// system's.
	Theme    string
	SetTheme func(string)
	// PickFolder and PickProgram ask for a path and block until the user
	// answers; "" means they canceled.
	PickFolder      func(title, start string) string
	PickProgram     func(title, filter string) string
	OpenFolder      func(path string)
	CheckForUpdates func()
}

const (
	ThemeLight = "light"
	ThemeDark  = "dark"
)

type App struct {
	Host
	engine  *pitstop.Engine
	version string

	page  string
	query string

	// in is the install, nil while there is none; openErr says why.
	in      *install.Install
	openErr error

	busy      bool
	cancel    context.CancelFunc
	step      pitstop.Step
	stepStart time.Time
	stepFrom  int64
	speed     float64
	opErr     error
	// retry runs the last operation again, after Chrome blocked it.
	retry func()
	toast string

	// running says whether Chrome runs from the install, as of Watch's last
	// look.
	running bool
	// elsewhere says whether another Pitstop, such as the background mode,
	// holds the install, as of Watch's last look.
	elsewhere   bool
	restartOpen bool
	clearOpen   bool

	checked      bool
	chromeUpdate *pitstop.ChromeUpdate
	plusUpdate   *pitstop.PlusUpdate

	setupChannel, setupArch string
	setupPolicy             install.Policy

	iniPath      string
	saved, draft *ini.File
	iniErr       error
	fieldErr     map[string]string
	review       *editor.DiffState
	raw          *editor.State
	rawErr       error

	switching    string // "chrome" or "chromeplus"
	switchOpen   bool
	deleting     string // "chrome" or "chromeplus"
	deleteOpen   bool
	kept         pitstop.KeptSizes
	changedOpen  bool
	overwriteNow bool
	// moveFrom and moveTo are the user data's way, while moveOpen asks about
	// it.
	moveFrom, moveTo string
	moveOpen         bool

	// recording is the hotkey field listening for keys.
	recording   string
	recordFocus bool
	recordHint  string
	// adding is the list showing a new, empty row, focusLine the list whose
	// last row takes the focus.
	adding    string
	focusLine string

	ref      *reference
	refOpen  bool
	refQuery string
}

// mappingRow is the value of App.adding for a new key mapping.
const mappingRow = "keymapping"

const (
	pageOverview = "overview"
	pageTabs     = "tabs"
	pageKeyboard = "keyboard"
	pageStartup  = "startup"
	pageAdvanced = "advanced"
	pageSettings = "settings"
	pageAbout    = "about"
)

func New(engine *pitstop.Engine, version string, host Host) *App {
	a := &App{Host: host, engine: engine, version: version, page: pageOverview,
		setupChannel: string(omaha.Stable), setupArch: nativeArch(), fieldErr: map[string]string{}}
	// A busy install gets this at the window's next start.
	engine.SyncINI()
	a.reload()
	a.running = engine.ChromeRunning()
	return a
}

// Watch keeps the window current: nothing tells when Chrome starts or stops,
// or when the background mode changes the install. Every such change ends by
// saving the state. A build Chrome swapped out may stay in use after Chrome
// restarts, so opening the install retries settling it until that succeeds.
// Settling can wait seconds for a file, so the install opens off the UI
// thread.
func (a *App) Watch() {
	running, elsewhere := a.running, false
	state := install.StateTime(a.engine.Root)
	go func() {
		kept := a.engine.KeptSizes()
		a.Post(func() { a.kept = kept })
		for range time.Tick(2 * time.Second) {
			if now := a.engine.ChromeRunning(); now != running {
				running = now
				a.Post(func() { a.setRunning(now) })
			}
			if now := a.engine.Busy(); now != elsewhere {
				elsewhere = now
				a.Post(func() { a.elsewhere = now })
			}
			if now := install.StateTime(a.engine.Root); !now.Equal(state) {
				state = now
				if in, err := a.engine.Open(); err == nil {
					kept := a.engine.KeptSizes()
					a.Post(func() { a.refresh(in, kept) })
				}
			} else if install.Unsettled(a.engine.Root) {
				if in, err := a.engine.Open(); err == nil {
					a.Post(func() { a.refresh(in, a.kept) })
				}
			}
		}
	}()
}

// refresh shows what the background mode did. It leaves chrome++.ini and its
// unsaved edits alone, as the background mode doesn't change the file, and
// work of this window reloads when it ends.
func (a *App) refresh(in *install.Install, kept pitstop.KeptSizes) {
	if a.busy || a.in == nil {
		return
	}
	a.in, a.kept = in, kept
	if u := a.chromeUpdate; u != nil && install.CompareVersions(u.Version, in.Chrome) <= 0 {
		a.chromeUpdate = nil
	}
	if u := a.plusUpdate; u != nil && u.Release.Tag == in.ChromePlus {
		a.plusUpdate = nil
	}
}

func (a *App) setRunning(now bool) {
	closed := a.running && !now
	a.running = now
	if closed {
		a.installWaiting()
	}
}

// installWaiting installs what waited for Chrome to close, as the background
// mode does when it runs. Whichever of the two finds the install busy leaves
// the work to the other, and a Chrome that started again keeps it waiting.
func (a *App) installWaiting() {
	if a.busy || a.in == nil || a.in.Staged == "" && a.in.State.Approved == "" && a.in.State.ApprovedPlus == "" {
		return
	}
	var done []string
	a.run(func(ctx context.Context) error {
		for _, p := range []struct {
			name    string
			install func(context.Context) (string, error)
		}{{"Google Chrome", a.engine.InstallApproved}, {"Chrome++ Next", a.engine.InstallApprovedPlus}} {
			v, err := p.install(ctx)
			if errors.Is(err, pitstop.ErrBusy) || errors.Is(err, pitstop.ErrChromeRunning) {
				return nil
			}
			if err != nil {
				return err
			}
			if v != "" {
				done = append(done, p.name+" "+v)
			}
		}
		return nil
	}, func() {
		if len(done) > 0 {
			a.toast = locales.InstalledTitle(strings.Join(done, ", "))
		}
	})
}

func (a *App) startChrome() {
	if err := a.engine.StartChrome(); err != nil {
		a.opErr = err
		return
	}
	a.running = true
}

func (a *App) restartChrome() {
	a.run(a.engine.RestartChrome, nil)
}

// nativeArch names Pitstop's own architecture, which Chrome is set up
// for unless the user picks another.
func nativeArch() string {
	switch runtime.GOARCH {
	case "386":
		return "x86"
	case "arm64":
		return "arm64"
	}
	return "x64"
}

func (a *App) reload() {
	a.in, a.openErr = a.engine.Open()
	if a.in == nil {
		a.saved, a.draft, a.iniPath = nil, nil, ""
		return
	}
	// Work that leaves the file alone leaves the user's edits to it too.
	if disk, err := os.ReadFile(a.iniPath); a.saved != nil && err == nil && bytes.Equal(disk, a.saved.Bytes()) {
		return
	}
	a.loadINI()
}

func (a *App) loadINI() {
	a.iniPath = filepath.Join(a.in.App(), "chrome++.ini")
	a.saved, a.iniErr = ini.Load(a.iniPath)
	if errors.Is(a.iniErr, os.ErrNotExist) {
		a.saved, a.iniErr = nil, nil
	}
	a.draft = nil
	if a.saved != nil {
		a.draft = ini.Parse(a.saved.Bytes())
	}
	clear(a.fieldErr)
}

func (a *App) createINI() {
	if err := os.WriteFile(a.iniPath, defaultini.Bytes, 0o644); err != nil {
		a.opErr = err
		return
	}
	a.loadINI()
}

func (a *App) run(op func(ctx context.Context) error, done func()) {
	ctx, cancel := context.WithCancel(context.Background())
	a.busy, a.cancel, a.opErr, a.retry = true, cancel, nil, nil
	a.step, a.speed = pitstop.Step{}, 0
	var lastPost time.Time
	var lastPhase pitstop.Phase
	a.engine.Progress = func(s pitstop.Step) {
		// Called from the work's goroutine, often: pass on a few a second.
		now := time.Now()
		if s.Phase == lastPhase && now.Sub(lastPost) < 100*time.Millisecond && (s.Total == 0 || s.Done < s.Total) {
			return
		}
		lastPost, lastPhase = now, s.Phase
		a.Post(func() { a.progress(s, now) })
	}
	go func() {
		err := op(ctx)
		kept := a.engine.KeptSizes()
		a.Post(func() {
			a.busy, a.cancel, a.kept = false, nil, kept
			a.reload()
			switch {
			case errors.Is(err, context.Canceled):
			case errors.Is(err, pitstop.ErrChromeRunning):
				a.opErr = err
				a.retry = func() { a.run(op, done) }
			case err != nil:
				a.opErr = err
			case done != nil:
				done()
			}
		})
	}()
}

func (a *App) progress(s pitstop.Step, at time.Time) {
	if s.Phase != a.step.Phase {
		a.stepStart, a.stepFrom, a.speed = at, s.Done, 0
	} else if el := at.Sub(a.stepStart).Seconds(); el > 0.5 {
		a.speed = float64(s.Done-a.stepFrom) / el
	}
	a.step = s
}

func (a *App) check() {
	in := a.in
	var cu *pitstop.ChromeUpdate
	var pu *pitstop.PlusUpdate
	a.run(func(ctx context.Context) error {
		var err error
		if cu, err = a.engine.CheckChrome(ctx, in); err != nil {
			return err
		}
		pu, err = a.engine.CheckChromePlus(ctx, in)
		return err
	}, func() {
		a.checked, a.chromeUpdate, a.plusUpdate = true, cu, pu
	})
}

func (a *App) updateChrome() {
	u := a.chromeUpdate
	var outcome pitstop.Outcome
	a.run(func(ctx context.Context) (err error) {
		outcome, err = a.engine.UpdateChrome(ctx, u)
		return err
	}, func() {
		a.chromeUpdate = nil
		a.toast = updatedToast("Google Chrome", u.Version, outcome)
	})
}

// installApproved installs the approved Chrome, which normally goes in when
// Chrome is seen to exit. A download that failed is fetched again.
func (a *App) installApproved() {
	var got string
	var outcome pitstop.Outcome
	a.run(func(ctx context.Context) error {
		v, err := a.engine.InstallApproved(ctx)
		if err != nil || v != "" {
			got = v
			return err
		}
		in, err := a.engine.Open()
		if err != nil {
			return err
		}
		u, err := a.engine.CheckChrome(ctx, in)
		if err != nil || u == nil || u.Version != in.State.Approved {
			return err
		}
		got = u.Version
		outcome, err = a.engine.UpdateChrome(ctx, u)
		return err
	}, func() {
		if got != "" {
			a.toast = updatedToast("Google Chrome", got, outcome)
		}
	})
}

// installApprovedPlus is installApproved for Chrome++, except that a release
// newer than the approved one goes in instead of it.
func (a *App) installApprovedPlus() {
	var got string
	var outcome pitstop.Outcome
	a.run(func(ctx context.Context) error {
		v, err := a.engine.InstallApprovedPlus(ctx)
		if err != nil || v != "" {
			got = v
			return err
		}
		in, err := a.engine.Open()
		if err != nil {
			return err
		}
		u, err := a.engine.CheckChromePlus(ctx, in)
		if err != nil || u == nil {
			return err
		}
		got = u.Release.Tag
		outcome, err = a.engine.UpdateChromePlus(ctx, u)
		return err
	}, func() {
		if got != "" {
			a.toast = updatedToast("Chrome++ Next", got, outcome)
		}
	})
}

func (a *App) dismissFailure() {
	if err := a.engine.UpdateState(func(s *install.State) { s.Failure = nil }); err != nil {
		a.opErr = err
		return
	}
	a.in.State.Failure = nil
}

func (a *App) updateChromePlus() {
	u := a.plusUpdate
	var outcome pitstop.Outcome
	a.run(func(ctx context.Context) (err error) {
		outcome, err = a.engine.UpdateChromePlus(ctx, u)
		return err
	}, func() {
		a.plusUpdate = nil
		a.toast = updatedToast("Chrome++ Next", u.Release.Tag, outcome)
	})
}

func (a *App) setup() {
	o := pitstop.SetupOptions{Channel: omaha.Channel(a.setupChannel), Arch: a.setupArch, Policy: a.setupPolicy}
	a.run(func(ctx context.Context) error { return a.engine.Setup(ctx, o) }, func() {
		if a.in != nil {
			a.checked = true
			a.toast = installedToast("Google Chrome", a.in.Chrome)
		}
	})
}

// switchTo swaps the kept version of Chrome or Chrome++ in. The user has
// confirmed it, milestone and all.
func (a *App) switchTo(what string) {
	a.checked, a.chromeUpdate, a.plusUpdate = false, nil, nil
	a.run(func(ctx context.Context) error {
		if what == "chrome" {
			return a.engine.RollbackChrome(ctx, true)
		}
		return a.engine.RollbackChromePlus(ctx)
	}, nil)
}

func (a *App) deleteKept(what string) {
	a.run(func(context.Context) error {
		if what == "chrome" {
			return a.engine.DeleteChromeRollback()
		}
		return a.engine.DeleteChromePlusRollback()
	}, func() { a.toast = describe(a.in.State.History[0]) })
}

// setKeep leaves the updates found so far on offer: they don't depend on it.
func (a *App) setKeep(on bool) {
	if err := a.engine.UpdateState(func(s *install.State) { s.DiscardReplaced = !on }); err != nil {
		a.opErr = err
	}
	a.in, a.openErr = a.engine.Open()
}

func (a *App) clearActivity() {
	if err := a.engine.UpdateState(func(s *install.State) { s.History = nil }); err != nil {
		a.opErr = err
	}
	a.in, a.openErr = a.engine.Open()
}

func (a *App) setCheckHours(hours int) {
	if err := a.engine.UpdateState(func(s *install.State) { s.CheckHours = hours }); err != nil {
		a.opErr = err
	}
	a.in, a.openErr = a.engine.Open()
}

// setState leaves chrome++.ini and its unsaved edits alone.
func (a *App) setState(change func(*install.State)) {
	if err := a.engine.UpdateState(change); err != nil {
		a.opErr = err
	}
	a.checked, a.chromeUpdate, a.plusUpdate = false, nil, nil
	a.in, a.openErr = a.engine.Open()
}

// setPolicy sets the update policy, which writes the opt-in to
// chrome++.ini. A draft with unsaved changes gets the same
// change, so that saving it later doesn't undo it, and so does the file it
// started from, so that saving it over a change made elsewhere still asks.
func (a *App) setPolicy(p install.Policy) {
	saved, draft := a.saved, a.draft
	if err := a.engine.SetPolicy(p); err != nil {
		a.opErr = err
		return
	}
	a.reload()
	if draft == nil || a.draft == nil || bytes.Equal(saved.Bytes(), draft.Bytes()) {
		return
	}
	on := p != install.Off
	if a.engine.SetOptIn(saved, on) == nil && a.engine.SetOptIn(draft, on) == nil {
		a.saved, a.draft = saved, draft
	}
}

// pending counts the keys whose values differ between the file and the
// draft; a draft that differs only in comments or layout counts as one.
func (a *App) pending() int {
	if a.saved == nil || a.draft == nil || bytes.Equal(a.saved.Bytes(), a.draft.Bytes()) {
		return 0
	}
	n := 0
	seen := map[string]bool{}
	for _, f := range []*ini.File{a.saved, a.draft} {
		for _, sec := range f.Sections() {
			if seen[strings.ToLower(sec)] {
				continue
			}
			seen[strings.ToLower(sec)] = true
			before, after := a.saved.Keys(sec), a.draft.Keys(sec)
			if strings.EqualFold(sec, "keymapping") {
				if !equalKeys(before, after) {
					n++
				}
				continue
			}
			for _, k := range union(before, after) {
				b, bok := a.saved.Raw(sec, k)
				d, dok := a.draft.Raw(sec, k)
				if b != d || bok != dok {
					n++
				}
			}
		}
	}
	return max(n, 1)
}

func equalKeys(a, b []ini.KeyValue) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func union(lists ...[]ini.KeyValue) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, kv := range l {
			if k := strings.ToLower(kv.Key); !seen[k] {
				seen[k] = true
				out = append(out, kv.Key)
			}
		}
	}
	return out
}

// invalid reports whether the draft holds a value Chrome++ would not read
// as meant, which keeps it from being saved.
func (a *App) invalid() bool {
	if a.draft == nil {
		return false
	}
	for _, o := range options.All {
		if o.Kind == options.Hotkey {
			if v, _ := a.draft.Get(o.Section, o.Key); !options.ValidHotkey(v) {
				return true
			}
		}
	}
	for _, kv := range a.draft.Keys("keymapping") {
		if !validMapping(kv) {
			return true
		}
	}
	return false
}

func validMapping(kv ini.KeyValue) bool {
	return validSource(kv.Key) && options.ValidTarget(kv.Value)
}

// validSource also refuses "=", as Chrome++ cuts a line of [keymapping] at
// the first one.
func validSource(key string) bool {
	return strings.TrimSpace(key) != "" && !strings.Contains(key, "=") && options.ValidHotkey(key)
}

func (a *App) set(sec, key, value string) {
	a.fieldErr[sec+"/"+key] = editError(a.draft.Set(sec, key, value))
}

func editError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ini.ErrNonASCII):
		return locales.NonASCII()
	}
	return err.Error()
}

func (a *App) discard() {
	a.draft = ini.Parse(a.saved.Bytes())
	a.review = nil
	clear(a.fieldErr)
}

// save writes the draft, unless the file changed on disk since it was
// read and the user has not said to replace it.
func (a *App) save(force bool) {
	disk, err := os.ReadFile(a.iniPath)
	if err == nil && !bytes.Equal(disk, a.saved.Bytes()) && !force {
		a.changedOpen = true
		return
	}
	from, okFrom := a.dataFolder(a.saved)
	to, okTo := a.dataFolder(a.draft)
	if okFrom && okTo && pitstop.MovableData(from, to) {
		a.moveFrom, a.moveTo, a.moveOpen = from, to, true
		return
	}
	a.write()
}

func (a *App) dataFolder(f *ini.File) (string, bool) {
	raw, _ := f.Raw("general", "data_dir")
	return a.engine.DataFolder(ini.Unquote(raw))
}

func (a *App) write() {
	if err := a.draft.Save(a.iniPath); err != nil {
		a.opErr = err
		return
	}
	a.loadINI()
	a.savedSettings()
	a.toast = savedToast(a.engine.ChromeRunning())
}

func (a *App) savedSettings() {
	a.review = nil
	if err := a.engine.UpdateState(func(s *install.State) { s.Record(install.Event{Kind: install.SettingsSaved}) }); err != nil {
		a.opErr = err
	}
	a.in, a.openErr = a.engine.Open()
}

// moveData moves the user data, then saves the draft that points to it, so
// that chrome++.ini and the folder never disagree. The Overview shows how
// it goes.
func (a *App) moveData() {
	from, to, draft, path := a.moveFrom, a.moveTo, a.draft, a.iniPath
	a.page, a.raw, a.review = pageOverview, nil, nil
	a.run(func(ctx context.Context) error {
		if a.engine.ChromeRunning() {
			if err := a.engine.CloseChrome(ctx); err != nil {
				return err
			}
		}
		if err := a.engine.MoveData(ctx, from, to); err != nil {
			return err
		}
		return draft.Save(path)
	}, func() {
		a.savedSettings()
		a.toast = locales.DoneMoved(to)
	})
}

func (a *App) startReview() {
	old, cur := editor.NewBuffer(a.saved.Text()), editor.NewBuffer(a.draft.Text())
	lang := editor.LanguageFor("chrome++.ini")
	old.SetLanguage(lang)
	cur.SetLanguage(lang)
	a.review = &editor.DiffState{Old: old, New: cur, Layout: editor.DiffUnified, Wrap: true}
}

func (a *App) startRaw() {
	buf := editor.NewBuffer(a.draft.Text())
	buf.SetLanguage(editor.LanguageFor("chrome++.ini"))
	a.raw = &editor.State{Buffer: buf, SoftWrap: true}
	a.rawErr = nil
}

func (a *App) endRaw() {
	if a.applyRaw() {
		a.raw = nil
	}
}

func (a *App) applyRaw() bool {
	f, err := a.draft.FromText(a.raw.Buffer.String())
	if err != nil {
		a.rawErr = err
		return false
	}
	a.draft, a.rawErr = f, nil
	return true
}

// saveKey is Ctrl+S. In the text editor it saves the text and stays there, as
// any editor does.
func (a *App) saveKey() {
	if a.busy || a.saved == nil || a.draft == nil || a.raw != nil && !a.applyRaw() {
		return
	}
	switch {
	case !a.invalid():
		if !bytes.Equal(a.saved.Bytes(), a.draft.Bytes()) {
			a.save(false)
		}
	case a.raw != nil:
		a.rawErr = errors.New(locales.RawInvalid())
	}
}
