package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHRoutePinsEveryHop(t *testing.T) {
	first, fp1, attempts1 := testSSHServer(t)
	second, fp2, attempts2 := testSSHServer(t)
	target, fp3, attempts3 := testSSHServer(t)
	first.Fingerprint, second.Fingerprint, target.Fingerprint = fp1, fp2, fp3
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bad := second
	bad.Fingerprint = "SHA256:incorrect"
	if c, err := connectSSHRoute(ctx, []connectionSettings{first, bad, target}); err == nil {
		_ = c.Close()
		t.Fatal("accepted an untrusted intermediate host")
	}
	if attempts1.Load() == 0 || attempts2.Load() != 0 || attempts3.Load() != 0 {
		t.Fatal("untrusted hop received credentials or target was contacted")
	}
	client, err := connectSSHRoute(ctx, []connectionSettings{first, second, target})
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if attempts2.Load() == 0 || attempts3.Load() == 0 {
		t.Fatal("the route did not authenticate every hop")
	}
}

func TestSSHJumpRouteCancellationDuringTargetHandshake(t *testing.T) {
	jump, fingerprint, _ := testSSHServer(t)
	jump.Fingerprint = fingerprint
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-ctx.Done()
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	target := connectionSettings{Host: host, Port: port, User: "fixture", Password: "fixture", Fingerprint: "SHA256:fixture"}
	result := make(chan error, 1)
	go func() {
		client, err := connectSSHRoute(ctx, []connectionSettings{jump, target})
		if client != nil {
			_ = client.Close()
		}
		result <- err
	}()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("target handshake was not reached")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt the tunneled handshake")
	}
}

func TestSSHJumpInspectAndSessionReconnectionGuard(t *testing.T) {
	jump, jumpFingerprint, _ := testSSHServer(t)
	target, targetFingerprint, targetAttempts := testSSHServer(t)
	jump.Fingerprint = jumpFingerprint
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parent, err := connectSSHClient(ctx, jump)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	fingerprint, err := inspectHostThrough(target, parent)
	if err != nil || fingerprint != targetFingerprint || targetAttempts.Load() != 0 {
		t.Fatalf("inspection through the jump failed or authenticated: %v", err)
	}
	target.Fingerprint = fingerprint
	child, err := connectSSHClientThrough(ctx, target, parent)
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	parentSession := &workspaceSession{id: 1, fileOnly: true, shell: &remoteShell{client: parent, done: make(chan struct{})}, active: jump}
	childSession := &workspaceSession{jumpParent: parentSession, jumpFingerprint: jumpFingerprint}
	d := &desktop{sessions: []*workspaceSession{parentSession}}
	if via, err := d.sessionJumpClient(childSession); err != nil || via != parent {
		t.Fatal("active verified parent was not available for reconnect")
	}
	parentSession.active.Fingerprint = ""
	if _, err := d.sessionJumpClient(childSession); err == nil {
		t.Fatal("reconnect accepted a jump with revoked trust")
	}
	parentSession.active.Fingerprint = jumpFingerprint
	grandparent := &workspaceSession{id: 2, fileOnly: true, shell: &remoteShell{client: &ssh.Client{}, done: make(chan struct{})}, active: jump}
	parentSession.jumpParent, parentSession.jumpFingerprint = grandparent, jumpFingerprint
	d.sessions = []*workspaceSession{parentSession, grandparent}
	grandparent.active.Fingerprint = ""
	if _, err := d.sessionJumpClient(childSession); err == nil {
		t.Fatal("reconnect accepted a route with revoked ancestor trust")
	}
	parentSession.jumpParent, parentSession.jumpFingerprint = nil, ""
	d.sessions = []*workspaceSession{{id: 1, fileOnly: true, shell: &remoteShell{client: &ssh.Client{}, done: make(chan struct{})}, active: jump}}
	if _, err := d.sessionJumpClient(childSession); err == nil {
		t.Fatal("reconnect accepted a different session with the same ID")
	}
}

func TestAgentCLIMultipleJumpHosts(t *testing.T) {
	first, fp1, _ := testSSHServer(t)
	second, fp2, _ := testSSHServer(t)
	target, fp3, _ := testSSHServer(t)
	first.Fingerprint, second.Fingerprint, target.Fingerprint = fp1, fp2, fp3
	store := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{}
	if err := d.loadProfiles(store); err != nil {
		t.Fatal(err)
	}
	for _, settings := range []connectionSettings{first, second, target} {
		d.selectedProfile = ""
		d.settings, d.active = settings, settings
		if err := d.saveCurrentProfile(); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]string, len(d.profiles))
	for i, profile := range d.profiles {
		ids[i] = agentHostID(profile)
	}
	if len(ids) != 3 {
		t.Fatalf("expected three saved profiles, got %d", len(ids))
	}
	code, result, output := agentTestCall(t, store, "ls", "--host", ids[2], "--jump", ids[0], "--jump", ids[1], "--path", filepath.ToSlash(t.TempDir()))
	if code != 0 || result["ok"] != true || strings.Contains(output, first.Password) {
		t.Fatalf("multi-hop CLI failed or exposed credentials: %v", result)
	}
	code, result, _ = agentTestCall(t, store, "ls", "--host", ids[2], "--jump", ids[0], "--jump", ids[0])
	if code != 2 || result["ok"] != false {
		t.Fatal("duplicate jump host was accepted")
	}
}
