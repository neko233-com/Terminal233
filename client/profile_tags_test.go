package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	if err := view.Click("按标签筛选：生产"); err != nil {
		t.Fatal(err)
	}
	if !hasProfileTag(restored.profileTagFilters, "生产") || !view.HasText("one") || view.HasText("two") {
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

func TestCloudTagsPayloadOldServiceAndEmptyClear(t *testing.T) {
	var body []byte
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"id"}`))
	}))
	defer s.Close()
	c, err := newCloudClient(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http = s.Client()
	c.token = "token"
	a := cloudAccount{ID: "id", Tags: []string{"prod"}}
	if _, err = c.saveReturningRecord(a); err == nil {
		t.Fatal("old service silently lost tags")
	}
	c.cloudTags = true
	a.Tags = []string{}
	if _, err = c.saveReturningRecord(a); err != nil || !strings.Contains(string(body), `"tags":[]`) {
		t.Fatalf("empty tags not sent as explicit clear: %s %v", body, err)
	}
	a.Tags = nil
	if _, err = c.saveReturningRecord(a); err != nil || strings.Contains(string(body), `"tags"`) {
		t.Fatal("omitted tags should preserve remote data")
	}
}

func TestCloudTagsVisibleSearchAndEditor(t *testing.T) {
	a := cloudAccount{ID: "cloud1", Label: "remote", Host: "example.test", Port: 22, Username: "alice", Tags: []string{"生产", "api"}}
	d := &desktop{workspace: "connections", cloud: &cloudClient{cloudTags: true}, accounts: []cloudAccount{a}}
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.Click("按云端标签筛选：生产"); err != nil {
		t.Fatal(err)
	}
	if !hasProfileTag(d.profileTagFilters, "生产") || !d.cloudAccountVisible(a) || d.cloudAccountVisible(cloudAccount{Label: "other", Host: "other.test"}) {
		t.Fatal("cloud tag filtering failed")
	}
	results := d.newTabResults("api remote")
	if len(results) != 1 || results[0].id != "cloud1" {
		t.Fatal("new-tab cloud tag search failed")
	}
	d.loadCloudAccount(a)
	if d.profileTags != "生产, api" {
		t.Fatal("cloud tags missing from editor")
	}
	binding := cloudSaveBinding{client: d.cloud, form: true, formSettings: d.settings, formLabel: d.profileLabel, formTags: d.profileTags, formFromCloud: true, sourceID: "cloud1"}
	d.profileTags = "edited"
	if d.bindSavedCloudID(binding, "cloud1", "") {
		t.Fatal("late save was bound to changed tags")
	}
	binding.formTags = d.profileTags
	if !d.hasUnsavedConnection() || !d.bindSavedCloudID(binding, "cloud1", "") || d.hasUnsavedConnection() {
		t.Fatal("confirmed cloud tag update did not clear its draft")
	}
}

func TestCloudArchiveRetainsTagsAndGroups(t *testing.T) {
	a := cloudAccount{ID: "id", Label: "work", Host: "example.test", Port: 22, Username: "alice", Password: "password", HostKeyFingerprint: "SHA256:verified", Tags: []string{"private-tag"}}
	groups := cloudGroupSnapshot{Groups: []string{"work"}, Assignments: map[string]string{"id": "work"}, AccountIDs: []string{"id"}, Revision: strings.Repeat("a", 64)}
	data, err := encodeCloudArchiveWithGroups([]cloudAccount{a}, "long-backup-password", groups)
	if err != nil || strings.Contains(string(data), "private-tag") {
		t.Fatal("encrypted tag backup failed", err)
	}
	doc, err := decodeCloudArchiveDocument(data, "long-backup-password")
	if err != nil || doc.Version != 3 || len(doc.Accounts) != 1 || len(doc.Accounts[0].Tags) != 1 || doc.Accounts[0].Tags[0] != "private-tag" || doc.AccountGroups[0] != "work" {
		t.Fatal("archive lost tags or groups", err)
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
