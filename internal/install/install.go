// Package install describes a portable Chrome with Chrome++: a folder that
// keeps the layout of Chrome++'s release archive.
//
//	D:\Chrome\
//	  App\           chrome.exe, chrome_proxy.exe, version.dll, chrome++.ini
//	    156.0.8078.74\
//	  Data\  Cache\
//	  .pitstop\      Pitstop's state for this install
package install

import (
	"bytes"
	"cmp"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/chrome_update/pefile"
)

type Injection string

const (
	None Injection = ""
	// VersionDLL: Chrome++ is App\version.dll, which Windows loads in place
	// of the system's.
	VersionDLL Injection = "version.dll"
	// SetDLL: chrome.exe was patched with setdll to import Chrome++, so a
	// new chrome.exe loses it.
	SetDLL Injection = "setdll"
)

type Install struct {
	Root string
	// Chrome is the version of App\chrome.exe; ChromePlus the release of
	// Chrome++ ("" without one).
	Chrome, ChromePlus string
	Arch               string
	Injection          Injection
	// ChromePlusDLL is the file Chrome++ is in: a name in App, or the full
	// path setdll was given.
	ChromePlusDLL string
	// ManagesUpdates reports whether that Chrome++ reads ManagedUpdatesKey.
	ManagesUpdates bool
	// Staged is the version of the build waiting in App for Chrome to swap
	// in when it restarts ("" without one).
	Staged string
	// Unsettled reports whether Pitstop has yet to keep or remove the build
	// that Chrome swapped out.
	Unsettled bool
	State     State
}

// Unsettled reports whether the build Chrome swapped out, as old_chrome.exe,
// is still in App.
func Unsettled(root string) bool { return isFile(filepath.Join(root, AppDir, "old_chrome.exe")) }

const (
	AppDir  = "App"
	MetaDir = ".pitstop"
)

func (i *Install) App() string { return filepath.Join(i.Root, AppDir) }

func (i *Install) Meta() string { return Meta(i.Root) }

func Meta(root string) string { return filepath.Join(root, MetaDir) }

var ErrNotInstalled = errors.New("no portable Chrome here")

const chromePlusDescription = "Chrome++ Next"

func Open(root string) (*Install, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	in := &Install{Root: root}
	exe := filepath.Join(in.App(), "chrome.exe")
	f, err := os.Open(exe)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s lacks App\\chrome.exe", ErrNotInstalled, root)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pf, err := pe.NewFile(f)
	if err != nil {
		return nil, fmt.Errorf("chrome.exe: %w", err)
	}
	if in.Arch, err = pefile.Arch(pf); err != nil {
		return nil, fmt.Errorf("chrome.exe: %w", err)
	}
	vi, err := pefile.ReadVersionInfo(f, pf)
	if err != nil {
		return nil, fmt.Errorf("chrome.exe: %w", err)
	}
	in.Chrome = trimVersion(vi.File)
	if st, err := os.Stat(filepath.Join(in.App(), in.Chrome, "chrome.dll")); err != nil || st.IsDir() {
		return nil, fmt.Errorf("App\\%s\\chrome.dll is missing: the install is incomplete", in.Chrome)
	}
	in.Staged = stagedVersion(in.App())
	in.Unsettled = Unsettled(root)

	imports, err := pefile.ImportedDLLs(f, pf)
	if err != nil {
		return nil, fmt.Errorf("chrome.exe imports: %w", err)
	}
	if v, ok := chromePlusVersion(filepath.Join(in.App(), "version.dll")); ok {
		in.ChromePlus, in.Injection, in.ChromePlusDLL = v, VersionDLL, "version.dll"
		in.ManagesUpdates = managesUpdates(filepath.Join(in.App(), "version.dll"))
	}
	for _, dll := range imports {
		if strings.EqualFold(dll, "version.dll") {
			continue
		}
		p := dll
		if !filepath.IsAbs(p) {
			p = filepath.Join(in.App(), dll)
		}
		if v, ok := chromePlusVersion(p); ok {
			in.ChromePlus, in.Injection, in.ChromePlusDLL = v, SetDLL, dll
			in.ManagesUpdates = managesUpdates(p)
		}
	}
	in.State, err = LoadState(root)
	return in, err
}

func trimVersion(v pefile.Version) string {
	return fmt.Sprintf("%d.%d.%d.%d", v[0], v[1], v[2], v[3])
}

// ChromeVersionOf returns the file version of a Chrome executable, such as
// the old_chrome.exe that Chrome leaves once it swapped a build in.
func ChromeVersionOf(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	pf, err := pe.NewFile(f)
	if err != nil {
		return "", err
	}
	vi, err := pefile.ReadVersionInfo(f, pf)
	if err != nil {
		return "", err
	}
	return trimVersion(vi.File), nil
}

// StagedMarker is the file in App that names the build staged there the way
// Chrome's own updater stages one while Chrome runs: new_chrome.exe,
// new_chrome_proxy.exe and the version folder. Chrome++ Next swaps that
// build in when Chrome restarts, but only while the marker names it, which
// tells it apart from a build Google's updater staged.
const StagedMarker = "chrome++.staged"

func stagedVersion(app string) string {
	b, err := os.ReadFile(filepath.Join(app, StagedMarker))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !isVersion(v) || !isFile(filepath.Join(app, "new_chrome.exe")) || !isFile(filepath.Join(app, v, "chrome.dll")) {
		return ""
	}
	return v
}

// isVersion reports whether v is four numbers, as a Chrome version is: one
// that also names a folder in App.
func isVersion(v string) bool {
	parts := strings.Split(v, ".")
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return len(parts) == 4
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func chromePlusVersion(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	pf, err := pe.NewFile(f)
	if err != nil {
		return "", false
	}
	vi, err := pefile.ReadVersionInfo(f, pf)
	if err != nil || vi.Strings["FileDescription"] != chromePlusDescription {
		return "", false
	}
	if v := vi.Strings["ProductVersion"]; v != "" {
		return v, true
	}
	return fmt.Sprintf("%d.%d.%d", vi.Product[0], vi.Product[1], vi.Product[2]), true
}

func ChromePlusVersionOf(path string) (string, bool) { return chromePlusVersion(path) }

// ManagedUpdatesKey is the chrome++.ini key that opts in to updates staged
// while Chrome runs.
const ManagedUpdatesKey = "managed_updates"

var wideManagedUpdatesKey, _ = binary.Append(nil, binary.LittleEndian, utf16.Encode([]rune(ManagedUpdatesKey)))

// managesUpdates looks for the wide literal Chrome++ reads the key by. The
// version can't tell: an alpha build names its commit, without the changes
// on top of it, so two builds of one name can differ.
func managesUpdates(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, wideManagedUpdatesKey)
}

func CompareVersions(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(ap), len(bp)) {
		var x, y int
		if i < len(ap) {
			fmt.Sscan(ap[i], &x)
		}
		if i < len(bp) {
			fmt.Sscan(bp[i], &y)
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// State is what Pitstop keeps about an install, in
// .pitstop\state.json, so that it travels with the folder.
type State struct {
	// Channel is the Chrome channel the install follows; empty until
	// Pitstop installs or the user says.
	Channel omaha.Channel `json:"channel,omitempty"`
	// Policy is how updates start. Choosing anything but Off is the user's
	// opt-in to updates that finish through Chrome's own "Relaunch to
	// update". Empty is Off.
	Policy Policy `json:"policy,omitempty"`
	// Skipped lists the Chrome and Chrome++ versions the user chose to skip.
	Skipped []string `json:"skipped,omitempty"`
	// CheckHours is how often the background mode checks; 0 is
	// DefaultCheckHours.
	CheckHours int `json:"checkHours,omitempty"`
	// Checked is when the background mode last got an answer from both
	// update sources.
	Checked time.Time `json:"checked,omitzero"`
	// Quiet is until when Google's update server asked for no background
	// check.
	Quiet time.Time `json:"quiet,omitzero"`
	// Approved is the Chrome version to install once Chrome exits, and
	// ApprovedPlus the Chrome++ one.
	Approved     string `json:"approved,omitempty"`
	ApprovedPlus string `json:"approvedPlus,omitempty"`
	// DiscardReplaced has an update delete what it replaces. A switch
	// between versions still keeps the current one.
	DiscardReplaced bool `json:"discardReplaced,omitempty"`
	// DiffUpdates has Chrome updates keep their package, which Google's
	// server can then patch into the next one.
	DiffUpdates bool `json:"diffUpdates,omitempty"`
	// DownloadHost is the server Chrome downloads try first; empty keeps
	// the update server's order.
	DownloadHost string `json:"downloadHost,omitempty"`
	// Failure is the background mode's last failed update, until an update
	// goes in or the user dismisses it.
	Failure *Failure `json:"failure,omitempty"`
	History []Event  `json:"history,omitempty"`
}

type Failure struct {
	Time  time.Time `json:"time"`
	What  string    `json:"what"`
	Error string    `json:"error"`
	// Trouble and Host are pitstop.Explain's reading of the error.
	Trouble string `json:"trouble,omitempty"`
	Host    string `json:"host,omitempty"`
}

// CheckEvery is how long the background mode waits between checks.
func (s State) CheckEvery() time.Duration {
	return time.Duration(cmp.Or(s.CheckHours, DefaultCheckHours)) * time.Hour
}

// Skip keeps version from being offered again.
func (s *State) Skip(version string) {
	if !slices.Contains(s.Skipped, version) {
		s.Skipped = append(s.Skipped, version)
	}
	if s.Approved == version {
		s.Approved = ""
	}
	if s.ApprovedPlus == version {
		s.ApprovedPlus = ""
	}
}

type Policy string

const (
	Automatic Policy = "automatic"
	Ask       Policy = "ask"
	Off       Policy = ""
)

var Policies = []Policy{Automatic, Ask, Off}

const DefaultCheckHours = 3

var CheckHours = []int{1, DefaultCheckHours, 24}

// DownloadHosts are the servers Google's update server links Chrome
// downloads to, in its usual order.
var DownloadHosts = []string{"edgedl.me.gvt1.com", "dl.google.com"}

type Event struct {
	Time time.Time `json:"time"`
	Kind EventKind `json:"kind"`
	From string    `json:"from,omitempty"`
	To   string    `json:"to,omitempty"`
}

type EventKind string

const (
	Installed        EventKind = "installed"
	ChromeUpdated    EventKind = "chromeUpdated"
	ChromeRolledBack EventKind = "chromeRolledBack"
	PlusUpdated      EventKind = "chromePlusUpdated"
	PlusRolledBack   EventKind = "chromePlusRolledBack"
	// KeptDeleted: the user deleted a kept version; To names it.
	KeptDeleted   EventKind = "keptDeleted"
	SettingsSaved EventKind = "settingsSaved"
	// Recovered: an interrupted change was undone, or finished once it had
	// committed; From describes it.
	Recovered EventKind = "recovered"
)

const (
	historyLimit = 50
	stateFile    = "state.json"
)

// LoadState reads the state of the install at root; a missing file is an
// empty state.
func LoadState(root string) (State, error) {
	var s State
	b, err := os.ReadFile(filepath.Join(Meta(root), stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("%s: %w", stateFile, err)
	}
	return s, nil
}

// StateTime returns when the state last changed, zero without one.
func StateTime(root string) time.Time {
	st, err := os.Stat(filepath.Join(Meta(root), stateFile))
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// Record adds an event, newest first, keeping the last ones.
func (s *State) Record(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	switch e.Kind {
	case Installed, ChromeUpdated, PlusUpdated:
		s.Failure = nil
	}
	s.History = slices.Insert(s.History, 0, e)
	if len(s.History) > historyLimit {
		s.History = s.History[:historyLimit]
	}
}

func SaveState(root string, s State) error {
	if err := os.MkdirAll(Meta(root), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(Meta(root), stateFile), b)
}

// WriteFileAtomic replaces path with b, through a temporary file in the
// same folder, so that readers see the old or the new contents.
func WriteFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
