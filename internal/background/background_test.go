package background

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
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/pitstop"
	"github.com/Bush2021/pitstop/internal/pitstop/pitstoptest"
)

const every = install.DefaultCheckHours * time.Hour

type fakeUI struct {
	r     *Runner
	calls []string
	// approve answers every question with Approve.
	approve bool
}

func (u *fakeUI) Ask(o Offer) {
	u.calls = append(u.calls, "ask "+o.Version)
	if u.approve {
		u.r.Approve(context.Background(), o)
	}
}
func (u *fakeUI) Installed(what string) { u.calls = append(u.calls, "installed "+what) }
func (u *fakeUI) Failed(what string, _ error) {
	u.calls = append(u.calls, "failed "+what)
}

type world struct {
	s       *pitstoptest.Servers
	engine  *pitstop.Engine
	r       *Runner
	ui      *fakeUI
	now     time.Time
	metered bool
	chrome  bool
	// waits are what Wait returns, in turn: nil once Chrome exits,
	// context.DeadlineExceeded once it is time to check again.
	waits     []error
	deadlines []time.Duration
}

// newWorld sets up Chrome at version with a Chrome++ that finishes in-use
// updates, under the policy, while Chrome runs. offered are the builds on
// offer.
func newWorld(t *testing.T, version string, p install.Policy, offered ...string) *world {
	t.Helper()
	s := pitstoptest.NewServers(t, offered...)
	engine, err := pitstop.New(filepath.Join(t.TempDir(), "Chrome"), s.Client())
	if err != nil {
		t.Fatal(err)
	}
	engine.Omaha.Endpoint = s.Endpoint()
	engine.GitHub.Base = s.URL
	engine.Hooks.Unpack = pitstoptest.Unpack(t)
	engine.Hooks.Verify = func(string, string) error { return nil }
	if err := engine.Setup(context.Background(), pitstop.SetupOptions{Channel: omaha.Stable, Arch: "x64", Target: version}); err != nil {
		t.Fatal(err)
	}
	pitstoptest.WriteManagedChromePlus(t, filepath.Join(engine.Root, "App", "version.dll"), "1.19.0")
	if err := engine.SetPolicy(p); err != nil {
		t.Fatal(err)
	}

	w := &world{s: s, engine: engine, now: time.Now().UTC().Truncate(time.Second), chrome: true}
	engine.Hooks.Running = func(dir string) ([]chromeproc.Process, error) {
		if w.chrome {
			return []chromeproc.Process{{PID: 42, Path: filepath.Join(dir, "chrome.exe")}}, nil
		}
		return nil, nil
	}
	w.ui = &fakeUI{}
	w.r = &Runner{
		Engine:     engine,
		UpdateSelf: func(context.Context) error { return nil },
		UI:         w.ui,
		Conditions: func() pitstop.Conditions { return pitstop.Conditions{Now: w.now, Metered: w.metered} },
		Wait:       w.wait,
	}
	w.ui.r = w.r
	return w
}

func (w *world) wait(ctx context.Context, dir string) error {
	d, _ := ctx.Deadline()
	w.deadlines = append(w.deadlines, d.Sub(time.Now()).Round(time.Hour))
	if len(w.waits) == 0 {
		return fmt.Errorf("an unexpected wait")
	}
	err := w.waits[0]
	w.waits = w.waits[1:]
	if err == nil {
		w.chrome = false
	} else {
		w.now = w.now.Add(every)
	}
	return err
}

// olderPlus leaves Chrome++ one release behind the 1.18.3 on offer.
func (w *world) olderPlus(t *testing.T) {
	t.Helper()
	pitstoptest.WriteChromePlus(t, filepath.Join(w.engine.Root, "App", "version.dll"), "1.18.2")
}

func (w *world) run(t *testing.T, waits ...error) {
	t.Helper()
	w.waits = waits
	if err := w.r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (w *world) open(t *testing.T) *install.Install {
	t.Helper()
	in, err := w.engine.Open()
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestPatchIsStagedAndGoesInOfflineOnceChromeExits(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Automatic, "156.0.8078.48", "156.0.8078.74")
	var staged string
	var gets int32
	w.r.Wait = func(ctx context.Context, dir string) error {
		staged, gets = w.open(t).Staged, w.s.Gets.Load()
		return w.wait(ctx, dir)
	}
	w.run(t, nil)
	if staged != "156.0.8078.74" {
		t.Fatalf("staged %q while Chrome ran", staged)
	}
	in := w.open(t)
	if in.Chrome != "156.0.8078.74" || in.Staged != "" || w.s.Gets.Load() != gets {
		t.Fatalf("Chrome %s, staged %q, after %d more downloads", in.Chrome, in.Staged, w.s.Gets.Load()-gets)
	}
	if got := fmt.Sprint(w.ui.calls); got != "[installed Chrome 156.0.8078.74]" {
		t.Errorf("told %s", got)
	}
	if !in.State.Checked.Equal(w.now) {
		t.Errorf("checked at %s, want %s", in.State.Checked, w.now)
	}
}

func TestApprovedMilestoneGoesInOnceChromeExits(t *testing.T) {
	w := newWorld(t, "155.", install.Ask, "155.0.8059.85", "156.0.8078.74")
	w.ui.approve = true
	w.run(t, nil)
	in := w.open(t)
	if in.Chrome != "156.0.8078.74" || in.State.Approved != "" {
		t.Fatalf("Chrome %s, approved %q", in.Chrome, in.State.Approved)
	}
	if got := fmt.Sprint(w.ui.calls); got != "[ask 156.0.8078.74 installed Chrome 156.0.8078.74]" {
		t.Errorf("told %s", got)
	}
}

func TestUnansweredQuestionDownloadsNothing(t *testing.T) {
	w := newWorld(t, "155.", install.Ask, "155.0.8059.85", "156.0.8078.74")
	gets := w.s.Gets.Load()
	w.run(t, nil)
	if in := w.open(t); in.Chrome != "155.0.8059.85" || w.s.Gets.Load() != gets {
		t.Fatalf("Chrome %s, after %d downloads", in.Chrome, w.s.Gets.Load()-gets)
	}
}

func TestAsksOncePerVersion(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48", "156.0.8078.74")
	w.run(t, context.DeadlineExceeded, nil)
	if got := fmt.Sprint(w.ui.calls); got != "[ask 156.0.8078.74]" {
		t.Errorf("told %s", got)
	}
}

func TestAsksOneQuestionPerCheck(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48", "156.0.8078.74")
	w.olderPlus(t)
	w.run(t, context.DeadlineExceeded, nil)
	if got := fmt.Sprint(w.ui.calls); got != "[ask 156.0.8078.74 ask 1.18.3]" {
		t.Errorf("told %s", got)
	}
}

func TestAutomaticAsksOnAMeteredNetwork(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Automatic, "156.0.8078.48", "156.0.8078.74")
	w.metered = true
	gets := w.s.Gets.Load()
	w.run(t, nil)
	if got := fmt.Sprint(w.ui.calls); got != "[ask 156.0.8078.74]" || w.s.Gets.Load() != gets {
		t.Errorf("told %s, after %d downloads", got, w.s.Gets.Load()-gets)
	}
}

func TestChecksEveryInterval(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48", "156.0.8078.74")
	w.engine.UpdateState(func(s *install.State) { s.Checked = w.now.Add(-time.Hour) })
	start := w.now
	w.run(t, context.DeadlineExceeded, nil)
	if got := fmt.Sprint(w.deadlines); got != "[2h0m0s 3h0m0s]" {
		t.Errorf("waited %s", got)
	}
	if in := w.open(t); !in.State.Checked.Equal(start.Add(every)) {
		t.Errorf("checked at %s", in.State.Checked)
	}
	if got := fmt.Sprint(w.ui.calls); got != "[ask 156.0.8078.74]" {
		t.Errorf("told %s", got)
	}
}

func TestChecksAtTheChosenInterval(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.engine.UpdateState(func(s *install.State) { s.CheckHours = 24 })
	w.run(t, nil)
	if got := fmt.Sprint(w.deadlines); got != "[24h0m0s]" {
		t.Errorf("waited %s", got)
	}
}

func TestChecksSayTheyRunInTheBackground(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.run(t, nil)
	if w.s.Interactivity != "bg" {
		t.Errorf("X-Goog-Update-Interactivity %q", w.s.Interactivity)
	}
}

func TestRetryAfterHoldsOffChecks(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.s.RetryAfter = "36000"
	start := w.now
	w.run(t, context.DeadlineExceeded, nil)
	// 10 hours, then the 7 left after the 3 the first wait took.
	if got := fmt.Sprint(w.deadlines); got != "[10h0m0s 7h0m0s]" {
		t.Errorf("waited %s", got)
	}
	in := w.open(t)
	if !in.State.Quiet.Equal(start.Add(10*time.Hour)) || !in.State.Checked.Equal(start) {
		t.Errorf("quiet until %s, checked at %s", in.State.Quiet, in.State.Checked)
	}
}

// A clock set back leaves a wait longer than the protocol allows; it
// doesn't stop the checks.
func TestOverlongQuietIsIgnored(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.engine.UpdateState(func(s *install.State) { s.Quiet = w.now.Add(48 * time.Hour) })
	w.run(t, nil)
	if in := w.open(t); !in.State.Checked.Equal(w.now) {
		t.Errorf("checked at %s", in.State.Checked)
	}
}

func TestPostponedCheck(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.r.Postpone = func(period time.Duration) time.Duration { return period / 3 }
	w.run(t, nil)
	if got := fmt.Sprint(w.deadlines); got != "[4h0m0s]" {
		t.Errorf("waited %s", got)
	}
}

func TestPostpone(t *testing.T) {
	const period = 3 * time.Hour
	var later int
	for range 2000 {
		d := Postpone(period)
		if d < 0 || d >= period/5 {
			t.Fatalf("postponed by %s", d)
		}
		if d > 0 {
			later++
		}
	}
	// One in five, give or take.
	if later < 300 || later > 500 {
		t.Errorf("%d of 2000 postponed", later)
	}
}

func TestFailedCheckRetriesInAnHour(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.engine.GitHub.Base = w.s.URL + "/nowhere"
	w.run(t, nil)
	if got := fmt.Sprint(w.deadlines); got != "[1h0m0s]" {
		t.Errorf("waited %s", got)
	}
	if in := w.open(t); !in.State.Checked.IsZero() {
		t.Errorf("checked at %s", in.State.Checked)
	}
}

func TestChromeStartingAgainKeepsItRunning(t *testing.T) {
	w := newWorld(t, "155.", install.Ask, "155.0.8059.85", "156.0.8078.74")
	w.ui.approve = true
	w.r.UI = &restarting{w.ui, w}
	w.run(t, nil, nil)
	if len(w.waits) != 0 {
		t.Fatalf("quit with %d waits left", len(w.waits))
	}
}

// restarting starts Chrome again as soon as the install is done.
type restarting struct {
	*fakeUI
	w *world
}

func (r *restarting) Installed(what string) {
	r.fakeUI.Installed(what)
	r.w.chrome = true
}

// Without managed updates, Pitstop installs the build once Chrome exits.
func TestAutomaticMilestoneGoesInOnceChromeExits(t *testing.T) {
	w := newWorld(t, "155.", install.Automatic, "155.0.8059.85", "156.0.8078.74")
	w.run(t, nil)
	if in := w.open(t); in.Chrome != "156.0.8078.74" || fmt.Sprint(w.ui.calls) != "[installed Chrome 156.0.8078.74]" {
		t.Fatalf("Chrome %s, told %v", in.Chrome, w.ui.calls)
	}
}

func TestApprovedChromePlusGoesInOfflineOnceChromeExits(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.olderPlus(t)
	w.ui.approve = true
	var gets int32
	w.r.Wait = func(ctx context.Context, dir string) error {
		w.engine.GitHub.Base, gets = "http://127.0.0.1:1", w.s.Gets.Load()
		return w.wait(ctx, dir)
	}
	w.run(t, nil)
	in := w.open(t)
	if e := in.State.History[0]; e.Kind != install.PlusUpdated || e.To != "1.18.3" || in.State.ApprovedPlus != "" || w.s.Gets.Load() != gets {
		t.Fatalf("history starts with %+v, approved %q, after %d more downloads", e, in.State.ApprovedPlus, w.s.Gets.Load()-gets)
	}
	if got := fmt.Sprint(w.ui.calls); got != "[ask 1.18.3 installed Chrome++ Next 1.18.3]" {
		t.Errorf("told %s", got)
	}
}

func TestOneNotificationCoversBothInstalls(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Automatic, "156.0.8078.48", "156.0.8078.74")
	w.olderPlus(t)
	w.run(t, nil)
	if got := fmt.Sprint(w.ui.calls); got != "[installed Chrome 156.0.8078.74, Chrome++ Next 1.18.3]" {
		t.Errorf("told %s", got)
	}
}

func TestFailedChromePlusDownloadIsKept(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.olderPlus(t)
	w.ui.approve = true
	// A file where the downloads go.
	cache := filepath.Join(w.engine.Root, ".pitstop", "cache", "chromeplus")
	os.RemoveAll(cache)
	if err := os.WriteFile(cache, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w.run(t, nil)
	in := w.open(t)
	f := in.State.Failure
	if f == nil || f.What != "Chrome++ Next 1.18.3" || !f.Time.Equal(w.now) || f.Error == "" || in.State.ApprovedPlus != "1.18.3" || in.ChromePlus != "1.18.2" {
		t.Fatalf("failure %+v, approved %q, Chrome++ %s", f, in.State.ApprovedPlus, in.ChromePlus)
	}
}

func TestRestartInstallsTheOfferNow(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48")
	w.olderPlus(t)
	p, err := w.engine.CheckChromePlus(context.Background(), w.open(t))
	if err != nil || p == nil {
		t.Fatalf("CheckChromePlus = %+v, %v", p, err)
	}
	w.engine.Hooks.Close = func(string) error {
		w.chrome = false
		return nil
	}
	w.engine.Hooks.Wait = func(context.Context, string) error { return nil }
	var started string
	w.engine.Hooks.Start = func(string) error {
		started, w.chrome = w.open(t).ChromePlus, true
		return nil
	}
	if err := w.r.Restart(context.Background(), Offer{Name: "Chrome++ Next", Version: "1.18.3", Plus: p}); err != nil {
		t.Fatal(err)
	}
	if in := w.open(t); started != "1.18.3" || in.State.ApprovedPlus != "" {
		t.Fatalf("started Chrome with Chrome++ %q, approved %q", started, in.State.ApprovedPlus)
	}
}

func TestFailedDownloadIsKept(t *testing.T) {
	w := newWorld(t, "155.", install.Automatic, "155.0.8059.85", "156.0.8078.74")
	w.engine.Hooks.Verify = func(string, string) error { return errors.New("bad signature") }
	w.run(t, nil)
	f := w.open(t).State.Failure
	if f == nil || f.What != "Chrome 156.0.8078.74" || !strings.Contains(f.Error, "bad signature") {
		t.Fatalf("failure %+v", f)
	}
}

// A clock set back leaves the last check in the future; it doesn't stop
// the checks.
func TestCheckInTheFuture(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Ask, "156.0.8078.48", "156.0.8078.74")
	w.engine.UpdateState(func(s *install.State) { s.Checked = w.now.Add(240 * time.Hour) })
	w.run(t, nil)
	if in := w.open(t); !in.State.Checked.Equal(w.now) || len(w.ui.calls) != 1 {
		t.Fatalf("checked at %s, told %v", in.State.Checked, w.ui.calls)
	}
}

func TestOffQuits(t *testing.T) {
	w := newWorld(t, "156.0.8078.48", install.Off, "156.0.8078.48", "156.0.8078.74")
	if err := w.r.Run(context.Background()); err != nil || len(w.deadlines) > 0 {
		t.Fatalf("Run = %v after %d waits", err, len(w.deadlines))
	}
}

func TestSelfUpdateFollowsPolicyAndConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  install.Policy
		metered bool
		err     error
		want    int
	}{
		{name: "automatic", policy: install.Automatic, want: 1},
		{name: "ask", policy: install.Ask},
		{name: "off", policy: install.Off},
		{name: "metered", policy: install.Automatic, metered: true},
		{name: "failed self-update", policy: install.Automatic, err: errors.New("unavailable"), want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, "156.0.8078.74", tc.policy, "156.0.8078.74")
			w.metered = tc.metered
			calls := 0
			w.r.UpdateSelf = func(context.Context) error {
				calls++
				return tc.err
			}
			w.run(t, nil)
			if calls != tc.want {
				t.Fatalf("self-update calls = %d, want %d", calls, tc.want)
			}
			if tc.policy != install.Off && !w.open(t).State.Checked.Equal(w.now) {
				t.Fatal("the Chrome update check did not finish")
			}
		})
	}
}

func TestSkip(t *testing.T) {
	w := newWorld(t, "155.", install.Ask, "155.0.8059.85", "156.0.8078.74")
	o := Offer{Name: "Chrome", Version: "156.0.8078.74"}
	w.engine.UpdateState(func(s *install.State) { s.Approved = o.Version })
	if err := w.r.Skip(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	w.r.Skip(context.Background(), o)
	w.run(t, nil)
	in := w.open(t)
	if fmt.Sprint(in.State.Skipped) != "[156.0.8078.74]" || in.State.Approved != "" || in.Chrome != "155.0.8059.85" || len(w.ui.calls) > 0 {
		t.Fatalf("skipped %v, approved %q, Chrome %s, told %v", in.State.Skipped, in.State.Approved, in.Chrome, w.ui.calls)
	}
}
