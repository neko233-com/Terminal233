package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPortableWorkspaceRequiresLocalTrustAndOmitsDeviceData(t *testing.T) {
	c, err := newCloudClient("https://sync.example")
	if err != nil {
		t.Fatal(err)
	}
	w := savedWorkspace{ID: "0123456789abcdef0123456789abcdef", Name: "work", Entries: []workspaceTemplateEntry{{Kind: "local", Label: "local", LocalDir: `C:\private`}, {Kind: "cloud", Host: "host.example", Port: "22", User: "alice", Fingerprint: "SHA256:local-trust", CloudURL: c.base.String(), CloudUser: "alice", CloudID: "account", FileDir: "/var", TerminalDir: "/srv"}}, SplitFirst: -1, SplitSecond: -1}
	portable, err := portableWorkspace(w, c, "alice")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(portable)
	if strings.Contains(string(data), "private") || strings.Contains(string(data), "local-trust") || strings.Contains(string(data), "password") {
		t.Fatal("device data leaked")
	}
	if _, err = cloudWorkspaceLocal(portable, c, "alice", func(cloudAccount) (string, error) { return "", errors.New("untrusted") }); err == nil {
		t.Fatal("untrusted host accepted")
	}
	restored, err := cloudWorkspaceLocal(portable, c, "alice", func(cloudAccount) (string, error) { return "SHA256:other-device", nil })
	if err != nil {
		t.Fatal(err)
	}
	if restored.Entries[0].LocalDir != "" || restored.Entries[1].Fingerprint != "SHA256:other-device" {
		t.Fatal("device-local trust or path was copied")
	}
	w.Entries[1].Kind = "profile"
	w.Entries[1].CloudURL = ""
	w.Entries[1].CloudUser = ""
	w.Entries[1].CloudID = ""
	if _, err = portableWorkspace(w, c, "alice"); err == nil {
		t.Fatal("local-only host accepted")
	}
}

func TestCloudWorkspaceTabNamesRequireCapableServer(t *testing.T) {
	c, err := newCloudClient("https://sync.example")
	if err != nil {
		t.Fatal(err)
	}
	w := sampleWorkspace("0123456789abcdef0123456789abcdef", "workspace")
	w.Entries[0].TabName = "database"
	if _, err = portableWorkspace(w, c, "owner"); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatal("older server accepted tab names", err)
	}
	c.cloudWorkspaceNames = true
	portable, err := portableWorkspace(w, c, "owner")
	if err != nil || portable.Entries[0].TabName != "database" {
		t.Fatal("capable server lost tab name", err)
	}
	restored, err := cloudWorkspaceLocal(portable, c, "owner", func(cloudAccount) (string, error) { return "", errors.New("unused") })
	if err != nil || restored.Entries[0].TabName != "database" {
		t.Fatal("cloud tab name did not round-trip", err)
	}
}
