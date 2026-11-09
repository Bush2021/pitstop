package pitstop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/pitstop/internal/chromeproc"
	"github.com/Bush2021/pitstop/internal/ini"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/pitstop/pitstoptest"
)

func TestDecide(t *testing.T) {
	const v = "156.0.8078.74"
	for _, tc := range []struct {
		name     string
		s        install.State
		approved string
		metered  bool
		want     Action
	}{
		{name: "off", s: install.State{Policy: install.Off}, want: Nothing},
		{name: "off beats an approval", s: install.State{Policy: install.Off}, approved: v, want: Nothing},
		{name: "skipped", s: install.State{Policy: install.Automatic, Skipped: []string{v}}, want: Nothing},
		{name: "skipped beats an approval", s: install.State{Policy: install.Ask, Skipped: []string{v}}, approved: v, want: Nothing},
		{name: "automatic", s: install.State{Policy: install.Automatic}, want: Get},
		{name: "automatic while metered", s: install.State{Policy: install.Automatic}, metered: true, want: Ask},
		{name: "ask", s: install.State{Policy: install.Ask}, want: Ask},
		{name: "approved", s: install.State{Policy: install.Ask}, approved: v, want: Get},
		{name: "approved while metered", s: install.State{Policy: install.Ask}, approved: v, metered: true, want: Get},
		{name: "another version approved", s: install.State{Policy: install.Ask}, approved: "156.0.8078.48", want: Ask},
	} {
		if got := Decide(tc.s, tc.approved, v, tc.metered); got != tc.want {
			t.Errorf("%s: Decide = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func optedInINI(t *testing.T, in *install.Install) (string, string) {
	t.Helper()
	f, err := ini.Load(filepath.Join(in.App(), "chrome++.ini"))
	if err != nil {
		t.Fatal(err)
	}
	launch, _ := f.Raw("general", launchOnStartup)
	managed, _ := f.Raw("general", managedUpdates)
	return launch, managed
}

func launchLine(t *testing.T, in *install.Install) (string, bool) {
	t.Helper()
	f, err := ini.Load(filepath.Join(in.App(), "chrome++.ini"))
	if err != nil {
		t.Fatal(err)
	}
	return f.Raw("general", launchOnStartup)
}

func TestSetPolicy(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48")
	engine := newEngine(t, s)
	in := setup(t, engine, "")
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	engine.Exe = `C:\Tools\pitstop.exe`
	if err := engine.SetPolicy(install.Ask); err != nil {
		t.Fatal(err)
	}
	in, _ = engine.Open()
	if v, m := optedInINI(t, in); v != `start "" "C:\Tools\pitstop.exe" -root "%app%\.." --background;` || m != "1" || in.State.Policy != install.Ask {
		t.Fatalf("launch_on_startup=%s, managed_updates=%s, policy %q", v, m, in.State.Policy)
	}

	// The user's own command stays, and Pitstop's moves with pitstop.exe.
	f, _ := ini.Load(filepath.Join(in.App(), "chrome++.ini"))
	v, _ := f.Raw("general", launchOnStartup)
	f.Set("general", launchOnStartup, `C:\Tools\sync.exe;C:\Tools\pitstop.exe;`+v)
	f.Save(filepath.Join(in.App(), "chrome++.ini"))
	engine.Exe = filepath.Join(engine.Root, "pitstop.exe")
	if err := engine.SetPolicy(install.Automatic); err != nil {
		t.Fatal(err)
	}
	if v, _ := launchLine(t, in); v != `C:\Tools\sync.exe;C:\Tools\pitstop.exe;start "" "%app%\..\pitstop.exe" --background;` {
		t.Fatalf("launch_on_startup=%s", v)
	}

	if err := engine.SetPolicy(install.Off); err != nil {
		t.Fatal(err)
	}
	in, _ = engine.Open()
	if v, m := optedInINI(t, in); v != `C:\Tools\sync.exe;C:\Tools\pitstop.exe;` || m != "0" || in.State.Policy != install.Off {
		t.Fatalf("launch_on_startup=%s, managed_updates=%s, policy %q", v, m, in.State.Policy)
	}
}

func TestGoingOffTakesTheStagedBuildOut(t *testing.T) {
	_, engine, _ := stageWhileRunning(t)
	engine.UpdateState(func(s *install.State) { s.Approved, s.ApprovedPlus = "156.0.8078.74", "1.19.1" })
	if err := engine.SetPolicy(install.Off); err != nil {
		t.Fatal(err)
	}
	in, _ := engine.Open()
	if in.Staged != "" || in.State.Approved != "" || in.State.ApprovedPlus != "" {
		t.Fatalf("staged %q, approved %q and %q", in.Staged, in.State.Approved, in.State.ApprovedPlus)
	}
	if got := fmt.Sprint(entries(t, in.App())); got != "[156.0.8078.48 chrome++.ini chrome++.ini.bak chrome.exe chrome_proxy.exe version.dll]" {
		t.Errorf("App holds %s", got)
	}
	if v, m := optedInINI(t, in); v != "" || m != "0" {
		t.Errorf("launch_on_startup=%s, managed_updates=%s", v, m)
	}
}

// An alpha build can predate the feature whatever its name says.
func TestChromePlusWithoutTheKeyStaysOut(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	pitstoptest.WriteChromePlus(t, filepath.Join(in.App(), "version.dll"), "alpha-7b80a11")
	if err := engine.SetPolicy(install.Ask); err != nil {
		t.Fatal(err)
	}
	if v, _ := launchLine(t, in); v != "" {
		t.Fatalf("launch_on_startup=%s", v)
	}
	u, _ := engine.CheckChrome(context.Background(), in)
	running(engine)
	if o, err := engine.UpdateChrome(context.Background(), u); o != Pending || err != nil {
		t.Fatalf("UpdateChrome = %v, %v", o, err)
	}
	if in, _ := engine.Open(); in.Staged != "" {
		t.Fatalf("staged %s", in.Staged)
	}
}

// The choice waits for a Chrome++ that can honor it.
func TestSetupWithAnOlderChromePlus(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48")
	engine := newEngine(t, s)
	err := engine.Setup(context.Background(), SetupOptions{Channel: omaha.Stable, Arch: "x64", Policy: install.Ask})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := engine.Open()
	if v, _ := launchLine(t, in); v != "" || in.State.Policy != install.Ask {
		t.Fatalf("launch_on_startup=%s, policy %q", v, in.State.Policy)
	}
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	if err := engine.SyncINI(); err != nil {
		t.Fatal(err)
	}
	if v, m := optedInINI(t, in); !strings.Contains(v, "--background") || m != "1" {
		t.Fatalf("launch_on_startup=%s, managed_updates=%s", v, m)
	}
}

func TestSyncINIRepairsOnlyWhatDiffers(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48")
	engine := newEngine(t, s)
	in := setup(t, engine, "")
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	engine.SetPolicy(install.Ask)
	path := filepath.Join(in.App(), "chrome++.ini")

	long := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(path, long, long)
	if err := engine.SyncINI(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); !st.ModTime().Equal(long) {
		t.Error("rewrote a chrome++.ini that agrees with the state")
	}

	f, _ := ini.Load(path)
	f.Set("general", managedUpdates, "0")
	f.Set("general", launchOnStartup, "")
	f.Save(path)
	if err := engine.SyncINI(); err != nil {
		t.Fatal(err)
	}
	if v, m := optedInINI(t, in); !strings.Contains(v, "--background") || m != "1" {
		t.Fatalf("launch_on_startup=%s, managed_updates=%s", v, m)
	}
}

func TestSyncINIWithoutAnInstall(t *testing.T) {
	if err := newEngine(t, pitstoptest.NewServers(t)).SyncINI(); err != nil {
		t.Fatal(err)
	}
}

func TestSetPolicyWithoutAnINI(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48")
	engine := newEngine(t, s)
	in := setup(t, engine, "")
	os.Remove(filepath.Join(in.App(), "chrome++.ini"))
	if err := engine.SetPolicy(install.Off); err != nil || exists(filepath.Join(in.App(), "chrome++.ini")) {
		t.Fatalf("SetPolicy(Off) = %v, or it wrote an ini", err)
	}
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	if err := engine.SetPolicy(install.Ask); err != nil {
		t.Fatal(err)
	}
	if _, ok := launchLine(t, in); !ok {
		t.Fatal("no launch line in the new chrome++.ini")
	}
}

func TestInstallApproved(t *testing.T) {
	s, engine, u := milestoneUpdate(t)
	if v, err := engine.InstallApproved(context.Background()); v != "" || err != nil {
		t.Fatalf("InstallApproved without an approval = %q, %v", v, err)
	}
	closeChrome := running(engine)
	if o, err := engine.UpdateChrome(context.Background(), u); o != Pending || err != nil {
		t.Fatalf("UpdateChrome while Chrome runs = %v, %v", o, err)
	}
	if _, err := engine.InstallApproved(context.Background()); !errors.Is(err, ErrChromeRunning) {
		t.Fatalf("InstallApproved while Chrome runs = %v", err)
	}
	closeChrome()
	downloads := s.Gets.Load()
	v, err := engine.InstallApproved(context.Background())
	if err != nil || v != "156.0.8078.74" || s.Gets.Load() != downloads {
		t.Fatalf("InstallApproved = %q, %v, after %d downloads", v, err, s.Gets.Load()-downloads)
	}
	in, _ := engine.Open()
	if in.Chrome != "156.0.8078.74" || in.State.Approved != "" {
		t.Fatalf("after the install: %s, approved %q", in.Chrome, in.State.Approved)
	}
}

// An approval waits for its download, which is still running or failed.
func TestInstallApprovedWithoutTheDownload(t *testing.T) {
	s, engine, u := milestoneUpdate(t)
	engine.UpdateState(func(s *install.State) { s.Approved = u.Version })
	downloads := s.Gets.Load()
	if v, err := engine.InstallApproved(context.Background()); v != "" || err != nil || s.Gets.Load() != downloads {
		t.Fatalf("InstallApproved = %q, %v, after %d downloads", v, err, s.Gets.Load()-downloads)
	}
	if in, _ := engine.Open(); in.Chrome != "155.0.8059.85" || in.State.Approved != u.Version {
		t.Fatalf("Chrome %s, approval %q", in.Chrome, in.State.Approved)
	}
}

func TestInstallApprovedPlus(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48")
	engine := newEngine(t, s)
	in := setup(t, engine, "")
	pitstoptest.WriteChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.18.2")
	if v, err := engine.InstallApprovedPlus(context.Background()); v != "" || err != nil {
		t.Fatalf("InstallApprovedPlus without an approval = %q, %v", v, err)
	}
	in, _ = engine.Open()
	u, _ := engine.CheckChromePlus(context.Background(), in)
	closeChrome := running(engine)
	if o, err := engine.UpdateChromePlus(context.Background(), u); o != Pending || err != nil {
		t.Fatalf("UpdateChromePlus while Chrome runs = %v, %v", o, err)
	}
	in, _ = engine.Open()
	if _, ready := engine.Downloaded(in); in.ChromePlus != "1.18.2" || in.State.ApprovedPlus != "1.18.3" || !ready {
		t.Fatalf("Chrome++ %s while Chrome ran, approved %q, downloaded %v", in.ChromePlus, in.State.ApprovedPlus, ready)
	}
	if _, err := engine.InstallApprovedPlus(context.Background()); !errors.Is(err, ErrChromeRunning) {
		t.Fatalf("InstallApprovedPlus while Chrome runs = %v", err)
	}
	closeChrome()
	// Nothing is left to fetch once Chrome is closed.
	engine.GitHub.Base = "http://127.0.0.1:1"
	downloads := s.Gets.Load()
	if v, err := engine.InstallApprovedPlus(context.Background()); v != "1.18.3" || err != nil || s.Gets.Load() != downloads {
		t.Fatalf("InstallApprovedPlus = %q, %v, after %d downloads", v, err, s.Gets.Load()-downloads)
	}
	if in, _ := engine.Open(); in.ChromePlus != "1.18.3" || in.State.ApprovedPlus != "" {
		t.Fatalf("Chrome++ %s, approval %q", in.ChromePlus, in.State.ApprovedPlus)
	}
}

// An approval waits for its download, which is still running or failed.
func TestInstallApprovedPlusWithoutTheDownload(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48")
	engine := newEngine(t, s)
	in := setup(t, engine, "")
	pitstoptest.WriteChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.18.2")
	engine.UpdateState(func(s *install.State) { s.ApprovedPlus = "1.18.3" })
	if v, err := engine.InstallApprovedPlus(context.Background()); v != "" || err != nil {
		t.Fatalf("InstallApprovedPlus = %q, %v", v, err)
	}
	if in, _ := engine.Open(); in.ChromePlus != "1.18.2" || in.State.ApprovedPlus != "1.18.3" {
		t.Fatalf("Chrome++ %s, approval %q", in.ChromePlus, in.State.ApprovedPlus)
	}
}

func TestInstallApprovedPlusDropsAnOvertakenApproval(t *testing.T) {
	for _, approved := range []string{"1.18.2", "1.18.3"} {
		s := pitstoptest.NewServers(t, "156.0.8078.48")
		engine := newEngine(t, s)
		setup(t, engine, "")
		engine.UpdateState(func(s *install.State) { s.ApprovedPlus = approved })
		if v, err := engine.InstallApprovedPlus(context.Background()); v != "" || err != nil {
			t.Fatalf("approved %s: InstallApprovedPlus = %q, %v", approved, v, err)
		}
		if in, _ := engine.Open(); in.ChromePlus != "1.18.3" || in.State.ApprovedPlus != "" {
			t.Errorf("approved %s: Chrome++ %s, approval %q", approved, in.ChromePlus, in.State.ApprovedPlus)
		}
	}
}

// A restart through Pitstop is when Chrome is closed: what waited for that
// is in before Chrome starts again, from what was downloaded.
func TestRestartChromeInstallsWhatWaited(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	pitstoptest.WriteChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.18.2")
	in, _ = engine.Open()
	u, _ := engine.CheckChrome(context.Background(), in)
	p, _ := engine.CheckChromePlus(context.Background(), in)
	closeChrome := running(engine)
	if o, err := engine.UpdateChrome(context.Background(), u); o != Pending || err != nil {
		t.Fatalf("UpdateChrome = %v, %v", o, err)
	}
	if o, err := engine.UpdateChromePlus(context.Background(), p); o != Pending || err != nil {
		t.Fatalf("UpdateChromePlus = %v, %v", o, err)
	}
	downloads := s.Gets.Load()
	engine.Hooks.Close = func(string) error {
		closeChrome()
		return nil
	}
	engine.Hooks.Wait = func(context.Context, string) error { return nil }
	var started string
	engine.Hooks.Start = func(string) error {
		in, err := install.Open(engine.Root)
		if err != nil {
			return err
		}
		started = in.Chrome + " with " + in.ChromePlus
		return nil
	}
	if err := engine.RestartChrome(context.Background()); err != nil {
		t.Fatal(err)
	}
	if started != "156.0.8078.74 with 1.18.3" || s.Gets.Load() != downloads {
		t.Fatalf("started Chrome %s, after %d downloads", started, s.Gets.Load()-downloads)
	}
	if in, _ := engine.Open(); in.State.Approved != "" || in.State.ApprovedPlus != "" {
		t.Errorf("approvals %q and %q stayed", in.State.Approved, in.State.ApprovedPlus)
	}
}

// Chrome++ could swap a patch in at Chrome's restart, but an approved build
// waits until Chrome is closed.
func TestInstallApprovedWaitsForChromeToClose(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	engine.UpdateState(func(s *install.State) { s.Approved = "156.0.8078.74" })
	running(engine)
	gets := s.Gets.Load()
	if _, err := engine.InstallApproved(context.Background()); !errors.Is(err, ErrChromeRunning) {
		t.Fatalf("InstallApproved = %v", err)
	}
	if in, _ := engine.Open(); in.Staged != "" || s.Gets.Load() != gets {
		t.Fatalf("staged %q after %d downloads", in.Staged, s.Gets.Load()-gets)
	}
}

// Chrome may start again between the check that it is closed and the
// install, as Chrome's own restart does: then the patch goes in staged.
func TestInstallApprovedWhileChromeStartsAgain(t *testing.T) {
	s := pitstoptest.NewServers(t, "156.0.8078.48", "156.0.8078.74")
	engine := newEngine(t, s)
	in := setup(t, engine, "156.0.8078.48")
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(in.App(), "version.dll"), "1.19.0")
	u, _ := engine.CheckChrome(context.Background(), in)
	closeChrome := running(engine)
	if o, err := engine.UpdateChrome(context.Background(), u); o != Pending || err != nil {
		t.Fatalf("UpdateChrome = %v, %v", o, err)
	}
	engine.SetPolicy(install.Ask)
	engine.UpdateState(func(s *install.State) { s.Approved = u.Version })
	checks := 0
	engine.Hooks.Running = func(dir string) ([]chromeproc.Process, error) {
		if checks++; checks == 1 {
			return nil, nil
		}
		return []chromeproc.Process{{PID: 42, Path: filepath.Join(dir, "chrome.exe")}}, nil
	}
	if v, err := engine.InstallApproved(context.Background()); v != "" || err != nil {
		t.Fatalf("InstallApproved = %q, %v", v, err)
	}
	if in, _ := engine.Open(); in.Chrome != "156.0.8078.48" || in.Staged != u.Version || in.State.Approved != "" {
		t.Fatalf("Chrome %s, staged %q, approval %q", in.Chrome, in.Staged, in.State.Approved)
	}
	closeChrome()
	if v, err := engine.InstallApproved(context.Background()); v != u.Version || err != nil {
		t.Fatalf("InstallApproved once Chrome closed = %q, %v", v, err)
	}
	if in, _ := engine.Open(); in.Chrome != u.Version || in.Staged != "" || in.State.Approved != "" {
		t.Fatalf("Chrome %s, staged %q, approval %q", in.Chrome, in.Staged, in.State.Approved)
	}
}

func TestInstallApprovedDropsAnOvertakenApproval(t *testing.T) {
	for _, approved := range []string{"155.0.8059.80", "155.0.8059.85"} {
		_, engine, _ := milestoneUpdate(t)
		engine.UpdateState(func(s *install.State) { s.Approved = approved })
		if v, err := engine.InstallApproved(context.Background()); v != "" || err != nil {
			t.Fatalf("approved %s: InstallApproved = %q, %v", approved, v, err)
		}
		if in, _ := engine.Open(); in.Chrome != "155.0.8059.85" || in.State.Approved != "" {
			t.Errorf("approved %s: Chrome %s, approval %q", approved, in.Chrome, in.State.Approved)
		}
	}
}
