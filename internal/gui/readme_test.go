package gui

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/plugins/render"
	"github.com/egoist/mygo/ui"
)

// TestReadmeScreenshots renders the README's pictures from the app's own
// views over a fake install, into the folder PITSTOP_README_SHOTS names.
// PITSTOP_README_ROOT, an empty folder, holds the install, so that the
// pictures show a short path: a drive made with subst.
func TestReadmeScreenshots(t *testing.T) {
	dir, root := os.Getenv("PITSTOP_README_SHOTS"), os.Getenv("PITSTOP_README_ROOT")
	if dir == "" || root == "" {
		t.Skip("PITSTOP_README_SHOTS and PITSTOP_README_ROOT name no folders")
	}
	root = filepath.Join(root, "Chrome")
	t.Cleanup(func() { os.RemoveAll(root) })
	h := newHarnessIn(t, root, "158.0.8127.27")
	h.a.version = "1.3.2"
	h.setUp()
	f := readINI(t, h)
	for _, kv := range [][3]string{
		{"general", "boss_key", "Ctrl+Alt+B"},
		{"general", "translate_key", "Ctrl+Alt+T"},
		{"keymapping", "F2", "Ctrl+PageUp"},
		{"keymapping", "F3", "Ctrl+PageDown"},
		{"keymapping", "Alt+Shift+S", "command:40015"},
	} {
		if err := f.Set(kv[0], kv[1], kv[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Save(filepath.Join(h.a.in.App(), "chrome++.ini")); err != nil {
		t.Fatal(err)
	}
	h.a.loadINI()

	scenes := []struct {
		name   string
		height int
		show   func()
	}{
		{"overview", 760, func() {
			h.s.Versions = append(h.s.Versions, "158.0.8127.36")
			h.click("Check for updates")
			h.settle()
			h.click("Update to 158.0.8127.36")
			h.settle()
			// The fake builds are a few bytes; the kept one gets a real
			// build's size, without taking the room.
			filler := filepath.Join(root, ".pitstop", "rollback", "chrome-158.0.8127.27", "158.0.8127.27", "resources.pak")
			if err := os.Truncate(filler, 515_000_000); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			} else if os.IsNotExist(err) {
				if err := os.WriteFile(filler, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(filler, 515_000_000); err != nil {
					t.Fatal(err)
				}
			}
			h.a.reload()
			h.s.Versions = append(h.s.Versions, "158.0.8127.37")
			h.click("Check for updates")
			h.settle()
			h.want("158.0.8127.37 is available", "Chrome 158.0.8127.27 is kept")
		}},
		{"keyboard", 540, func() {
			h.click("Keyboard")
			h.want("Boss key", "PageDown")
		}},
		{"review", 640, func() {
			h.click("Tabs & mouse")
			h.click("Activate a tab by resting the pointer on it")
			h.click("Advanced")
			h.click("Ignore enterprise policies")
			h.click("Review")
			h.want("Review changes to chrome++.ini")
		}},
	}
	t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	for _, s := range scenes {
		// The clicks find their labels in English.
		i18n.SetLocaleFunc(func() string { return "en" })
		s.show()
		for _, loc := range [][2]string{{"en", "en"}, {"zh-Hans", "zh-CN"}} {
			i18n.SetLocaleFunc(func() string { return loc[0] })
			for _, dark := range []bool{false, true} {
				// One second in, so that the page has finished coming in.
				comp := render.Composition{
					Width: 2000, Height: 2 * s.height, Scale: 2, FPS: 30, Frames: 31,
					Dark: dark, Locale: loc[0],
					View: func(c *ui.Context, _ render.Frame) { h.a.View(c) },
				}
				img, err := render.Still(comp, 30, render.Options{})
				if err != nil {
					t.Fatal(err)
				}
				theme := "light"
				if dark {
					theme = "dark"
				}
				out, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%s-%s.png", s.name, loc[1], theme)))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(out, img)
				if cerr := out.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
