package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestGroupCardsNavigateHierarchyWithoutHidingDescendantHosts(t *testing.T) {
	d := &desktop{
		workspace: "connections", profileGroups: []string{"work", "work/prod", "personal"},
		profiles: []profile{{ID: "1", Label: "database", Group: "work/prod", Settings: connectionSettings{Host: "db.test", Port: "22", User: "alice"}}},
	}
	view := ui.NewTester(d.view, 1280, 1000)
	if !view.HasText("打开分组：work") || !view.HasText("打开分组：personal") || view.HasText("打开分组：work/prod") {
		t.Fatal("root cards should show only top-level groups")
	}
	if err := view.Click("打开分组：work"); err != nil {
		t.Fatal(err)
	}
	if d.profileFilter != "work" || !view.HasText("打开分组：work/prod") || !view.HasText("work/prod / database") {
		t.Fatal("group navigation did not show child group and descendant host")
	}
	if err := view.Click("打开分组：work/prod"); err != nil {
		t.Fatal(err)
	}
	if d.profileFilter != "work/prod" {
		t.Fatal("nested group did not open")
	}
	if err := view.Click("返回上级分组"); err != nil {
		t.Fatal(err)
	}
	if d.profileFilter != "work" {
		t.Fatal("parent navigation did not return to work")
	}
}

func TestGroupCardsAcceptHostDrag(t *testing.T) {
	d := &desktop{workspace: "connections", profileGroups: []string{"work"}, profiles: []profile{{ID: "1", Label: "host", Settings: connectionSettings{Host: "host.test", Port: "22", User: "alice"}}}}
	view := ui.NewTester(d.view, 1280, 900)
	source, sourceOK := view.Find("host")
	target, targetOK := view.Find("打开分组：work")
	if !sourceOK || !targetOK {
		t.Fatal("drag source or group card missing")
	}
	view.Press(source.X+source.W/2, source.Y+source.H/2)
	view.Move(target.X+target.W/2, target.Y+target.H/2)
	view.Release(target.X+target.W/2, target.Y+target.H/2)
	if d.profiles[0].Group != "work" {
		t.Fatal("dragging the host to a group card did not move it")
	}
}
