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
