package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func navigationFixture(t *testing.T) (*desktop, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.settings = connectionSettings{Host: "one.example", Port: "22", User: "alice"}
	d.profileLabel, d.profileGroup = "one", "work"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.selectedProfile = ""
	d.settings = connectionSettings{Host: "two.example", Port: "22", User: "bob"}
	d.profileLabel, d.profileGroup = "two", "other"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	d.loadProfile(d.profiles[0])
	return d, path
}

func TestSwitchHostImmediatelyFlushesDraft(t *testing.T) {
	d, path := navigationFixture(t)
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("连接名称"); err != nil {
		t.Fatal(err)
	}
	view.TypeKey(ui.Cmd, ui.KeyA, "")
	view.Type("renamed")
	if !d.hasUnsavedConnection() {
		t.Fatal("test did not create a pending draft")
	}
	if err := view.Click("other / two"); err != nil {
		t.Fatal(err)
	}
	if d.selectedProfile != "2" || d.settings.Host != "two.example" {
		t.Fatal("switch was blocked while local save was pending")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if reopened.profiles[0].Label != "renamed" {
		t.Fatal("switch lost the previous host edit")
	}
}

func TestSwitchHostKeepsDraftWhenAutosaveConflicts(t *testing.T) {
	d, path := navigationFixture(t)
	var other desktop
	if err := other.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	other.loadProfile(other.profiles[0])
	other.profileLabel = "other window"
	if err := other.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("连接名称"); err != nil {
		t.Fatal(err)
	}
	view.TypeKey(ui.Cmd, ui.KeyA, "")
	view.Type("my draft")
	if err := view.Click("other / two"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) || !d.profileStoreConflict || d.selectedProfile != "1" || d.profileLabel != "my draft" || !strings.Contains(d.status, "自动保存失败") {
		t.Fatal("conflicting switch overwrote data or abandoned the draft")
	}
}

func TestReopeningCurrentHostUsesFlushedValue(t *testing.T) {
	d, _ := navigationFixture(t)
	d.showCompactProfileTools = true
	d.profileLabel, d.profileAutoDirty = "edited", true
	view := ui.NewTester(d.view, 1280, 1000)
	if err := view.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	if d.selectedProfile != "1" || d.profileLabel != "edited" {
		t.Fatal("reopening reloaded the stale pre-save profile")
	}
}

func TestGroupNavigationFlushesBeforeClosing(t *testing.T) {
	d, path := navigationFixture(t)
	d.profileLabel, d.profileAutoDirty = "edited", true
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("打开分组：other"); err != nil {
		t.Fatal(err)
	}
	if d.editConnection || d.profileFilter != "other" {
		t.Fatal("group navigation did not close the saved editor")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if reopened.profiles[0].Label != "edited" {
		t.Fatal("group navigation lost edit")
	}
}

func TestNewTabHostSwitchFlushesDraft(t *testing.T) {
	d, path := navigationFixture(t)
	d.profileLabel, d.profileAutoDirty = "edited", true
	d.openNewTab()
	d.activateNewTabResult("new-tab-profile-2")
	if d.newTabOpen || d.selectedProfile != "2" {
		t.Fatal("new tab could not open the other host")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if reopened.profiles[0].Label != "edited" {
		t.Fatal("new tab lost previous edit")
	}
}

func TestCloudPageHostSwitchFlushesDraft(t *testing.T) {
	d, path := navigationFixture(t)
	d.profileLabel, d.profileAutoDirty = "edited", true
	d.workspace = "cloud"
	d.cloud = &cloudClient{}
	d.accounts = []cloudAccount{{ID: "cloud", Label: "cloud host", Host: "cloud.example", Port: 22, Username: "carol"}}
	view := ui.NewTester(d.view, 1280, 1400)
	if err := view.Click("cloud host"); err != nil {
		t.Fatal(err)
	}
	if !d.settingsFromCloud || d.selectedCloudProfile != "cloud" {
		t.Fatal("cloud host did not open")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if reopened.profiles[0].Label != "edited" {
		t.Fatal("cloud page discarded local edit")
	}
}

func TestFilePickerHostSwitchFlushesDraft(t *testing.T) {
	d, path := navigationFixture(t)
	d.profileLabel, d.profileAutoDirty = "edited", true
	d.workspace = "files"
	d.localPane = localFilePane{initialized: true, directory: t.TempDir()}
	view := ui.NewTester(d.view, 1280, 900)
	if err := view.Click("选择文件会话"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("文件主机：two · 本机"); err != nil {
		t.Fatal(err)
	}
	if d.selectedProfile != "2" || !d.fileReturnAfterConnect {
		t.Fatal("file picker did not open the host")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if reopened.profiles[0].Label != "edited" {
		t.Fatal("file picker discarded local edit")
	}
}

func TestIncompleteOrCloudDraftStillBlocksNavigation(t *testing.T) {
	d, _ := navigationFixture(t)
	d.settings.Host = ""
	d.profileAutoDirty = true
	if !d.connectionNavigationBlocked() || d.prepareConnectionNavigation() {
		t.Fatal("incomplete draft could be abandoned")
	}
	d.settings.Host = "cloud.example"
	d.settingsFromCloud = true
	if !d.connectionNavigationBlocked() || d.prepareConnectionNavigation() {
		t.Fatal("cloud draft could be abandoned")
	}
}
