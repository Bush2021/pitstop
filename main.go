// Pitstop installs, updates and configures a portable Google Chrome with
// Chrome++ Next. Place pitstop.exe beside App, Data and Cache.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"

	"github.com/Bush2021/pitstop/internal/gui"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/internal/pitstop"
	"github.com/Bush2021/pitstop/locales"
)

func defaultRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func picked(paths []string, err error) string {
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func showTheme(name string) {
	switch name {
	case gui.ThemeLight:
		mygo.Theme.SetSource(mygo.ThemeLight)
	case gui.ThemeDark:
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func main() {
	root := flag.String("root", defaultRoot(), "the portable Chrome's folder")
	bg := flag.Bool("background", false, "check for updates while Chrome runs, as Chrome++ starts Pitstop")
	flag.Parse()
	engine, err := pitstop.New(*root, &http.Client{Transport: netproxy.Transport()})
	if err != nil {
		log.Fatal(err)
	}
	// Chrome++ starts Pitstop with Chrome's version folder as the working
	// directory, which then can't move into rollback while Pitstop runs.
	if err := os.Chdir(engine.Root); err != nil {
		log.Fatal(err)
	}
	// Pitstop's own settings travel with the install, as its state does.
	own := filepath.Join(install.Meta(engine.Root), "prefs")
	mygo.App.SetPath(mygo.PathUserData, own)
	mygo.App.SetPath(mygo.PathCache, filepath.Join(own, "cache"))
	configureUpdates()
	if *bg {
		runBackground(engine)
		return
	}
	if !firstWindow(engine.Root) {
		return
	}
	mygo.Use(native.New(updater.Options{DisableAutomaticChecks: true}))

	mygo.App.WhenReady(func() {
		var win *mygo.Window
		theme, _ := os.ReadFile(filepath.Join(own, "theme"))
		showTheme(strings.TrimSpace(string(theme)))
		// SetLocale saves the choice here, but mygo has no way to read it back.
		language, _ := os.ReadFile(filepath.Join(own, "locale"))
		app := gui.New(engine, mygo.App.Version(), gui.Host{
			Post:        func(fn func()) { win.Update(fn) },
			Language:    strings.TrimSpace(string(language)),
			SetLanguage: mygo.App.SetLocale,
			Theme:       strings.TrimSpace(string(theme)),
			SetTheme: func(name string) {
				showTheme(name)
				if err := os.WriteFile(filepath.Join(own, "theme"), []byte(name), 0o644); err != nil {
					log.Print(err)
				}
			},
			PickFolder: func(title, start string) string {
				return picked(mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: title, DefaultPath: start, Directory: true}))
			},
			PickProgram: func(title, filter string) string {
				return picked(mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: title,
					Filters: []mygo.FileFilter{{Name: filter, Extensions: []string{"exe", "bat", "cmd"}}}}))
			},
			OpenFolder:      func(path string) { mygo.Shell.OpenPath(path) },
			CheckForUpdates: updater.CheckForUpdates,
		})
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:     locales.AppTitle(),
			Width:     1000,
			Height:    720,
			MinWidth:  760,
			MinHeight: 520,
			StateKey:  "main",
			Vibrancy:  mygo.VibrancyMica,
			Content:   ui.View(app.View),
		})
		keepKeys(win, app.Recording)
		app.Watch()
		mygo.App.OnSecondInstance(func([]string, string) {
			if win.IsMinimized() {
				win.Restore()
			}
			win.Focus()
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// firstWindow reports whether no window of the install at root is open, and
// otherwise brings that one forward. mygo's lock goes by the app's name, and
// each install has a window of its own, so the name holds the root while the
// lock is taken. The background mode stays out of the lock.
func firstWindow(root string) bool {
	name := mygo.App.Name()
	defer mygo.App.SetName(name)
	mygo.App.SetName(name + " " + strings.ToLower(root))
	return mygo.App.RequestSingleInstanceLock()
}
