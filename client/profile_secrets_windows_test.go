//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileCredentialProtectionAndEndpointBinding(t *testing.T) {
	s := connectionSettings{Host: "safe.example", Port: "22", User: "alice", KeyPath: "test-key", Password: "test-password", Passphrase: "test-passphrase"}
	protected, err := protectProfileCredentials(s)
	if err != nil || protected == "" || strings.Contains(protected, s.Password) || strings.Contains(protected, s.Passphrase) {
		t.Fatal("protect failed or exposed secrets")
	}
	password, passphrase, err := unprotectProfileCredentials(protected, s)
	if err != nil || password != s.Password || passphrase != s.Passphrase {
		t.Fatal("failed to decrypt credentials")
	}
	for _, mutate := range []func(*connectionSettings){
		func(s *connectionSettings) { s.Host = "other.example" },
		func(s *connectionSettings) { s.Port = "2222" },
		func(s *connectionSettings) { s.User = "bob" },
		func(s *connectionSettings) { s.KeyPath = "other-key" },
	} {
		changed := s
		mutate(&changed)
		if _, _, err := unprotectProfileCredentials(protected, changed); err == nil {
			t.Fatal("credentials were accepted for a different endpoint or key")
		}
	}
	if _, _, err := unprotectProfileCredentials(protected+"broken", s); err == nil {
		t.Fatal("corrupted credentials were accepted")
	}
}

func TestProfileCredentialsSurviveMetadataChangesAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{editConnection: true, settings: connectionSettings{Host: "safe.example", Port: "22", User: "alice", Password: "private-password"}}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk diskProfiles
	if json.Unmarshal(data, &disk) != nil || disk.Version != 3 || len(disk.Items) != 1 || disk.Items[0].EncryptedCredentials == "" || strings.Contains(string(data), "private-password") {
		t.Fatal("protected format invalid")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	reopened.loadProfile(reopened.profiles[0])
	if reopened.settings.Password != "private-password" {
		t.Fatal("credential lost on reopen")
	}
	reopened.settings.Host = "new.example"
	if err := reopened.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	var updated desktop
	if err := updated.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	updated.loadProfile(updated.profiles[0])
	if updated.settings.Password != "private-password" || updated.settings.Host != "new.example" {
		t.Fatal("credential not rebound after endpoint edit")
	}
	updated.settings.Password = ""
	if err := updated.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	var cleared desktop
	if err := cleared.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	cleared.loadProfile(cleared.profiles[0])
	if cleared.settings.Password != "" || cleared.profiles[0].EncryptedCredentials != "" {
		t.Fatal("cleared credential remained stored")
	}
}

func TestUndecryptableCredentialIsPreservedOnMetadataEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{editConnection: true, settings: connectionSettings{Host: "safe.example", Port: "22", User: "alice", Password: "private-password"}}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file diskProfiles
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	file.Items[0].Host = "tampered.example"
	data, _ = json.Marshal(file)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	reopened.loadProfile(reopened.profiles[0])
	if !reopened.profileSecretError || reopened.settings.Password != "" {
		t.Fatal("mismatched endpoint exposed credentials")
	}
	reopened.profileLabel = "changed"
	if err := reopened.saveCurrentProfile(); err == nil {
		t.Fatal("saved over unreadable credentials")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("original encrypted data was changed")
	}
}

func TestCopyClearsProtectedCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{editConnection: true, settings: connectionSettings{Host: "safe.example", Port: "22", User: "alice", Password: "private-password"}}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	d.checkedProfiles = map[profileID]bool{d.profiles[0].ID: true}
	d.copyCheckedProfiles()
	if len(d.profiles) != 2 || d.profiles[0].EncryptedCredentials == "" || d.profiles[1].EncryptedCredentials != "" {
		t.Fatal("copy retained protected credentials or removed the original")
	}
}

func TestExplicitCloudSaveRebindsProtectedLocalCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{settings: connectionSettings{Host: "safe.example", Port: "22", User: "alice", Password: "old-password"}}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	d.settingsFromCloud = true
	d.settings.Host, d.settings.Password = "new.example", "new-password"
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	reopened.loadProfile(reopened.profiles[0])
	if reopened.profileSecretError || reopened.settings.Host != "new.example" || reopened.settings.Password != "new-password" {
		t.Fatal("explicit local save did not rebind the protected credential")
	}
}

func TestCloudLogoutDoesNotEraseLocalCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{editConnection: true, settings: connectionSettings{Host: "safe.example", Port: "22", User: "alice", Password: "local-password"}}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d.settingsFromCloud = true
	d.settings.Password = "cloud-password"
	d.profileAutoDirty = true
	d.clearCloudFormCredentials()
	d.profileLabel = "renamed"
	if err := d.saveCurrentProfile(); err == nil {
		t.Fatal("logout allowed the local secret to be silently erased")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) || d.settings.Password != "" || d.profileAutoDirty {
		t.Fatal("logout did not preserve local data and clear cloud credentials")
	}
}

func TestNavigationSavesPendingProtectedCredential(t *testing.T) {
	d, path := navigationFixture(t)
	d.settings.Password, d.profileAutoDirty = "pending-secret", true
	if !d.prepareConnectionNavigation() {
		t.Fatal("valid credential draft failed to save")
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "pending-secret") {
		t.Fatal("credential leaked into plaintext storage")
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	reopened.loadProfile(reopened.profiles[0])
	if reopened.settings.Password != "pending-secret" {
		t.Fatal("navigation did not protect the pending credential")
	}
}
