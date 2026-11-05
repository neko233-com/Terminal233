package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
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
