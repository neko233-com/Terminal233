package main

import (
	"os"
	"path/filepath"
	"testing"
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
	if a.preferencesStatus != "" || b.preferencesStatus != "" {
		t.Fatal(a.preferencesStatus, b.preferencesStatus)
	}
	restored := &desktop{}
	if err := restored.loadPreferences(path); err != nil {
		t.Fatal(err)
	}
	if !restored.lightTheme || !restored.sidebarCollapsed || !restored.showFilePanel {
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
