package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestProfileAutosaveThroughEditorProtectsSecrets(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI requires Windows")
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Port: "22"}}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.captureConnectionDraft()
	view := ui.NewTester(d.view, 1200, 900)
	if err := view.Click("主机地址"); err != nil {
		t.Fatal(err)
	}
	view.Type("auto.example")
	if err := view.Click("用户名"); err != nil {
		t.Fatal(err)
	}
	view.Type("alice")
	if err := view.Click("SSH 密码"); err != nil {
		t.Fatal(err)
	}
	view.Type("sensitive-password")
	d.settings.Passphrase = "sensitive-passphrase"
	view.Advance(profileAutosaveDelay + time.Second)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.profiles) != 1 || d.selectedProfile == "" || !strings.Contains(string(data), "auto.example") || strings.Contains(string(data), "sensitive-") {
		t.Fatal("metadata was not saved correctly or a secret reached disk")
	}
	if d.hasUnsavedConnection() {
		t.Fatal("protected credentials should no longer block navigation")
	}
	if err := view.Click("连接名称"); err != nil {
		t.Fatal(err)
	}
	view.Type("生产")
	view.Advance(profileAutosaveDelay + time.Second)
	if len(d.profiles) != 1 || d.profiles[0].Label != "生产" {
		t.Fatal("editing an autosaved host created a duplicate or failed")
	}
	if !d.closeConnectionEditor() {
		t.Fatal("saved editor did not close")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	reopened.loadProfile(reopened.profiles[0])
	if reopened.profileSecretError || reopened.settings.Password != "sensitive-password" || reopened.settings.Passphrase != "sensitive-passphrase" {
		t.Fatal("protected credentials were not restored for the same host")
	}
}

func TestAutosaveConflictPreservesDiskAndDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	a := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Host: "old.example", Port: "22", User: "alice"}}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := a.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	b := &desktop{}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	b.loadProfile(b.profiles[0])
	a.profileLabel = "other window"
	if err := a.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b.settings.Host = "draft.example"
	b.flushProfileAutosave()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !b.profileStoreConflict || b.profiles[0].Settings.Host != "old.example" || b.settings.Host != "draft.example" || !b.hasUnsavedConnection() {
		t.Fatal("conflicting autosave lost the persisted file or the editable draft")
	}
}

func TestQuickConnectionAutosavesBeforeClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections", quickConnect: "alice@quick.example"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.openQuickConnection()
	if !d.closeConnectionEditor() || len(d.profiles) != 1 || d.profiles[0].Settings.Host != "quick.example" {
		t.Fatal("closing did not flush the quick connection")
	}
}

func TestAutosaveSkipsIncompleteAndCloudDrafts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections", editConnection: true, settings: connectionSettings{Port: "22"}}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.captureConnectionDraft()
	d.settings.Host = "incomplete.example"
	d.flushProfileAutosave()
	if _, err := os.Stat(path); !os.IsNotExist(err) || !d.hasUnsavedConnection() {
		t.Fatal("incomplete form was saved or lost")
	}
	d.settings.User = "alice"
	d.settingsFromCloud = true
	d.flushProfileAutosave()
	if _, err := os.Stat(path); !os.IsNotExist(err) || !d.hasUnsavedConnection() {
		t.Fatal("cloud form was written to local profiles or lost")
	}
}
