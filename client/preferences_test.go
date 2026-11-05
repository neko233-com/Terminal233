package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestPreferencesPersistAndMergeAcrossWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	a, b := &desktop{}, &desktop{}
	if err := a.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	if err := b.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	a.savePreference("light", true)
	b.savePreference("sidebar", true)
	b.savePreference("files", true)
	if err := writeFontPreference(path, 18); err != nil {
		t.Fatal(err)
	}
	if a.preferencesStatus != "" || b.preferencesStatus != "" {
		t.Fatal(a.preferencesStatus, b.preferencesStatus)
	}
	restored := &desktop{}
	if err := restored.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	if !restored.lightTheme || !restored.sidebarCollapsed || !restored.showFilePanel || restored.terminalFontSize != 18 {
		t.Fatal("preferences lost across windows or restart")
	}
}

func TestPanelSizesSaveResetAndMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	d := &desktop{}
	if err := d.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	d.terminalFilePercent, d.splitHorizontalPercent, d.splitVerticalPercent = 68, 43, 57
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("保存面板尺寸"); err != nil {
		t.Fatal(err)
	}
	if d.preferencesStatus != "面板尺寸已保存" {
		t.Fatal(d.preferencesStatus)
	}
	if err := writePreference(path, "light", true); err != nil {
		t.Fatal(err)
	}
	restored := &desktop{}
	if err := restored.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	if !restored.lightTheme || restored.terminalFilePercent != 68 || restored.splitHorizontalPercent != 43 || restored.splitVerticalPercent != 57 {
		t.Fatal("panel sizes or unrelated settings were not restored")
	}
	if err := view.Click("恢复默认面板尺寸"); err != nil {
		t.Fatal(err)
	}
	reset := &desktop{}
	if err := reset.loadPreferences(path); err != nil || reset.terminalFilePercent != 0 || reset.splitHorizontalPercent != 0 || reset.splitVerticalPercent != 0 || !reset.lightTheme {
		t.Fatal("reset was not persisted or discarded another setting")
	}
}

func TestTerminalThemesPersistAndApplyToExistingAndNewSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "preferences.json")
	d := &desktop{workspace: "terminal"}
	if err := d.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer right.Close()
	options := d.terminalOptions()
	options.Conn = left
	term, err := terminal.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	d.term, d.currentSession, d.nextSession = term, 1, 1
	d.sessions = []*workspaceSession{{id: 1, label: "test", term: term}}
	view := ui.NewTester(d.view, 1200, 900)
	d.changeTerminalTheme("catppuccin")
	view.Frame()
	if d.preferencesStatus != "" || d.terminalTheme != "catppuccin" || d.terminalLightTheme == nil || d.terminalDarkTheme == nil || d.terminalLightTheme.Background == d.terminalDarkTheme.Background {
		t.Fatalf("theme did not apply: %s", d.preferencesStatus)
	}
	r, ok := view.Find("Terminal")
	if !ok || r.W < 20 || r.H < 20 {
		t.Fatal("terminal view unavailable")
	}
	color := view.Image().RGBAAt(int(r.X+10), int(r.Y+r.H-10))
	want := d.terminalDarkTheme.Background
	if color.R != want.R || color.G != want.G || color.B != want.B {
		t.Fatalf("existing terminal background did not change: got=%v want=%v", color, want)
	}
	d.lightTheme = true
	view.Frame()
	color = view.Image().RGBAAt(int(r.X+10), int(r.Y+r.H-10))
	light := d.terminalLightTheme.Background
	if color.R != light.R || color.G != light.G || color.B != light.B {
		t.Fatal("paired light theme did not follow interface appearance")
	}
	d.lightTheme = false
	view.Frame()
	newLeft, newRight := net.Pipe()
	defer newRight.Close()
	newTerm, err := terminal.New(terminal.Options{Conn: newLeft, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer newTerm.Close()
	d.addSession(newTerm, nil, connectionSettings{}, "new")
	view.Frame()
	r, ok = view.Find("Terminal")
	if !ok {
		t.Fatal("new terminal view unavailable")
	}
	color = view.Image().RGBAAt(int(r.X+10), int(r.Y+r.H-10))
	if color.R != want.R || color.G != want.G || color.B != want.B {
		t.Fatal("new session did not inherit the current theme")
	}
	restored := &desktop{}
	if err := restored.loadPreferences(path); err != nil || restored.terminalTheme != "catppuccin" || restored.terminalOptions().DarkTheme == nil {
		t.Fatalf("theme did not survive restart: %v", err)
	}
	if err := writePreference(path, "sidebar", true); err != nil {
		t.Fatal(err)
	}
	p, err := readPreferences(path)
	if err != nil || p.TerminalTheme != "catppuccin" || !p.SidebarCollapsed {
		t.Fatal("theme was lost when another setting was saved")
	}
	d.changeTerminalTheme("")
	if d.terminalOptions().Theme != nil || d.terminalOptions().DarkTheme != nil {
		t.Fatal("follow-interface theme did not restore defaults")
	}
	view.Frame()
	if err := view.Click("选择终端配色"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Nord"); err != nil {
		t.Fatalf("theme picker did not expose presets: %v", err)
	}
	if d.terminalTheme != "nord" {
		t.Fatal("theme picker did not apply selected preset")
	}
}

func TestInvalidPreferencesAreNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte(`{"version":99,"future":"preserve"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	d := &desktop{}
	if err := d.loadPreferences(path); err == nil {
		t.Fatal("unknown version accepted")
	}
	d.savePreference("light", true)
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) || d.preferencesStatus == "" {
		t.Fatal("unknown preferences overwritten or error hidden")
	}
}

func TestUnknownTerminalThemeIsNotOverwritten(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "preferences.json")
	original := []byte(`{"version":1,"terminal_theme":"future-theme"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	d := &desktop{}
	if err := d.loadPreferences(path); err == nil {
		t.Fatal("unknown theme accepted")
	}
	d.changeTerminalTheme("nord")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(original) || d.preferencesStatus == "" {
		t.Fatal("unknown preferences were overwritten or error hidden")
	}
}

func TestTerminalFontPreferencesClampAndPreserveOldSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"light":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	d := &desktop{}
	if err := d.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	if d.currentTerminalFontSize() != defaultTerminalFontSize {
		t.Fatal("legacy settings did not use default size")
	}
	d.changeTerminalFont(terminal.Zoom{Steps: 100})
	if d.currentTerminalFontSize() != maxTerminalFontSize {
		t.Fatal("font maximum not enforced")
	}
	p, err := readPreferences(path)
	if err != nil || p.TerminalFontSize != maxTerminalFontSize || !p.Light {
		t.Fatalf("font update lost other settings: %+v %v", p, err)
	}
	d.changeTerminalFont(terminal.Zoom{Steps: -100})
	if d.currentTerminalFontSize() != minTerminalFontSize {
		t.Fatal("font minimum not enforced")
	}
	d.changeTerminalFont(terminal.Zoom{Reset: true})
	if d.currentTerminalFontSize() != defaultTerminalFontSize || d.terminalOptions().Font.Size != defaultTerminalFontSize {
		t.Fatal("font reset or new session default failed")
	}
}
