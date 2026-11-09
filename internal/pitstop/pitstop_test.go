package pitstop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"

	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/pitstop/internal/apply"
	"github.com/Bush2021/pitstop/internal/chromeproc"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/pitstop/pitstoptest"
)

const plusFixture = "../../testdata/chromeplus-1.18.3-x64.dll"

func newEngine(t *testing.T, s *pitstoptest.Servers) *Engine {
	t.Helper()
	engine, err := New(filepath.Join(t.TempDir(), "Chrome"), s.Client())
	if err != nil {
		t.Fatal(err)
	}
	engine.Omaha.Endpoint = s.Endpoint()
	engine.GitHub.Base = s.URL
	engine.Hooks.Unpack = pitstoptest.Unpack(t)
	// The fake builds are unsigned, which Windows would refuse.
	engine.Hooks.Verify = func(string, string) error { return nil }
	return engine
}

func setup(t *testing.T, engine *Engine, target string) *install.Install {
	t.Helper()
	if err := engine.Setup(context.Background(), SetupOptions{Channel: omaha.Stable, Arch: "x64", Target: target}); err != nil {
		t.Fatal(err)
	}
	in, err := engine.Open()
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestSetup(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	var phases []Phase
	engine.Progress = func(st Step) {
		if len(phases) == 0 || phases[len(phases)-1] != st.Phase {
			phases = append(phases, st.Phase)
		}
	}
	in := setup(t, engine, "")
	if in.Chrome != "156.0.8078.74" || in.Arch != "x64" || in.ChromePlus != "1.18.3" || in.Injection != install.VersionDLL {
		t.Errorf("installed %+v", in)
	}
	if in.State.Channel != omaha.Stable || len(in.State.History) != 1 || in.State.History[0].Kind != install.Installed {
		t.Errorf("state %+v", in.State)
	}
	if got := fmt.Sprint(entries(t, engine.Root)); got != "[.pitstop App Cache Data]" {
		t.Errorf("root holds %s", got)
	}
	ini, _ := os.ReadFile(filepath.Join(in.App(), "chrome++.ini"))
	if len(ini) != 16088 {
		t.Errorf("chrome++.ini of %d bytes", len(ini))
	}
	for _, dir := range []string{staging, chromeCache, plusCache, rollback} {
		if got := entries(t, engine.path(dir)); len(got) > 0 {
			t.Errorf("%s keeps %v", dir, got)
		}
	}
	want := "[download decode unpack verify download unpack apply]"
	if got := fmt.Sprint(phases); got != want {
		t.Errorf("phases %s, want %s", got, want)
	}
	if err := engine.Setup(context.Background(), SetupOptions{Channel: omaha.Stable, Arch: "x64"}); !errors.Is(err, ErrInstalled) {
		t.Errorf("second Setup: %v", err)
	}
}

func TestSetupRefusesAReleaseWithoutADigest(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.74")
	s.SHA256 = ""
	engine := newEngine(t, s)
	err := engine.Setup(context.Background(), SetupOptions{Channel: omaha.Stable, Arch: "x64"})
	if err == nil || !strings.Contains(err.Error(), "no SHA-256") {
		t.Fatalf("Setup = %v", err)
	}
	if exists(engine.path(app)) || s.Gets.Load() != 0 {
		t.Errorf("installed anyway, after %d downloads", s.Gets.Load())
	}
}

func TestUpdateAndRollBackChrome(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	if in.Chrome != "156.0.8078.48" {
		t.Fatalf("pinned setup installed %s", in.Chrome)
	}
	// The user's settings must survive every swap.
	userIni := []byte("\xff\xfe[general]\r\n")
	os.WriteFile(filepath.Join(in.App(), "chrome++.ini"), userIni, 0o644)
	os.WriteFile(filepath.Join(in.App(), "notes.txt"), []byte("mine"), 0o644)

	u, err := engine.CheckChrome(context.Background(), in)
	if err != nil || u == nil || u.Version != "156.0.8078.74" {
		t.Fatalf("CheckChrome = %+v, %v", u, err)
	}
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	in, _ = engine.Open()
	if in.Chrome != "156.0.8078.74" || in.ChromePlus != "1.18.3" {
		t.Fatalf("after the update: %+v", in)
	}
	if got := fmt.Sprint(entries(t, in.App())); got != "[156.0.8078.74 chrome++.ini chrome.exe chrome_proxy.exe notes.txt version.dll]" {
		t.Errorf("App holds %s", got)
	}
	if v, ok := engine.ChromeRollback(); !ok || v != "156.0.8078.48" {
		t.Errorf("ChromeRollback = %q, %v", v, ok)
	}
	if u, err := engine.CheckChrome(context.Background(), in); u != nil || err != nil {
		t.Errorf("up-to-date CheckChrome = %+v, %v", u, err)
	}

	if err := engine.RollbackChrome(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	in, _ = engine.Open()
	if v, _ := engine.ChromeRollback(); in.Chrome != "156.0.8078.48" || v != "156.0.8078.74" {
		t.Fatalf("after the rollback: %s, kept %s", in.Chrome, v)
	}
	if got := entries(t, engine.path(rollback)); len(got) != 1 {
		t.Errorf("rollback holds %v", got)
	}
	ini, _ := os.ReadFile(filepath.Join(in.App(), "chrome++.ini"))
	if !bytes.Equal(ini, userIni) {
		t.Error("the swaps changed chrome++.ini")
	}
	kinds := []install.EventKind{}
	for _, e := range in.State.History {
		kinds = append(kinds, e.Kind)
	}
	if got := fmt.Sprint(kinds); got != "[chromeRolledBack chromeUpdated installed]" {
		t.Errorf("history %s", got)
	}
}

func TestUpdateWaitsForChromeToClose(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	u, _ := engine.CheckChrome(context.Background(), in)
	engine.Hooks.Running = func(dir string) ([]chromeproc.Process, error) {
		return []chromeproc.Process{{PID: 42, Path: filepath.Join(dir, "chrome.exe")}}, nil
	}
	if o, err := engine.UpdateChrome(context.Background(), u); o != Pending || err != nil {
		t.Fatalf("UpdateChrome = %v, %v", o, err)
	}
	in, _ = engine.Open()
	if ready, _ := engine.Downloaded(in); in.Chrome != "156.0.8078.48" || in.State.Approved != u.Version || !ready {
		t.Fatalf("Chrome %s while it ran, approved %q, downloaded %v", in.Chrome, in.State.Approved, ready)
	}
	downloads := s.Gets.Load()
	engine.Hooks.Running = func(string) ([]chromeproc.Process, error) { return nil, nil }
	if o, err := engine.UpdateChrome(context.Background(), u); o != Installed || err != nil {
		t.Fatalf("UpdateChrome once Chrome closed = %v, %v", o, err)
	}
	if s.Gets.Load() != downloads {
		t.Error("downloaded the staged build again")
	}
	if in, _ := engine.Open(); in.State.Approved != "" {
		t.Errorf("approval %q stayed", in.State.Approved)
	}
}

// running makes Chrome run from the install, and returns a function that
// closes it.
func running(engine *Engine) (close func()) {
	engine.Hooks.Running = func(dir string) ([]chromeproc.Process, error) {
		return []chromeproc.Process{{PID: 42, Path: filepath.Join(dir, "chrome.exe")}}, nil
	}
	return func() { engine.Hooks.Running = func(string) ([]chromeproc.Process, error) { return nil, nil } }
}

// stageWhileRunning sets up Chrome 156.0.8078.48 with a Chrome++ that
// finishes in-use updates, and updates it to 156.0.8078.74 while it runs.
func stageWhileRunning(t *testing.T) (*pitstoptest.Servers, *Engine, func()) {
	t.Helper()
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	if err := engine.SetPolicy(install.Ask); err != nil {
		t.Fatal(err)
	}
	u, _ := engine.CheckChrome(context.Background(), in)
	closeChrome := running(engine)
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return s, engine, closeChrome
}

// swapStaged renames what Chrome++ renames when Chrome restarts with a
// staged build, the first n renames when n >= 0.
func swapStaged(t *testing.T, app string, n int) {
	t.Helper()
	renames := [][2]string{
		{"chrome.exe", "old_chrome.exe"}, {"new_chrome.exe", "chrome.exe"},
		{"chrome_proxy.exe", "old_chrome_proxy.exe"}, {"new_chrome_proxy.exe", "chrome_proxy.exe"},
	}
	if n < 0 {
		n = len(renames)
	}
	for _, r := range renames[:n] {
		if err := os.Rename(filepath.Join(app, r[0]), filepath.Join(app, r[1])); err != nil {
			t.Fatal(err)
		}
	}
	if n == len(renames) {
		os.Remove(filepath.Join(app, install.StagedMarker))
	}
}

func TestUpdateWhileChromeRunsWaitsWithoutTheOptIn(t *testing.T) {
	for _, plus := range []string{"1.19.0", "alpha-083e1ac"} {
		s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
		engine := newEngine(t, s)
		in := setup(t, engine, "156.0.8078.48")
		pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), plus)
		u, _ := engine.CheckChrome(context.Background(), in)
		running(engine)
		if o, err := engine.UpdateChrome(context.Background(), u); o != Pending || err != nil {
			t.Errorf("Chrome++ %s: UpdateChrome = %v, %v", plus, o, err)
		}
		if in, _ := engine.Open(); in.Staged != "" {
			t.Errorf("Chrome++ %s: staged %s", plus, in.Staged)
		}
	}
}

func TestUpdateWhileChromeRunsGoesInAtItsRestart(t *testing.T) {
	s, engine, _ := stageWhileRunning(t)
	in, err := engine.Open()
	if err != nil || in.Chrome != "156.0.8078.48" || in.Staged != "156.0.8078.74" {
		t.Fatalf("after staging: %+v, %v", in, err)
	}
	if got := fmt.Sprint(entries(t, in.App())); got != "[156.0.8078.48 156.0.8078.74 chrome++.ini chrome++.ini.bak chrome++.staged chrome.exe chrome_proxy.exe new_chrome.exe new_chrome_proxy.exe version.dll]" {
		t.Errorf("App holds %s", got)
	}
	if got := entries(t, engine.path(staging)); len(got) > 0 {
		t.Errorf("staging keeps %v", got)
	}
	downloads := s.Gets.Load()
	u, _ := engine.CheckChrome(context.Background(), in)
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil || s.Gets.Load() != downloads {
		t.Errorf("updating to the staged build again: %v, after %d downloads", err, s.Gets.Load()-downloads)
	}

	swapStaged(t, in.App(), -1)
	in, err = engine.Open()
	if err != nil || in.Chrome != "156.0.8078.74" || in.Staged != "" {
		t.Fatalf("after Chrome's restart: %+v, %v", in, err)
	}
	if got := fmt.Sprint(entries(t, in.App())); got != "[156.0.8078.74 chrome++.ini chrome++.ini.bak chrome.exe chrome_proxy.exe version.dll]" {
		t.Errorf("App holds %s", got)
	}
	if v, ok := engine.ChromeRollback(); !ok || v != "156.0.8078.48" {
		t.Errorf("ChromeRollback = %q, %v", v, ok)
	}
	if e := in.State.History[0]; e.Kind != install.ChromeUpdated || e.From != "156.0.8078.48" || e.To != "156.0.8078.74" {
		t.Errorf("history starts with %+v", e)
	}
}

// A file of the swapped-out build can stay in use after Chrome restarts.
func TestSettleWaitsForTheReplacedBuildToBeFree(t *testing.T) {
	_, engine, _ := stageWhileRunning(t)
	app := engine.path(app)
	swapStaged(t, app, -1)
	f, err := os.Open(filepath.Join(app, "156.0.8078.48", "chrome.dll"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := engine.Open()
	f.Close()
	if err != nil || in.Chrome != "156.0.8078.74" || !in.Unsettled {
		t.Fatalf("while in use: %+v, %v", in, err)
	}
	if _, ok := engine.ChromeRollback(); ok {
		t.Error("kept the build while in use")
	}
	in, err = engine.Open()
	if err != nil || in.Unsettled {
		t.Fatalf("once free: %+v, %v", in, err)
	}
	if v, ok := engine.ChromeRollback(); !ok || v != "156.0.8078.48" {
		t.Errorf("ChromeRollback = %q, %v", v, ok)
	}
	if e := in.State.History[0]; e.Kind != install.ChromeUpdated || e.From != "156.0.8078.48" {
		t.Errorf("history starts with %+v", e)
	}
}

func TestStagedBuildGoesInOnceChromeCloses(t *testing.T) {
	s, engine, closeChrome := stageWhileRunning(t)
	in, _ := engine.Open()
	u, _ := engine.CheckChrome(context.Background(), in)
	closeChrome()
	downloads := s.Gets.Load()
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	in, _ = engine.Open()
	if in.Chrome != "156.0.8078.74" || in.Staged != "" || s.Gets.Load() != downloads {
		t.Errorf("after the update: %+v, after %d downloads", in, s.Gets.Load()-downloads)
	}
	if got := fmt.Sprint(entries(t, in.App())); got != "[156.0.8078.74 chrome++.ini chrome++.ini.bak chrome.exe chrome_proxy.exe version.dll]" {
		t.Errorf("App holds %s", got)
	}
}

func TestSettleFinishesAnInterruptedSwap(t *testing.T) {
	_, engine, _ := stageWhileRunning(t)
	app := engine.path(app)
	// Chrome++ crashed between renaming chrome.exe away and new_chrome.exe in.
	swapStaged(t, app, 1)
	in, err := engine.Open()
	if err != nil || in.Chrome != "156.0.8078.74" || in.Staged != "" {
		t.Fatalf("after settling: %+v, %v", in, err)
	}
	if got := fmt.Sprint(entries(t, app)); got != "[156.0.8078.74 chrome++.ini chrome++.ini.bak chrome.exe chrome_proxy.exe version.dll]" {
		t.Errorf("App holds %s", got)
	}
	proxy, _ := os.ReadFile(filepath.Join(app, "chrome_proxy.exe"))
	if string(proxy) != "proxy 156.0.8078.74" {
		t.Errorf("chrome_proxy.exe is %q", proxy)
	}
}

func TestRollbackTakesTheStagedBuildOut(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	u, _ := engine.CheckChrome(context.Background(), in)
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	engine.SetPolicy(install.Ask)
	s.Versions = append(s.Versions, "156.0.8078.80")
	in, _ = engine.Open()
	u, _ = engine.CheckChrome(context.Background(), in)
	closeChrome := running(engine)
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if err := engine.RollbackChrome(context.Background(), false); !errors.Is(err, ErrChromeRunning) {
		t.Fatalf("RollbackChrome while Chrome runs = %v", err)
	}
	closeChrome()
	if err := engine.RollbackChrome(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	in, _ = engine.Open()
	if in.Chrome != "156.0.8078.48" || in.Staged != "" {
		t.Fatalf("after the rollback: %+v", in)
	}
	if got := fmt.Sprint(entries(t, in.App())); got != "[156.0.8078.48 chrome++.ini chrome++.ini.bak chrome.exe chrome_proxy.exe version.dll]" {
		t.Errorf("App holds %s", got)
	}
	if got := fmt.Sprint(entries(t, engine.path(staging))); got != "[chrome-156.0.8078.80]" {
		t.Errorf("staging holds %s", got)
	}
}

func TestRollbackAcrossMilestones(t *testing.T) {
	s := pitstoptest.NewServers(t, "155.0.8059.85", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "155.")
	u, _ := engine.CheckChrome(context.Background(), in)
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if err := engine.RollbackChrome(context.Background(), false); !errors.Is(err, ErrMilestone) {
		t.Fatalf("RollbackChrome = %v", err)
	}
	os.WriteFile(engine.path(data, "Local State"), []byte("156"), 0o644)
	if err := engine.RollbackChrome(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if in, _ := engine.Open(); in.Chrome != "155.0.8059.85" {
		t.Errorf("rolled back to %s", in.Chrome)
	}
	if got := files(t, engine.path(data)); got["Local State"] != "156" {
		t.Errorf("Data holds %v", got)
	}
	if err := engine.RollbackChrome(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if in, _ := engine.Open(); in.Chrome != "156.0.8078.74" {
		t.Errorf("switched up to %s", in.Chrome)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// files lists the files under dir with their contents.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// milestoneUpdate sets up Chrome 155.0.8059.85 with a profile, and finds the
// update to 156.0.8078.74.
func milestoneUpdate(t *testing.T) (*pitstoptest.Servers, *Engine, *ChromeUpdate) {
	t.Helper()
	s := pitstoptest.NewServers(t, "155.0.8059.85", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "155.")
	writeFiles(t, engine.path(data), map[string]string{
		"Local State":                         "155",
		"Default/History":                     "155",
		"Default/Extensions/abc/GPUCache/own": "an extension's own folder",
		"Default/GPUCache/data_0":             "cache",
		"Default/Cache/Cache_Data/f_000001":   "cache",
		"GrShaderCache/data_1":                "cache",
	})
	u, err := engine.CheckChrome(context.Background(), in)
	if err != nil || u == nil || u.Version != "156.0.8078.74" {
		t.Fatalf("CheckChrome = %+v, %v", u, err)
	}
	return s, engine, u
}

func TestMilestoneUpdateStagesWhileChromeRuns(t *testing.T) {
	_, engine, u := milestoneUpdate(t)
	pitstoptest.WriteManagedChromePlus(t, engine.path(app, "version.dll"), "1.19.0")
	engine.SetPolicy(install.Ask)
	running(engine)
	if outcome, err := engine.UpdateChrome(context.Background(), u); outcome != Staged || err != nil {
		t.Fatalf("UpdateChrome while Chrome runs = %v, %v", outcome, err)
	}
	in, _ := engine.Open()
	if in.Chrome != "155.0.8059.85" || in.Staged != u.Version || !exists(engine.path(app, "new_chrome.exe")) {
		t.Fatalf("after staging: %+v", in)
	}
}

func TestMilestoneUpdateLeavesTheProfile(t *testing.T) {
	_, engine, u := milestoneUpdate(t)
	var phases []Phase
	engine.Progress = func(st Step) {
		if len(phases) == 0 || phases[len(phases)-1] != st.Phase {
			phases = append(phases, st.Phase)
		}
	}
	if _, err := engine.UpdateChrome(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(phases); got != "[download decode unpack verify apply]" {
		t.Errorf("phases %s", got)
	}
	if got := files(t, engine.path(data)); len(got) != 6 {
		t.Errorf("Data changed: %v", got)
	}
	writeFiles(t, engine.path(data), map[string]string{"Default/History": "156"})
	if err := engine.RollbackChrome(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	in, _ := engine.Open()
	if got := files(t, engine.path(data)); in.Chrome != "155.0.8059.85" || got["Default/History"] != "156" || len(got) != 6 {
		t.Fatalf("after the switch back: %s, Data holds %v", in.Chrome, got)
	}
	if exists(engine.path(journal)) {
		t.Error("a journal is left")
	}
}

func TestPatchedChromeIsLeftAlone(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	pitstoptest.PatchWithSetDLL(t, in.App(), in.Chrome)
	in, _ = engine.Open()
	if in.Injection != install.SetDLL || in.ChromePlusDLL != "version-x64.dll" {
		t.Fatalf("the fixture is not a setdll install: %+v", in)
	}
	u, err := engine.CheckChrome(context.Background(), in)
	if err != nil || u == nil {
		t.Fatalf("CheckChrome = %+v, %v", u, err)
	}
	downloads := s.Gets.Load()
	if _, err := engine.UpdateChrome(context.Background(), u); !errors.Is(err, ErrPatched) {
		t.Fatalf("UpdateChrome = %v", err)
	}
	if err := engine.RollbackChrome(context.Background(), true); !errors.Is(err, ErrPatched) {
		t.Fatalf("RollbackChrome = %v", err)
	}
	if s.Gets.Load() != downloads {
		t.Error("downloaded an update it won't install")
	}
	if in, _ := engine.Open(); in.Chrome != "156.0.8078.48" || in.Injection != install.SetDLL {
		t.Errorf("the install changed: %+v", in)
	}
}

func TestExtractChromePlus(t *testing.T) {
	const archive = "../../testdata/Chrome++_v1.18.3_x86_x64_arm64.7z"
	dir := t.TempDir()
	if err := extractChromePlus(archive, "x64", dir); err != nil {
		t.Fatal(err)
	}
	dll, _ := os.ReadFile(filepath.Join(dir, "version.dll"))
	want, _ := os.ReadFile(plusFixture)
	if !bytes.Equal(dll, want) {
		t.Error("version.dll differs from the x64 one")
	}
	ini, _ := os.ReadFile(filepath.Join(dir, "chrome++.ini"))
	if len(ini) != 16088 || !bytes.HasPrefix(ini, []byte{0xFF, 0xFE}) {
		t.Errorf("chrome++.ini: %d bytes", len(ini))
	}
	if err := extractChromePlus(archive, "mips", t.TempDir()); err == nil {
		t.Error("extracted an architecture the release lacks")
	}
}

func TestUpdateAndRollBackChromePlus(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "")
	if u, err := engine.CheckChromePlus(context.Background(), in); u != nil || err != nil {
		t.Fatalf("CheckChromePlus on the newest = %+v, %v", u, err)
	}
	pitstoptest.WriteChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.18.2")
	userIni := []byte("\xff\xfe[general]\r\n")
	os.WriteFile(filepath.Join(in.App(), "chrome++.ini"), userIni, 0o644)
	in, _ = engine.Open()
	u, err := engine.CheckChromePlus(context.Background(), in)
	if err != nil || u == nil || u.Release.Tag != "1.18.3" {
		t.Fatalf("CheckChromePlus = %+v, %v", u, err)
	}
	if o, err := engine.UpdateChromePlus(context.Background(), u); o != Installed || err != nil {
		t.Fatalf("UpdateChromePlus = %v, %v", o, err)
	}
	in, _ = engine.Open()
	dll, _ := os.ReadFile(filepath.Join(in.App(), "version.dll"))
	want, _ := os.ReadFile(plusFixture)
	ini, _ := os.ReadFile(filepath.Join(in.App(), "chrome++.ini"))
	if in.ChromePlus != "1.18.3" || !bytes.Equal(dll, want) || !bytes.Equal(ini, userIni) {
		t.Fatalf("after the update: %+v, ini %q", in, ini)
	}
	if v, ok := engine.ChromePlusRollback(); !ok || v != "1.18.2" {
		t.Fatalf("ChromePlusRollback = %q, %v", v, ok)
	}
	if err := engine.RollbackChromePlus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if in, _ = engine.Open(); in.ChromePlus != "1.18.2" {
		t.Fatalf("rolled back to %q", in.ChromePlus)
	}
	if v, _ := engine.ChromePlusRollback(); v != "1.18.3" {
		t.Errorf("kept %q", v)
	}
}

func TestBusy(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.74")
	engine := newEngine(t, s)
	setup(t, engine, "")
	other := flock.New(engine.path(lockFile))
	if ok, err := other.TryLock(); !ok || err != nil {
		t.Fatalf("TryLock = %v, %v", ok, err)
	}
	if !engine.Busy() {
		t.Error("Busy = false while another holds the lock")
	}
	if err := engine.RollbackChrome(context.Background(), false); !errors.Is(err, ErrBusy) {
		t.Fatalf("RollbackChrome = %v", err)
	}
	other.Unlock()
	if engine.Busy() {
		t.Error("Busy = true once the lock is free")
	}
}

func TestLockRecoversAnInterruptedChange(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.74")
	engine := newEngine(t, s)
	setup(t, engine, "")
	// A crash right after the first move of a Chrome++ update.
	tx := apply.Tx{Op: "Chrome++ 1.18.3 to 1.18.4", Moves: []apply.Move{
		{From: app + "/version.dll", To: rollback + "/chromeplus-1.18.3/version.dll"},
		{From: staging + "/chromeplus-1.18.4/version.dll", To: app + "/version.dll"},
	}}
	b, _ := json.Marshal(tx)
	os.WriteFile(engine.path(journal), b, 0o644)
	os.MkdirAll(engine.path(rollback, "chromeplus-1.18.3"), 0o755)
	os.Rename(engine.path(app, "version.dll"), engine.path(rollback, "chromeplus-1.18.3", "version.dll"))

	if err := engine.UpdateState(func(s *install.State) { s.Channel = omaha.Beta }); err != nil {
		t.Fatal(err)
	}
	in, err := engine.Open()
	if err != nil || in.ChromePlus != "1.18.3" || in.State.Channel != omaha.Beta {
		t.Fatalf("after recovery: %+v, %v", in, err)
	}
	if e := in.State.History[0]; e.Kind != install.Recovered || e.From != tx.Op {
		t.Errorf("history starts with %+v", e)
	}
}

func TestMilestone(t *testing.T) {
	if Milestone("156.0.8078.74") != 156 || Milestone("") != 0 {
		t.Error(Milestone("156.0.8078.74"))
	}
}
