package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestHostDragKeepsPendingIntentBoundToSourceAndTarget(t *testing.T) {
	terms := groupTestTerminals(t, 2)
	target := &workspaceSession{id: 1, label: "alpha", term: terms[0]}
	p := profile{ID: "1", Label: "fixture host", Settings: connectionSettings{Host: "example.test", Port: "22", User: "alice"}}
	d := &desktop{workspace: "connections", currentSession: 1, term: terms[0], sessions: []*workspaceSession{target}, profiles: []profile{p}}
	view := ui.NewTester(d.view, 1500, 1000)
	a, ok := view.Find("fixture host")
	if !ok {
		t.Fatal("host card missing")
	}
	b, ok := view.Find("会话标签：alpha")
	if !ok {
		t.Fatal("terminal tab missing")
	}
	sx, sy := a.X+a.W/2, a.Y+a.H/2
	tx, ty := b.X+b.W/2, b.Y+b.H/2
	view.Move(sx, sy)
	view.Press(sx, sy)
	view.Move(tx, ty)
	view.Release(tx, ty)
	intent := d.pendingWorkspaceHost
	if intent == nil || !d.editConnection || !intent.matchesForm(d, d.settings, false) || !intent.targetStillValid(d) || d.fingerprint != "" {
		t.Fatal("untrusted host drop bypassed or lost verification")
	}
	changed := d.settings
	changed.Host = "different.test"
	if intent.matchesForm(d, changed, false) || intent.matchesForm(d, d.settings, true) {
		t.Fatal("changed endpoint or file-only connection matched intent")
	}
	d.sessions = append(d.sessions, &workspaceSession{id: 2, label: "bravo", term: terms[1]})
	if err := d.createTerminalGroup("other", []int{1, 2}, 1, false); err != nil {
		t.Fatal(err)
	}
	if intent.targetStillValid(d) {
		t.Fatal("target changing groups remained valid")
	}
	d.loadProfile(profile{ID: "2", Label: "other", Settings: p.Settings})
	if d.pendingWorkspaceHost != nil {
		t.Fatal("switching profiles preserved pending drop")
	}
}

func TestCloudHostDropRequiresSameScopeAndExplicitTrust(t *testing.T) {
	term := groupTestTerminals(t, 1)[0]
	c, err := newCloudClient("https://sync.example.test")
	if err != nil {
		t.Fatal(err)
	}
	c.token = "test-token"
	c.teamID = "team-a"
	account := cloudAccount{ID: "host", Label: "cloud fixture", Host: "host.example.test", Port: 22, Username: "alice", Password: "test-secret", HostKeyFingerprint: "SHA256:cloud-only"}
	d := &desktop{workspace: "connections", currentSession: 1, term: term, sessions: []*workspaceSession{{id: 1, label: "target", term: term}}, cloud: c, cloudUser: "owner", accounts: []cloudAccount{account}}
	view := ui.NewTester(d.view, 1500, 1000)
	a, ok := view.Find("cloud fixture · alice@host.example.test")
	if !ok {
		t.Fatal("cloud host card missing")
	}
	b, ok := view.Find("会话标签：target")
	if !ok {
		t.Fatal("terminal tab missing")
	}
	sx, sy := a.X+a.W/2, a.Y+a.H/2
	tx, ty := b.X+b.W/2, b.Y+b.H/2
	view.Move(sx, sy)
	view.Press(sx, sy)
	view.Move(tx, ty)
	view.Release(tx, ty)
	intent := d.pendingWorkspaceHost
	if intent == nil || !intent.matchesForm(d, d.settings, false) || d.fingerprint != "" {
		t.Fatal("cloud fingerprint became locally trusted")
	}
	c.teamID = "team-b"
	if intent.matchesForm(d, d.settings, false) {
		t.Fatal("other team matched pending host")
	}
	c.teamID = "team-a"
	if !d.closeConnectionEditor() || d.pendingWorkspaceHost != nil {
		t.Fatal("closing editor failed to cancel pending drop")
	}
}
