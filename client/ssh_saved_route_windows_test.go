//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSavedJumpChainConnectionWorkspaceAndBackup(t *testing.T) {
	first, fp1, _ := testSSHServer(t)
	second, fp2, _ := testSSHServer(t)
	target, fp3, _ := testSSHServer(t)
	first.Fingerprint, second.Fingerprint, target.Fingerprint = fp1, fp2, fp3
	dir := t.TempDir()
	store := filepath.Join(dir, "profiles.json")
	d := &desktop{forwardRulesPath: filepath.Join(dir, "forwards.json")}
	if err := d.loadProfiles(store); err != nil {
		t.Fatal(err)
	}
	save := func(settings connectionSettings, name string) profile {
		t.Helper()
		d.selectedProfile = ""
		d.settings, d.active, d.profileLabel = settings, settings, name
		if err := d.saveCurrentProfile(); err != nil {
			t.Fatal(err)
		}
		p := d.profiles[len(d.profiles)-1]
		if _, err := d.ensureForwardHostID(p.ID); err != nil {
			t.Fatal(err)
		}
		return d.profiles[len(d.profiles)-1]
	}
	p1 := save(first, "first")
	second.JumpID = p1.StableID
	p2 := save(second, "second")
	target.JumpID = p2.StableID
	p3 := save(target, "target")
	data, err := os.ReadFile(store)
	if err != nil || strings.Contains(string(data), target.Password) {
		t.Fatal("profile storage exposed a password or could not be read")
	}
	reopened := &desktop{forwardRulesPath: d.forwardRulesPath}
	if err := reopened.loadProfiles(store); err != nil {
		t.Fatal(err)
	}
	settings, err := reopened.profileCredential(reopened.profiles[2])
	if err != nil || settings.JumpID != p2.StableID {
		t.Fatalf("saved jump binding did not round-trip: %v", err)
	}
	tampered := reopened.profiles[2]
	tampered.Settings.JumpID = p1.StableID
	if _, err := reopened.profileCredential(tampered); err == nil {
		t.Fatal("protected credentials accepted a changed jump binding")
	}
	settings, err = reopened.resolveSavedRoute(settings)
	if err != nil || settings.route == nil || len(settings.route.hops) != 2 {
		t.Fatalf("saved multi-hop route did not resolve: %v", err)
	}
	if fingerprint, err := inspectHostThrough(settings, nil); err != nil || fingerprint != fp3 {
		t.Fatalf("could not inspect target through the saved route: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := connectSSHClient(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	code, hosts, _ := agentTestCall(t, store, "hosts")
	if code != 0 || hosts["hosts"].([]any)[2].(map[string]any)["jump_host_id"] != "local:"+p2.StableID {
		t.Fatal("agent host metadata omitted the saved jump reference")
	}
	code, result, output := agentTestCall(t, store, "ls", "--host", agentHostID(p3), "--path", filepath.ToSlash(t.TempDir()))
	if code != 0 || result["ok"] != true || strings.Contains(output, target.Password) {
		t.Fatalf("agent CLI did not use the saved route safely: %v", result)
	}
	w := savedWorkspace{ID: "0123456789abcdef0123456789abcdef", Name: "route", Active: 0, SplitFirst: -1, SplitSecond: -1, Entries: []workspaceTemplateEntry{{Kind: "profile", Label: p3.Label, Group: p3.Group, Host: target.Host, Port: target.Port, User: target.User, KeyPath: target.KeyPath, Fingerprint: fp3, JumpRoute: settings.savedRouteSignature()}}}
	if _, err := reopened.resolveWorkspace(w); err != nil {
		t.Fatalf("workspace could not resolve its unchanged route: %v", err)
	}
	if _, err := prepareCloudAccount(settings, "target", nil, ""); err == nil {
		t.Fatal("cloud sync silently discarded a saved jump route")
	}
	unresolved := settings
	unresolved.route = nil
	if direct, err := connectSSHClient(ctx, unresolved); err == nil {
		_ = direct.Close()
		t.Fatal("unresolved saved route fell back to a direct connection")
	}
	reopened.selectedProfile = string(p3.ID)
	reopened.profiles[0].Settings.JumpID = p3.StableID
	if err := reopened.validateSavedJumpChoice(p2.StableID); err == nil {
		t.Fatal("a cyclic jump chain was accepted")
	}
	reopened.profiles[0].Settings.JumpID = ""
	reopened.profiles[0].Settings.Fingerprint = ""
	if reopened.validateSessionSavedRoute(settings) == nil {
		t.Fatal("revoked jump trust did not block reconnect")
	}
	if _, err := reopened.resolveWorkspace(w); err == nil {
		t.Fatal("workspace accepted revoked jump trust")
	}
	reopened.profiles[0].Settings.Fingerprint = fp1
	doc, err := reopened.snapshotLocalArchive()
	if err != nil || doc.Version != 2 {
		t.Fatalf("saved route was not included in backup: %v", err)
	}
	destDir := t.TempDir()
	dest := &desktop{forwardRulesPath: filepath.Join(destDir, "forwards.json")}
	if err := dest.loadProfiles(filepath.Join(destDir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if err := dest.importLocalArchive(doc); err != nil {
		t.Fatal(err)
	}
	if len(dest.profiles) != 3 || dest.profiles[1].Settings.JumpID != dest.profiles[0].StableID || dest.profiles[2].Settings.JumpID != dest.profiles[1].StableID || dest.profiles[0].StableID == p1.StableID {
		t.Fatal("backup import did not remap the jump chain")
	}
	if _, err := dest.profileCredential(dest.profiles[2]); err != nil {
		t.Fatal("imported credentials were not rebound to the remapped jump")
	}
	if _, err := dest.resolveSavedRoute(dest.profiles[2].Settings); err == nil {
		t.Fatal("imported jump was trusted without local fingerprint verification")
	}
}

func TestExistingCredentialsAndIdentityRebindToSavedJump(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.settings = connectionSettings{Host: "jump.example", Port: "22", User: "jump", Password: "jump-fixture"}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	jumpID, err := d.ensureForwardHostID(d.profiles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	d.selectedProfile = ""
	d.settings = connectionSettings{Host: "target.example", Port: "22", User: "user", Password: "target-fixture"}
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	d.loadProfile(d.profiles[1])
	d.setSavedConnectionJump(jumpID, nil)
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	var reloaded desktop
	if err := reloaded.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	credentials, err := reloaded.profileCredential(reloaded.profiles[1])
	if err != nil || credentials.JumpID != jumpID || credentials.Password != "target-fixture" {
		t.Fatalf("existing credentials were not rebound to the new jump: %v", err)
	}
	reloaded.loadProfile(reloaded.profiles[1])
	reloaded.setSavedConnectionJump("", nil)
	if err := reloaded.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	credentials, err = reloaded.profileCredential(reloaded.profiles[1])
	if err != nil || credentials.JumpID != "" || credentials.Password != "target-fixture" {
		t.Fatalf("credentials were not rebound after removing the jump: %v", err)
	}
	if err := reloaded.saveIdentity("", identityDraft{Name: "shared", User: "alice", Password: "identity-fixture"}); err != nil {
		t.Fatal(err)
	}
	reloaded.selectedProfile = ""
	reloaded.settings = connectionSettings{Host: "identity.example", Port: "22", JumpID: jumpID}
	if err := reloaded.chooseProfileIdentity(reloaded.identities[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	identityProfile := reloaded.profiles[len(reloaded.profiles)-1]
	credentials, err = reloaded.profileCredential(identityProfile)
	if err != nil || credentials.JumpID != jumpID || credentials.Password != "identity-fixture" {
		t.Fatalf("saved identity did not retain its jump binding: %v", err)
	}
	identityProfile.Settings.JumpID = ""
	if _, err := reloaded.profileCredential(identityProfile); err == nil {
		t.Fatal("identity binding accepted a removed jump route")
	}
}

func TestSavedJumpBackupPreservesMissingReference(t *testing.T) {
	missing := "0123456789abcdef0123456789abcdef"
	doc := localArchiveDocument{Version: 2, Profiles: []localArchiveProfile{{Host: "target.example", Port: "22", User: "alice", Password: "fixture", JumpID: missing}}}
	if err := validateLocalArchive(doc); err != nil {
		t.Fatalf("cannot back up a missing jump reference: %v", err)
	}
	dir := t.TempDir()
	d := &desktop{forwardRulesPath: filepath.Join(dir, "forwards.json")}
	if err := d.loadProfiles(filepath.Join(dir, "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if err := d.importLocalArchive(doc); err != nil {
		t.Fatal(err)
	}
	profile := d.profiles[0]
	if profile.Settings.JumpID == "" || profile.Settings.JumpID == missing {
		t.Fatal("missing jump reference was dropped or not remapped")
	}
	settings, err := d.profileCredential(profile)
	if err != nil || settings.Password != "fixture" {
		t.Fatalf("imported credentials were not bound to the preserved reference: %v", err)
	}
	if _, err := d.resolveSavedRoute(settings); err == nil {
		t.Fatal("missing jump reference allowed direct connection")
	}
}
