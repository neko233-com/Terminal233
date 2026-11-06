//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityReuseUpdateAndBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.saveIdentity("", identityDraft{Name: "Work", User: "alice", Password: "identity-secret", Passphrase: "identity-phrase", KeyPath: "C:/keys/work"}); err != nil {
		t.Fatal(err)
	}
	id := d.identities[0].ID
	for _, host := range []string{"one.example", "two.example"} {
		d.selectedProfile = ""
		d.settings = connectionSettings{Host: host, Port: "22"}
		if err := d.chooseProfileIdentity(id); err != nil {
			t.Fatal(err)
		}
		if err := d.saveCurrentProfile(); err != nil {
			t.Fatal(err)
		}
	}
	d.profiles[0].Tags = []string{"production"}
	for n := range d.profiles {
		d.profiles[n].Group = "work"
		d.profiles[n].Settings.Fingerprint = "SHA256:trusted"
	}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "identity-secret") || strings.Contains(string(data), "identity-phrase") {
		t.Fatal("plaintext secret saved")
	}
	var disk diskProfiles
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Version != 5 || len(disk.Identities) != 1 || len(disk.Items) != 2 || disk.Items[0].IdentityBinding == "" || disk.Items[0].EncryptedCredentials != "" {
		t.Fatalf("invalid storage format: version=%d", disk.Version)
	}
	var reopened desktop
	if err := reopened.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	for _, p := range reopened.profiles {
		s, err := reopened.profileCredential(p)
		if err != nil || s.Password != "identity-secret" || s.Passphrase != "identity-phrase" {
			t.Fatalf("identity not resolved: %v", err)
		}
	}
	if err := reopened.deleteIdentity(id); err == nil {
		t.Fatal("deleted identity still referenced by hosts")
	}
	if err := reopened.saveIdentity(id, identityDraft{Name: "Work", User: "alice", Password: "rotated-secret", KeyPath: "C:/keys/work"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range reopened.profiles {
		s, err := reopened.profileCredential(p)
		if err != nil || s.Password != "rotated-secret" {
			t.Fatalf("rotation did not propagate: %v", err)
		}
	}
	targets, failures, err := reopened.groupQuickTargets("work", false)
	if err != nil || len(failures) != 0 || len(targets) != 2 || targets[0].settings.Password != "rotated-secret" {
		t.Fatalf("group connection did not use rotated identity: %v", err)
	}
	p := reopened.profiles[0]
	s := p.Settings
	w := savedWorkspace{ID: strings.Repeat("a", 32), Name: "Work", Active: 0, SplitFirst: -1, SplitSecond: -1, Entries: []workspaceTemplateEntry{{Kind: "profile", Label: p.Label, Group: p.Group, Host: s.Host, Port: s.Port, User: s.User, KeyPath: s.KeyPath, Fingerprint: s.Fingerprint}}}
	restoredTargets, err := reopened.resolveWorkspace(w)
	if err != nil || len(restoredTargets) != 1 || restoredTargets[0].settings.Password != "rotated-secret" {
		t.Fatalf("workspace did not use rotated identity: %v", err)
	}
	if reopened.profiles[0].Settings.Fingerprint != "SHA256:trusted" {
		t.Fatal("password rotation removed trust")
	}
	if err := reopened.saveIdentity(id, identityDraft{Name: "Work", User: "bob", Password: "rotated-secret", KeyPath: "C:/keys/work"}); err != nil {
		t.Fatal(err)
	}
	if reopened.profiles[0].Settings.Fingerprint != "" || reopened.profiles[0].Settings.User != "bob" {
		t.Fatal("username update did not invalidate fingerprint")
	}
	modified := reopened.profiles[0]
	modified.Settings.Host = "attacker.example"
	if _, err := reopened.profileCredential(modified); err == nil {
		t.Fatal("changed host accepted identity credentials")
	}
	modified = reopened.profiles[0]
	modified.IdentityID = reopened.identities[0].ID
	modified.Settings.Port = "2200"
	if _, err := reopened.profileCredential(modified); err == nil {
		t.Fatal("changed port accepted identity credentials")
	}
	if err := reopened.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if err := json.Unmarshal(data, &disk); err != nil || disk.Version != 5 {
		t.Fatal("version was downgraded when tags were present")
	}
}

func TestIdentityStaleWriteAndUnlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	a, b := &desktop{}, &desktop{}
	if err := a.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := a.saveIdentity("", identityDraft{Name: "Shared", User: "alice", Password: "first"}); err != nil {
		t.Fatal(err)
	}
	id := a.identities[0].ID
	a.settings = connectionSettings{Host: "one.example", Port: "22"}
	if err := a.chooseProfileIdentity(id); err != nil {
		t.Fatal(err)
	}
	if err := a.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	if err := b.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := a.saveIdentity(id, identityDraft{Name: "Shared", User: "alice", Password: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := b.saveIdentity(id, identityDraft{Name: "Shared", User: "alice", Password: "stale"}); err == nil || !b.profileStoreConflict {
		t.Fatal("stale identity update overwrote newer data")
	}
	var c desktop
	if err := c.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	c.loadProfile(c.profiles[0])
	if err := c.chooseProfileIdentity(""); err != nil {
		t.Fatal(err)
	}
	if err := c.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	if c.profiles[0].IdentityID != "" || c.profiles[0].EncryptedCredentials == "" {
		t.Fatal("unlink did not preserve independent encrypted credentials")
	}
	if err := c.deleteIdentity(id); err != nil {
		t.Fatal(err)
	}
	var restored desktop
	if err := restored.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	s, err := restored.profileCredential(restored.profiles[0])
	if err != nil || s.Password != "new" {
		t.Fatalf("unlinked credentials not preserved: %v", err)
	}
}
