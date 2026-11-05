package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileStoreRoundTripDoesNotWriteCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice", Password: "private-password", Passphrase: "private-passphrase"}, active: connectionSettings{Host: "one.test", Port: "22", User: "alice", Fingerprint: "SHA256:test"}, profileGroup: "工作"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.profiles[0].Favorite = true
	d.profiles[0].LastConnected = 12345
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-password") || strings.Contains(string(data), "private-passphrase") {
		t.Fatal("credential written to profile store")
	}
	d.settings.Host = "two.test"
	d.selectedProfile = ""
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	var restored desktop
	if err := restored.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if len(restored.profiles) != 2 || !restored.profiles[0].Favorite || restored.profiles[0].LastConnected != 12345 || restored.profiles[0].Settings.Fingerprint != "SHA256:test" || restored.profiles[1].Settings.Host != "two.test" {
		t.Fatalf("incorrect restored profiles: %+v", restored.profiles)
	}
	if restored.profiles[0].Settings.Password != "" || restored.profiles[0].Settings.Passphrase != "" {
		t.Fatal("credential restored unexpectedly")
	}
}

func TestProfileStoreRejectsCorruptFileWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("bad-json"), 0600); err != nil {
		t.Fatal(err)
	}
	var d desktop
	if err := d.loadProfiles(path); err == nil {
		t.Fatal("expected corrupt file error")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "bad-json" {
		t.Fatalf("corrupt file changed: %q %v", data, err)
	}
}

func TestProfileStoreRejectsStaleWindowWithoutLosingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	a := &desktop{settings: connectionSettings{Host: "first.test", Port: "22", User: "alice"}}
	b := &desktop{settings: connectionSettings{Host: "second.test", Port: "22", User: "bob"}}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := a.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := a.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	if err := b.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := b.saveProfiles(); err == nil {
		t.Fatal("stale window overwrote a newer connection list")
	}
	var restored desktop
	if err := restored.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if len(restored.profiles) != 1 || restored.profiles[0].Settings.Host != "first.test" {
		t.Fatalf("saved data was lost: %+v", restored.profiles)
	}
}
