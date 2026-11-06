package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestQuickConnectionInput(t *testing.T) {
	for _, tc := range []struct{ input, user, host, port string }{
		{"alice@example.test", "alice", "example.test", "22"},
		{"ssh alice@example.test -p 2222", "alice", "example.test", "2222"},
		{"ssh -p 0022 root@192.0.2.1", "root", "192.0.2.1", "22"},
		{"ssh root@[2001:db8::1]", "root", "2001:db8::1", "22"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseQuickConnection(tc.input)
			if err != nil || got.User != tc.user || got.Host != tc.host || got.Port != tc.port {
				t.Fatalf("unexpected parse: %#v, %v", got, err)
			}
			if _, err := got.address(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, input := range []string{"", "host", "ssh -i key alice@host", "ssh alice:secret@host", "ssh alice@host;whoami", "ssh alice@host\n-p 22", "ssh alice@host -p 0", "ssh alice@host -p 65536", "ssh alice@host -p +22", "ssh alice@host -p 22 -p 23", "ssh alice@host extra", "ssh alice@2001:db8::1", "ssh alice@[bad]", "ssh alice@host/path", "ssh alice@@host"} {
		t.Run(input, func(t *testing.T) {
			got, err := parseQuickConnection(input)
			if err == nil || got != (connectionSettings{}) {
				t.Fatalf("accepted invalid input: %#v", got)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed credentials")
			}
		})
	}
}

func TestQuickConnectionButtonOpensForm(t *testing.T) {
	d := &desktop{workspace: "connections", profileSearch: "ssh alice@example.test -p 2222"}
	view := ui.NewTester(d.view, 1200, 1600)
	if err := view.Click("填写连接信息"); err != nil {
		t.Fatal(err)
	}
	if !d.editConnection || d.settings.Host != "example.test" || d.settings.Port != "2222" || d.fingerprint != "" {
		t.Fatal("quick connection did not open an untrusted connection form")
	}
}

func TestSearchEnterOpensQuickConnection(t *testing.T) {
	d := &desktop{workspace: "connections"}
	view := ui.NewTester(d.view, 1200, 1600)
	if err := view.Click("连接列表：搜索连接或快速连接"); err != nil {
		t.Fatal(err)
	}
	view.Type("ssh alice@example.test -p 2222")
	view.Key(0, ui.KeyEnter)
	if !d.editConnection || d.settings.Host != "example.test" || d.settings.Port != "2222" || d.fingerprint != "" || d.profileSearch != "" {
		t.Fatal("Enter did not open the untrusted connection form")
	}
}

func TestQuickConnectionClearsPreviousTrustAndCredentials(t *testing.T) {
	d := &desktop{quickConnect: "ssh alice@new.example -p 2222", selectedProfile: "old", profileGroup: "old", profileLabel: "old", fingerprint: "SHA256:old", expectedFingerprint: "SHA256:old", settings: connectionSettings{Password: "secret", Passphrase: "secret", KeyPath: "key", Fingerprint: "SHA256:old"}}
	d.openQuickConnection()
	if !d.editConnection || d.settings.Host != "new.example" || d.settings.Port != "2222" || d.settings.User != "alice" || d.settings.Password != "" || d.settings.Passphrase != "" || d.settings.KeyPath != "" || d.settings.Fingerprint != "" || d.fingerprint != "" || d.expectedFingerprint != "" || d.selectedProfile != "" || d.quickConnect != "" {
		t.Fatalf("previous trust or credentials survived: %#v", d.settings)
	}
}
