package main

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/pitstop/internal/background"
	"github.com/Bush2021/pitstop/internal/chromeproc"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/pitstop"
	"github.com/Bush2021/pitstop/internal/pitstop/pitstoptest"
)

// Each answer to an offer of Chrome++ 1.18.3 over 1.18.2, with Chrome
// running, leaves the install in its own state.
func TestFlyoutAnswers(t *testing.T) {
	for i, tc := range []struct {
		button string
		want   string
	}{
		{"Restart and update", "Chrome++ 1.18.3, approved \"\", skipped [], started true"},
		{"Later", "Chrome++ 1.18.2, approved \"1.18.3\", skipped [], started false"},
		{"Skip this version", "Chrome++ 1.18.2, approved \"\", skipped [1.18.3], started false"},
	} {
		s := pitstoptest.NewServers(t, "156.0.8078.48")
		engine, err := pitstop.New(filepath.Join(t.TempDir(), "Chrome"), s.Client())
		if err != nil {
			t.Fatal(err)
		}
		engine.Omaha.Endpoint = s.Endpoint()
		engine.GitHub.Base = s.URL
		engine.Hooks.Unpack = pitstoptest.Unpack(t)
		engine.Hooks.Verify = func(string, string) error { return nil }
		ctx := context.Background()
		if err := engine.Setup(ctx, pitstop.SetupOptions{Channel: omaha.Stable, Arch: "x64"}); err != nil {
			t.Fatal(err)
		}
		pitstoptest.WriteChromePlus(t, filepath.Join(engine.Root, "App", "version.dll"), "1.18.2")
		in, _ := engine.Open()
		p, err := engine.CheckChromePlus(ctx, in)
		if err != nil || p == nil {
			t.Fatalf("CheckChromePlus = %+v, %v", p, err)
		}
		var running, started atomic.Bool
		running.Store(true)
		engine.Hooks.Running = func(dir string) ([]chromeproc.Process, error) {
			if running.Load() {
				return []chromeproc.Process{{PID: 42, Path: filepath.Join(dir, "chrome.exe")}}, nil
			}
			return nil, nil
		}
		engine.Hooks.Close = func(string) error {
			running.Store(false)
			return nil
		}
		engine.Hooks.Wait = func(context.Context, string) error { return nil }
		engine.Hooks.Start = func(string) error {
			started.Store(true)
			return nil
		}
		n := &notifier{root: engine.Root, r: &background.Runner{Engine: engine}, asked: &background.Offer{Name: "Chrome++ Next", Version: "1.18.3", Plus: p}}
		tt := ui.NewTester(n.view, 420, 160)
		for _, s := range []string{"Chrome++ Next 1.18.3 is available", "Restart and update", "Later", "Skip this version"} {
			if !tt.HasText(s) {
				t.Fatalf("the flyout shows %q", tt.Texts())
			}
		}
		if r, ok := tt.Find("Skip this version"); !ok || r.X+r.W > 420-16 {
			t.Fatalf("Skip this version at %+v in a flyout 420 wide", r)
		}
		if dir := os.Getenv("PITSTOP_SHOTS"); dir != "" && i == 0 {
			f, _ := os.Create(filepath.Join(dir, "flyout.png"))
			png.Encode(f, tt.Image())
			f.Close()
		}
		if err := tt.Click(tc.button); err != nil {
			t.Fatal(err)
		}
		var got string
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && got != tc.want; time.Sleep(10 * time.Millisecond) {
			if in, err := install.Open(engine.Root); err == nil {
				got = fmt.Sprintf("Chrome++ %s, approved %q, skipped %v, started %v", in.ChromePlus, in.State.ApprovedPlus, in.State.Skipped, started.Load())
			}
		}
		if got != tc.want || n.asked != nil {
			t.Errorf("%s: %s, still asking: %v; want %s", tc.button, got, n.asked != nil, tc.want)
		}
	}
}

func TestFlyoutOffersToStartChromeAfterAnInstall(t *testing.T) {
	n := &notifier{status: "Chrome 157.0.8100.10, Chrome++ Next 1.19.1 is ready", start: true}
	tt := ui.NewTester(n.view, 420, 160)
	for _, s := range []string{"Start Chrome", "Open Pitstop"} {
		if !tt.HasText(s) {
			t.Fatalf("the flyout shows %q", tt.Texts())
		}
	}
	if dir := os.Getenv("PITSTOP_SHOTS"); dir != "" {
		f, _ := os.Create(filepath.Join(dir, "flyout-installed.png"))
		png.Encode(f, tt.Image())
		f.Close()
	}
}

// The flyout is as small as openFlyout makes it, and a long error, as a
// path makes it, keeps the button in view.
func TestFlyoutFitsALongError(t *testing.T) {
	n := &notifier{status: `an interrupted change (chromeUpdated) could not be finished: rename D:\Chrome\123\.pitstop\staging\chrome-158.0.8127.2\App\chrome_proxy.exe D:\Chrome\123\App\chrome_proxy.exe: The process cannot access the file because it is being used by another process.`}
	tt := ui.NewTester(n.view, 420, 160)
	if dir := os.Getenv("PITSTOP_SHOTS"); dir != "" {
		f, _ := os.Create(filepath.Join(dir, "flyout-error.png"))
		png.Encode(f, tt.Image())
		f.Close()
	}
	r, ok := tt.Find("Open Pitstop")
	if !ok || r.Y+r.H > 160-16 {
		t.Fatalf("Open Pitstop at %+v in a flyout 160 high", r)
	}
}
