package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTagFilterRequiresAllTagsAcrossLocalAndCloud(t *testing.T) {
	d := &desktop{workspace: "connections", cloud: &cloudClient{}, profiles: []profile{
		{ID: "1", Label: "local-prod", Group: "work", Tags: []string{"PROD", "database"}, Settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}},
		{ID: "2", Label: "local-api", Group: "work", Tags: []string{"prod", "api"}, Settings: connectionSettings{Host: "two.test", Port: "22", User: "alice"}},
	}, accounts: []cloudAccount{{ID: "3", Label: "cloud-prod", Host: "three.test", Port: 22, Username: "alice", Tags: []string{"prod", "database"}}}}
	v := ui.NewTester(d.view, 1280, 900)
	if err := v.Click("标签筛选"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("筛选标签：PROD"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("筛选标签：database"); err != nil {
		t.Fatal(err)
	}
	if !d.profileVisible(d.profiles[0]) || d.profileVisible(d.profiles[1]) || !d.cloudAccountVisible(d.accounts[0]) {
		t.Fatal("tags did not require all selected values")
	}
	if v.HasText("打开本机终端") || v.HasText("打开分组：work") {
		t.Fatal("tag filter retained unrelated cards")
	}
	d.profileSearch = "three"
	if d.profileVisible(d.profiles[0]) || !d.cloudAccountVisible(d.accounts[0]) {
		t.Fatal("search and tags did not combine")
	}
	if err := v.Click("清除标签筛选"); err != nil {
		t.Fatal(err)
	}
	if len(d.profileTagFilters) != 0 || d.profileSearch != "three" {
		t.Fatal("clear removed search or retained filters")
	}
}

func TestTagFilterOptionsRespectScopeAndRetainSelected(t *testing.T) {
	d := &desktop{profileSource: "local", profileFilter: "work", profileTagFilters: []string{"orphan"}, profiles: []profile{{Group: "work/prod", Tags: []string{"one"}}, {Group: "else", Tags: []string{"two"}}}, cloud: &cloudClient{}, accounts: []cloudAccount{{Tags: []string{"cloud"}}}}
	opts := d.profileTagOptions()
	if len(opts) != 2 || opts[0].name != "one" || opts[0].count != 1 || opts[1].name != "orphan" || opts[1].count != 0 {
		t.Fatalf("incorrect scoped tags: %+v", opts)
	}
	d.toggleProfileTagFilter("ORPHAN")
	if len(d.profileTagFilters) != 0 {
		t.Fatal("case-insensitive deselection failed")
	}
}

func TestTagFilterCountsUnicodeCaseFoldTogether(t *testing.T) {
	d := &desktop{profiles: []profile{{Tags: []string{"ς"}}, {Tags: []string{"Σ"}}}}
	options := d.profileTagOptions()
	if len(options) != 1 || options[0].count != 2 || !matchesProfileTags([]string{"ς"}, []string{"σ"}) {
		t.Fatal("case-fold equivalent tags were split")
	}
}
