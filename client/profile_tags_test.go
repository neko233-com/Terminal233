package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestProfileTagsRoundTripSearchAndFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections", settings: connectionSettings{Host: "one.example", Port: "22", User: "alice"}, profileLabel: "one", profileTags: "生产, api， API"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved diskProfiles
	if json.Unmarshal(data, &saved) != nil || saved.Version != 4 || len(saved.Items[0].Tags) != 2 {
		t.Fatalf("tags not saved in version 4: %s", data)
	}
	var restored desktop
	if err = restored.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if strings.Join(restored.profiles[0].Tags, ",") != "生产,api" {
		t.Fatal("tag normalization lost")
	}
	restored.profiles = append(restored.profiles, profile{ID: "2", Label: "two", Settings: connectionSettings{Host: "two.example", Port: "22", User: "bob"}, Tags: []string{"development"}})
	view := ui.NewTester(restored.view, 1280, 900)
	if err := view.Click("搜索标签：生产"); err != nil {
		t.Fatal(err)
	}
	if restored.profileSearch != "生产" || !view.HasText("one") || view.HasText("two") {
		t.Fatal("clicking a tag did not filter hosts")
	}
	results := restored.newTabResults("api one")
	if len(results) != 1 || results[0].id != "1" {
		t.Fatal("new-tab search did not match tags")
	}
	restored.loadProfile(restored.profiles[0])
	restored.profileTags = "changed"
	if !restored.hasUnsavedConnection() {
		t.Fatal("tag edits not included in draft protection")
	}
}

func TestProfileTagsRejectInvalidAndLegacyTaggedFiles(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", 49), "ok,\x01bad", "a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q"} {
		if _, err := parseProfileTags(input); err == nil {
			t.Fatalf("invalid tags accepted: %q", input)
		}
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := []byte(`{"Version":3,"Items":[{"Host":"example.test","Port":"22","User":"alice","Tags":["prod"]}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var d desktop
	if err := d.loadProfiles(path); err == nil {
		t.Fatal("legacy version accepted tags it could erase")
	}
	read, err := os.ReadFile(path)
	if err != nil || string(read) != string(data) {
		t.Fatal("invalid source file changed")
	}
}
