package main

import "testing"

func TestProfilesDoNotRetainSecretsAndReorder(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice", Password: "password", Passphrase: "phrase"}, fingerprint: "SHA256:key", profileGroup: "工作"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host = "two.test"
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
