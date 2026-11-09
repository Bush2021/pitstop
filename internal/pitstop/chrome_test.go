package pitstop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Bush2021/pitstop/internal/chromeproc"
	"github.com/Bush2021/pitstop/internal/pitstop/pitstoptest"
)

func chromeEngine(t *testing.T) (*Engine, *bool) {
	t.Helper()
	engine := newEngine(t, pitstoptest.NewServers(t))
	running := new(bool)
	engine.Hooks.Running = func(string) ([]chromeproc.Process, error) {
		if *running {
			return []chromeproc.Process{{PID: 1}}, nil
		}
		return nil, nil
	}
	engine.Hooks.Close = func(string) error { return nil }
	engine.Hooks.Wait = func(context.Context, string) error { return nil }
	engine.Hooks.Start = func(string) error { return nil }
	return engine, running
}

func TestRestartChrome(t *testing.T) {
	engine, running := chromeEngine(t)
	pitstoptest.WriteBuild(t, engine.path(app), "156.0.8078.48")
	*running = true
	var did []string
	engine.Hooks.Close = func(dir string) error {
		did = append(did, "close "+dir)
		*running = false
		return nil
	}
	engine.Hooks.Wait = func(_ context.Context, dir string) error {
		did = append(did, "wait "+dir)
		return nil
	}
	engine.Hooks.Start = func(exe string) error {
		did = append(did, "start "+exe)
		return nil
	}
	var phases []Phase
	engine.Progress = func(s Step) { phases = append(phases, s.Phase) }
	if err := engine.RestartChrome(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := engine.path(app)
	if want := []string{"close " + dir, "wait " + dir, "start " + filepath.Join(dir, "chrome.exe")}; !slices.Equal(did, want) {
		t.Errorf("did %q, want %q", did, want)
	}
	if !slices.Contains(phases, Closing) {
		t.Errorf("phases %q", phases)
	}
}

func TestChromeThatStaysIsNotStartedAgain(t *testing.T) {
	engine, running := chromeEngine(t)
	*running = true
	engine.Hooks.Wait = func(context.Context, string) error { return context.DeadlineExceeded }
	engine.Hooks.Start = func(string) error {
		t.Error("started a second Chrome")
		return nil
	}
	if err := engine.RestartChrome(context.Background()); !errors.Is(err, ErrChromeRunning) {
		t.Errorf("err = %v", err)
	}
}

// Chrome running as administrator ignores Close, so the user is told at
// once instead of after closeWait.
func TestElevatedChromeIsLeftToTheUser(t *testing.T) {
	engine, running := chromeEngine(t)
	*running = true
	engine.Hooks.Close = func(string) error { return chromeproc.ErrElevated }
	engine.Hooks.Wait = func(context.Context, string) error {
		t.Error("waited for a Chrome that won't close")
		return nil
	}
	engine.Hooks.Start = func(string) error {
		t.Error("started a second Chrome")
		return nil
	}
	err := engine.RestartChrome(context.Background())
	if r, ok := errors.AsType[*RunningError](err); !ok || !r.Elevated {
		t.Errorf("err = %v", err)
	}
}

func TestClosingChromeCanBeCanceled(t *testing.T) {
	engine, running := chromeEngine(t)
	*running = true
	ctx, cancel := context.WithCancel(context.Background())
	engine.Hooks.Wait = func(ctx context.Context, _ string) error {
		cancel()
		return ctx.Err()
	}
	if err := engine.CloseChrome(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestDataFolder(t *testing.T) {
	engine, _ := chromeEngine(t)
	sep := string(filepath.Separator)
	abs := filepath.Join(t.TempDir(), "Profile")
	t.Setenv("PITSTOP_TEST_DIR", filepath.Dir(abs))
	for _, c := range []struct {
		value, want string
		ok          bool
	}{
		{"", engine.path(data), true},
		{"none", "", false},
		{"%app%" + sep + ".." + sep + "Data", engine.path(data), true},
		{abs, abs, true},
		{"%PITSTOP_TEST_DIR%" + sep + "Profile", abs, true},
		{"Profile", "", false},
		{"%PITSTOP_NO_SUCH_DIR%" + sep + "Profile", "", false},
	} {
		got, ok := engine.DataFolder(c.value)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("DataFolder(%q) = %q, %v", c.value, got, ok)
		}
	}
}

func TestMovableData(t *testing.T) {
	dir := t.TempDir()
	p := func(name string) string { return filepath.Join(dir, name) }
	writeFiles(t, p("from"), map[string]string{"Local State": "1", "Default/History": "1"})
	writeFiles(t, p("taken"), map[string]string{"Local State": "2"})
	for _, name := range []string{"empty", "unused"} {
		if err := os.Mkdir(p(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"from", "missing", true},
		{"from", "empty", true},
		{"from", "taken", false},
		{"missing", "other", false},
		{"unused", "empty", false},
		{"from", "from", false},
		{"from", filepath.Join("from", "inner"), false},
		{filepath.Join("from", "Default"), "from", false},
	} {
		if got := MovableData(p(c.from), p(c.to)); got != c.want {
			t.Errorf("MovableData(%s, %s) = %v", c.from, c.to, got)
		}
	}
	if !MovableData(p("from"), p("from-too")) {
		t.Error("a sibling with the same prefix counts as inside")
	}
}

func TestMoveData(t *testing.T) {
	for name, make := range map[string]func(string) error{
		"to a new folder":      func(string) error { return nil },
		"into an empty folder": func(to string) error { return os.Mkdir(to, 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			engine, _ := chromeEngine(t)
			dir := t.TempDir()
			from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
			held := map[string]string{"Local State": "1", "Default/Cache/data_0": "2"}
			writeFiles(t, from, held)
			if err := make(to); err != nil {
				t.Fatal(err)
			}
			if err := engine.MoveData(context.Background(), from, to); err != nil {
				t.Fatal(err)
			}
			if got := files(t, to); len(got) != 2 || got["Default/Cache/data_0"] != "2" {
				t.Errorf("to holds %q", got)
			}
			if exists(from) {
				t.Error("from is still there")
			}
		})
	}
}

func TestMoveDataWaitsForChromeToClose(t *testing.T) {
	engine, running := chromeEngine(t)
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	writeFiles(t, from, map[string]string{"Local State": "1"})
	*running = true
	if err := engine.MoveData(context.Background(), from, to); !errors.Is(err, ErrChromeRunning) {
		t.Errorf("err = %v", err)
	}
	if len(files(t, from)) != 1 || exists(to) {
		t.Error("the move went ahead")
	}
}

func TestMoveDataRefusesAFolderInUse(t *testing.T) {
	engine, _ := chromeEngine(t)
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	writeFiles(t, from, map[string]string{"Local State": "1"})
	writeFiles(t, to, map[string]string{"Local State": "2"})
	if err := engine.MoveData(context.Background(), from, to); err == nil {
		t.Error("moved over a folder in use")
	}
	if files(t, from)["Local State"] != "1" || files(t, to)["Local State"] != "2" {
		t.Error("the move touched the folders")
	}
}

// The move across volumes keeps every file, caches included: it's the only copy.
func TestMoveByCopy(t *testing.T) {
	engine, _ := chromeEngine(t)
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	writeFiles(t, from, map[string]string{"Local State": "1", "Default/History": "2", "Default/Code Cache/js/x": "3"})
	if err := engine.moveByCopy(context.Background(), from, to); err != nil {
		t.Fatal(err)
	}
	if got := files(t, to); len(got) != 3 || got["Default/Code Cache/js/x"] != "3" {
		t.Errorf("to holds %q", got)
	}
	if exists(from) || exists(to+".partial") {
		t.Errorf("left behind: %q", entries(t, dir))
	}
}

func TestMoveByCopyLeavesTheFolderIfItStops(t *testing.T) {
	stops := map[string]func(t *testing.T, running *bool) context.Context{
		"canceled": func(*testing.T, *bool) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		},
		"Chrome started": func(_ *testing.T, running *bool) context.Context {
			*running = true
			return context.Background()
		},
	}
	for name, stop := range stops {
		t.Run(name, func(t *testing.T) {
			engine, running := chromeEngine(t)
			dir := t.TempDir()
			from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
			writeFiles(t, from, map[string]string{"Local State": "1"})
			if err := engine.moveByCopy(stop(t, running), from, to); err == nil {
				t.Fatal("the move went through")
			}
			if files(t, from)["Local State"] != "1" {
				t.Error("from lost its files")
			}
			if exists(to) || exists(to+".partial") {
				t.Errorf("left behind: %q", entries(t, dir))
			}
		})
	}
}
