// Package pitstop installs, updates and rolls back the Chrome and the
// Chrome++ of one portable install: Chrome from Google's update server, and
// Chrome++ from its GitHub releases. Each change is downloaded, verified and
// unpacked into a staging folder first, then swapped in by one journaled
// transaction while Chrome is closed. The files it replaces are kept for a
// rollback, unless the user turned that off. While Chrome runs, an install
// that opted in gets the new Chrome staged in App instead, for Chrome to swap
// in when it restarts.
//
// Pitstop's files sit in the install's .pitstop folder:
//
//	state.json  journal.json  lock
//	cache\chrome\          packages being downloaded and decoded
//	cache\chrome\base\     the last package, kept for differential updates
//	cache\chromeplus\      Chrome++ release archives
//	staging\chrome-<version>\       a verified build, ready to swap in
//	staging\chromeplus-<release>\   version.dll and the default chrome++.ini
//	rollback\chrome-<version>\      the build an update replaced
//	rollback\chromeplus-<release>\  the Chrome++ an update replaced
package pitstop

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"

	"github.com/Bush2021/chrome_update/fetch"
	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/chrome_update/sevenz"
	"github.com/Bush2021/chrome_update/unpack"
	"github.com/Bush2021/pitstop/internal/apply"
	"github.com/Bush2021/pitstop/internal/chromeproc"
	"github.com/Bush2021/pitstop/internal/ghrelease"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/verify"
)

const (
	app         = install.AppDir
	meta        = install.MetaDir
	chromeCache = meta + "/cache/chrome"
	plusCache   = meta + "/cache/chromeplus"
	staging     = meta + "/staging"
	rollback    = meta + "/rollback"
	journal     = meta + "/journal.json"
	lockFile    = meta + "/lock"
)

type Engine struct {
	Root string
	// Exe is pitstop.exe, which Chrome++ starts for the background mode.
	Exe      string
	HTTP     *http.Client
	Omaha    omaha.Client
	GitHub   ghrelease.Client
	Progress func(Step)
	Hooks    Hooks
}

// Hooks are the parts of the work that need Google's real packages or
// Windows.
type Hooks struct {
	Unpack  func(ctx context.Context, crx3, dir string, progress unpack.Progress) (string, error)
	Verify  func(dir, version string) error
	Running func(dir string) ([]chromeproc.Process, error)
	Wait    func(ctx context.Context, dir string) error
	Close   func(dir string) error
	Start   func(exe string) error
}

func New(root string, client *http.Client) (*Engine, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &Engine{
		Root:   root,
		Exe:    exe,
		HTTP:   client,
		Omaha:  omaha.Client{HTTP: client},
		GitHub: ghrelease.Client{HTTP: client},
		Hooks: Hooks{Unpack: unpackChrome, Verify: verifyChrome, Running: chromeproc.Running,
			Wait: chromeproc.Wait, Close: chromeproc.Close, Start: chromeproc.Start},
	}, nil
}

type Phase string

const (
	Downloading Phase = "download"
	Decoding    Phase = "decode"
	Unpacking   Phase = "unpack"
	Verifying   Phase = "verify"
	Diffing     Phase = "diff"
	Applying    Phase = "apply"
	Closing     Phase = "close"
	Moving      Phase = "move"
	Deleting    Phase = "delete"
)

// Step is progress within a phase. Total is 0 when unknown.
type Step struct {
	Phase       Phase
	Done, Total int64
}

func (e *Engine) report(p Phase) func(done, total int64) {
	return func(done, total int64) {
		if e.Progress != nil {
			e.Progress(Step{p, done, total})
		}
	}
}

var (
	ErrBusy       = errors.New("another copy of Pitstop is working on this install")
	ErrInstalled  = errors.New("this folder already holds a portable Chrome")
	ErrNoRollback = errors.New("no earlier version is kept")
	// ErrMilestone is returned for a rollback to an earlier milestone
	// unless it is allowed: Chrome does not convert a profile back, so
	// parts of it, such as History, may not open.
	ErrMilestone     = errors.New("the rollback goes back to an earlier Chrome milestone, which may not open the profile's newer data")
	ErrPatched       = errors.New("chrome.exe is patched with setdll to load Chrome++ Next, and replacing it would drop the patch: restore it with setdll /r chrome.exe, then install Chrome++ Next as version.dll")
	ErrChromeRunning = errors.New("Chrome is running from this install")
)

type RunningError struct {
	Processes []chromeproc.Process
	// Elevated is set when Chrome runs as administrator and only the user
	// can close it.
	Elevated bool
}

func (e *RunningError) Error() string {
	if e.Elevated {
		return "Chrome runs as administrator, so Pitstop cannot close it: exit Chrome, then try again"
	}
	return fmt.Sprintf("close Chrome first: %d programs are running from the install", len(e.Processes))
}

func (e *RunningError) Is(target error) bool { return target == ErrChromeRunning }

func (e *Engine) path(rel ...string) string {
	return filepath.Join(e.Root, filepath.FromSlash(path.Join(rel...)))
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// lock takes the install's lock, which every change holds, and finishes or
// undoes a change that a crash interrupted. Only the lock's holder may
// recover: another Engine's journal is a change in progress.
func (e *Engine) lock() (*flock.Flock, error) {
	if err := os.MkdirAll(e.path(meta), 0o755); err != nil {
		return nil, err
	}
	l := flock.New(e.path(lockFile))
	ok, err := l.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBusy
	}
	op, _, err := apply.Recover(e.Root, e.path(journal))
	if err != nil {
		l.Unlock()
		return nil, fmt.Errorf("an interrupted change (%s) could not be finished: %w", op, err)
	}
	if op != "" {
		if err := e.record(install.Event{Kind: install.Recovered, From: op}, nil); err != nil {
			l.Unlock()
			return nil, err
		}
	}
	return l, nil
}

// Busy reports whether another Engine holds the install's lock.
func (e *Engine) Busy() bool {
	l := flock.New(e.path(lockFile))
	ok, err := l.TryLock()
	if ok {
		l.Unlock()
	}
	return err == nil && !ok
}

func (e *Engine) record(event install.Event, change func(*install.State)) error {
	s, err := install.LoadState(e.Root)
	if err != nil {
		return err
	}
	if change != nil {
		change(&s)
	}
	s.Record(event)
	return install.SaveState(e.Root, s)
}

func (e *Engine) UpdateState(change func(*install.State)) error {
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	var diff bool
	err = e.change(func(s *install.State) {
		change(s)
		diff = s.DiffUpdates
	})
	if err != nil || diff {
		return err
	}
	return os.RemoveAll(e.path(baseCache))
}

func (e *Engine) change(change func(*install.State)) error {
	s, err := install.LoadState(e.Root)
	if err != nil {
		return err
	}
	change(&s)
	return install.SaveState(e.Root, s)
}

// Open describes the install, once it settled a build that Chrome swapped
// in when it restarted. Settling waits for the next change if the install
// is busy.
func (e *Engine) Open() (*install.Install, error) {
	if install.Unsettled(e.Root) {
		if l, err := e.lock(); err == nil {
			e.settle()
			l.Unlock()
		}
	}
	return install.Open(e.Root)
}

func (e *Engine) closed() error {
	ps, err := e.Hooks.Running(e.path(app))
	if err != nil {
		return err
	}
	if len(ps) > 0 {
		return &RunningError{Processes: ps}
	}
	return nil
}

type ChromeUpdate struct {
	Channel  omaha.Channel
	Arch     string
	Version  string
	Pipeline omaha.Pipeline
	// Fallback is the full pipeline of a differential Pipeline, should
	// the differential update fail.
	Fallback *omaha.Pipeline
}

func (u *ChromeUpdate) DownloadSize() int64 {
	op, _ := u.Pipeline.Download()
	return op.Size
}

// accept lists the operations Pitstop performs on a full package: it
// downloads, decodes xz streams and unpacks a CRX3 package.
var accept = []string{omaha.OpDownload, omaha.OpXZ, omaha.OpCRX3}

func (e *Engine) CheckChrome(ctx context.Context, in *install.Install) (*ChromeUpdate, error) {
	u, _, err := e.checkInstall(ctx, in, false)
	return u, err
}

// CheckChromeInBackground checks as the background mode, and also returns
// how long the update server asked for no background check.
func (e *Engine) CheckChromeInBackground(ctx context.Context, in *install.Install) (*ChromeUpdate, time.Duration, error) {
	return e.checkInstall(ctx, in, true)
}

func (e *Engine) checkInstall(ctx context.Context, in *install.Install, background bool) (*ChromeUpdate, time.Duration, error) {
	req := omaha.Request{Channel: cmp.Or(in.State.Channel, omaha.Stable), Arch: in.Arch, Version: in.Chrome, Background: background}
	if in.State.DiffUpdates {
		if base, ok := e.base(); ok {
			req.Cached = []string{base}
		}
	}
	return e.checkChrome(ctx, req)
}

func (e *Engine) checkChrome(ctx context.Context, req omaha.Request) (*ChromeUpdate, time.Duration, error) {
	req.Accept = accept
	if len(req.Cached) > 0 {
		req.Accept = append(slices.Clone(accept), omaha.OpZucc)
	}
	ans, err := e.Omaha.Check(ctx, req)
	if se, ok := errors.AsType[*omaha.StatusError](err); ok {
		return nil, se.RetryAfter, err
	}
	if err != nil {
		return nil, 0, err
	}
	u, err := e.chromeUpdate(req, ans)
	return u, ans.RetryAfter, err
}

func (e *Engine) chromeUpdate(req omaha.Request, ans *omaha.Answer) (*ChromeUpdate, error) {
	if ans.Version == "" {
		return nil, nil
	}
	u := &ChromeUpdate{Channel: req.Channel, Arch: req.Arch, Version: ans.Version}
	full, ok := ans.Pick(accept...)
	if p, found := pickDiff(ans.Pipelines, req.Cached, full, ok); found {
		u.Pipeline = p
		if ok {
			u.Fallback = &full
		}
		return u, nil
	}
	if !ok {
		return nil, fmt.Errorf("the update server offers Chrome %s in no form Pitstop can install", ans.Version)
	}
	u.Pipeline = full
	return u, nil
}

// Outcome is where an update ended up.
type Outcome int

const (
	// Installed: the build is in App and runs from Chrome's next start.
	Installed Outcome = iota
	// Staged: Chrome swaps the build in when it exits or restarts.
	Staged
	// Pending: the update waits in staging, approved, for Chrome to close.
	Pending
)

// UpdateChrome installs u in place of the install's build, which it keeps
// for a rollback. It downloads and stages u while Chrome runs. If Chrome
// still runs once u is staged, an install that opted in gets u in App for
// Chrome to swap in. Otherwise u is approved, for InstallApproved to install
// once Chrome is closed. With State.DiscardReplaced it keeps nothing. It
// returns ErrPatched, before any download, when setdll patched chrome.exe.
func (e *Engine) UpdateChrome(ctx context.Context, u *ChromeUpdate) (Outcome, error) {
	l, err := e.lock()
	if err != nil {
		return 0, err
	}
	defer l.Unlock()
	if err := e.settle(); err != nil {
		return 0, err
	}
	in, err := install.Open(e.Root)
	if err != nil {
		return 0, err
	}
	if in.Injection == install.SetDLL {
		return 0, ErrPatched
	}
	if u.Arch != in.Arch {
		return 0, fmt.Errorf("the update is for %s, and the install's Chrome is %s", u.Arch, in.Arch)
	}
	if install.CompareVersions(u.Version, in.Chrome) <= 0 {
		return 0, fmt.Errorf("Chrome %s is not newer than the installed %s", u.Version, in.Chrome)
	}
	if in.Staged != "" {
		// With Chrome closed, u goes in now rather than at its next start.
		if in.Staged == u.Version && errors.Is(e.closed(), ErrChromeRunning) {
			return Staged, nil
		}
		if err := e.unstage(in.Staged); err != nil {
			return 0, err
		}
	}
	staged, err := e.stageChrome(ctx, u)
	if err != nil {
		return 0, err
	}
	outcome, err := e.place(ctx, in, staged, u.Version, u.Channel)
	if errors.Is(err, ErrChromeRunning) {
		return Pending, e.change(func(s *install.State) { s.Approved = u.Version })
	}
	return outcome, err
}

// place puts the verified build at dir in, or hands it to Chrome.
func (e *Engine) place(ctx context.Context, in *install.Install, dir, version string, ch omaha.Channel) (Outcome, error) {
	running := e.closed()
	if errors.Is(running, ErrChromeRunning) && optedIn(in) && e.swappableInUse(dir, version) {
		if err := e.stageInUse(dir, version); err != nil {
			return 0, err
		}
		return Staged, e.change(func(s *install.State) { s.Approved = "" })
	}
	if running != nil {
		return 0, running
	}
	if err := e.swapChrome(ctx, in, dir, version, in.State.DiscardReplaced); err != nil {
		return 0, err
	}
	return Installed, e.record(install.Event{Kind: install.ChromeUpdated, From: in.Chrome, To: version},
		func(s *install.State) {
			s.Channel = ch
			s.Approved = ""
		})
}

// optedIn reports whether the install's updates finish through Chrome.
func optedIn(in *install.Install) bool {
	return in.State.Policy != install.Off && in.ManagesUpdates
}

// swappableInUse reports whether the build at dir holds only what Chrome's
// rename command swaps in: chrome.exe, chrome_proxy.exe and the version
// folder.
func (e *Engine) swappableInUse(dir, version string) bool {
	entries, err := os.ReadDir(e.path(dir))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !slices.Contains([]string{"chrome.exe", "chrome_proxy.exe", version}, entry.Name()) {
			return false
		}
	}
	return true
}

// stageInUse moves the build at from into App as Chrome's own updater
// stages one while Chrome runs: the version folder beside the running one,
// and the executables under new_ names. The marker, which tells Chrome++ to
// swap the build in, goes last.
func (e *Engine) stageInUse(from, version string) error {
	marker := staging + "/" + install.StagedMarker
	if err := os.WriteFile(e.path(marker), []byte(version), 0o644); err != nil {
		return err
	}
	tx := apply.Tx{
		Op:     fmt.Sprintf("stage Chrome %s for Chrome's restart", version),
		Moves:  []apply.Move{{From: from + "/" + version, To: app + "/" + version}},
		Remove: []string{from},
	}
	if exists(e.path(from, "chrome_proxy.exe")) {
		tx.Moves = append(tx.Moves, apply.Move{From: from + "/chrome_proxy.exe", To: app + "/new_chrome_proxy.exe"})
	}
	tx.Moves = append(tx.Moves,
		apply.Move{From: from + "/chrome.exe", To: app + "/new_chrome.exe"},
		apply.Move{From: marker, To: app + "/" + install.StagedMarker})
	e.report(Applying)(0, 0)
	return apply.Run(e.Root, e.path(journal), tx)
}

// unstage moves a build staged in App for Chrome's restart back into
// staging. The marker goes first, so that Chrome++ no longer swaps it in.
func (e *Engine) unstage(version string) error {
	to := staging + "/chrome-" + version
	if err := os.RemoveAll(e.path(to)); err != nil {
		return err
	}
	marker := staging + "/" + install.StagedMarker
	tx := apply.Tx{
		Op: fmt.Sprintf("unstage Chrome %s", version),
		Moves: []apply.Move{
			{From: app + "/" + install.StagedMarker, To: marker},
			{From: app + "/new_chrome.exe", To: to + "/chrome.exe"},
		},
		Remove: []string{marker},
	}
	if exists(e.path(app, "new_chrome_proxy.exe")) {
		tx.Moves = append(tx.Moves, apply.Move{From: app + "/new_chrome_proxy.exe", To: to + "/chrome_proxy.exe"})
	}
	tx.Moves = append(tx.Moves, apply.Move{From: app + "/" + version, To: to + "/" + version})
	return apply.Run(e.Root, e.path(journal), tx)
}

// settle finishes a swap that Chrome made when it restarted. Chrome++
// renames the running build's executables to old_ names, as Chrome's
// setup.exe does; that build moves into rollback, and the update is
// recorded. A swap that Chrome++ left half done, by crashing, is completed
// first: Chrome cannot start without chrome.exe.
func (e *Engine) settle() error {
	if !exists(e.path(app, "old_chrome.exe")) {
		return nil
	}
	if !exists(e.path(app, "chrome.exe")) {
		if err := os.Rename(e.path(app, "new_chrome.exe"), e.path(app, "chrome.exe")); err != nil {
			return err
		}
	}
	if exists(e.path(app, "new_chrome_proxy.exe")) && !exists(e.path(app, "new_chrome.exe")) {
		if exists(e.path(app, "chrome_proxy.exe")) {
			if err := os.Rename(e.path(app, "chrome_proxy.exe"), e.path(app, "old_chrome_proxy.exe")); err != nil {
				return err
			}
		}
		if err := os.Rename(e.path(app, "new_chrome_proxy.exe"), e.path(app, "chrome_proxy.exe")); err != nil {
			return err
		}
	}
	old, err := install.ChromeVersionOf(e.path(app, "old_chrome.exe"))
	if err != nil {
		return fmt.Errorf("old_chrome.exe: %w", err)
	}
	cur, err := install.ChromeVersionOf(e.path(app, "chrome.exe"))
	if err != nil {
		return fmt.Errorf("chrome.exe: %w", err)
	}
	s, err := install.LoadState(e.Root)
	if err != nil {
		return err
	}
	keep := rollback + "/chrome-" + old
	if err := os.RemoveAll(e.path(keep)); err != nil {
		return err
	}
	tx := apply.Tx{Op: fmt.Sprintf("keep Chrome %s, which Chrome %s replaced", old, cur)}
	// The folder goes first: it can't move while the old build still runs.
	if old != cur && exists(e.path(app, old)) {
		tx.Moves = append(tx.Moves, apply.Move{From: app + "/" + old, To: keep + "/" + old})
	}
	tx.Moves = append(tx.Moves, apply.Move{From: app + "/old_chrome.exe", To: keep + "/chrome.exe"})
	if exists(e.path(app, "old_chrome_proxy.exe")) {
		tx.Moves = append(tx.Moves, apply.Move{From: app + "/old_chrome_proxy.exe", To: keep + "/chrome_proxy.exe"})
	}
	tx.Remove = e.staleKept(old, s.DiscardReplaced)
	if exists(e.path(app, install.StagedMarker)) {
		tx.Remove = append(tx.Remove, app+"/"+install.StagedMarker)
	}
	if err := apply.Run(e.Root, e.path(journal), tx); err != nil {
		return err
	}
	return e.record(install.Event{Kind: install.ChromeUpdated, From: old, To: cur}, nil)
}

func (e *Engine) ChromeRollback() (string, bool) {
	for _, rel := range e.kept("chrome-") {
		v := strings.TrimPrefix(path.Base(rel), "chrome-")
		if exists(e.path(rel, "chrome.exe")) && exists(e.path(rel, v, "chrome.dll")) {
			return v, true
		}
	}
	return "", false
}

// RollbackChrome swaps the kept build back in, and keeps the current one in
// its place. Going back to an earlier milestone returns ErrMilestone unless
// acrossMilestones is set. It returns ErrPatched when
// setdll patched chrome.exe.
func (e *Engine) RollbackChrome(ctx context.Context, acrossMilestones bool) error {
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	if err := e.settle(); err != nil {
		return err
	}
	in, err := install.Open(e.Root)
	if err != nil {
		return err
	}
	if in.Injection == install.SetDLL {
		return ErrPatched
	}
	v, ok := e.ChromeRollback()
	if !ok {
		return ErrNoRollback
	}
	if Milestone(v) < Milestone(in.Chrome) && !acrossMilestones {
		return ErrMilestone
	}
	if err := e.closed(); err != nil {
		return err
	}
	// Chrome would otherwise swap the staged build in at its next start.
	if in.Staged != "" {
		if err := e.unstage(in.Staged); err != nil {
			return err
		}
	}
	if err := e.swapChrome(ctx, in, rollback+"/chrome-"+v, v, false); err != nil {
		return err
	}
	return e.record(install.Event{Kind: install.ChromeRolledBack, From: in.Chrome, To: v}, nil)
}

// DeleteChromeRollback deletes the kept build. Chrome may be running.
func (e *Engine) DeleteChromeRollback() error {
	return e.deleteKept("Chrome", e.ChromeRollback, func(v string) []string { return e.staleKept(v, true) })
}

func (e *Engine) DeleteChromePlusRollback() error {
	return e.deleteKept("Chrome++ Next", e.ChromePlusRollback, func(string) []string { return e.kept("chromeplus-") })
}

func (e *Engine) deleteKept(name string, version func() (string, bool), remove func(version string) []string) error {
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	if err := e.settle(); err != nil {
		return err
	}
	v, ok := version()
	if !ok {
		return ErrNoRollback
	}
	what := name + " " + v
	e.report(Deleting)(0, 0)
	if err := apply.Run(e.Root, e.path(journal), apply.Tx{Op: "delete the kept " + what, Remove: remove(v)}); err != nil {
		return err
	}
	return e.record(install.Event{Kind: install.KeptDeleted, To: what}, nil)
}

// KeptSizes is the disk use of what is kept, in bytes.
type KeptSizes struct{ Chrome, Plus int64 }

func (e *Engine) KeptSizes() KeptSizes {
	var k KeptSizes
	for _, rel := range e.kept("chrome-") {
		k.Chrome += folderSize(e.path(rel))
	}
	for _, rel := range e.kept("chromeplus-") {
		k.Plus += folderSize(e.path(rel))
	}
	return k
}

func folderSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func Milestone(version string) int {
	var n int
	fmt.Sscan(strings.SplitN(version, ".", 2)[0], &n)
	return n
}

func (e *Engine) kept(prefix string, except ...string) []string {
	entries, _ := os.ReadDir(e.path(rollback))
	var out []string
	for _, entry := range entries {
		rel := rollback + "/" + entry.Name()
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && !slices.Contains(except, rel) {
			out = append(out, rel)
		}
	}
	return out
}

// staleKept lists the kept builds that go once Chrome version is the one
// kept. With discard, version's goes too.
func (e *Engine) staleKept(version string, discard bool) []string {
	keep := rollback + "/chrome-" + version
	out := e.kept("chrome-", keep)
	if discard {
		out = append(out, keep)
	}
	return out
}

func (e *Engine) prune(dir string, keep func(name string) bool) error {
	entries, err := os.ReadDir(e.path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !keep(entry.Name()) {
			if err := os.RemoveAll(e.path(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageChrome downloads, unpacks and verifies u into staging and returns
// its folder, relative to the root. A build staged earlier is reused.
func (e *Engine) stageChrome(ctx context.Context, u *ChromeUpdate) (string, error) {
	name := "chrome-" + u.Version
	rel := staging + "/" + name
	if err := e.prune(staging, func(n string) bool { return n == name || strings.HasPrefix(n, "chromeplus-") }); err != nil {
		return "", err
	}
	if exists(e.path(rel, "chrome.exe")) {
		return rel, nil
	}
	crx, err := e.fetchChrome(ctx, u.Pipeline)
	if err != nil && u.Fallback != nil && ctx.Err() == nil {
		crx, err = e.fetchChrome(ctx, *u.Fallback)
	}
	if err != nil {
		return "", err
	}
	partial := e.path(rel) + ".partial"
	if err := os.RemoveAll(partial); err != nil {
		return "", err
	}
	if err := os.MkdirAll(e.path(staging), 0o755); err != nil {
		return "", err
	}
	e.report(Unpacking)(0, 0)
	version, err := e.Hooks.Unpack(ctx, crx, partial, e.report(Unpacking))
	if err == nil && version != u.Version {
		err = fmt.Errorf("the package holds Chrome %s, not %s", version, u.Version)
	}
	if err == nil {
		e.report(Verifying)(0, 0)
		err = e.Hooks.Verify(partial, version)
		if errors.Is(err, verify.ErrUnsupported) {
			err = nil // a developer's run outside Windows
		}
	}
	if err == nil {
		err = os.Rename(partial, e.path(rel))
	}
	if err != nil {
		os.RemoveAll(partial)
		return "", err
	}
	e.keepBase(crx)
	return rel, nil
}

// urls puts the update server's URLs on the user's download host first,
// read when the download starts so that a change applies to an update
// found before it.
func (e *Engine) urls(urls []string) []string {
	s, err := install.LoadState(e.Root)
	if err != nil || s.DownloadHost == "" {
		return urls
	}
	return preferHost(urls, s.DownloadHost)
}

func preferHost(urls []string, host string) []string {
	out := slices.Clone(urls)
	slices.SortStableFunc(out, func(a, b string) int {
		return cmp.Compare(otherHost(a, host), otherHost(b, host))
	})
	return out
}

func otherHost(u, host string) int {
	if p, err := url.Parse(u); err == nil && strings.EqualFold(p.Hostname(), host) {
		return 0
	}
	return 1
}

// fetchChrome downloads the package the pipeline describes into the cache,
// decoding an xz stream as it arrives, and returns the CRX3's path. Every
// step's output must match the SHA-256 the update server gave.
func (e *Engine) fetchChrome(ctx context.Context, p omaha.Pipeline) (string, error) {
	dl, ok := p.Download()
	pkg, ok2 := p.Package()
	if !ok || !ok2 || dl.Out == "" || pkg.In == "" {
		return "", fmt.Errorf("pipeline %s lacks a download or a package", p.ID)
	}
	if err := e.prune(chromeCache, func(n string) bool {
		return n == "base" || strings.HasPrefix(n, dl.Out) || strings.HasPrefix(n, pkg.In)
	}); err != nil {
		return "", err
	}
	if err := os.MkdirAll(e.path(chromeCache), 0o755); err != nil {
		return "", err
	}
	crx := e.path(chromeCache, pkg.In+".crx3")
	if ok, err := fetch.Matches(crx, pkg.Size, pkg.In); err != nil || ok {
		return crx, err
	}
	switch p.Kind() {
	case "full":
		if dl.Out != pkg.In {
			return "", fmt.Errorf("pipeline %s downloads something other than its package", p.ID)
		}
		return crx, fetch.Get(ctx, e.HTTP, fetch.File{URLs: e.urls(dl.URLs), Size: dl.Size, SHA256: dl.Out, Dest: crx}, e.report(Downloading))
	case "xz":
		xz := e.path(chromeCache, dl.Out+".xz")
		// Only one phase can be on screen: decoding, which runs alongside the
		// download, reports once the download is done.
		var downloaded atomic.Bool
		downloading, decoding := e.report(Downloading), e.report(Decoding)
		file := fetch.File{URLs: e.urls(dl.URLs), Size: dl.Size, SHA256: dl.Out, Dest: xz, Follow: func(r io.Reader) error {
			return unpack.DecodeXZ(ctx, r, crx, pkg.In, pkg.Size, func(done, total int64) {
				if downloaded.Load() {
					decoding(done, total)
				}
			})
		}}
		err := fetch.Get(ctx, e.HTTP, file, func(done, total int64) {
			downloading(done, total)
			downloaded.Store(done == total)
		})
		if err != nil {
			return "", err
		}
		decoding(pkg.Size, pkg.Size) // it may have finished before the download did
		os.Remove(xz)
		return crx, nil
	case "zucc":
		return crx, e.diffChrome(ctx, p, crx)
	}
	return "", fmt.Errorf("pipeline %s: Pitstop cannot use %q pipelines", p.ID, p.Kind())
}

func unpackChrome(ctx context.Context, crx3, dir string, progress unpack.Progress) (string, error) {
	inst, err := unpack.OpenInstaller(crx3)
	if err != nil {
		return "", err
	}
	defer inst.Close()
	return unpack.ExtractChrome(ctx, inst, dir, progress)
}

func verifyChrome(dir, version string) error {
	return verify.SignedBy(verify.Google, filepath.Join(dir, "chrome.exe"), filepath.Join(dir, version, "chrome.dll"))
}

// swapChrome moves the install's build into rollback and the build in from
// (relative to the root) into App, in one transaction. The build's files are chrome.exe, chrome_proxy.exe and the version
// folder; Chrome++, its settings and anything else in App stay. With discard,
// the replaced build goes once the transaction commits. Chrome must be closed.
func (e *Engine) swapChrome(ctx context.Context, in *install.Install, from, version string, discard bool) error {
	entries, err := os.ReadDir(e.path(from))
	if err != nil {
		return err
	}
	keep := rollback + "/chrome-" + in.Chrome
	// A folder already there can only be a copy of the build in App.
	if err := os.RemoveAll(e.path(keep)); err != nil {
		return err
	}
	tx := apply.Tx{Op: fmt.Sprintf("Chrome %s to %s", in.Chrome, version)}
	out := []string{"chrome.exe", "chrome_proxy.exe", in.Chrome}
	for _, entry := range entries {
		if !slices.Contains(out, entry.Name()) {
			out = append(out, entry.Name())
		}
	}
	for _, name := range out {
		if exists(e.path(app, name)) {
			tx.Moves = append(tx.Moves, apply.Move{From: app + "/" + name, To: keep + "/" + name})
		}
	}
	for _, entry := range entries {
		tx.Moves = append(tx.Moves, apply.Move{From: from + "/" + entry.Name(), To: app + "/" + entry.Name()})
	}
	tx.Remove = append(e.staleKept(in.Chrome, discard), from)
	e.report(Applying)(0, 0)
	return apply.Run(e.Root, e.path(journal), tx)
}

type PlusUpdate struct {
	Release *ghrelease.Release
}

func (e *Engine) CheckChromePlus(ctx context.Context, in *install.Install) (*PlusUpdate, error) {
	u, err := e.latestPlus(ctx)
	if err != nil {
		return nil, err
	}
	if in.ChromePlus != "" && ghrelease.Compare(u.Release.Tag, in.ChromePlus) <= 0 {
		return nil, nil
	}
	return u, nil
}

func (e *Engine) latestPlus(ctx context.Context) (*PlusUpdate, error) {
	r, err := e.GitHub.Latest(ctx)
	if err != nil {
		return nil, err
	}
	return &PlusUpdate{Release: r}, nil
}

func (e *Engine) stagePlus(ctx context.Context, u *PlusUpdate, arch string) (string, error) {
	name := "chromeplus-" + u.Release.Tag
	rel := staging + "/" + name
	if err := e.prune(staging, func(n string) bool { return n == name || strings.HasPrefix(n, "chrome-") }); err != nil {
		return "", err
	}
	if exists(e.path(rel, "version.dll")) {
		return rel, nil
	}
	if err := e.prune(plusCache, func(n string) bool { return strings.HasPrefix(n, u.Release.Archive.Name) }); err != nil {
		return "", err
	}
	if err := os.MkdirAll(e.path(plusCache), 0o755); err != nil {
		return "", err
	}
	archive := e.path(plusCache, u.Release.Archive.Name)
	if err := fetch.Get(ctx, e.HTTP, fetch.File{URLs: []string{u.Release.Archive.URL}, Size: u.Release.Archive.Size, SHA256: u.Release.Archive.SHA256, Dest: archive}, e.report(Downloading)); err != nil {
		return "", err
	}
	partial := e.path(rel) + ".partial"
	if err := os.RemoveAll(partial); err != nil {
		return "", err
	}
	err := os.MkdirAll(partial, 0o755)
	if err == nil {
		e.report(Unpacking)(0, 0)
		err = extractChromePlus(archive, arch, partial)
	}
	if err == nil {
		want := strings.TrimPrefix(u.Release.Tag, "v")
		if v, ok := install.ChromePlusVersionOf(filepath.Join(partial, "version.dll")); !ok || v != want {
			err = fmt.Errorf("the archive of Chrome++ Next %s holds a version.dll of %q", want, v)
		}
	}
	if err == nil {
		err = os.Rename(partial, e.path(rel))
	}
	if err != nil {
		os.RemoveAll(partial)
		return "", err
	}
	os.Remove(archive)
	return rel, nil
}

var chromePlusFiles = []string{"version.dll", "chrome++.ini"}

func extractChromePlus(archive, arch, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	r, err := sevenz.NewReader(f, st.Size())
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(archive), err)
	}
	found := 0
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(archive), err)
		}
		for _, name := range chromePlusFiles {
			if e.Name != arch+"/App/"+name {
				continue
			}
			if err := extractFile(r, filepath.Join(dir, name)); err != nil {
				return fmt.Errorf("%s: %w", e.Name, err)
			}
			found++
		}
	}
	if found != len(chromePlusFiles) {
		return fmt.Errorf("%s lacks the %s files", filepath.Base(archive), arch)
	}
	return nil
}

// extractFile writes the reader's current file to dst.
func extractFile(r *sevenz.Reader, dst string) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// UpdateChromePlus installs u in place of the install's Chrome++. It
// downloads and stages u while Chrome runs. If Chrome still runs then, u is
// approved, for InstallApprovedPlus to install once Chrome is closed: the
// processes Chrome starts later must load the Chrome++ its browser started
// with.
func (e *Engine) UpdateChromePlus(ctx context.Context, u *PlusUpdate) (Outcome, error) {
	l, err := e.lock()
	if err != nil {
		return 0, err
	}
	defer l.Unlock()
	in, err := install.Open(e.Root)
	if err != nil {
		return 0, err
	}
	if _, err := plusDLL(in); err != nil {
		return 0, err
	}
	staged, err := e.stagePlus(ctx, u, in.Arch)
	if err != nil {
		return 0, err
	}
	running := e.closed()
	if errors.Is(running, ErrChromeRunning) {
		return Pending, e.change(func(s *install.State) { s.ApprovedPlus = u.Release.Tag })
	}
	if running != nil {
		return 0, running
	}
	return Installed, e.swapPlus(in, staged, u.Release.Tag)
}

// swapPlus moves the Chrome++ in staged (relative to the root) into App,
// under the DLL name the install uses, and keeps the one it replaces for a
// rollback unless State.DiscardReplaced is set. The user's chrome++.ini
// stays; the release's is added only when App has none. Chrome must be
// closed.
func (e *Engine) swapPlus(in *install.Install, staged, tag string) error {
	dll, err := plusDLL(in)
	if err != nil {
		return err
	}
	tx := apply.Tx{Op: fmt.Sprintf("Chrome++ Next %s to %s", cmp.Or(in.ChromePlus, "none"), tag)}
	if in.ChromePlus != "" {
		keep := rollback + "/chromeplus-" + in.ChromePlus
		if err := os.RemoveAll(e.path(keep)); err != nil {
			return err
		}
		tx.Moves = append(tx.Moves, apply.Move{From: app + "/" + dll, To: keep + "/" + dll})
		tx.Remove = e.kept("chromeplus-", keep)
		if in.State.DiscardReplaced {
			tx.Remove = append(tx.Remove, keep)
		}
	}
	tx.Moves = append(tx.Moves, apply.Move{From: staged + "/version.dll", To: app + "/" + dll})
	if !exists(e.path(app, "chrome++.ini")) {
		tx.Moves = append(tx.Moves, apply.Move{From: staged + "/chrome++.ini", To: app + "/chrome++.ini"})
	}
	tx.Remove = append(tx.Remove, staged)
	e.report(Applying)(0, 0)
	if err := apply.Run(e.Root, e.path(journal), tx); err != nil {
		return err
	}
	err = e.record(install.Event{Kind: install.PlusUpdated, From: in.ChromePlus, To: tag},
		func(s *install.State) { s.ApprovedPlus = "" })
	if err != nil {
		return err
	}
	return e.syncINI()
}

// plusDLL returns the name Chrome++ has in App: the DLL chrome.exe loads.
func plusDLL(in *install.Install) (string, error) {
	switch {
	case in.ChromePlusDLL == "":
		return "version.dll", nil
	case filepath.IsAbs(in.ChromePlusDLL) || strings.ContainsAny(in.ChromePlusDLL, `/\`):
		return "", fmt.Errorf("chrome.exe loads Chrome++ Next from %s, outside App, which Pitstop does not update", in.ChromePlusDLL)
	}
	return in.ChromePlusDLL, nil
}

func (e *Engine) ChromePlusRollback() (string, bool) {
	for _, rel := range e.kept("chromeplus-") {
		if _, err := keptDLL(e.path(rel)); err == nil {
			return strings.TrimPrefix(path.Base(rel), "chromeplus-"), true
		}
	}
	return "", false
}

func keptDLL(dir string) (string, error) {
	dlls, _ := filepath.Glob(filepath.Join(dir, "*.dll"))
	if len(dlls) != 1 {
		return "", fmt.Errorf("%s holds %d DLLs", dir, len(dlls))
	}
	return filepath.Base(dlls[0]), nil
}

func (e *Engine) RollbackChromePlus(ctx context.Context) error {
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	in, err := install.Open(e.Root)
	if err != nil {
		return err
	}
	v, ok := e.ChromePlusRollback()
	if !ok || in.ChromePlus == "" {
		return ErrNoRollback
	}
	dll, err := plusDLL(in)
	if err != nil {
		return err
	}
	from := rollback + "/chromeplus-" + v
	old, err := keptDLL(e.path(from))
	if err != nil {
		return err
	}
	if err := e.closed(); err != nil {
		return err
	}
	keep := rollback + "/chromeplus-" + in.ChromePlus
	if err := os.RemoveAll(e.path(keep)); err != nil {
		return err
	}
	tx := apply.Tx{
		Op: fmt.Sprintf("Chrome++ Next %s back to %s", in.ChromePlus, v),
		Moves: []apply.Move{
			{From: app + "/" + dll, To: keep + "/" + dll},
			{From: from + "/" + old, To: app + "/" + dll},
		},
		Remove: append(e.kept("chromeplus-", keep, from), from),
	}
	e.report(Applying)(0, 0)
	if err := apply.Run(e.Root, e.path(journal), tx); err != nil {
		return err
	}
	if err := e.record(install.Event{Kind: install.PlusRolledBack, From: in.ChromePlus, To: v}, nil); err != nil {
		return err
	}
	return e.syncINI()
}

type SetupOptions struct {
	Channel omaha.Channel
	Arch    string
	// Target pins Chrome to a version prefix, such as "156." or
	// "156.0.8078.48".
	Target string
	Policy install.Policy
}

// Setup installs Chrome and Chrome++ into the root, which must not hold a
// portable Chrome yet, in the layout of Chrome++'s release archive: App,
// Data and Cache. A Data folder already there is kept as it is.
func (e *Engine) Setup(ctx context.Context, o SetupOptions) error {
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	if exists(e.path(app)) {
		return ErrInstalled
	}
	u, _, err := e.checkChrome(ctx, omaha.Request{Channel: o.Channel, Arch: o.Arch, Target: o.Target})
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("the update server offers no %s Chrome for %s", o.Channel, o.Arch)
	}
	plus, err := e.latestPlus(ctx)
	if err != nil {
		return err
	}
	chrome, err := e.stageChrome(ctx, u)
	if err != nil {
		return err
	}
	plusDir, err := e.stagePlus(ctx, plus, o.Arch)
	if err != nil {
		return err
	}
	tx := apply.Tx{
		Op: fmt.Sprintf("set up Chrome %s with Chrome++ Next %s", u.Version, plus.Release.Tag),
		Moves: []apply.Move{
			{From: chrome, To: app},
			{From: plusDir + "/version.dll", To: app + "/version.dll"},
			{From: plusDir + "/chrome++.ini", To: app + "/chrome++.ini"},
		},
		Remove: []string{plusDir},
	}
	e.report(Applying)(0, 0)
	if err := apply.Run(e.Root, e.path(journal), tx); err != nil {
		return err
	}
	for _, dir := range []string{"Data", "Cache"} {
		if err := os.MkdirAll(e.path(dir), 0o755); err != nil {
			return err
		}
	}
	err = e.record(install.Event{Kind: install.Installed, To: u.Version + " + Chrome++ Next " + plus.Release.Tag},
		func(s *install.State) {
			s.Channel = o.Channel
			s.Policy = o.Policy
		})
	if err != nil {
		return err
	}
	return e.syncINI()
}
