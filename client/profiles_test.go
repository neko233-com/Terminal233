package main

import "testing"

func TestProfilesDoNotRetainSecretsAndReorder(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice", Password: "password", Passphrase: "phrase"}, active: connectionSettings{Host: "one.test", Port: "22", User: "alice", Fingerprint: "SHA256:key"}, profileGroup: "工作"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host = "two.test"
	d.selectedProfile = ""
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if d.profiles[0].Settings.Password != "" || d.profiles[0].Settings.Passphrase != "" {
		t.Fatal("profile retained credential")
	}
	d.moveProfile(d.profiles[1].ID, d.profiles[0].ID)
	if d.profiles[0].Settings.Host != "two.test" {
		t.Fatal("profile order did not change")
	}
	d.loadProfile(d.profiles[1])
	if d.expectedFingerprint != "SHA256:key" || d.fingerprint != "" {
		t.Fatal("loaded profile bypassed host verification")
	}
}

func TestProfileEditPreservesOnlyPreviouslyTrustedKey(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}, fingerprint: "SHA256:unverified", profileGroup: "工作"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if d.profiles[0].Settings.Fingerprint != "" {
		t.Fatal("inspection was treated as trust")
	}
	d.profiles[0].Settings.Fingerprint = "SHA256:trusted"
	d.loadProfile(d.profiles[0])
	if d.profileGroup != "工作" {
		t.Fatal("editing lost group")
	}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if len(d.profiles) != 1 || d.profiles[0].Settings.Fingerprint != "SHA256:trusted" {
		t.Fatal("editing duplicated the profile or lost its trusted key")
	}
	d.settings.Host = "other.test"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if d.profiles[0].Settings.Fingerprint != "" {
		t.Fatal("trusted key retained for a changed endpoint")
	}
}

func TestTrustedReconnectOnlyUsesTheMatchingLocalProfile(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}, active: connectionSettings{Host: "one.test", Port: "22", User: "alice", Fingerprint: "SHA256:trusted"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.loadProfile(d.profiles[0])
	if d.trustedProfileFingerprint() != "SHA256:trusted" {
		t.Fatal("saved trusted profile was not available")
	}
	d.settings.Host = "other.test"
	if d.trustedProfileFingerprint() != "" {
		t.Fatal("trusted key was reused for a different endpoint")
	}
	d.settings.Host = "one.test"
	d.selectedProfile = ""
	if d.trustedProfileFingerprint() != "" {
		t.Fatal("unselected or cloud-loaded profile inherited local trust")
	}
}
