package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestNativeViewLoadsSyncedAccountWithoutTrustingIt(t *testing.T) {
	d := &desktop{settings: connectionSettings{Port: "22"}, accounts: []cloudAccount{{ID: "test", Label: "示例服务器", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:old"}}}
	test := ui.NewTester(d.view, 1200, 1800)
	if !test.HasText("SSH 连接") || !test.HasText("远程文件（测试中）") || !test.HasText("连接列表（仅本次运行）") {
		t.Fatalf("missing native controls: %v", test.Texts())
	}
	if err := test.Click("示例服务器"); err != nil {
		t.Fatal(err)
	}
	if d.settings.Host != "example.test" || d.fingerprint != "" || d.expectedFingerprint != "SHA256:old" {
		t.Fatalf("loaded unsafe state: %+v", d.settings)
	}
}

func TestConnectionListSearchAndContextMenu(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice", Password: "private"}, profileGroup: "工作"}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host = "two.test"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	test := ui.NewTester(d.view, 1200, 2200)
	if !test.HasText("工作 / alice@one.test") || !test.HasText("工作 / alice@two.test") {
		t.Fatalf("missing profiles: %v", test.Texts())
	}
	if err := test.RightClick("工作 / alice@one.test"); err != nil {
		t.Fatal(err)
	}
	if err := test.ChooseMenuItem("移除"); err != nil {
		t.Fatal(err)
	}
	if len(d.profiles) != 1 || d.profiles[0].Settings.Host != "two.test" {
		t.Fatalf("remove failed: %+v", d.profiles)
	}
	d.profileSearch = "not-found"
	test.Frame()
	if test.HasText("工作 / alice@two.test") {
		t.Fatal("search did not filter profile")
	}
}

func TestConnectionListDragOrder(t *testing.T) {
	d := &desktop{settings: connectionSettings{Host: "one.test", Port: "22", User: "alice"}}
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	d.settings.Host = "two.test"
	if err := d.addProfile(); err != nil {
		t.Fatal(err)
	}
	test := ui.NewTester(d.view, 1200, 2200)
	from, ok := test.Find("alice@two.test")
	if !ok {
		t.Fatal("source not found")
	}
	to, ok := test.Find("alice@one.test")
	if !ok {
		t.Fatal("target not found")
	}
	test.Press(from.X+from.W/2, from.Y+from.H/2)
	test.Move(to.X+to.W/2, to.Y+to.H/2)
	test.Release(to.X+to.W/2, to.Y+to.H/2)
	if d.profiles[0].Settings.Host != "two.test" {
		t.Fatalf("drag did not reorder: %+v", d.profiles)
	}
	test.Frame()
	first, _ := test.Find("alice@two.test")
	second, _ := test.Find("alice@one.test")
	if first.Y >= second.Y {
		t.Fatalf("view did not render new order: %v %v", first, second)
	}
}

func TestDroppedFilesWithoutConnectionGivesFeedback(t *testing.T) {
	d := &desktop{settings: connectionSettings{Port: "22"}}
	test := ui.NewTester(d.view, 1200, 2200)
	zone, ok := test.Find("拖入一个文件上传")
	if !ok {
		t.Fatal("drop zone missing")
	}
	if !test.DropFiles(zone.X+zone.W/2, zone.Y+zone.H/2, "example.txt") {
		t.Fatal("drop not accepted")
	}
	if d.fileStatus != "请先连接后再上传" {
		t.Fatalf("unexpected feedback: %q", d.fileStatus)
	}
}
