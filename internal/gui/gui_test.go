package gui

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/Bush2021/pitstop/internal/chromeproc"
	"github.com/Bush2021/pitstop/internal/ini"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/options"
	"github.com/Bush2021/pitstop/internal/pitstop"
	"github.com/Bush2021/pitstop/internal/pitstop/pitstoptest"
	"github.com/Bush2021/pitstop/locales"
)

// harness runs the window in a tester, with the work's goroutines posting
// to a queue the test drains, as the window's Update would run them.
type harness struct {
	t       *testing.T
	s       *pitstoptest.Servers
	engine  *pitstop.Engine
	a       *App
	tt      *ui.Tester
	posts   chan func()
	running []chromeproc.Process
	// picked is what a file dialog answers.
	picked string
	opened string
	// started is the program Chrome was last started from.
	started      string
	updateChecks int
}

func newHarness(t *testing.T, versions ...string) *harness {
	h := &harness{t: t, s: pitstoptest.NewServers(t, versions...), posts: make(chan func(), 4096)}
	engine, err := pitstop.New(filepath.Join(t.TempDir(), "Chrome"), h.s.Client())
	if err != nil {
		t.Fatal(err)
	}
	engine.Omaha.Endpoint = h.s.Endpoint()
	engine.GitHub.Base = h.s.URL
	engine.Hooks.Unpack = pitstoptest.Unpack(t)
	// The fake builds are unsigned, which Windows would refuse.
	engine.Hooks.Verify = func(string, string) error { return nil }
	engine.Hooks.Running = func(string) ([]chromeproc.Process, error) { return h.running, nil }
	engine.Hooks.Close = func(string) error {
		h.running = nil
		return nil
	}
	engine.Hooks.Wait = func(context.Context, string) error { return nil }
	engine.Hooks.Start = func(exe string) error {
		h.started = exe
		return nil
	}
	h.engine = engine
	h.a = New(engine, "0.1.0", h.host())
	h.tt = ui.NewTester(h.a.View, 1000, 1000)
	return h
}

func (h *harness) host() Host {
	return Host{
		Post:            func(fn func()) { h.posts <- fn },
		SetLanguage:     func(string) {},
		SetTheme:        func(string) {},
		PickFolder:      func(string, string) string { return h.picked },
		PickProgram:     func(string, string) string { return h.picked },
		OpenFolder:      func(path string) { h.opened = path },
		CheckForUpdates: func() { h.updateChecks++ },
	}
}

// settle runs what the work posts until it is done, then draws a frame.
func (h *harness) settle() {
	h.t.Helper()
	deadline := time.After(30 * time.Second)
	for h.a.busy || len(h.posts) > 0 {
		select {
		case fn := <-h.posts:
			fn()
		case <-deadline:
			h.t.Fatal("the work did not finish")
		}
	}
	h.frame()
}

// answer runs what a file dialog posts once the user picked something.
func (h *harness) answer() {
	h.t.Helper()
	select {
	case fn := <-h.posts:
		fn()
	case <-time.After(5 * time.Second):
		h.t.Fatal("no answer from the dialog")
	}
	h.frame()
}

func (h *harness) click(label string) {
	h.t.Helper()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatalf("%v; the window shows %q", err, h.tt.Texts())
	}
	h.frame()
}

// frame draws what changed and lets it finish moving, as the tester's clock
// stands still.
func (h *harness) frame() {
	h.tt.Frame()
	h.tt.Advance(time.Second)
}

func (h *harness) want(texts ...string) {
	h.t.Helper()
	for _, s := range texts {
		if !h.tt.HasText(s) {
			h.t.Errorf("the window does not show %q; it shows %q", s, h.tt.Texts())
		}
	}
}

// shot saves the window as a PNG when PITSTOP_SHOTS names a folder.
func (h *harness) shot(name string) {
	dir := os.Getenv("PITSTOP_SHOTS")
	if dir == "" {
		return
	}
	// The tester's clock stands still: let switches finish sliding.
	h.tt.Advance(time.Second)
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, h.tt.Image())
}

func (h *harness) setUp() {
	h.t.Helper()
	h.want("No portable Chrome here")
	h.shot("setup")
	h.click("Set up")
	h.settle()
	if h.a.in == nil {
		h.t.Fatalf("not installed: %v %v", h.a.openErr, h.a.opErr)
	}
}

func TestSetUpUpdateAndSwitchBack(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.want("Google Chrome 156.0.8078.48", "Chrome++ Next 1.18.3", "Loaded as version.dll", "Up to date", "Set up Chrome 156.0.8078.48 + Chrome++ Next 1.18.3")

	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.want("156.0.8078.74 is available")
	h.shot("overview-update")
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.want("Google Chrome 156.0.8078.74", "Chrome 156.0.8078.48 is kept", "Updated Chrome 156.0.8078.48 to 156.0.8078.74")
	h.shot("overview-kept")

	h.click("Switch to 156.0.8078.48")
	h.want("Switch to 156.0.8078.48?", "156.0.8078.74 is kept in its place, so you can switch back.")
	h.click("Switch")
	h.settle()
	h.want("Google Chrome 156.0.8078.48", "Chrome 156.0.8078.74 is kept", "Switched Chrome 156.0.8078.74 to 156.0.8078.48")
}

func TestClearActivity(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.click("Clear")
	h.want("Clear recent activity?")
	h.shot("clear-activity")
	h.click("Cancel")
	h.want("Updated Chrome 156.0.8078.48 to 156.0.8078.74")

	h.click("Clear")
	h.click("Clear all")
	h.want("Nothing yet.")
	if slices.Contains(h.tt.Texts(), "Clear") {
		t.Error("Clear stayed on an empty list")
	}
	s, err := install.LoadState(h.engine.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.History) != 0 {
		t.Fatalf("history after removal: %+v", s.History)
	}
}

func TestRefreshShowsBackgroundWork(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.want("Update to 156.0.8078.74")

	if _, err := h.engine.UpdateChrome(context.Background(), h.a.chromeUpdate); err != nil {
		t.Fatal(err)
	}
	in, err := h.engine.Open()
	if err != nil {
		t.Fatal(err)
	}
	h.a.refresh(in, h.engine.KeptSizes())
	h.frame()
	h.want("Google Chrome 156.0.8078.74", "Up to date")
}

func TestUpdateWaitsForChromeToClose(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.running = []chromeproc.Process{{PID: 7, Path: `D:\Chrome\App\chrome.exe`}}
	h.a.running = true
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.want("Google Chrome 156.0.8078.48", "156.0.8078.74 will be applied after you close Chrome",
		"Google Chrome 156.0.8078.74 goes in after you close Chrome")
	if h.tt.HasText("Close Chrome to finish") {
		t.Error("asked to close Chrome")
	}
	h.shot("pending")
	h.click("Restart and update")
	h.want("Restart Chrome?")
	h.click("Restart")
	h.settle()
	h.want("Google Chrome 156.0.8078.74")
	if h.started == "" || h.a.opErr != nil {
		t.Errorf("started %q, error %v", h.started, h.a.opErr)
	}
}

// The window installs what waits once it sees Chrome closed, which without
// the background mode nothing else does.
func TestWaitingChromePlusGoesInWhenChromeCloses(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	pitstoptest.WriteChromePlus(t, filepath.Join(h.a.in.App(), "version.dll"), "1.18.2")
	h.a.reload()
	h.click("Check for updates")
	h.settle()
	h.running = []chromeproc.Process{{PID: 7, Path: `D:\Chrome\App\chrome.exe`}}
	h.a.running = true
	h.click("Update to 1.18.3")
	h.settle()
	h.want("Chrome++ Next 1.18.2", "1.18.3 will be applied after you close Chrome", "Restart and update")
	h.shot("pending-plus")
	h.running = nil
	h.a.setRunning(false)
	h.settle()
	h.want("Chrome++ Next 1.18.3", "Chrome++ Next 1.18.3 is ready")
	if h.a.opErr != nil {
		t.Errorf("error %v", h.a.opErr)
	}
}

func TestUpdateWhileChromeRuns(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(h.a.in.App(), "version.dll"), "1.19.0")
	if err := h.engine.SetPolicy(install.Ask); err != nil {
		t.Fatal(err)
	}
	h.a.reload()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.running = []chromeproc.Process{{PID: 7, Path: `D:\Chrome\App\chrome.exe`}}
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.want("Google Chrome 156.0.8078.48", "156.0.8078.74 is ready and goes in when Chrome restarts",
		"Google Chrome 156.0.8078.74 goes in when Chrome restarts")
	if h.tt.HasText("Close Chrome to finish") {
		t.Error("asked to close Chrome")
	}
	h.shot("staged")
}

func TestSwitchBackAcrossMilestones(t *testing.T) {
	h := newHarness(t, "155.0.8059.85")
	h.setUp()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.want("Google Chrome 156.0.8078.74")

	h.click("Switch to 155.0.8059.85")
	h.want("Chrome doesn't convert a profile back")
	h.shot("switch-milestone")
}

func TestDeleteKeptVersion(t *testing.T) {
	h := newHarness(t, "155.0.8059.85")
	h.setUp()
	os.WriteFile(filepath.Join(h.a.in.Root, "Data", "Local State"), []byte("{}"), 0o644)
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.want("Chrome 155.0.8059.85 is kept", mb(h.a.kept.Chrome))
	h.shot("overview-delete")

	h.click("Delete")
	h.want("Delete Chrome 155.0.8059.85?", locales.DeleteBody())
	h.shot("delete-chrome")
	// The dialog's button comes after the row's, which the dialog covers.
	cancel, _ := h.tt.Find("Cancel")
	h.tt.ClickAt(cancel.X+cancel.W+30, cancel.Y+cancel.H/2)
	h.frame()
	h.settle()
	h.want("Deleted the kept Chrome 155.0.8059.85", "None yet. Each update keeps the version it replaces.")
	if h.a.kept != (pitstop.KeptSizes{}) {
		t.Errorf("kept %+v", h.a.kept)
	}
}

func TestKeepSwitch(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	h.click("Settings")
	h.want("Keep earlier versions")
	h.shot("settings-keep")
	h.a.setKeep(false)
	if !h.a.in.State.DiscardReplaced || h.a.chromeUpdate == nil {
		t.Fatalf("after turning it off: state %+v, update %+v", h.a.in.State, h.a.chromeUpdate)
	}
	h.click("Overview")
	h.want("Updates don't keep the version they replace.")
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.want("Google Chrome 156.0.8078.74", "Updates don't keep the version they replace.")
}

func readINI(t *testing.T, h *harness) *ini.File {
	t.Helper()
	f, err := ini.Load(filepath.Join(h.a.in.App(), "chrome++.ini"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEditReviewAndSave(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Tabs & mouse")
	h.want("Closing tabs", "Double-click a tab to close it")
	h.click("Activate a tab by resting the pointer on it")
	h.want("Hover delay")
	h.click("Background tab") // the first of the two choices: the address bar's
	h.want("2 unsaved changes")
	h.shot("tabs-pending")

	h.click("Review")
	h.want("Review changes to chrome++.ini")
	h.shot("review")
	h.click("Save")
	h.tt.Frame()
	if h.tt.HasText("unsaved") {
		t.Error("the pending bar stayed after saving")
	}
	f := readINI(t, h)
	if v, _ := f.Get("tabs", "hover_tab"); v != "1" {
		t.Errorf("hover_tab = %q", v)
	}
	if v, _ := f.Get("tabs", "open_url_new_tab"); v != "2" {
		t.Errorf("open_url_new_tab = %q", v)
	}
	if f.Encoding() != ini.UTF16LE {
		t.Errorf("saved as %v", f.Encoding())
	}
	in, _ := install.Open(h.engine.Root)
	if in.State.History[0].Kind != install.SettingsSaved {
		t.Errorf("history starts with %+v", in.State.History[0])
	}
}

func TestDiscard(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Advanced")
	h.click("Allow win32k")
	h.want("1 unsaved change")
	h.click("Discard")
	if h.tt.HasText("unsaved") {
		t.Error("Discard kept the change")
	}
}

func TestSaveAsksWhenTheFileChanged(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Advanced")
	h.click("Ignore enterprise policies")
	path := filepath.Join(h.a.in.App(), "chrome++.ini")
	other := ini.Parse(readINI(t, h).Bytes())
	other.Set("general", "command_line", "--from-elsewhere")
	if err := other.Save(path); err != nil {
		t.Fatal(err)
	}
	h.click("Save")
	h.want("chrome++.ini changed")
	h.click("Reload")
	if v, _ := h.a.draft.Get("general", "command_line"); v != "--from-elsewhere" || h.a.pending() != 0 {
		t.Errorf("after Reload: command_line=%q, %d pending", v, h.a.pending())
	}
}

func TestKeyMapping(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Keyboard")
	h.want("No mappings yet.")
	mappings := func(want ...ini.KeyValue) {
		t.Helper()
		if got := h.a.draft.Keys("keymapping"); !slices.Equal(got, want) {
			t.Errorf("keymapping = %v, want %v", got, want)
		}
	}

	h.click("Add a mapping")
	h.want("Press a shortcut")
	h.tt.Key(0, ui.KeyEscape)
	h.want("No mappings yet.")
	mappings()

	h.click("Add a mapping")
	h.tt.Key(0, ui.KeyF2)
	h.want("F2", locales.InvalidTarget(), "Press a shortcut")
	if !h.a.invalid() {
		t.Error("a mapping without a target can be saved")
	}
	h.tt.Key(0, ui.KeyA)
	mappings(ini.KeyValue{Key: "F2", Value: "A"})

	h.click("Command")
	h.want("Chrome commands", "All command IDs on GitHub")
	h.shot("commands")
	h.tt.Type("新建标签")
	h.tt.Frame()
	h.want("New tab")
	h.tt.Key(0, ui.KeyEnter)
	mappings(ini.KeyValue{Key: "F2", Value: "command:34014"})
	h.want("New tab")
	if h.a.invalid() {
		t.Errorf("a valid mapping can't be saved: %v", h.a.draft.Keys("keymapping"))
	}
	h.shot("keyboard")
	h.click("Save")
	if kv := readINI(t, h).Keys("keymapping"); len(kv) != 1 || kv[0] != (ini.KeyValue{Key: "F2", Value: "command:34014"}) {
		t.Errorf("keymapping = %v", kv)
	}

	h.click("Remove")
	h.click("Save")
	if kv := readINI(t, h).Keys("keymapping"); len(kv) != 0 {
		t.Errorf("keymapping = %v", kv)
	}
}

func TestCommandNotInTheList(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.a.draft.SetKeys("keymapping", []ini.KeyValue{{Key: "F2", Value: "command:"}})
	h.click("Keyboard")
	h.click("Choose a command")
	h.tt.Type("40250")
	h.tt.Frame()
	h.click("Use command 40250")
	h.want("Command 40250")
	if v := h.a.draft.Keys("keymapping")[0].Value; v != "command:40250" {
		t.Errorf("target = %q", v)
	}
}

func TestHotkeyRecorder(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Keyboard")
	boss := func() string {
		v, _ := h.a.draft.Get("general", "boss_key")
		return v
	}
	before := boss()

	h.click("Boss key")
	h.want("Press a shortcut")
	h.tt.Key(0, ui.KeyB)
	h.want("Add Ctrl, Alt or Win")
	if boss() != before {
		t.Errorf("a plain letter was taken: %q", boss())
	}
	h.tt.Key(0, ui.KeyEscape)
	if boss() != before || h.a.recording != "" {
		t.Errorf("Esc: boss_key %q, recording %q", boss(), h.a.recording)
	}

	h.click("Boss key")
	h.tt.Key(ui.Ctrl|ui.Shift, ui.KeyK)
	if boss() != "Ctrl+Shift+K" || h.a.recording != "" {
		t.Errorf("boss_key %q, recording %q", boss(), h.a.recording)
	}
	h.want("Ctrl", "Shift", "K")

	h.click("Boss key")
	h.tt.Key(0, ui.KeyF9)
	if boss() != "F9" {
		t.Errorf("a function key needs no modifier, got %q", boss())
	}

	h.click("Clear")
	if boss() != "" {
		t.Errorf("after Clear: %q", boss())
	}
	h.want("Not set")
}

func TestHotkeyRecorderTakesTab(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Keyboard")
	h.click("Boss key")
	h.tt.Key(0, ui.KeyTab)
	h.want("Add Ctrl, Alt or Win")
	if !h.a.Recording() {
		t.Error("Tab ended the recording")
	}
	h.tt.Key(ui.Ctrl, ui.KeyTab)
	if v, _ := h.a.draft.Get("general", "boss_key"); v != "Ctrl+Tab" {
		t.Errorf("boss_key %q", v)
	}
}

func TestHotkeyNotes(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Keyboard")
	h.click("Boss key")
	h.tt.Key(ui.Ctrl, ui.KeyJ)
	h.frame()
	h.want(locales.HotkeyChrome(locales.CmdDownloads()))

	h.click(locales.OptTranslateKey())
	h.tt.Key(ui.Ctrl, ui.KeyJ)
	h.frame()
	h.want(locales.HotkeyUsedBy(locales.OptBossKey()), locales.HotkeyUsedBy(locales.OptTranslateKey()))
	h.shot("hotkey-notes")

	h.a.draft.Set("general", "translate_key", "")
	h.a.draft.Set("keymapping", "Ctrl+Shift+J", "F12")
	h.a.draft.Set("general", "boss_key", "J+Shift+Control")
	h.frame()
	h.want(locales.HotkeyUsedBy(locales.KeymapTitle()))
}

func TestHotkeyRecorderKnowsOnlyKeysChromePlusReads(t *testing.T) {
	if len(keyNames) != 74 {
		t.Errorf("%d key names", len(keyNames))
	}
	for key, name := range keyNames {
		if !options.ValidHotkey(name) {
			t.Errorf("key %v is named %q, which Chrome++ can't read", key, name)
		}
	}
	for key, want := range map[ui.Key]string{ui.KeyF12: "F12", ui.Key9: "9", ui.KeyZ: "Z"} {
		if keyNames[key] != want {
			t.Errorf("key %v is named %q, not %q", key, keyNames[key], want)
		}
	}
}

func TestBossKeyIsChecked(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.a.draft.Set("general", "boss_key", "Ctrl+Foo")
	h.click("Keyboard")
	h.want(locales.InvalidHotkey())
	if !h.a.invalid() {
		t.Error("an unknown key can be saved")
	}
}

func TestFolderModes(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Startup")
	folder := func() string {
		v, _ := h.a.draft.Get("general", "data_dir")
		return v
	}
	// The profile folder's row comes first, and h.click takes the first match.
	h.click("Chrome's default")
	if folder() != "none" {
		t.Errorf("data_dir = %q", folder())
	}
	h.want(locales.OptDataDirNote())
	h.shot("startup")

	h.click("Portable")
	if folder() != `%app%\..\Data` {
		t.Errorf("data_dir = %q", folder())
	}

	// Canceling the dialog leaves the folder as it was.
	h.click("Custom")
	if h.tt.HasText("Browse") {
		t.Error("custom before a folder was picked")
	}

	h.picked = filepath.Join(h.engine.Root, "Profiles")
	h.click("Custom")
	h.answer()
	if folder() != `%app%\..\Profiles` {
		t.Errorf("data_dir = %q", folder())
	}
	h.want("Browse")
}

func TestFileWithoutByteOrderMarkRefusesNonASCII(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.a.saved = ini.Parse([]byte("[general]\r\ndata_dir=\r\n"))
	h.a.draft = ini.Parse(h.a.saved.Bytes())
	h.click("Startup")
	h.picked = `D:\用户数据`
	h.click("Custom")
	h.answer()
	h.want(locales.NonASCII())
	if v, _ := h.a.draft.Raw("general", "data_dir"); v != "" {
		t.Errorf("data_dir = %q", v)
	}
	if h.a.invalid() {
		t.Error("a refused value keeps the other edits from being saved")
	}
	h.click(locales.FolderChrome())
	if h.tt.HasText(locales.NonASCII()) {
		t.Error("the error stayed")
	}
}

func TestSwitchList(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Startup")
	value := func() string {
		v, _ := h.a.draft.Get("general", "command_line")
		return v
	}
	h.click("Add a switch")
	h.want("Common switches", "All switches on GitHub")
	h.tt.Type("no-first")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	if value() != "--no-first-run" {
		t.Fatalf("command_line = %q", value())
	}

	// A switch that takes a value goes in with the focus on it, to edit.
	h.click("Add a switch")
	h.tt.Type("语言")
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Key(ui.Ctrl, ui.KeyA)
	h.tt.Type("--lang=en-GB")
	h.tt.Frame()
	if value() != "--no-first-run --lang=en-GB" {
		t.Fatalf("command_line = %q", value())
	}

	h.click("Add a switch")
	h.tt.Type("--my-own-switch")
	h.tt.Frame()
	h.click("Add --my-own-switch")
	if !strings.HasSuffix(value(), " --my-own-switch") {
		t.Fatalf("command_line = %q", value())
	}
}

func TestCommandListHidesThePolicysCommand(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(h.a.in.App(), "version.dll"), "1.19.0")
	if err := h.engine.SetPolicy(install.Ask); err != nil {
		t.Fatal(err)
	}
	h.a.reload()
	background, _ := h.a.draft.Get("general", "launch_on_startup")
	h.click("Startup")

	h.picked = filepath.Join(h.a.in.App(), "tools", "my.exe")
	// launch_on_startup comes first.
	h.click("Add a program")
	h.answer()
	value, _ := h.a.draft.Get("general", "launch_on_startup")
	want := `"%app%\tools\my.exe";` + background
	if value != want {
		t.Fatalf("launch_on_startup = %s, want %s", value, want)
	}
	h.click("Remove")
	if value, _ := h.a.draft.Get("general", "launch_on_startup"); value != background {
		t.Fatalf("after Remove: %s", value)
	}
	if h.tt.HasText("Remove") {
		t.Error("the list shows Pitstop's own command")
	}
}

func TestDependentOptions(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Tabs & mouse")
	if h.tt.HasText("Hover delay") || h.tt.HasText("Stay in the current tab when it's a new tab") {
		t.Fatal("options that do nothing yet show")
	}
	h.click("Activate a tab by resting the pointer on it")
	h.want("Hover delay")
	h.click("Background tab")
	h.want("Stay in the current tab when it's a new tab", "Also count these tab titles as a new tab")
	h.shot("tabs-open")

	h.click("Also count these tab titles as a new tab")
	h.tt.Type("Speed Dial")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if v, _ := h.a.draft.Raw("tabs", "new_tab_disable_name"); !strings.HasSuffix(v, `"Speed Dial"`) {
		t.Errorf("new_tab_disable_name = %s", v)
	}

	h.click("Find a setting")
	h.tt.Type("hover")
	h.tt.Frame()
	h.want("Hover delay")
}

func TestAbout(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("About")
	h.want("Version 0.1.0", "GPL-3.0-or-later", locales.AboutBody(), locales.AboutPitstopHelp(), locales.AboutPlusHelp())
	h.shot("about")
	h.click("Check for updates")
	if h.updateChecks != 1 {
		t.Fatalf("self-update checks = %d", h.updateChecks)
	}
}

func TestOverviewOpensTheInstallFolder(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.want("Portable folder", h.engine.Root)
	h.click("Open")
	if h.opened != h.engine.Root {
		t.Errorf("opened %q", h.opened)
	}
}

func TestStartAndRestartChrome(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Start Chrome")
	if want := filepath.Join(h.a.in.App(), "chrome.exe"); h.started != want {
		t.Fatalf("started %q, want %q", h.started, want)
	}
	h.want("Restart Chrome")

	h.click("Advanced")
	h.click("Ignore enterprise policies")
	h.click("Overview")
	h.running = []chromeproc.Process{{PID: 1}}
	h.started = ""
	h.click("Restart Chrome")
	h.want("Restart Chrome?")
	h.click("Restart")
	h.settle()
	if h.started == "" || h.a.opErr != nil {
		t.Errorf("started %q, error %v", h.started, h.a.opErr)
	}
	if h.a.pending() != 1 {
		t.Errorf("%d edits left after the restart", h.a.pending())
	}
}

func TestStartChromeShowsAFailure(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.engine.Hooks.Start = func(string) error { return os.ErrNotExist }
	h.click("Start Chrome")
	if h.a.running || h.a.opErr == nil {
		t.Errorf("running %v, error %v", h.a.running, h.a.opErr)
	}
}

// moveSetUp leaves an install with user data and a draft that points away
// from it, to a folder that returns.
func moveSetUp(t *testing.T) (*harness, string) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	if err := os.MkdirAll(filepath.Join(h.engine.Root, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.engine.Root, "Data", "Local State"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "Profile")
	h.a.draft.Set("general", "data_dir", to)
	h.frame()
	return h, to
}

func TestSavingANewDataFolderMovesTheData(t *testing.T) {
	h, to := moveSetUp(t)
	h.running = []chromeproc.Process{{PID: 1}}
	h.a.running = true
	h.click("Save")
	h.want("Move your profile?", "Pitstop closes it first")
	h.shot("move")
	h.click("Move")
	h.settle()
	if h.a.opErr != nil {
		t.Fatal(h.a.opErr)
	}
	if _, err := os.Stat(filepath.Join(to, "Local State")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(h.engine.Root, "Data")); err == nil {
		t.Error("the old folder is still there")
	}
	if v, _ := readINI(t, h).Get("general", "data_dir"); v != to || h.a.pending() != 0 {
		t.Errorf("data_dir=%q, %d pending", v, h.a.pending())
	}
	h.want(locales.DoneMoved(to))
}

func TestChromeThatStaysLeavesTheMoveUndone(t *testing.T) {
	h, to := moveSetUp(t)
	h.engine.Hooks.Close = func(string) error { return nil }
	h.running = []chromeproc.Process{{PID: 1}}
	h.a.running = true
	h.click("Save")
	h.click("Move")
	h.settle()
	if !errors.Is(h.a.opErr, pitstop.ErrChromeRunning) || h.a.retry == nil {
		t.Errorf("error %v", h.a.opErr)
	}
	if v, _ := readINI(t, h).Get("general", "data_dir"); v == to || h.a.pending() != 1 {
		t.Errorf("data_dir=%q, %d pending", v, h.a.pending())
	}
}

func TestElevatedChromeAsksTheUserToExitIt(t *testing.T) {
	h, _ := moveSetUp(t)
	h.engine.Hooks.Close = func(string) error { return chromeproc.ErrElevated }
	h.running = []chromeproc.Process{{PID: 1}}
	h.a.running = true
	h.click("Save")
	h.click("Move")
	h.settle()
	h.want(locales.RunningTitle(), locales.RunningElevatedBody())
}

func TestNotMovingTheDataStillSaves(t *testing.T) {
	h, to := moveSetUp(t)
	h.click("Save")
	h.want("Move your profile?", "Chrome starts with an empty profile")
	h.click("Don't Move")
	if v, _ := readINI(t, h).Get("general", "data_dir"); v != to || h.a.pending() != 0 {
		t.Errorf("data_dir=%q, %d pending", v, h.a.pending())
	}
	if _, err := os.Stat(filepath.Join(h.engine.Root, "Data", "Local State")); err != nil {
		t.Error(err)
	}
}

func TestEscapeAtTheMoveCancelsTheSave(t *testing.T) {
	h, to := moveSetUp(t)
	h.click("Save")
	h.tt.Key(0, ui.KeyEscape)
	h.frame()
	if v, _ := readINI(t, h).Get("general", "data_dir"); v == to || h.a.pending() != 1 {
		t.Errorf("data_dir=%q, %d pending", v, h.a.pending())
	}
}

func TestAFolderInUseIsNotOfferedTheMove(t *testing.T) {
	h, to := moveSetUp(t)
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "Local State"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.click("Save")
	if h.a.moveOpen || h.a.pending() != 0 {
		t.Errorf("asked: %v, %d pending", h.a.moveOpen, h.a.pending())
	}
}

func TestThemeSetting(t *testing.T) {
	h := newHarness(t)
	var set []string
	h.a.SetTheme = func(name string) { set = append(set, name) }
	h.click("Settings")
	h.want("Appearance")
	h.click("Dark")
	h.click("Light")
	if !slices.Equal(set, []string{ThemeDark, ThemeLight}) || h.a.Theme != ThemeLight {
		t.Errorf("chose %q, the app holds %q", set, h.a.Theme)
	}
}

func TestSearchFindsOptionsInBothLanguages(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Find a setting")
	h.tt.Type("滚轮")
	h.want("Results for “滚轮”", "Scroll over the tab strip to switch tabs", "Scroll anywhere while holding the right button to switch tabs")
	h.shot("search")
}

func TestRawEditor(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Advanced")
	h.click("Edit chrome++.ini as text")
	text := h.a.raw.Buffer.String()
	h.a.raw.Buffer.Set(strings.Replace(text, "hover_tab=0", "hover_tab=1", 1))
	h.tt.Frame()
	h.shot("raw")
	h.click("Done")
	if v, _ := h.a.draft.Get("tabs", "hover_tab"); v != "1" || h.a.pending() != 1 {
		t.Errorf("after editing the text: hover_tab=%q, %d pending", v, h.a.pending())
	}
}

func TestSidebarLeavesRawEditor(t *testing.T) {
	for _, target := range []struct{ label, page string }{
		{"Overview", pageOverview},
		{"Tabs & mouse", pageTabs},
		{"Keyboard", pageKeyboard},
		{"Startup", pageStartup},
		{"Settings", pageSettings},
		{"About", pageAbout},
	} {
		t.Run(target.page, func(t *testing.T) {
			h := newHarness(t, "156.0.8078.74")
			h.setUp()
			before := onDisk(t, h.a.iniPath)
			h.click("Advanced")
			h.click("Edit chrome++.ini as text")
			text := strings.Replace(h.a.raw.Buffer.String(), "hover_tab=0", "hover_tab=1", 1)
			h.a.raw.Buffer.Set(text)
			h.frame()
			h.click(target.label)
			if h.a.raw != nil || h.a.page != target.page || h.tt.HasText("Done") {
				t.Fatalf("editor open: %v; page %q, want %q", h.a.raw != nil, h.a.page, target.page)
			}
			if v, _ := h.a.draft.Get("tabs", "hover_tab"); v != "1" || h.a.pending() != 1 {
				t.Errorf("draft hover_tab=%q, %d pending", v, h.a.pending())
			}
			if onDisk(t, h.a.iniPath) != before {
				t.Error("navigation saved the draft")
			}
			h.click("Advanced")
			h.click("Edit chrome++.ini as text")
			if h.a.raw.Buffer.String() != text {
				t.Error("reopening the editor lost the text")
			}
		})
	}
}

func TestSidebarKeepsRawEditorOnEncodingError(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	const before = "[tabs]\r\nhover_tab=0\r\n"
	if err := os.WriteFile(h.a.iniPath, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	h.a.loadINI()
	h.frame()
	h.click("Advanced")
	h.click("Edit chrome++.ini as text")
	text := h.a.raw.Buffer.String() + "; \u4e2d\u6587\n"
	h.a.raw.Buffer.Set(text)
	h.frame()
	h.click("Settings")
	if h.a.raw == nil || h.a.page != pageAdvanced || !errors.Is(h.a.rawErr, ini.ErrNonASCII) {
		t.Fatalf("editor open: %v; page %q; error %v", h.a.raw != nil, h.a.page, h.a.rawErr)
	}
	h.want(h.a.rawErr.Error(), "Done")
	if h.a.raw.Buffer.String() != text || onDisk(t, h.a.iniPath) != before || h.a.pending() != 0 {
		t.Error("failed navigation changed the text, file or draft")
	}
	h.a.raw.Buffer.Set(strings.Replace(before, "hover_tab=0", "hover_tab=1", 1))
	h.frame()
	h.click("Settings")
	if h.a.raw != nil || h.a.page != pageSettings || h.a.rawErr != nil || h.a.pending() != 1 {
		t.Fatalf("after correction: editor open %v; page %q; error %v; %d pending", h.a.raw != nil, h.a.page, h.a.rawErr, h.a.pending())
	}
	h.want("Appearance")
}

func onDisk(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hoverTab(t *testing.T, h *harness) string {
	t.Helper()
	v, _ := readINI(t, h).Get("tabs", "hover_tab")
	return v
}

func TestCtrlSSaves(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	before := onDisk(t, h.a.iniPath)
	h.tt.Key(ui.Ctrl, ui.KeyS)
	if after := onDisk(t, h.a.iniPath); after != before {
		t.Error("Ctrl+S wrote a file with nothing to save")
	}

	h.a.set("tabs", "hover_tab", "1")
	h.frame()
	h.tt.Key(ui.Ctrl, ui.KeyS)
	if v := hoverTab(t, h); v != "1" || h.a.pending() != 0 {
		t.Errorf("after Ctrl+S: hover_tab=%q, %d pending", v, h.a.pending())
	}
}

func TestCtrlSInTheTextEditorStaysThere(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Advanced")
	h.click("Edit chrome++.ini as text")
	text := h.a.raw.Buffer.String()

	h.a.raw.Buffer.Set(strings.Replace(text, "hover_tab=0", "hover_tab=1", 1))
	h.frame()
	h.tt.Key(ui.Ctrl, ui.KeyS)
	if v := hoverTab(t, h); h.a.raw == nil || v != "1" {
		t.Fatalf("editor open: %v; hover_tab=%q", h.a.raw != nil, v)
	}
	saved, good := onDisk(t, h.a.iniPath), h.a.raw.Buffer.String()

	h.a.raw.Buffer.Set(strings.Replace(good, "boss_key=", "boss_key=Ctrl+Foo", 1))
	h.frame()
	h.tt.Key(ui.Ctrl, ui.KeyS)
	h.want("Fix the invalid shortcut or key mapping to save.")
	if onDisk(t, h.a.iniPath) != saved {
		t.Error("an invalid mapping was saved")
	}

	h.a.raw.Buffer.Set(good)
	h.frame()
	h.click("Done")
	if h.a.raw != nil || h.a.pending() != 0 {
		t.Errorf("after Done: editor open %v, %d pending", h.a.raw != nil, h.a.pending())
	}
}

func TestCtrlSInTheTextEditorKeepsAFileThatChanged(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Advanced")
	h.click("Edit chrome++.ini as text")
	h.a.raw.Buffer.Set(strings.Replace(h.a.raw.Buffer.String(), "hover_tab=0", "hover_tab=1", 1))
	h.frame()
	other, err := ini.Load(h.a.iniPath)
	if err != nil {
		t.Fatal(err)
	}
	other.Set("tabs", "hover_tab", "2")
	if err := other.Save(h.a.iniPath); err != nil {
		t.Fatal(err)
	}
	h.tt.Key(ui.Ctrl, ui.KeyS)
	h.click("Reload")
	if v := hoverTab(t, h); h.a.raw != nil || v != "2" {
		t.Errorf("after Reload: editor open %v, hover_tab=%q", h.a.raw != nil, v)
	}
}

func TestCtrlSInTheTextEditorMovesTheData(t *testing.T) {
	h, to := moveSetUp(t)
	h.click("Advanced")
	h.click("Edit chrome++.ini as text")
	h.tt.Key(ui.Ctrl, ui.KeyS)
	h.click("Move")
	h.settle()
	if h.a.opErr != nil {
		t.Fatal(h.a.opErr)
	}
	if _, err := os.Stat(filepath.Join(to, "Local State")); err != nil {
		t.Error(err)
	}
	if h.a.raw != nil || h.a.page != pageOverview {
		t.Errorf("editor open %v, page %q", h.a.raw != nil, h.a.page)
	}
}

func TestRecordingTakesTheWindowsShortcuts(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Keyboard")
	before := onDisk(t, h.a.iniPath)
	for key, want := range map[ui.Key]string{ui.KeyS: "Ctrl+S", ui.KeyF: "Ctrl+F"} {
		h.click("Boss key")
		h.tt.Key(ui.Ctrl, key)
		if v, _ := h.a.draft.Get("general", "boss_key"); v != want {
			t.Errorf("boss_key %q, want %q", v, want)
		}
	}
	if onDisk(t, h.a.iniPath) != before {
		t.Error("recording Ctrl+S saved the file")
	}
}

func TestCtrlFGoesToTheSearch(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.tt.Key(ui.Ctrl, ui.KeyF)
	h.tt.Type("滚轮")
	h.want("Results for “滚轮”")
}

func TestMissingINI(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	os.Remove(filepath.Join(h.a.in.App(), "chrome++.ini"))
	h.a.reload()
	h.click("Startup")
	h.want("chrome++.ini is missing")
	h.click("Create chrome++.ini")
	h.want("Profile folder")
	if _, err := os.Stat(filepath.Join(h.a.in.App(), "chrome++.ini")); err != nil {
		t.Error(err)
	}
}

func TestChannel(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Settings")
	h.click("Beta")
	in, _ := install.Open(h.engine.Root)
	if in.State.Channel != "beta" {
		t.Errorf("channel %q", in.State.Channel)
	}
}

func TestChannelKeepsUnsavedEdits(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Tabs & mouse")
	h.click("Activate a tab by resting the pointer on it")
	h.click("Settings")
	h.click("Beta")
	h.want("1 unsaved change")
}

func TestPolicy(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	h.click("Settings")
	h.want("Chrome channel")
	h.want("Update mode", "Needs Chrome++ Next 1.19.0 or later.")
	h.shot("settings-old-plus")
	h.click("Automatically")
	if h.a.in.State.Policy != install.Off {
		t.Fatalf("Chrome++ 1.18.3 took policy %q", h.a.in.State.Policy)
	}
	h.click("Overview")
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(h.a.in.App(), "version.dll"), "1.19.0")
	if err := h.engine.SetPolicy(install.Ask); err != nil {
		t.Fatal(err)
	}
	h.a.reload()
	in, _ := install.Open(h.engine.Root)
	if v, ok := readINI(t, h).Get("general", "launch_on_startup"); !ok || !strings.Contains(v, "--background") || in.State.Policy != install.Ask {
		t.Fatalf("after setup: launch_on_startup=%s, policy %q", v, in.State.Policy)
	}
	// An unsaved edit survives the policy's change to the file.
	h.click("Tabs & mouse")
	h.click("Activate a tab by resting the pointer on it")
	h.want("1 unsaved change")
	h.click("Settings")
	h.want("Update mode", "How often to check")
	h.shot("settings")
	h.click("Daily")
	if h.a.in.State.CheckHours != 24 {
		t.Fatalf("checks every %d hours", h.a.in.State.CheckHours)
	}
	h.click("Only when I check")
	h.want("1 unsaved change")
	in, _ = install.Open(h.engine.Root)
	if v, _ := readINI(t, h).Get("general", "launch_on_startup"); v != "" || in.State.Policy != install.Off {
		t.Fatalf("under Off: launch_on_startup=%s, policy %q", v, in.State.Policy)
	}
	h.click("Review")
	h.click("Save")
	h.tt.Frame()
	f := readINI(t, h)
	if v, _ := f.Get("tabs", "hover_tab"); v != "1" {
		t.Errorf("hover_tab = %q", v)
	}
	if v, _ := f.Get("general", "launch_on_startup"); v != "" {
		t.Errorf("saving the draft brought back launch_on_startup=%s", v)
	}
	if v, _ := f.Get("general", "managed_updates"); v != "0" {
		t.Errorf("managed_updates = %q", v)
	}
	h.want("Update mode")
	if h.tt.HasText("How often to check") {
		t.Error("the interval shows under Off")
	}
}

func TestSetupChoosesThePolicy(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.want("Update mode", "Only when I check")
	h.shot("setup-policy")
	if h.a.setupPolicy != install.Off {
		t.Fatalf("preselected %q", h.a.setupPolicy)
	}
	h.click("Ask before each update")
	h.click("Set up")
	h.settle()
	if in, _ := install.Open(h.engine.Root); in.State.Policy != install.Ask {
		t.Fatalf("policy %q", in.State.Policy)
	}
}

func TestWaitingUpdateCanBeSkipped(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.engine.UpdateState(func(s *install.State) { s.Policy, s.Approved = install.Ask, "156.0.8078.74" })
	h.a.reload()
	h.a.running = true
	h.tt.Frame()
	h.want("156.0.8078.74 will be applied after you close Chrome")
	h.shot("waiting")
	h.click("Skip this version")
	in, _ := install.Open(h.engine.Root)
	if in.State.Approved != "" || !slices.Contains(in.State.Skipped, "156.0.8078.74") {
		t.Fatalf("approved %q, skipped %v", in.State.Approved, in.State.Skipped)
	}
}

// The background mode failed to install an approved build once Chrome
// closed: the window keeps the failure, and installs the build itself.
func TestFailedBackgroundUpdateCanBeInstalled(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.engine.UpdateState(func(s *install.State) {
		s.Policy, s.Approved = install.Ask, "156.0.8078.74"
		s.Failure = &install.Failure{Time: time.Now(), What: "Chrome", Error: "the download broke off"}
	})
	h.a.reload()
	h.tt.Frame()
	h.want("Pitstop couldn't update Chrome", "the download broke off", "156.0.8078.74 isn't applied yet")
	h.shot("background-failed")
	h.click("Update now")
	h.settle()
	h.want("Google Chrome 156.0.8078.74")
	if slices.Contains(h.tt.Texts(), "Pitstop couldn't update Chrome") {
		t.Error("the failure stays after the update")
	}
	if in, _ := install.Open(h.engine.Root); in.State.Approved != "" || in.State.Failure != nil {
		t.Errorf("approved %q, failure %+v", in.State.Approved, in.State.Failure)
	}
}

func TestBackgroundFailureCanBeDismissed(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.engine.UpdateState(func(s *install.State) {
		s.Failure = &install.Failure{Time: time.Now(), What: "Chrome++ Next", Error: "boom"}
	})
	h.a.reload()
	h.tt.Frame()
	h.want("Pitstop couldn't update Chrome++ Next")
	h.click("Dismiss")
	if in, _ := install.Open(h.engine.Root); in.State.Failure != nil || slices.Contains(h.tt.Texts(), "boom") {
		t.Errorf("failure %+v", in.State.Failure)
	}
}

func TestBackgroundWorkShowsProgress(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	h.a.elsewhere = true
	h.tt.Frame()
	h.want("Pitstop is updating in the background")
	h.shot("background-busy")
}

func TestWindowRepairsTheOptIn(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(h.a.in.App(), "version.dll"), "1.19.0")
	h.engine.UpdateState(func(s *install.State) { s.Policy = install.Ask })
	New(h.engine, "0.1.0", h.host())
	if v, _ := readINI(t, h).Get("general", "managed_updates"); v != "1" {
		t.Fatalf("managed_updates = %q", v)
	}
}

// A change made to chrome++.ini elsewhere survives a policy change: the
// window takes it up when it has no unsaved edits, and asks before saving
// over it when it has.
func TestPolicyKeepsAChangeMadeElsewhere(t *testing.T) {
	for _, edited := range []bool{false, true} {
		h := newHarness(t, "156.0.8078.74")
		h.setUp()
		pitstoptest.WriteManagedChromePlus(t, filepath.Join(h.a.in.App(), "version.dll"), "1.19.0")
		if err := h.engine.SetPolicy(install.Ask); err != nil {
			t.Fatal(err)
		}
		h.a.reload()
		if edited {
			h.click("Tabs & mouse")
			h.click("Activate a tab by resting the pointer on it")
		}
		other := readINI(t, h)
		other.Set("general", "command_line", "--from-elsewhere")
		if err := other.Save(filepath.Join(h.a.in.App(), "chrome++.ini")); err != nil {
			t.Fatal(err)
		}
		h.click("Settings")
		h.click("Only when I check")
		if !edited {
			if v, _ := h.a.draft.Get("general", "command_line"); v != "--from-elsewhere" || h.a.pending() != 0 {
				t.Errorf("without edits: command_line=%q, %d pending", v, h.a.pending())
			}
			continue
		}
		h.want("1 unsaved change")
		h.click("Review")
		h.click("Save")
		h.want("chrome++.ini changed")
	}
}

func TestChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(nil)
	h := newHarness(t, "156.0.8078.74")
	h.tt.SetLocale("zh-Hans")
	h.want("这里还没有便携版 Chrome")
	h.click("部署")
	h.settle()
	h.want("已是最新", "以 version.dll 加载")
	h.shot("zh-overview")
	h.click("标签页与鼠标")
	h.want("关闭标签页", "鼠标停留在标签页上时激活它")
	h.shot("zh-tabs")
}

func TestDark(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.tt.SetDark(true)
	h.setUp()
	h.shot("dark-overview")
	h.click("Tabs & mouse")
	h.shot("dark-tabs")
}

func TestOneCheckButton(t *testing.T) {
	h := newHarness(t, "156.0.8078.74")
	h.setUp()
	// A window opened on the install has not checked yet.
	h.a = New(h.engine, "0.1.0", h.host())
	h.tt = ui.NewTester(h.a.View, 1000, 1000)
	buttons := func() (n int) {
		for _, s := range h.tt.Texts() {
			if s == "Check for updates" {
				n++
			}
		}
		return n
	}
	if n := buttons(); n != 1 {
		t.Errorf("before a check, the window shows %d Check for updates buttons", n)
	}
	h.click("Check for updates")
	h.settle()
	if n := buttons(); n != 1 {
		t.Errorf("after a check, the window shows %d Check for updates buttons", n)
	}
}

func TestSidebarShowsTheMaterial(t *testing.T) {
	h := newHarness(t)
	about, ok := h.tt.Find("About")
	if !ok {
		t.Fatalf("no About in %q", h.tt.Texts())
	}
	// The list is empty below its last item.
	x, y := int(about.X+about.W/2), int(about.Y+about.H+40)
	if c := h.tt.Image().RGBAAt(x, y); c.A != 255 {
		t.Fatalf("without the material, the sidebar is see-through at %d,%d: %v", x, y, c)
	}
	h.tt.SetVibrancy(true)
	if c := h.tt.Image().RGBAAt(x, y); c.A != 0 {
		t.Errorf("the sidebar covers the material at %d,%d with %v", x, y, c)
	}
}

func TestPatchedChromeIsLeftAlone(t *testing.T) {
	h := newHarness(t, "156.0.8078.48")
	h.setUp()
	pitstoptest.PatchWithSetDLL(t, h.a.in.App(), "156.0.8078.48")
	h.a.reload()
	h.s.Versions = append(h.s.Versions, "156.0.8078.74")
	h.click("Check for updates")
	h.settle()
	downloads := h.s.Gets.Load()
	h.click("Update to 156.0.8078.74")
	h.settle()
	h.want(locales.PlusSetDLL("version-x64.dll"), locales.PatchedBody(), "Google Chrome 156.0.8078.48")
	if h.s.Gets.Load() != downloads {
		t.Error("downloaded an update it won't install")
	}
	h.shot("patched")
}

func TestLanguageSetting(t *testing.T) {
	h := newHarness(t)
	var set []string
	h.a.SetLanguage = func(tag string) { set = append(set, tag) }
	h.click("Settings")
	h.want("Language", "System default", "English", "简体中文")
	h.click("简体中文")
	if !slices.Equal(set, []string{"zh-Hans"}) || h.a.Language != "zh-Hans" {
		t.Errorf("chose %q, the app holds %q", set, h.a.Language)
	}
	h.a.query = "语言"
	h.tt.Advance(time.Millisecond)
	h.want("Language")
}
