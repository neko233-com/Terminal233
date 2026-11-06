package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func sampleWorkspace(id, name string) savedWorkspace {
	return savedWorkspace{ID: id, Name: name, Entries: []workspaceTemplateEntry{{Kind: "local", FileDir: "."}}, Active: 0, SplitFirst: -1, SplitSecond: -1}
}

func TestWorkspaceTemplatesPersistMergeAndRejectUnknownFields(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "workspaces.json")
	first := sampleWorkspace("00000000000000000000000000000001", "开发")
	second := sampleWorkspace("00000000000000000000000000000002", "运维")
	if _, err := updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, first); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, second); return nil }); err != nil {
		t.Fatal(err)
	}
	file, err := readWorkspaceTemplates(filename)
	if err != nil || len(file.Items) != 2 {
		t.Fatalf("merge lost workspaces: %+v %v", file, err)
	}
	original := []byte(`{"version":1,"items":[],"password":"never write"}`)
	if err = os.WriteFile(filename, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, first); return nil }); err == nil {
		t.Fatal("accepted unknown secret field")
	}
	after, _ := os.ReadFile(filename)
	if string(after) != string(original) {
		t.Fatal("invalid storage was overwritten")
	}
}

func TestWorkspaceSnapshotKeepsSplitAndNoCredentials(t *testing.T) {
	shell := &remoteShell{}
	settings := connectionSettings{Host: "example.test", Port: "22", User: "alice", KeyPath: "C:/key", Fingerprint: "SHA256:trusted"}
	p := profile{Label: "server", Group: "ops", Settings: settings}
	a, b := &terminal.Terminal{}, &terminal.Terminal{}
	d := &desktop{currentSession: 2, term: b, shell: shell, active: settings, fileDir: "/two", splitFirstSession: 1, splitSession: 1, splitLayout: "side", profiles: []profile{p}, sessions: []*workspaceSession{{id: 1, label: "local", term: a, fileDir: "C:/local"}, {id: 2, label: "remote", term: b, shell: shell, active: settings, fileDir: "/two"}}}
	w, err := d.workspaceSnapshot("项目")
	if err != nil {
		t.Fatal(err)
	}
	if w.Active != 1 || w.SplitFirst != 0 || w.SplitSecond != 1 || len(w.Entries) != 2 || w.Entries[1].Kind != "profile" {
		t.Fatalf("bad snapshot: %+v", w)
	}
	targets, err := d.resolveWorkspace(w)
	if err != nil || len(targets) != 2 || targets[1].settings.Fingerprint != "SHA256:trusted" {
		t.Fatalf("bad restore targets: %+v %v", targets, err)
	}
	d.profiles[0].Settings.Fingerprint = "SHA256:changed"
	if _, err = d.resolveWorkspace(w); err == nil {
		t.Fatal("changed host key was accepted")
	}
}

func TestWorkspaceCloudRestoreUsesLocallyVerifiedFingerprint(t *testing.T) {
	settings, fingerprint, attempts := testSSHServer(t)
	cloud, err := newCloudClient("https://cloud.example")
	if err != nil {
		t.Fatal(err)
	}
	cloud.teamID = "team"
	cloud.token = "test-token"
	d := &desktop{cloud: cloud, cloudUser: "owner", accounts: []cloudAccount{{ID: "host-id", Host: settings.Host, Port: mustWorkspacePort(t, settings.Port), Username: settings.User, Password: settings.Password, HostKeyFingerprint: fingerprint, UpdatedAt: "version-1"}}}
	e := workspaceTemplateEntry{Kind: "cloud", Host: settings.Host, Port: settings.Port, User: settings.User, Fingerprint: "SHA256:wrong", CloudURL: cloud.base.String(), CloudUser: "owner", CloudTeam: "team", CloudID: "host-id"}
	w := sampleWorkspace("00000000000000000000000000000003", "cloud")
	w.Entries = []workspaceTemplateEntry{e}
	targets, err := d.resolveWorkspace(w)
	if err != nil {
		t.Fatal(err)
	}
	targets, err = hydrateWorkspaceCloudTargets(context.Background(), targets, func(team string) ([]cloudAccount, error) {
		if team != "team" {
			t.Fatal("wrong cloud scope")
		}
		return d.accounts, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if targets[0].cloudVersion != "version-1" {
		t.Fatal("cloud revision was not retained")
	}
	if targets[0].settings.Fingerprint != e.Fingerprint {
		t.Fatal("trusted the cloud fingerprint")
	}
	if shell, term, err := openSSHSession(targets[0].settings, terminal.Options{}); err == nil {
		if term != nil {
			_ = term.Close()
		}
		if shell != nil {
			_ = shell.Close()
		}
		t.Fatal("accepted wrong host key")
	}
	if attempts.Load() != 0 {
		t.Fatal("sent credentials to unverified host")
	}
	d.cloudUser = "different-user"
	if _, err = d.resolveWorkspace(w); err == nil {
		t.Fatal("accepted the wrong cloud account")
	}
}

func mustWorkspacePort(t *testing.T, port string) int {
	t.Helper()
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWorkspaceTemplateUIAndNoSecretSerialization(t *testing.T) {
	dir := t.TempDir()
	term := &terminal.Terminal{}
	d := &desktop{workspace: "new-tab", newTabOpen: true, currentSession: 1, term: term, workspaceTemplatesPath: filepath.Join(dir, "workspaces.json"), sessions: []*workspaceSession{{id: 1, label: "local", term: term}}}
	v := ui.NewTester(d.view, 1200, 900)
	if err := v.Click("工作区"); err != nil {
		t.Fatal(err)
	}
	if err := v.Click("新工作区名称"); err != nil {
		t.Fatal(err)
	}
	v.Type("我的工作区")
	if err := v.Click("保存当前会话"); err != nil {
		t.Fatal(err)
	}
	if len(d.workspaceTemplates) != 1 || !v.HasText("我的工作区") {
		t.Fatal("workspace not saved in UI")
	}
	data, err := os.ReadFile(d.workspaceTemplatesPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "password") || strings.Contains(strings.ToLower(string(data)), "passphrase") {
		t.Fatal("credentials serialized")
	}
}

func TestWorkspaceRestoreRollsBackOnFailureAndCancellation(t *testing.T) {
	targets := []workspaceRestoreTarget{{label: "one"}, {label: "two"}, {label: "three"}}
	closed := []string{}
	closeSession := func(s workspaceRestoredSession) {
		if s.target.label != "" {
			closed = append(closed, s.target.label)
		}
	}
	_, err := prepareWorkspaceSessions(context.Background(), targets, func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
		if target.label == "two" {
			return workspaceRestoredSession{}, errors.New("connection failed")
		}
		return workspaceRestoredSession{target: target}, nil
	}, closeSession, nil)
	if err == nil || len(closed) != 1 || closed[0] != "one" {
		t.Fatalf("failure leaked a session: %v %v", closed, err)
	}
	closed = nil
	ctx, cancel := context.WithCancel(context.Background())
	_, err = prepareWorkspaceSessions(ctx, targets, func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
		cancel()
		return workspaceRestoredSession{target: target}, nil
	}, closeSession, nil)
	if !errors.Is(err, context.Canceled) || len(closed) != 1 || closed[0] != "one" {
		t.Fatalf("cancellation leaked a session: %v %v", closed, err)
	}
}

func TestWorkspaceSnapshotDoesNotSerializeSessionSecrets(t *testing.T) {
	shell := &remoteShell{}
	settings := connectionSettings{Host: "example.test", Port: "22", User: "alice", Password: "sensitive-session-password", KeyPath: "C:/key", Fingerprint: "SHA256:trusted"}
	p := profile{ID: "7", Label: "server", Group: "ops", Settings: settings}
	term := &terminal.Terminal{}
	d := &desktop{currentSession: 1, term: term, shell: shell, active: settings, profiles: []profile{p}, sessions: []*workspaceSession{{id: 1, term: term, shell: shell, active: settings, sourceProfileID: "7"}}}
	w, err := d.workspaceSnapshot("project")
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "workspaces.json")
	if _, err = updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, w); return nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), settings.Password) || strings.Contains(strings.ToLower(string(data)), "password") || strings.Contains(strings.ToLower(string(data)), "passphrase") {
		t.Fatal("SSH credentials were serialized")
	}
}

func TestWorkspaceHydratesMultipleCloudTeamsWithoutChangingScope(t *testing.T) {
	e := workspaceTemplateEntry{Kind: "cloud", Host: "example.test", Port: "22", User: "alice", Fingerprint: "SHA256:local", CloudTeam: "one", CloudID: "id-1"}
	other := e
	other.CloudTeam = "two"
	other.CloudID = "id-2"
	targets := []workspaceRestoreTarget{{entry: e, settings: connectionSettings{Fingerprint: e.Fingerprint}}, {entry: other, settings: connectionSettings{Fingerprint: other.Fingerprint}}, {entry: e, settings: connectionSettings{Fingerprint: e.Fingerprint}}}
	calls := map[string]int{}
	result, err := hydrateWorkspaceCloudTargets(context.Background(), targets, func(team string) ([]cloudAccount, error) {
		calls[team]++
		id := "id-1"
		if team == "two" {
			id = "id-2"
		}
		return []cloudAccount{{ID: id, Host: "example.test", Port: 22, Username: "alice", Password: "fixture-password", UpdatedAt: "revision"}}, nil
	})
	if err != nil || len(result) != 3 || calls["one"] != 1 || calls["two"] != 1 || result[1].settings.Fingerprint != "SHA256:local" || result[1].settings.Password != "fixture-password" {
		t.Fatalf("mixed-team resolution failed: %v", err)
	}
	if targets[0].settings.Password != "" {
		t.Fatal("mutated original target")
	}
}

func TestWorkspaceHydratesScopesThroughHTTPSWithoutChangingActiveTeam(t *testing.T) {
	var unauthorized atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			unauthorized.Store(true)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/accounts":
			_, _ = w.Write([]byte(`[{"id":"personal","host":"personal.test","port":22,"username":"alice","password":"fixture-one","updatedAt":"v1"}]`))
		case "/api/v1/teams/team-b/accounts":
			_, _ = w.Write([]byte(`[{"id":"team","host":"team.test","port":22,"username":"bob","password":"fixture-two","updatedAt":"v2"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client, err := newCloudClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http = srv.Client()
	client.token = "fixture-token"
	client.teamID = "team-a"
	entries := []workspaceTemplateEntry{
		{Kind: "cloud", Host: "personal.test", Port: "22", User: "alice", Fingerprint: "SHA256:personal", CloudID: "personal"},
		{Kind: "cloud", Host: "team.test", Port: "22", User: "bob", Fingerprint: "SHA256:team", CloudTeam: "team-b", CloudID: "team"},
	}
	targets := []workspaceRestoreTarget{{entry: entries[0], settings: connectionSettings{Fingerprint: entries[0].Fingerprint}}, {entry: entries[1], settings: connectionSettings{Fingerprint: entries[1].Fingerprint}}}
	snapshot := *client
	resolved, err := hydrateWorkspaceCloudTargets(context.Background(), targets, func(team string) ([]cloudAccount, error) { scope := snapshot; scope.teamID = team; return scope.list() })
	if err != nil || unauthorized.Load() || client.teamID != "team-a" || resolved[0].settings.Password != "fixture-one" || resolved[1].settings.Password != "fixture-two" || resolved[1].cloudVersion != "v2" {
		t.Fatalf("HTTPS cloud scope resolution failed: %v", err)
	}
}

func TestWorkspaceSnapshotPersistsCloudReferenceWithoutSecret(t *testing.T) {
	shell := &remoteShell{}
	term := &terminal.Terminal{}
	settings := connectionSettings{Host: "cloudhost.test", Port: "22", User: "alice", Password: "cloud-password-fixture", Fingerprint: "SHA256:verified"}
	d := &desktop{currentSession: 1, term: term, shell: shell, active: settings, sessions: []*workspaceSession{{id: 1, term: term, shell: shell, active: settings, cloudOriginURL: "https://cloud.example", cloudOriginUser: "owner", cloudOriginTeam: "team", cloudOriginID: "id"}}}
	w, err := d.workspaceSnapshot("cloud workspace")
	if err != nil {
		t.Fatal(err)
	}
	e := w.Entries[0]
	if e.Kind != "cloud" || e.CloudTeam != "team" || e.CloudID != "id" || e.Fingerprint != settings.Fingerprint {
		t.Fatal("cloud source identity was lost")
	}
	data, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), settings.Password) {
		t.Fatal("cloud password serialized")
	}
}

func TestWorkspaceRejectsStaleTemplateBeforeRestore(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "workspaces.json")
	w := sampleWorkspace("00000000000000000000000000000009", "original")
	if _, err := updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items = append(f.Items, w); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := workspaceTemplateUnchanged(filename, w); err != nil {
		t.Fatal(err)
	}
	if _, err := updateWorkspaceTemplates(filename, func(f *workspaceTemplatesFile) error { f.Items[0].Name = "renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := workspaceTemplateUnchanged(filename, w); err == nil {
		t.Fatal("accepted a stale template")
	}
}

func TestWorkspaceRestoreRejectsSessionThatEndsBeforeCommit(t *testing.T) {
	left, right := net.Pipe()
	term, err := terminal.New(terminal.Options{Conn: left, NoClipboardWrite: true, ImageLimit: -1})
	if err != nil {
		t.Fatal(err)
	}
	_ = right.Close()
	select {
	case <-term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("test terminal did not finish")
	}
	closed := 0
	_, err = prepareWorkspaceSessions(context.Background(), []workspaceRestoreTarget{{label: "ended"}}, func(target workspaceRestoreTarget) (workspaceRestoredSession, error) {
		return workspaceRestoredSession{target: target, term: term}, nil
	}, func(s workspaceRestoredSession) { closed++; _ = s.term.Close() }, nil)
	if err == nil || closed != 1 {
		t.Fatalf("accepted an ended session: %v", err)
	}
}
