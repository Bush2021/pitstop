package options

import (
	"slices"
	"strings"
	"testing"

	"github.com/Bush2021/pitstop/internal/ini"
	"github.com/Bush2021/pitstop/internal/install"
	"github.com/Bush2021/pitstop/locales"
)

// The schema must match the chrome++.ini Chrome++ ships: every option is
// there, and the file's values are the defaults, but for the titles it
// adds to the built-in list. Pitstop sets managed_updates itself, so it is
// not an option.
func TestSchemaMatchesTheShippedFile(t *testing.T) {
	f, err := ini.Load("../defaultini/chrome++.ini")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, o := range All {
		if seen[o.Section+"/"+o.Key] {
			t.Errorf("%s/%s twice", o.Section, o.Key)
		}
		seen[o.Section+"/"+o.Key] = true
		v, ok := f.Get(o.Section, o.Key)
		if !ok {
			t.Errorf("%s/%s is not in the shipped file", o.Section, o.Key)
			continue
		}
		if o.Key != "new_tab_disable_name" && v != o.Default {
			t.Errorf("%s/%s ships %q, default %q", o.Section, o.Key, v, o.Default)
		}
		if o.Label == nil || o.Label(locales.In("en")) == "" || o.Label(locales.In("zh-Hans")) == o.Label(locales.In("en")) {
			t.Errorf("%s/%s lacks a label in both languages", o.Section, o.Key)
		}
	}
	for _, sec := range []string{"general", "tabs"} {
		for _, kv := range f.Keys(sec) {
			if !seen[sec+"/"+kv.Key] && kv.Key != install.ManagedUpdatesKey {
				t.Errorf("the shipped file has %s/%s, which the schema lacks", sec, kv.Key)
			}
		}
	}
}

func TestEffective(t *testing.T) {
	byKey := map[string]Option{}
	for _, o := range All {
		byKey[o.Key] = o
	}
	for _, tc := range []struct {
		key, value string
		present    bool
		want       string
	}{
		{"keep_last_tab", "", false, "1"},
		{"keep_last_tab", " ", true, "1"},
		{"keep_last_tab", "0", true, "0"},
		{"keep_last_tab", "7", true, "1"},
		{"keep_last_tab", "yes", true, "0"},
		{"hover_tab_delay", "250", true, "250"},
		{"hover_tab_delay", "9000", true, "400"},
		{"hover_tab_delay", "-1", true, "400"},
		{"open_url_new_tab", "2", true, "2"},
		{"open_url_new_tab", "5", true, "0"},
		{"data_dir", "", true, `%app%\..\Data`},
		{"data_dir", "none", true, "none"},
	} {
		if got := byKey[tc.key].Effective(tc.value, tc.present); got != tc.want {
			t.Errorf("%s=%q: %q, want %q", tc.key, tc.value, got, tc.want)
		}
	}
}

func TestValidHotkey(t *testing.T) {
	for s, want := range map[string]bool{
		"": true, "Ctrl+Alt+B": true, "F2": true, "ctrl+pageup": true, "Win+←": true, "Ctrl+Shift+F24": true, "Alt+.": true,
		"Ctrl": false, "Ctrl+Alt": false, "Ctrl+Foo": false, "F25": false, "Ctrl+B+": true, "Ctrl+é": false,
	} {
		if ValidHotkey(s) != want {
			t.Errorf("ValidHotkey(%q) != %v", s, want)
		}
	}
	for s, want := range map[string]bool{
		"command:40015": true, "Ctrl+PageUp": true, "command:": false, "command:4x": false, "command:0": false, "": false,
	} {
		if ValidTarget(s) != want {
			t.Errorf("ValidTarget(%q) != %v", s, want)
		}
	}
}

func TestMatches(t *testing.T) {
	langs := []locales.Messages{locales.In("en"), locales.In("zh-Hans")}
	count := func(q string) (keys []string) {
		for _, o := range All {
			if o.Matches(q, langs...) {
				keys = append(keys, o.Key)
			}
		}
		return keys
	}
	if got := count("滚轮"); len(got) != 2 {
		t.Errorf("滚轮 finds %v", got)
	}
	if got := count("Scroll"); len(got) != 2 {
		t.Errorf("Scroll finds %v", got)
	}
	if got := count("hover_tab"); len(got) != 2 {
		t.Errorf("hover_tab finds %v", got)
	}
	if got := count("  "); len(got) != 0 {
		t.Errorf("blank finds %v", got)
	}
}

func TestSplitSwitches(t *testing.T) {
	v := `--a  --user-agent="x y" "--b=c d"	--e`
	want := []string{"--a", `--user-agent="x y"`, `"--b=c d"`, "--e"}
	if got := SplitSwitches(v); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
	if got := SplitSwitches(JoinSwitches(want)); !slices.Equal(got, want) {
		t.Errorf("round trip: %q", got)
	}
}

func TestCommands(t *testing.T) {
	v := `"C:\my tools\a.exe" --x; b.exe;;  ;`
	want := []string{`"C:\my tools\a.exe" --x`, " b.exe"}
	if got := SplitCommands(v); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
	if got := JoinCommands(append(want, " ")); got != `"C:\my tools\a.exe" --x; b.exe;` {
		t.Errorf("joined %s", got)
	}
}

func TestNames(t *testing.T) {
	want := []string{"新标签页", "New Tab"}
	if got := SplitNames(` "新标签页", New Tab ,, `); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
	if got := JoinNames(want); got != `"新标签页","New Tab"` {
		t.Errorf("joined %s", got)
	}
}

func TestInstallPath(t *testing.T) {
	app := `D:\Chrome\App`
	for path, want := range map[string]string{
		`D:\Chrome\App\tools\a.exe`: `%app%\tools\a.exe`,
		`D:\Chrome\Data2`:           `%app%\..\Data2`,
		`D:\Chrome`:                 `D:\Chrome`,
		`D:\Other\Data`:             `D:\Other\Data`,
		`D:\Chromium\Data`:          `D:\Chromium\Data`,
		`E:\Data`:                   `E:\Data`,
	} {
		if got := InstallPath(app, path); got != want {
			t.Errorf("InstallPath(%s) = %s, want %s", path, got, want)
		}
	}
}

func TestReferenceLists(t *testing.T) {
	en, zh := locales.In("en"), locales.In("zh-Hans")
	for name, list := range map[string][]Entry{"commands": ChromeCommands, "switches": ChromeSwitches} {
		seen := map[string]bool{}
		for _, e := range list {
			if seen[e.Value] {
				t.Errorf("%s: %s twice", name, e.Value)
			}
			seen[e.Value] = true
			if e.Label(en) == "" || e.Label(en) == e.Label(zh) || e.Group(en) == e.Group(zh) {
				t.Errorf("%s: %s reads %q and %q", name, e.Value, e.Label(en), e.Label(zh))
			}
		}
	}
	for _, e := range ChromeCommands {
		if !ValidTarget("command:" + e.Value) {
			t.Errorf("command:%s isn't a target", e.Value)
		}
	}
	for _, e := range ChromeSwitches {
		if !strings.HasPrefix(e.Value, "--") {
			t.Errorf("%s isn't a switch", e.Value)
		}
	}
	if got, ok := CommandLabel("34014", zh); !ok || got == "" {
		t.Errorf("CommandLabel = %q, %v", got, ok)
	}
	if _, ok := CommandLabel("1", en); ok {
		t.Error("CommandLabel found an ID that isn't listed")
	}
	if !ChromeCommands[0].Matches("IDC_NEW_TAB") || !ChromeCommands[0].Matches("34014") || !ChromeCommands[0].Matches("新建", zh) {
		t.Error("Matches misses the first command")
	}
}

func TestChord(t *testing.T) {
	if Chord("Shift+Control+a") != Chord("ctrl+A+shift") {
		t.Error("two spellings of a shortcut differ")
	}
	if Chord("Ctrl+A") == Chord("Ctrl+Shift+A") || Chord("Ctrl+A") == Chord("Ctrl+B") {
		t.Error("two shortcuts are the same")
	}
}

func TestChromeShortcut(t *testing.T) {
	en := locales.In("en")
	if got, ok := ChromeShortcut("Shift+Ctrl+T", en); !ok || got != en.CmdRestoreTab() {
		t.Errorf("ChromeShortcut = %q, %v", got, ok)
	}
	if got, ok := ChromeShortcut("Control+PageDown", en); !ok || got != en.CmdNextTab() {
		t.Errorf("ChromeShortcut = %q, %v", got, ok)
	}
	if got, ok := ChromeShortcut("Ctrl+Alt+F9", en); ok {
		t.Errorf("ChromeShortcut found %q", got)
	}
}
