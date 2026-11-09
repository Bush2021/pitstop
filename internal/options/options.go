// Package options describes the settings of chrome++.ini Pitstop edits:
// their sections, keys, kinds, defaults and ranges, as Chrome++'s
// config.cc reads them, and their text in each language.
package options

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/Bush2021/pitstop/locales"
)

type Text = func(locales.Messages) string

type Kind int

const (
	// Switch is 0 or 1, which Chrome++ reads as an integer: anything but 0
	// is on.
	Switch Kind = iota
	Select
	Number
	Hotkey
	// Folder is a path: "none" leaves it to Chrome, and empty means Default.
	Folder
	NameList
	SwitchList
	CommandList
)

type Page int

const (
	Tabs Page = iota
	Keyboard
	Startup
	Advanced
)

type Option struct {
	Section, Key string
	Kind         Kind
	// Default is what Chrome++ uses when the key is missing or empty.
	Default     string
	Min, Max    int
	Choices     []Choice
	Page        Page
	Group       Text
	Label, Help Text
	// Applies says whether the option does anything, given the others'
	// values. Nil means it always does.
	Applies func(value Values) bool
}

// Values gives the effective value of an option by its key.
type Values func(key string) string

type Choice struct {
	Value string
	Label Text
}

var tabChoices = []Choice{
	{"0", locales.Messages.ChoiceCurrentTab},
	{"1", locales.Messages.ChoiceNewTab},
	{"2", locales.Messages.ChoiceBackgroundTab},
}

func hovering(v Values) bool { return v("hover_tab") != "0" }

// Chrome++ looks at new_tab_disable only for a click or Enter it would
// otherwise send to a new tab.
func opensNewTabs(v Values) bool {
	return v("open_url_new_tab") != "0" || v("open_bookmark_new_tab") != "0"
}

func keepsNewTab(v Values) bool { return opensNewTabs(v) && v("new_tab_disable") != "0" }

var All = []Option{
	{Section: "tabs", Key: "double_click_close", Kind: Switch, Default: "1", Page: Tabs, Group: locales.Messages.GroupClosing, Label: locales.Messages.OptDoubleClickClose},
	{Section: "tabs", Key: "right_click_close", Kind: Switch, Default: "0", Page: Tabs, Group: locales.Messages.GroupClosing, Label: locales.Messages.OptRightClickClose, Help: locales.Messages.OptRightClickCloseHelp},
	{Section: "tabs", Key: "keep_last_tab", Kind: Switch, Default: "1", Page: Tabs, Group: locales.Messages.GroupClosing, Label: locales.Messages.OptKeepLastTab},
	{Section: "tabs", Key: "wheel_tab", Kind: Switch, Default: "1", Page: Tabs, Group: locales.Messages.GroupSwitching, Label: locales.Messages.OptWheelTab},
	{Section: "tabs", Key: "wheel_tab_when_press_rbutton", Kind: Switch, Default: "1", Page: Tabs, Group: locales.Messages.GroupSwitching, Label: locales.Messages.OptWheelTabRButton, Help: locales.Messages.OptWheelTabRButtonHelp},
	{Section: "tabs", Key: "hover_tab", Kind: Switch, Default: "0", Page: Tabs, Group: locales.Messages.GroupSwitching, Label: locales.Messages.OptHoverTab},
	{Section: "tabs", Key: "hover_tab_delay", Kind: Number, Default: "400", Min: 0, Max: 5000, Page: Tabs, Group: locales.Messages.GroupSwitching, Label: locales.Messages.OptHoverTabDelay, Applies: hovering},
	{Section: "tabs", Key: "open_url_new_tab", Kind: Select, Default: "0", Choices: tabChoices, Page: Tabs, Group: locales.Messages.GroupOpening, Label: locales.Messages.OptOpenURL},
	{Section: "tabs", Key: "open_bookmark_new_tab", Kind: Select, Default: "0", Choices: tabChoices, Page: Tabs, Group: locales.Messages.GroupOpening, Label: locales.Messages.OptOpenBookmark},
	{Section: "tabs", Key: "new_tab_disable", Kind: Switch, Default: "1", Page: Tabs, Group: locales.Messages.GroupOpening, Label: locales.Messages.OptNewTabDisable, Applies: opensNewTabs},
	{Section: "tabs", Key: "new_tab_disable_name", Kind: NameList, Page: Tabs, Group: locales.Messages.GroupOpening, Label: locales.Messages.OptNewTabNames, Help: locales.Messages.OptNewTabNamesHelp, Applies: keepsNewTab},

	{Section: "general", Key: "boss_key", Kind: Hotkey, Page: Keyboard, Label: locales.Messages.OptBossKey, Help: locales.Messages.OptBossKeyHelp},
	{Section: "general", Key: "translate_key", Kind: Hotkey, Page: Keyboard, Label: locales.Messages.OptTranslateKey},

	{Section: "general", Key: "data_dir", Kind: Folder, Default: `%app%\..\Data`, Page: Startup, Group: locales.Messages.GroupFolders, Label: locales.Messages.OptDataDir},
	{Section: "general", Key: "cache_dir", Kind: Folder, Default: `%app%\..\Cache`, Page: Startup, Group: locales.Messages.GroupFolders, Label: locales.Messages.OptCacheDir},
	{Section: "general", Key: "command_line", Kind: SwitchList, Page: Startup, Group: locales.Messages.GroupPrograms, Label: locales.Messages.OptCommandLine, Help: locales.Messages.OptCommandLineHelp},
	{Section: "general", Key: "launch_on_startup", Kind: CommandList, Page: Startup, Group: locales.Messages.GroupPrograms, Label: locales.Messages.OptLaunchOnStartup, Help: locales.Messages.OptRunHelp},
	{Section: "general", Key: "launch_on_exit", Kind: CommandList, Page: Startup, Group: locales.Messages.GroupPrograms, Label: locales.Messages.OptLaunchOnExit, Help: locales.Messages.OptRunHelp},

	{Section: "general", Key: "win32k", Kind: Switch, Default: "0", Page: Advanced, Label: locales.Messages.OptWin32k, Help: locales.Messages.OptWin32kHelp},
	{Section: "general", Key: "ignore_policies", Kind: Switch, Default: "0", Page: Advanced, Label: locales.Messages.OptIgnorePolicies, Help: locales.Messages.OptIgnorePoliciesHelp},
	{Section: "general", Key: "suppress_false_upgrade_notification", Kind: Switch, Default: "0", Page: Advanced, Label: locales.Messages.OptSuppressUpgrade, Help: locales.Messages.OptSuppressUpgradeHelp},
}

func OnPage(p Page) []Option {
	var out []Option
	for _, o := range All {
		if o.Page == p {
			out = append(out, o)
		}
	}
	return out
}

// Effective returns the value Chrome++ works with for a key's value as
// written: the default when it is missing or empty, and for numbers and
// choices out of range.
func (o Option) Effective(value string, present bool) string {
	if !present || strings.TrimSpace(value) == "" {
		return o.Default
	}
	switch o.Kind {
	case Switch:
		if Int(value) != 0 {
			return "1"
		}
		return "0"
	case Number:
		if n := Int(value); n >= o.Min && n <= o.Max {
			return strconv.Itoa(n)
		}
		return o.Default
	case Select:
		v := strconv.Itoa(Int(value))
		for _, c := range o.Choices {
			if c.Value == v {
				return v
			}
		}
		return o.Default
	}
	return value
}

// Int reads an integer as GetPrivateProfileIntW does: an optional sign,
// then digits, up to the first other character.
func Int(s string) int {
	s = strings.TrimLeft(s, " \t")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' || n > 1e9 {
			break
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}

func (o Option) Matches(query string, langs ...locales.Messages) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	if strings.Contains(o.Key, q) {
		return true
	}
	for _, m := range langs {
		for _, t := range []Text{o.Label, o.Help, o.Group} {
			if t != nil && strings.Contains(strings.ToLower(t(m)), q) {
				return true
			}
		}
	}
	return false
}

var (
	modifiers = []string{"shift", "ctrl", "control", "alt", "win"}
	special   = []string{"left", "right", "up", "down", "←", "→", "↑", "↓", "esc", "escape", "tab", "backspace",
		"enter", "return", "space", "prtsc", "printscreen", "scroll", "pause", "insert", "delete", "del", "home",
		"end", "pageup", "pgup", "pagedown", "pgdn"}
)

// ValidHotkey reports whether Chrome++ reads s as a shortcut: parts joined
// by "+", each a modifier, a named key, F1 to F24 or one character, with
// a key besides the modifiers. Chrome++ skips parts it doesn't know; they
// count as mistakes here. An empty s, which turns the shortcut off, is
// valid.
func ValidHotkey(s string) bool {
	if strings.TrimSpace(s) == "" {
		return true
	}
	key := false
	for part := range strings.SplitSeq(s, "+") {
		p := strings.ToLower(part)
		switch {
		case p == "":
		case contains(modifiers, p):
		case contains(special, p) || isFunctionKey(p):
			key = true
		case len([]rune(p)) == 1 && isKeyChar([]rune(p)[0]):
			key = true
		default:
			return false
		}
	}
	return key
}

// Chord spells a shortcut one way, so that two spellings of it compare
// equal.
func Chord(s string) string {
	var parts []string
	key := ""
	for part := range strings.SplitSeq(strings.ToLower(s), "+") {
		switch {
		case part == "control":
			parts = append(parts, "ctrl")
		case contains(modifiers, part):
			parts = append(parts, part)
		case part != "":
			key = part
		}
	}
	slices.Sort(parts)
	return strings.Join(append(parts, key), "+")
}

// ValidTarget reports whether s is what a key mapping can send: a
// shortcut, or "command:" and a Chrome command ID.
func ValidTarget(s string) bool {
	if id, ok := strings.CutPrefix(s, "command:"); ok {
		return id != "" && strings.Trim(id, "0123456789") == "" && Int(id) != 0
	}
	return strings.TrimSpace(s) != "" && ValidHotkey(s)
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}

func isFunctionKey(p string) bool {
	if len(p) < 2 || p[0] != 'f' || strings.Trim(p[1:], "0123456789") != "" {
		return false
	}
	n := Int(p[1:])
	return n >= 1 && n <= 24
}

// isKeyChar reports whether a character has a key: letters and digits,
// and the punctuation of a US keyboard, which VkKeyScanW finds on most
// layouts.
func isKeyChar(r rune) bool {
	if r < 0x80 {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("`-=[]\\;',./", r)
	}
	return false
}
