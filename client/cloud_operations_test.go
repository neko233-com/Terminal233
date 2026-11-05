package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCloudSaveSuccessWithRefreshFailureIsNotReportedAsFailed(t *testing.T) {
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport, client.token = server.Client().Transport, "token"
	accounts, mutationErr, refreshErr := mutateAndRefresh(func() error { return client.save(cloudAccount{Label: "new"}) }, client.list)
	if mutationErr != nil || refreshErr == nil || posts != 1 {
		t.Fatalf("wrong result: mutation=%v refresh=%v posts=%d", mutationErr, refreshErr, posts)
	}
	d := &desktop{cloud: client, workspace: "cloud", active: connectionSettings{Password: "secret"}}
	d.finishCloudSave(accounts, mutationErr, refreshErr)
	if !d.cloudListStale || !strings.Contains(d.cloudStatus, "已同步") || !strings.Contains(d.cloudStatus, "刷新失败") {
		t.Fatalf("unsafe result: %s", d.cloudStatus)
	}
	view := ui.NewTester(d.view, 1200, 1600)
	if !view.HasText("连接列表可能过期，请先刷新后再修改或删除。") {
		t.Fatal("stale list warning missing")
	}
	d.saveCloudConnection(connectionSettings{Host: "host", Port: "22", User: "u", Password: "p", Fingerprint: "SHA256:key"}, "", "", cloudSaveBinding{})
	if posts != 1 {
		t.Fatal("write repeated while list was stale")
	}
}

func TestCloudDeletePartialAndUncertainResults(t *testing.T) {
	failure := errors.New("network error")
	d := &desktop{accounts: []cloudAccount{{ID: "a"}, {ID: "b"}}}
	d.finishCloudDelete("a", nil, nil, failure)
	if !d.cloudListStale || len(d.accounts) != 1 || d.accounts[0].ID != "b" || !strings.Contains(d.cloudStatus, "已删除") {
		t.Fatal("successful delete was misreported or retained")
	}
	d.finishCloudDelete("b", nil, failure, nil)
	if !strings.Contains(d.cloudStatus, "未确认") || !strings.Contains(d.cloudStatus, "可能已生效") {
		t.Fatal("uncertain deletion treated as definitely failed")
	}
}

func TestInviteLinkSurvivesTeamRefreshFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/invitations") && r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"url":"https://example.test/#join=private"}`))
			return
		}
		http.Error(w, "unavailable", 503)
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token, c.teamID = server.Client().Transport, "token", "team"
	var invite string
	teams, mutationErr, refreshErr := mutateAndRefresh(func() error { var e error; invite, e = c.createInvite("password", 3600, 1); return e }, c.teams)
	d := &desktop{}
	d.finishTeamOperation("invite", invite, teams, mutationErr, refreshErr)
	if d.teamInviteURL != "https://example.test/#join=private" || !strings.Contains(d.teamStatus, "邀请已创建") || !strings.Contains(d.teamStatus, "刷新失败") {
		t.Fatal("successful invitation lost after refresh failure")
	}
}

func TestLoginStateKeepsSessionWhenAccountListFails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/login":
			_, _ = w.Write([]byte(`{"token":"session"}`))
		case "/api/v1/teams":
			_, _ = w.Write([]byte(`[{"id":"one","name":"team"}]`))
		default:
			http.Error(w, "unavailable", 503)
		}
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = server.Client().Transport
	if err = c.login("alice", "password"); err != nil {
		t.Fatal(err)
	}
	state := loadCloudInitialState(c, false)
	if c.token != "session" || state.accountErr == nil || state.teamErr != nil || len(state.teams) != 1 {
		t.Fatal("partial read failure discarded successful session or team list")
	}
}

func TestSavedIDLinksOnlyUnchangedFormAcrossScopes(t *testing.T) {
	c := &cloudClient{teamID: "personal"}
	settings := connectionSettings{Host: "example.test", Port: "22", User: "alice", Password: "secret"}
	d := &desktop{cloud: c, settings: settings, fingerprint: "SHA256:key", profileLabel: "work"}
	binding := cloudSaveBinding{client: c, scope: "personal", form: true, formSettings: settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel}
	if !d.bindSavedCloudID(binding, "first") || d.formCloudSource() != "first" {
		t.Fatal("saved ID was not linked to originating form")
	}
	c.teamID = "team"
	binding = cloudSaveBinding{client: c, scope: "team", form: true, formSettings: settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel, formFromCloud: true}
	if !d.bindSavedCloudID(binding, "second") || d.formCloudSource() != "second" {
		t.Fatal("second space was not linked")
	}
	c.teamID = "personal"
	if d.formCloudSource() != "first" {
		t.Fatal("switching space lost original record ID")
	}
	binding = cloudSaveBinding{client: c, scope: "personal", sourceID: "first", form: true, formSettings: settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel, formFromCloud: true}
	d.settings.User = "bob"
	if d.bindSavedCloudID(binding, "wrong") || d.cloudFormSources["personal"] != "first" {
		t.Fatal("late response attached to changed form")
	}
}

func TestSavedIDLinksToExactSessionAndClearsOnLogout(t *testing.T) {
	c := &cloudClient{teamID: "one"}
	settings := connectionSettings{Host: "host", Port: "22", User: "alice"}
	session := &workspaceSession{id: 3, active: settings, cloudSources: map[string]string{"other": "keep"}}
	d := &desktop{cloud: c, currentSession: 3, active: settings, sessions: []*workspaceSession{session}}
	binding := cloudSaveBinding{client: c, scope: "one", sessionID: 3, sessionSettings: settings}
	if !d.bindSavedCloudID(binding, "new") || session.cloudSources["one"] != "new" || session.cloudSources["other"] != "keep" {
		t.Fatal("session lost scope links")
	}
	binding.sourceID = "new"
	session.cloudSources["one"] = "changed"
	if d.bindSavedCloudID(binding, "stale") {
		t.Fatal("late result overwrote a newer source")
	}
	d.clearCloudState()
	if len(session.cloudSources) != 0 {
		t.Fatal("session mappings survived logout")
	}
}

func TestSaveCloudAndRefreshUsesExactServerID(t *testing.T) {
	var stored []cloudAccount
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var account cloudAccount
			if err := json.NewDecoder(r.Body).Decode(&account); err != nil {
				t.Error(err)
				return
			}
			if account.ID == "" {
				account.ID = "assigned"
				stored = append(stored, account)
			} else {
				if account.ID != "assigned" {
					t.Errorf("unexpected ID %q", account.ID)
				}
				stored[0] = account
			}
			_ = json.NewEncoder(w).Encode(account)
			return
		}
		_ = json.NewEncoder(w).Encode(stored)
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "token"
	id, accounts, mutationErr, refreshErr := saveCloudAndRefresh(c, cloudAccount{Label: "first"})
	if id != "assigned" || len(accounts) != 1 || mutationErr != nil || refreshErr != nil {
		t.Fatal("create did not return exact ID")
	}
	id, accounts, mutationErr, refreshErr = saveCloudAndRefresh(c, cloudAccount{ID: id, Label: "updated"})
	if id != "assigned" || len(accounts) != 1 || accounts[0].Label != "updated" || mutationErr != nil || refreshErr != nil {
		t.Fatal("repeat save did not update same record")
	}
}

func TestSavedIDSurvivesRefreshFailureAndUncertainResponses(t *testing.T) {
	status := http.StatusServiceUnavailable
	body := `{"id":"saved"}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(body))
			return
		}
		if status != http.StatusOK {
			http.Error(w, "unavailable", status)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "token"
	id, _, mutationErr, refreshErr := saveCloudAndRefresh(c, cloudAccount{})
	if id != "saved" || mutationErr != nil || refreshErr == nil {
		t.Fatal("successful save ID lost when refresh failed")
	}
	d := &desktop{cloud: c, settings: connectionSettings{Host: "example.test", Port: "22", User: "alice", Password: "secret"}, fingerprint: "SHA256:key"}
	binding := cloudSaveBinding{client: c, form: true, formSettings: d.settings, formFingerprint: d.fingerprint}
	if !d.bindSavedCloudID(binding, id) {
		t.Fatal("confirmed ID not bound after refresh failure")
	}
	d.finishCloudSave(nil, mutationErr, refreshErr)
	if !d.cloudListStale || d.formCloudSource() != "saved" {
		t.Fatal("refresh failure lost saved ID or failed to block edits")
	}
	d.accounts = []cloudAccount{{ID: "saved"}}
	d.cloudListStale = false
	if d.formCloudSource() != "saved" {
		t.Fatal("refresh did not retain exact source")
	}
	status = http.StatusOK
	id, _, mutationErr, refreshErr = saveCloudAndRefresh(c, cloudAccount{})
	if id != "saved" || mutationErr != nil || refreshErr == nil {
		t.Fatal("missing saved ID in fresh list accepted")
	}
	body = `{"id":"other"}`
	_, _, mutationErr, _ = saveCloudAndRefresh(c, cloudAccount{ID: "expected"})
	if mutationErr == nil {
		t.Fatal("mismatched update response accepted")
	}
}
