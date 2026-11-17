package gui

import (
	"fmt"
	"github.com/Bush2021/mygo-kit"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/Bush2021/pitstop/internal/ini"
	"github.com/Bush2021/pitstop/internal/options"
	"github.com/Bush2021/pitstop/locales"
)

var (
	closeIcon = kit.Icon(`<path d="M18 6 6 18M6 6l12 12"/>`)
	trashIcon = kit.Icon(`<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v5M14 11v5"/>`)
)

// keyNames are the keys a shortcut can use, as chrome++.ini writes them. A
// key that isn't here can't be recorded.
var keyNames = func() map[ui.Key]string {
	names := map[ui.Key]string{
		ui.KeyEnter: "Enter", ui.KeyEscape: "Esc", ui.KeyBackspace: "Backspace", ui.KeyTab: "Tab", ui.KeySpace: "Space",
		ui.KeyDelete: "Delete", ui.KeyInsert: "Insert", ui.KeyHome: "Home", ui.KeyEnd: "End", ui.KeyPageUp: "PageUp",
		ui.KeyPageDown: "PageDown", ui.KeyLeft: "Left", ui.KeyRight: "Right", ui.KeyUp: "Up", ui.KeyDown: "Down",
		ui.KeyMinus: "-", ui.KeyEqual: "=", ui.KeyComma: ",", ui.KeyPeriod: ".", ui.KeySlash: "/", ui.KeySemicolon: ";",
		ui.KeyQuote: "'", ui.KeyBracketLeft: "[", ui.KeyBracketRight: "]", ui.KeyBackslash: `\`, ui.KeyBackquote: "`",
	}
	for i := range ui.Key(12) {
		names[ui.KeyF1+i] = fmt.Sprintf("F%d", i+1)
	}
	for i := range ui.Key(10) {
		names[ui.Key0+i] = string('0' + rune(i))
	}
	for i := range ui.Key(26) {
		names[ui.KeyA+i] = string('A' + rune(i))
	}
	return names
}()

// heldKeys names the modifiers in mods.
func heldKeys(mods ui.Modifiers) []string {
	var parts []string
	for _, m := range []struct {
		mod  ui.Modifiers
		name string
	}{{ui.Ctrl, "Ctrl"}, {ui.Alt, "Alt"}, {ui.Shift, "Shift"}, {ui.Super, "Win"}} {
		if mods&m.mod != 0 {
			parts = append(parts, m.name)
		}
	}
	return parts
}

// typesText reports whether the chord would type or edit text, and so
// would take the key away from every app or page.
func typesText(mods ui.Modifiers, key ui.Key) bool {
	if mods&(ui.Ctrl|ui.Alt|ui.Super) != 0 {
		return false
	}
	switch key {
	case ui.KeyInsert, ui.KeyDelete, ui.KeyHome, ui.KeyEnd, ui.KeyPageUp, ui.KeyPageDown:
		return false
	}
	return key < ui.KeyF1 || key > ui.KeyF12
}

// Recording reports whether a hotkey field is taking keys, which Windows
// must then leave alone.
func (a *App) Recording() bool { return a.recording != "" }

func (a *App) startRecording(id string) {
	a.recording, a.recordFocus, a.recordHint = id, true, ""
}

func (a *App) stopRecording() {
	a.recording, a.recordHint = "", ""
	if a.adding == mappingRow {
		a.adding = ""
	}
}

// hotkeyField records a shortcut as Chrome's page of extension shortcuts
// does: click it and press the keys. plain lets it take a key that types.
func (a *App) hotkeyField(c *ui.Context, id, label, value string, plain bool, set func(string)) ui.Element {
	t := c.Theme()
	box := ui.ButtonBase(c.Key(id)).Width(190).Height(32).Gap(4).Justify(ui.Start).Padding(0, 8).
		Radius(t.Radius).Background(t.Background).Label(label).Value(value)
	box.HandleInput(func(ev ui.InputEvent) bool {
		if a.recording != id || ev.Kind != ui.InputKeyDown {
			return false
		}
		key := ev.Key
		if key == ui.KeyUnknown {
			key = ev.PhysicalKey
		}
		switch {
		case ev.Mods == 0 && key == ui.KeyEscape:
			a.stopRecording()
		case ev.Repeat || keyNames[key] == "":
		case !plain && typesText(ev.Mods, key):
			a.recordHint = locales.HotkeyNeedsModifier()
		default:
			a.stopRecording()
			set(strings.Join(append(heldKeys(ev.Mods), keyNames[key]), "+"))
		}
		return true
	})
	if box.Clicked() {
		a.startRecording(id)
	}
	if a.recording == id {
		if a.recordFocus {
			box.Focus()
			a.recordFocus = false
		} else if !box.Focused() {
			a.stopRecording()
		}
	}
	recording := a.recording == id
	if recording {
		box.Border(1, t.Accent)
	} else {
		box.Border(1, t.Border)
	}
	box.Children(func() {
		switch {
		case recording && a.recordHint != "":
			ui.Text(c, a.recordHint).FontSize(12).TextColor(t.Warning).SingleLine()
		case recording:
			if held := heldKeys(c.Modifiers()); len(held) > 0 {
				keycaps(c, held)
			} else {
				kit.Muted(c, locales.HotkeyPress()).SingleLine()
			}
		case value == "":
			kit.Muted(c, locales.HotkeyNone()).SingleLine()
		default:
			keycaps(c, strings.Split(value, "+"))
		}
	})
	return box
}

// hotkeyNote says what else answers to the shortcut in the field with that
// id: another of Chrome++'s, which leaves one of them out, or Chrome's own,
// which is often the point.
func (a *App) hotkeyNote(c *ui.Context, field, value string) {
	if value == "" {
		return
	}
	msgs := locales.In(c.Locale())
	chord := options.Chord(value)
	used := ""
	for _, o := range options.All {
		raw, _ := a.draft.Raw(o.Section, o.Key)
		if o.Kind == options.Hotkey && "option/"+o.Key != field && options.Chord(ini.Unquote(raw)) == chord {
			used = o.Label(msgs)
		}
	}
	for i, kv := range a.draft.Keys("keymapping") {
		if sourceField(i) != field && options.Chord(kv.Key) == chord {
			used = locales.KeymapTitle()
		}
	}
	if used != "" {
		ui.Text(c, locales.HotkeyUsedBy(used)).FontSize(12).TextColor(c.Theme().Warning)
	} else if label, ok := options.ChromeShortcut(value, msgs); ok {
		kit.Muted(c, locales.HotkeyChrome(label))
	}
}

func keycaps(c *ui.Context, keys []string) {
	t := c.Theme()
	for i, k := range keys {
		if k != "" {
			ui.Text(c.Key(i), k).FontSize(12).SingleLine().Padding(1, 6).Radius(4).Background(t.Surface).Border(1, t.Border)
		}
	}
}

// reference is a list to pick a command or a switch from, as a dialog.
type reference struct {
	title, search string
	entries       []options.Entry
	linkLabel     string
	link          string
	// custom reads what was typed as a value of its own, for what the list
	// lacks, and words for it.
	custom func(query string) (label, value string, ok bool)
	choose func(value string)
}

func (a *App) openReference(r *reference) {
	a.ref, a.refQuery, a.refOpen = r, "", true
}

func (a *App) commandReference(choose func(id string)) {
	a.openReference(&reference{
		title: locales.CommandsTitle(), search: locales.CommandsSearch(), entries: options.ChromeCommands,
		linkLabel: locales.CommandsAll(), link: options.CommandsURL,
		custom: func(q string) (string, string, bool) {
			if q == "" || strings.Trim(q, "0123456789") != "" || options.Int(q) == 0 {
				return "", "", false
			}
			return locales.CommandsUseID(q), q, true
		},
		choose: choose,
	})
}

func (a *App) switchReference(choose func(value string)) {
	a.openReference(&reference{
		title: locales.SwitchesTitle(), search: locales.SwitchesSearch(), entries: options.ChromeSwitches,
		linkLabel: locales.SwitchesAll(), link: options.SwitchesURL,
		custom: func(q string) (string, string, bool) {
			if !strings.HasPrefix(q, "--") || len(q) == 2 {
				return "", "", false
			}
			return locales.SwitchesUse(q), q, true
		},
		choose: choose,
	})
}

func (a *App) referenceDialog(c *ui.Context) {
	if a.ref == nil {
		return
	}
	r := a.ref
	ui.Modal(c, &a.refOpen, func() {
		t := c.Theme()
		query := strings.TrimSpace(a.refQuery)
		var found []options.Entry
		for _, e := range r.entries {
			if e.Matches(query, locales.In("en"), locales.In("zh-Hans")) {
				found = append(found, e)
			}
		}
		customLabel, customValue, custom := r.custom(query)
		take := func(value string) {
			a.refOpen = false
			r.choose(value)
		}
		ui.Column(c).Width(480).Gap(12).Children(func() {
			ui.Text(c, r.title).FontSize(16).Bold()
			if ui.SearchField(c.Key("search"), &a.refQuery).Placeholder(r.search).Label(r.search).AutoFocus().Submitted() {
				switch {
				case custom:
					take(customValue)
				case len(found) > 0:
					take(found[0].Value)
				}
			}
			msgs := locales.In(c.Locale())
			ui.Scroll(c.Key("list")).Height(min(320, listRoom(c))).Gap(2).Children(func() {
				if custom && ui.Button(c.Key("custom"), customLabel).Clicked() {
					take(customValue)
				}
				if len(found) == 0 && !custom {
					kit.Muted(c, locales.ReferenceNone())
				}
				group := ""
				for _, e := range found {
					if g := e.Group(msgs); g != group {
						group = g
						ui.Text(c.Key(g), g).FontSize(12).Bold().TextColor(t.TextMuted).Margin(8, 0, 2)
					}
					if entryRow(c, e, msgs).Clicked() {
						take(e.Value)
					}
				}
			})
			kit.Link(c, r.linkLabel, r.link)
		})
	})
}

// listRoom is what the window leaves for the list, around the dialog's
// title, search field and link.
func listRoom(c *ui.Context) float32 {
	_, h := c.Size()
	return max(120, h-220)
}

func entryRow(c *ui.Context, e options.Entry, msgs locales.Messages) ui.Element {
	t := c.Theme()
	detail := e.Value
	if e.Detail != "" {
		detail = e.Value + " · " + e.Detail
	}
	row := ui.ButtonBase(c.Key(e.Value)).Justify(ui.Start).Gap(12).Padding(6, 8).Radius(4)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	return row.Children(func() {
		ui.Text(c, e.Label(msgs)).Grow(1).MinWidth(0)
		kit.Muted(c, detail).SingleLine()
	})
}

// lines edits a list with a field for each item. A new item starts as an
// empty field, which is no item until it holds text.
func (a *App) lines(c *ui.Context, id, label, placeholder string, items []string, write func([]string)) {
	rows := items
	if a.adding == id {
		rows = append(slices.Clone(items), "")
	}
	remove := -1
	for i := range rows {
		ui.Row(c.Key(i)).Gap(8).AlignItems(ui.Center).Transition(kit.RowMotion).Children(func() {
			field := ui.TextInput(c.Key("field"), &rows[i]).Grow(1).MinWidth(0).Label(label).Placeholder(placeholder)
			if a.focusLine == id && i == len(rows)-1 {
				field.Focus()
				a.focusLine = ""
			}
			if field.Changed() {
				if a.adding == id {
					a.adding = ""
				}
				write(rows)
			}
			if kit.IconButton(c, "remove", trashIcon, locales.ListRemove(), "").Clicked() {
				remove = i
			}
		})
	}
	switch {
	case remove < 0:
	case remove == len(items):
		a.adding = ""
	default:
		write(slices.Delete(slices.Clone(items), remove, remove+1))
	}
}

// browse asks for a path off the view's thread, where a dialog would hold
// up the frame it was called from.
func (a *App) browse(pick func() string, done func(path string)) {
	go func() {
		if path := pick(); path != "" {
			a.Post(func() { done(path) })
		}
	}()
}
