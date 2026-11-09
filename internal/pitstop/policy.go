package pitstop

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Bush2021/pitstop/internal/defaultini"
	"github.com/Bush2021/pitstop/internal/ghrelease"
	"github.com/Bush2021/pitstop/internal/ini"
	"github.com/Bush2021/pitstop/internal/install"
)

type Action int

const (
	// Nothing: the policy is Off, or the user skipped the version.
	Nothing Action = iota
	// Ask the user, who may approve the version or skip it.
	Ask
	// Get the version.
	Get
)

func (a Action) String() string { return [...]string{"nothing", "ask", "get"}[a] }

// Conditions are what the computer allows now.
type Conditions struct {
	Now     time.Time
	Metered bool
}

// Decide applies the policy to a version the background mode found, with
// approved the version of that product the user approved. On a metered
// network a download of hundreds of MB is the user's call.
func Decide(s install.State, approved, version string, metered bool) Action {
	switch {
	case s.Policy == install.Off, slices.Contains(s.Skipped, version):
		return Nothing
	case version == approved, s.Policy == install.Automatic && !metered:
		return Get
	}
	return Ask
}

// InstallApproved installs, while Chrome is closed, the build Chrome left
// staged in App or else the approved one, from what is in staging, and
// returns its version: "" when none went in. An approval waits for its
// download; one that the install's Chrome reached goes.
func (e *Engine) InstallApproved(ctx context.Context) (string, error) {
	l, err := e.lock()
	if err != nil {
		return "", err
	}
	defer l.Unlock()
	return e.installApproved(ctx)
}

func (e *Engine) installApproved(ctx context.Context) (string, error) {
	if err := e.settle(); err != nil {
		return "", err
	}
	in, err := install.Open(e.Root)
	if err != nil {
		return "", err
	}
	version := cmp.Or(in.Staged, in.State.Approved)
	if version == "" {
		return "", nil
	}
	if err := e.closed(); err != nil {
		return "", err
	}
	if install.CompareVersions(version, in.Chrome) <= 0 {
		return "", e.change(func(s *install.State) { s.Approved = "" })
	}
	if in.Staged != "" {
		if err := e.unstage(in.Staged); err != nil {
			return "", err
		}
	}
	dir := staging + "/chrome-" + version
	if !exists(e.path(dir, "chrome.exe")) {
		return "", nil
	}
	outcome, err := e.place(ctx, in, dir, version, in.State.Channel)
	if err != nil || outcome != Installed {
		return "", err
	}
	return version, nil
}

// InstallApprovedPlus installs the approved Chrome++ while Chrome is closed,
// from what is in staging, and returns its version: "" when none went in. An
// approval waits for its download; one that the install's Chrome++ reached
// goes.
func (e *Engine) InstallApprovedPlus(context.Context) (string, error) {
	l, err := e.lock()
	if err != nil {
		return "", err
	}
	defer l.Unlock()
	return e.installApprovedPlus()
}

func (e *Engine) installApprovedPlus() (string, error) {
	in, err := install.Open(e.Root)
	if err != nil {
		return "", err
	}
	tag := in.State.ApprovedPlus
	if tag == "" {
		return "", nil
	}
	if err := e.closed(); err != nil {
		return "", err
	}
	if in.ChromePlus != "" && ghrelease.Compare(tag, in.ChromePlus) <= 0 {
		return "", e.change(func(s *install.State) { s.ApprovedPlus = "" })
	}
	dir := staging + "/chromeplus-" + tag
	if !exists(e.path(dir, "version.dll")) {
		return "", nil
	}
	return tag, e.swapPlus(in, dir, tag)
}

// Downloaded reports whether the approved Chrome and Chrome++ wait in
// staging, where a failed download leaves nothing.
func (e *Engine) Downloaded(in *install.Install) (chrome, plus bool) {
	return exists(e.path(staging, "chrome-"+in.State.Approved, "chrome.exe")),
		exists(e.path(staging, "chromeplus-"+in.State.ApprovedPlus, "version.dll"))
}

const (
	launchOnStartup = "launch_on_startup"
	managedUpdates  = install.ManagedUpdatesKey
)

// SetPolicy sets the update policy. Off first takes a staged build out, as
// Chrome would otherwise run a rename command that Chrome++ no longer honors.
func (e *Engine) SetPolicy(p install.Policy) error {
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	in, err := install.Open(e.Root)
	if err != nil {
		return err
	}
	if p == install.Off && in.Staged != "" {
		if err := e.unstage(in.Staged); err != nil {
			return err
		}
	}
	s := in.State
	s.Policy = p
	if p == install.Off {
		s.Approved, s.ApprovedPlus = "", ""
	}
	if err := install.SaveState(e.Root, s); err != nil {
		return err
	}
	return e.syncINI()
}

// SyncINI brings chrome++.ini in line with the state, which decides what
// Pitstop's keys hold.
func (e *Engine) SyncINI() error {
	if !exists(e.path(app)) {
		return nil
	}
	l, err := e.lock()
	if err != nil {
		return err
	}
	defer l.Unlock()
	return e.syncINI()
}

func (e *Engine) syncINI() error {
	in, err := install.Open(e.Root)
	if err != nil {
		return err
	}
	on := optedIn(in)
	path := e.path(app, "chrome++.ini")
	f, err := ini.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		if !on {
			return nil
		}
		f, err = ini.Parse(defaultini.Bytes), nil
	}
	if err != nil {
		return err
	}
	before := bytes.Clone(f.Bytes())
	if err := e.SetOptIn(f, on); err != nil {
		return err
	}
	if bytes.Equal(before, f.Bytes()) {
		return nil
	}
	return f.Save(path)
}

// SetOptIn writes what the opt-in means to f: Chrome++ starts Pitstop's
// background mode with Chrome and honors a staged build.
func (e *Engine) SetOptIn(f *ini.File, on bool) error {
	if err := e.setBackground(f, on); err != nil {
		return err
	}
	if _, ok := f.Raw("general", managedUpdates); !ok && !on {
		return nil
	}
	v := "0"
	if on {
		v = "1"
	}
	return f.Set("general", managedUpdates, v)
}

// setBackground adds the command that starts Pitstop's background mode to
// f's launch_on_startup, or takes it out. The user's own commands there
// stay. Each command ends in a semicolon, as chrome++.ini asks: Chrome++
// reads the line through GetPrivateProfileStringW, which drops the outer
// quotes of a value that starts and ends with one.
func (e *Engine) setBackground(f *ini.File, on bool) error {
	v, _ := f.Raw("general", launchOnStartup)
	var line strings.Builder
	var had, want string
	for c := range strings.SplitSeq(v, ";") {
		switch c = strings.TrimSpace(c); {
		case e.IsBackground(c):
			had = c
		case c != "":
			line.WriteString(c + ";")
		}
	}
	if on {
		want = e.backgroundCommand()
		line.WriteString(want + ";")
	}
	if had == want {
		return nil
	}
	return f.Set("general", launchOnStartup, line.String())
}

// backgroundCommand names pitstop.exe through %app%, which Chrome++
// expands, when it sits in the install, so that the folder can move. cmd
// waits for the programs it runs, so start keeps it from staying around as
// long as Pitstop does.
func (e *Engine) backgroundCommand() string {
	if strings.EqualFold(filepath.Dir(e.Exe), e.Root) {
		return `start "" "%app%\..\` + filepath.Base(e.Exe) + `" --background`
	}
	return `start "" "` + e.Exe + `" -root "%app%\.." --background`
}

// IsBackground reports whether cmd starts a Pitstop's background mode,
// wherever that Pitstop is.
func (e *Engine) IsBackground(cmd string) bool {
	cmd = strings.TrimPrefix(cmd, `start "" `)
	prog, args, _ := strings.Cut(cmd, " ")
	if strings.HasPrefix(cmd, `"`) {
		prog, args, _ = strings.Cut(cmd[1:], `"`)
	}
	return strings.EqualFold(filepath.Base(prog), filepath.Base(e.Exe)) && slices.Contains(strings.Fields(args), "--background")
}
