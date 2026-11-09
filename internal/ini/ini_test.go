package ini

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ../defaultini/chrome++.ini is src/chrome++.ini of Chrome++ 1.20.0, as its
// release ships it: UTF-16LE with a BOM, CRLF line endings.
func shipped(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../defaultini/chrome++.ini")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := shipped(t)
	f := Parse(b)
	if !bytes.Equal(f.Bytes(), b) {
		t.Fatal("the shipped chrome++.ini does not survive a round trip")
	}
	if f.Encoding() != UTF16LE || f.eol != "\r\n" || len(f.Lines()) != 182 {
		t.Errorf("encoding %v, eol %q, %d lines", f.Encoding(), f.eol, len(f.Lines()))
	}
	if !strings.Contains(f.Text(), "新建标签") {
		t.Error("the Chinese text was not decoded")
	}
}

func TestGet(t *testing.T) {
	f := Parse(shipped(t))
	for _, tc := range []struct{ sec, key, raw, value string }{
		{"general", "data_dir", `%app%\..\Data`, `%app%\..\Data`},
		{"general", "command_line", "", ""},
		{"TABS", "Wheel_Tab", "1", "1"},
		{"tabs", "hover_tab_delay", "400", "400"},
		// Windows drops the outer quotes; Chrome++ splits the rest.
		{"tabs", "new_tab_disable_name", `"about:blank","新建标签"`, `about:blank","新建标签`},
	} {
		raw, ok := f.Raw(tc.sec, tc.key)
		value, _ := f.Get(tc.sec, tc.key)
		if !ok || raw != tc.raw || value != tc.value {
			t.Errorf("%s/%s = %q (%q), %v", tc.sec, tc.key, raw, value, ok)
		}
	}
	if _, ok := f.Get("general", "boss"); ok {
		t.Error("found a missing key")
	}
	// The commented-out command_line in [general] must not count.
	if raw, _ := f.Raw("general", "command_line"); raw != "" {
		t.Errorf("command_line = %q", raw)
	}
	if kv := f.Keys("keymapping"); len(kv) != 0 {
		t.Errorf("keymapping has %v", kv)
	}
	if kv := f.Keys("tabs"); len(kv) != 11 || kv[0].Key != "double_click_close" {
		t.Errorf("tabs has %v", kv)
	}
}

func changed(a, b []string) []int {
	var out []int
	for i := range max(len(a), len(b)) {
		if i >= len(a) || i >= len(b) || a[i] != b[i] {
			out = append(out, i)
		}
	}
	return out
}

func TestSetExistingKey(t *testing.T) {
	b := shipped(t)
	f := Parse(b)
	before := f.Lines()
	if err := f.Set("Tabs", "HOVER_TAB", "1"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("general", "command_line", "--disable-features=OutdatedBuildDetector"); err != nil {
		t.Fatal(err)
	}
	after := f.Lines()
	if got := changed(before, after); len(got) != 2 || after[got[0]] != "command_line=--disable-features=OutdatedBuildDetector" || after[got[1]] != "hover_tab=1" {
		t.Fatalf("changed lines %v", got)
	}
	g := Parse(f.Bytes())
	if v, _ := g.Get("tabs", "hover_tab"); v != "1" || g.Encoding() != UTF16LE {
		t.Errorf("reparsed: hover_tab=%q, %v", v, g.Encoding())
	}
	// Everything else is byte for byte the same.
	if err := g.Set("tabs", "hover_tab", "0"); err != nil {
		t.Fatal(err)
	}
	g.Set("general", "command_line", "")
	if !bytes.Equal(g.Bytes(), b) {
		t.Error("undoing the edits did not restore the file")
	}
}

func TestSetKeepsSpacing(t *testing.T) {
	f := Parse([]byte("[a]\n  key =  old value \t\n"))
	if v, _ := f.Raw("a", "key"); v != "old value" {
		t.Fatalf("value %q", v)
	}
	f.Set("a", "key", "new")
	if got := string(f.Bytes()); got != "[a]\n  key =  new \t\n" {
		t.Errorf("file %q", got)
	}
}

func TestSetNewKeys(t *testing.T) {
	f := Parse(shipped(t))
	if err := f.Set("tabs", "new_option", "1"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("keymapping", "F2", "Ctrl+PageUp"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("extra", "a", "b"); err != nil {
		t.Fatal(err)
	}
	lines := f.Lines()
	at := func(s string) int {
		for i, l := range lines {
			if l == s {
				return i
			}
		}
		return -1
	}
	if at("new_option=1") != at("hover_tab_delay=400")+1 {
		t.Error("new_option is not after the last key of [tabs]")
	}
	if at("F2=Ctrl+PageUp") != at("[keymapping]")+1 {
		t.Error("F2 is not right after [keymapping]")
	}
	text := f.Text()
	if !strings.HasSuffix(text, "; Alt+Shift+S=command:40015\r\n\r\n[extra]\r\na=b\r\n") {
		t.Errorf("file ends with %q", text[len(text)-80:])
	}
	g := Parse(f.Bytes())
	for _, kv := range [][3]string{{"tabs", "new_option", "1"}, {"keymapping", "F2", "Ctrl+PageUp"}, {"extra", "a", "b"}} {
		if v, ok := g.Get(kv[0], kv[1]); !ok || v != kv[2] {
			t.Errorf("%s/%s = %q, %v", kv[0], kv[1], v, ok)
		}
	}
}

func TestSetAfterALastLineWithoutEnding(t *testing.T) {
	f := Parse([]byte("[a]\nx=1"))
	f.Set("a", "y", "2")
	if got := string(f.Bytes()); got != "[a]\nx=1\ny=2\n" {
		t.Errorf("file %q", got)
	}
	f = Parse([]byte("x=1"))
	f.Set("b", "y", "2")
	if got := string(f.Bytes()); got != "x=1\r\n\r\n[b]\r\ny=2\r\n" {
		t.Errorf("file %q", got)
	}
}

func TestFirstSectionAndKeyWin(t *testing.T) {
	f := Parse([]byte("[a]\nk=1\nk=2\n[a]\nj=3\n"))
	if v, _ := f.Get("a", "k"); v != "1" {
		t.Errorf("k = %q", v)
	}
	// Windows searches only the first [a], so j is not there.
	if _, ok := f.Get("a", "j"); ok {
		t.Error("found a key of the second [a]")
	}
	f.Set("a", "j", "4")
	if got := string(f.Bytes()); got != "[a]\nk=1\nk=2\nj=4\n[a]\nj=3\n" {
		t.Errorf("file %q", got)
	}
}

func TestDelete(t *testing.T) {
	f := Parse(shipped(t))
	if !f.Delete("tabs", "hover_tab") || f.Delete("tabs", "hover_tab") {
		t.Fatal("Delete did not remove the key once")
	}
	if _, ok := f.Get("tabs", "hover_tab"); ok || len(f.Lines()) != 181 {
		t.Error("hover_tab is still there")
	}
}

func TestEncodings(t *testing.T) {
	utf8bom := Parse([]byte("\xef\xbb\xbf[tabs]\nnew_tab_disable_name=\"x\"\n"))
	if utf8bom.Encoding() != UTF8 {
		t.Fatalf("encoding %v", utf8bom.Encoding())
	}
	if err := utf8bom.Set("tabs", "new_tab_disable_name", `"新建标签"`); err != nil {
		t.Fatal(err)
	}
	if got := string(utf8bom.Bytes()); got != "\xef\xbb\xbf[tabs]\nnew_tab_disable_name=\"新建标签\"\n" {
		t.Errorf("UTF-8 file %q", got)
	}

	ansi := Parse([]byte("[tabs]\r\nnew_tab_disable_name=\"\xd0\xc2\xbd\xa8\"\r\n")) // GBK bytes stay as they are
	if err := ansi.Set("tabs", "new_tab_disable_name", `"新建标签"`); err == nil {
		t.Error("wrote Chinese into a file without a byte order mark")
	}
	ansi.Set("tabs", "hover_tab", "1")
	if got := string(ansi.Bytes()); got != "[tabs]\r\nnew_tab_disable_name=\"\xd0\xc2\xbd\xa8\"\r\nhover_tab=1\r\n" {
		t.Errorf("ANSI file %q", got)
	}

	be := []byte{0xFE, 0xFF, 0, '[', 0, 'a', 0, ']', 0, '\r', 0, '\n', 0, 'k', 0, '=', 0x65, 0xB0, 0, '\r', 0, '\n'}
	f := Parse(be)
	if v, _ := f.Get("a", "k"); f.Encoding() != UTF16BE || v != "新" || !bytes.Equal(f.Bytes(), be) {
		t.Errorf("UTF-16BE: %v %q", f.Encoding(), v)
	}
}

func TestLineBreaksAreRefused(t *testing.T) {
	f := Parse(shipped(t))
	if err := f.Set("general", "command_line", "a\r\nb=c"); err != ErrLineBreak {
		t.Errorf("Set = %v", err)
	}
}

func TestFromText(t *testing.T) {
	f := Parse(shipped(t))
	text := strings.ReplaceAll(f.Text(), "\r\n", "\n") // as an editor may hand it back
	g, err := f.FromText(text)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g.Bytes(), shipped(t)) {
		t.Error("the editor's text does not encode back to the file")
	}
	g, _ = f.FromText(strings.Replace(text, "hover_tab=0", "hover_tab=1", 1))
	if v, _ := g.Get("tabs", "hover_tab"); v != "1" || g.Encoding() != UTF16LE {
		t.Errorf("edited text: hover_tab=%q, %v", v, g.Encoding())
	}
}

func TestSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chrome++.ini")
	orig := shipped(t)
	os.WriteFile(path, orig, 0o644)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Set("tabs", "hover_tab", "1")
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	bak, _ := os.ReadFile(path + ".bak")
	if !bytes.Equal(saved, f.Bytes()) || !bytes.Equal(bak, orig) {
		t.Error("Save wrote the wrong bytes")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Errorf("the folder holds %d files", len(entries))
	}
}

func TestSections(t *testing.T) {
	f := Parse([]byte("x=1\n[a]\n[B]\n[a]\n"))
	if got := strings.Join(f.Sections(), ","); got != "a,B" {
		t.Errorf("Sections = %s", got)
	}
}

func TestSetKeys(t *testing.T) {
	f := Parse(shipped(t))
	before := f.Lines()
	add := []KeyValue{{"F2", "Ctrl+PageUp"}, {"Alt+Shift+S", "command:40015"}}
	if err := f.SetKeys("keymapping", add); err != nil {
		t.Fatal(err)
	}
	if got := f.Keys("keymapping"); len(got) != 2 || got[1] != add[1] {
		t.Fatalf("keymapping = %v", got)
	}
	// Edit the first in place, keeping its spacing; drop the second.
	lines := f.Lines()
	at := slicesIndex(lines, "F2=Ctrl+PageUp")
	f.SetKeys("keymapping", []KeyValue{{"F3", "Ctrl+PageDown"}})
	if got := f.Lines(); got[at] != "F3=Ctrl+PageDown" || len(got) != len(lines)-1 {
		t.Errorf("after the edit, line %d is %q of %d", at, got[at], len(got))
	}
	f.SetKeys("keymapping", nil)
	if got := f.Lines(); len(changed(before, got)) != 0 {
		t.Errorf("emptying the list left %v changed", changed(before, got))
	}
	if !bytes.Equal(f.Bytes(), shipped(t)) {
		t.Error("emptying the list did not restore the file")
	}

	g := Parse([]byte("[a]\nk=1\n"))
	if err := g.SetKeys("b", []KeyValue{{"x", "1"}, {"y", "2"}}); err != nil {
		t.Fatal(err)
	}
	if got := string(g.Bytes()); got != "[a]\nk=1\n\n[b]\nx=1\ny=2\n" {
		t.Errorf("new section: %q", got)
	}
}

func slicesIndex(lines []string, s string) int {
	for i, l := range lines {
		if l == s {
			return i
		}
	}
	return -1
}

func TestNonASCIIInAFileWithoutByteOrderMark(t *testing.T) {
	f := Parse([]byte("[general]\r\ndata_dir=\r\n"))
	if err := f.Set("general", "data_dir", "D:\\用户数据"); !errors.Is(err, ErrNonASCII) {
		t.Errorf("Set = %v", err)
	}
	if err := f.SetKeys("keymapping", []KeyValue{{"F2", "数据"}}); !errors.Is(err, ErrNonASCII) {
		t.Errorf("SetKeys = %v", err)
	}
}
