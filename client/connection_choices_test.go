package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestHostTagCompletionsKeepExistingInputAndRespectLimits(t *testing.T) {
	choices := hostTagCompletions("生产, da", []string{"database", "API", "生产"}, true)
	if len(choices) != 1 || choices[0].value != "生产, database" {
		t.Fatalf("incorrect completion: %+v", choices)
	}
	choices = hostTagCompletions("生产", []string{"生产", "api"}, false)
	if len(choices) != 1 || choices[0].value != "生产, api" {
		t.Fatal("opening suggestions discarded an existing tag")
	}
	choices = hostTagCompletions("alpha， da", []string{"database"}, true)
	if len(choices) != 1 || choices[0].value != "alpha， database" {
		t.Fatal("full-width separator not supported")
	}
	if len(hostTagCompletions(strings.Join([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"}, ","), []string{"extra"}, false)) != 0 {
		t.Fatal("tag limit ignored")
	}
}

func TestHostEditorTagAndGroupSuggestions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{workspace: "connections"}
	if err := d.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	d.profileGroups = []string{"work", "work/prod"}
	d.profiles = []profile{{ID: "1", Label: "source", Tags: []string{"production", "database"}, Settings: connectionSettings{Host: "source.test", Port: "22", User: "alice"}}, {ID: "2", Label: "edited", Settings: connectionSettings{Host: "edited.test", Port: "22", User: "alice"}}}
	d.nextProfileID = 2
	if err := d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	d.loadProfile(d.profiles[1])
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.Click("连接分组"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("选择分组：work/prod"); err != nil {
		t.Fatal(err)
	}
	if d.profileGroup != "work/prod" || !d.hasUnsavedConnection() {
		t.Fatal("group selection was not a draft change")
	}
	if err := v.Click("主机标签"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("添加标签：database"); err != nil {
		t.Fatal(err)
	}
	if d.profileTags != "database" {
		t.Fatalf("clicked tag suggestion lost content: %q", d.profileTags)
	}
	v.Type(", pro")
	if !v.HasText("添加标签：production") {
		t.Fatalf("typed tag suggestion missing: %v", v.Texts())
	}
	v.Key(0, ui.KeyEnter)
	if d.profileTags != "database, production" {
		t.Fatalf("keyboard completion failed: %q", d.profileTags)
	}
	d.flushProfileAutosave()
	var restored desktop
	if err := restored.loadProfiles(path); err != nil {
		t.Fatal(err)
	}
	if restored.profiles[1].Group != "work/prod" || strings.Join(restored.profiles[1].Tags, ",") != "database,production" {
		t.Fatal("selected values were not saved")
	}
}

func TestHostSuggestionsAreScopedAndDontReplaceMiddleText(t *testing.T) {
	d := &desktop{profiles: []profile{{Tags: []string{"local-only"}}}, accounts: []cloudAccount{{Tags: []string{"team-only"}}}, cloud: &cloudClient{cloudTags: true}, settingsFromCloud: true}
	if tags := d.hostTagVocabulary(); len(tags) != 1 || tags[0] != "team-only" {
		t.Fatal("local tags leaked into cloud suggestions")
	}
	d.cloudListStale = true
	if len(d.hostTagVocabulary()) != 0 {
		t.Fatal("stale cloud tags offered")
	}
	d.settingsFromCloud = false
	if tags := d.hostTagVocabulary(); len(tags) != 1 || tags[0] != "local-only" {
		t.Fatal("cloud tags leaked into local suggestions")
	}
	d.workspace = "connections"
	d.profiles = []profile{{ID: "1", Label: "edited", Tags: []string{"first", "tail"}, Settings: connectionSettings{Host: "example.test", Port: "22", User: "alice"}}, {ID: "2", Tags: []string{"extra"}}}
	d.loadProfile(d.profiles[0])
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.Click("主机标签"); err != nil {
		t.Fatal(err)
	}
	v.Key(0, ui.KeyHome)
	v.Frame()
	if v.HasText("添加标签：extra") {
		t.Fatal("suggestions could replace text while caret was in the middle")
	}
	v.Key(0, ui.KeyEnter)
	if d.profileTags != "first, tail" {
		t.Fatal("keyboard completion changed unrelated text")
	}
}

func TestCloudHostGroupPickerHonorsReadOnlyAndStaleState(t *testing.T) {
	account := cloudAccount{ID: "a", Label: "host", Host: "example.test", Port: 22, Username: "alice"}
	d := &desktop{workspace: "connections", cloud: &cloudClient{teamID: "team", cloudGroups: true}, teams: []cloudTeam{{ID: "team", Role: "member"}}, accounts: []cloudAccount{account}, cloudGroupSnapshot: cloudGroupSnapshot{Groups: []string{"target"}, Assignments: map[string]string{}, AccountIDs: []string{"a"}, Revision: strings.Repeat("a", 64)}}
	d.loadCloudAccount(account)
	v := ui.NewTester(d.view, 1280, 900)
	_ = v.Click("云端主机分组")
	if v.HasText("分组 / target") || d.busy {
		t.Fatal("read-only member could open group selection")
	}
	d.teams[0].Role = "owner"
	d.cloudGroupsStale = true
	v.Frame()
	_ = v.Click("云端主机分组")
	if v.HasText("分组 / target") || d.busy {
		t.Fatal("stale group selection was available")
	}
}
