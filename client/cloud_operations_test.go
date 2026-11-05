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
			_, _ = w.Write([]byte(`{"token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://other.test/#join=wrong"}`))
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
	if d.teamInviteURL != server.URL+"/#join="+strings.Repeat("a", 48) || !strings.Contains(d.teamStatus, "邀请已创建") || !strings.Contains(d.teamStatus, "刷新失败") {
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

func TestScopeSelectionKeepsAccountsWhenMembersFail(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/teams/new/accounts":
			_, _ = w.Write([]byte(`[{"id":"shared","label":"team connection"}]`))
		case "/api/v1/teams/new/members":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token, c.teamID = server.Client().Transport, "token", "old"
	state := loadCloudScope(c, "new")
	if c.teamID != "old" || state.accountErr != nil || state.memberErr == nil {
		t.Fatal("scope read modified current scope or lost partial error")
	}
	d := &desktop{cloud: c, accounts: []cloudAccount{{ID: "old"}}, members: []cloudTeamMember{{Username: "old"}}, confirmMember: "old", confirmCloudDelete: "old", teamInviteURL: "old-link"}
	d.finishCloudScopeSelection("new", state)
	if c.teamID != "new" || len(d.accounts) != 1 || d.accounts[0].ID != "shared" || len(d.members) != 0 || d.confirmMember != "" || d.confirmCloudDelete != "" || d.teamInviteURL != "" || !strings.Contains(d.teamStatus, "成员列表加载失败") {
		t.Fatalf("partial scope result applied incorrectly: %s", d.teamStatus)
	}
	d.workspace, d.showTeams = "cloud", true
	view := ui.NewTester(d.view, 1200, 2000)
	if !view.HasText("team connection") || !strings.Contains(strings.Join(view.Texts(), " "), "成员列表加载失败") {
		t.Fatal("partial result and member warning were not visible together")
	}
	state = loadCloudScope(c, "missing")
	d.finishCloudScopeSelection("missing", state)
	if c.teamID != "new" || len(d.accounts) != 1 || d.accounts[0].ID != "shared" || !strings.Contains(d.teamStatus, "无法切换") {
		t.Fatal("failed account read replaced the previous scope")
	}
}

func TestSavedIDLinksOnlyUnchangedFormAcrossScopes(t *testing.T) {
	c := &cloudClient{teamID: "personal"}
	settings := connectionSettings{Host: "example.test", Port: "22", User: "alice", Password: "secret"}
	d := &desktop{cloud: c, settings: settings, fingerprint: "SHA256:key", profileLabel: "work"}
	binding := cloudSaveBinding{client: c, scope: "personal", form: true, formSettings: settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel}
	if !d.bindSavedCloudID(binding, "first", "") || d.formCloudSource() != "first" {
		t.Fatal("saved ID was not linked to originating form")
	}
	c.teamID = "team"
	binding = cloudSaveBinding{client: c, scope: "team", form: true, formSettings: settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel, formFromCloud: true}
	if !d.bindSavedCloudID(binding, "second", "") || d.formCloudSource() != "second" {
		t.Fatal("second space was not linked")
	}
	c.teamID = "personal"
	if d.formCloudSource() != "first" {
		t.Fatal("switching space lost original record ID")
	}
	binding = cloudSaveBinding{client: c, scope: "personal", sourceID: "first", form: true, formSettings: settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel, formFromCloud: true}
	d.settings.User = "bob"
	if d.bindSavedCloudID(binding, "wrong", "") || d.cloudFormSources["personal"] != "first" {
		t.Fatal("late response attached to changed form")
	}
}

func TestConditionalUpdateUsesOriginalRevisionAndKeepsScopeBindings(t *testing.T) {
	c := &cloudClient{teamID: "one", conditionalUpdates: true}
	a := cloudAccount{ID: "item"}
	if err := applyConditionalVersion(c, &a, ""); err == nil {
		t.Fatal("missing source revision was accepted")
	}
	if err := applyConditionalVersion(c, &a, "old-revision"); err != nil || a.ExpectedUpdatedAt != "old-revision" {
		t.Fatal("source revision not applied")
	}
	legacy := &cloudClient{}
	b := cloudAccount{ID: "item"}
	if err := applyConditionalVersion(legacy, &b, "old-revision"); err != nil || b.ExpectedUpdatedAt != "" {
		t.Fatal("legacy service received unsupported field")
	}
	d := &desktop{cloud: c, settingsFromCloud: true, cloudFormSources: map[string]string{"one": "item"}, cloudFormVersions: map[string]string{"one": "old-revision"}}
	binding := cloudSaveBinding{client: c, scope: "one", form: true, formFromCloud: true, sourceID: "item", sourceVersion: "old-revision"}
	if !d.bindSavedCloudID(binding, "item", "new-revision") || d.formCloudVersion() != "new-revision" {
		t.Fatal("confirmed revision was not bound")
	}
	d.finishCloudRefresh([]cloudAccount{{ID: "item", UpdatedAt: "changed-elsewhere"}}, nil)
	if d.formCloudVersion() != "new-revision" {
		t.Fatal("refresh silently advanced an open form's revision")
	}
	if d.bindSavedCloudID(binding, "item", "late-revision") || d.formCloudVersion() != "new-revision" {
		t.Fatal("late response replaced newer revision")
	}
	d.finishCloudSave(nil, &cloudHTTPError{Status: http.StatusConflict, Code: "account_conflict", Message: "conflict"}, nil)
	if !d.cloudListStale || !strings.Contains(d.cloudStatus, "本次未保存") {
		t.Fatal("confirmed conflict was reported as uncertain")
	}
	d.clearCloudState()
	if len(d.cloudFormVersions) != 0 {
		t.Fatal("logout retained cloud revisions")
	}
}

func TestLegacyCloudCapabilityFallback(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"status":"ok","version":"0.3.2"}`))
			return
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if _, ok := body["expectedUpdatedAt"]; ok {
				t.Error("legacy request included an unsupported field")
			}
			_, _ = w.Write([]byte(`{"id":"item"}`))
			return
		}
		if r.Method == http.MethodDelete {
			if r.Header.Get("X-Neko-Expected-Updated-At") != "" {
				t.Error("legacy delete included an unsupported header")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"item"}]`))
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "token"
	c.discoverCapabilities()
	if c.conditionalUpdates {
		t.Fatal("legacy server was treated as conditional")
	}
	view := ui.NewTester((&desktop{workspace: "cloud", cloud: c}).view, 1200, 1400)
	if !view.HasText("未确认当前服务支持并发修改或删除保护；多设备同时操作可能冲突，请先刷新并核对。") {
		t.Fatal("legacy protection warning missing")
	}
	account := cloudAccount{ID: "item"}
	if err := applyConditionalVersion(c, &account, "original"); err != nil {
		t.Fatal(err)
	}
	if _, _, err, _ := saveCloudAndRefresh(c, account); err != nil {
		t.Fatal(err)
	}
	if err := c.removeWithRevision("item", "old-version"); err != nil {
		t.Fatal(err)
	}
}

func TestFailedCapabilityProbeBlocksExistingMutationUntilRecovery(t *testing.T) {
	healthOK := false
	writes := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			if !healthOK {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","capabilities":["conditional-account-update","conditional-account-delete"]}`))
			return
		}
		writes++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "token"
	c.discoverCapabilities()
	if !c.capabilityUnknown {
		t.Fatal("failed probe was treated as a legacy service")
	}
	a := cloudAccount{ID: "item"}
	if applyConditionalVersion(c, &a, "2026-01-01T00:00:00Z") == nil || c.removeWithRevision("item", "2026-01-01T00:00:00Z") == nil || writes != 0 {
		t.Fatal("an unverified service allowed an existing connection to be changed")
	}
	view := ui.NewTester((&desktop{workspace: "cloud", cloud: c}).view, 1200, 1400)
	if !view.HasText("无法确认同步服务的并发保护能力；已暂停更新和删除已有连接，请刷新云端连接重试。") {
		t.Fatal("failed capability probe was not explained")
	}
	healthOK = true
	c.discoverCapabilities()
	if c.capabilityUnknown || !c.conditionalUpdates || !c.conditionalDeletes {
		t.Fatal("successful retry did not restore advertised capabilities")
	}
	if err := applyConditionalVersion(c, &a, "2026-01-01T00:00:00Z"); err != nil || a.ExpectedUpdatedAt == "" {
		t.Fatal("recovered update did not include its original revision")
	}
	if err := c.removeWithRevision("item", "2026-01-01T00:00:00Z"); err != nil || writes != 1 {
		t.Fatal("recovered delete was not sent")
	}
}

func TestConditionalDeleteBindsSelectionAndReportsConflict(t *testing.T) {
	c := &cloudClient{conditionalDeletes: true}
	d := &desktop{cloud: c}
	d.prepareCloudDelete(cloudAccount{ID: "item", Label: "test"})
	if d.confirmCloudDelete != "" || !strings.Contains(d.cloudStatus, "刷新") {
		t.Fatal("missing revision allowed deletion")
	}
	original := "2026-01-01T00:00:00Z"
	d.prepareCloudDelete(cloudAccount{ID: "item", Label: "test", UpdatedAt: original})
	if d.confirmCloudDelete != "item" || d.confirmCloudDeleteVersion != original {
		t.Fatal("selection revision was not retained")
	}
	d.finishCloudDelete("item", nil, &cloudHTTPError{Status: http.StatusConflict, Code: "account_conflict", Message: "conflict"}, nil)
	if !d.cloudListStale || !strings.Contains(d.cloudStatus, "本次未删除") {
		t.Fatal("confirmed conflict was not explained")
	}
	d.finishCloudRefresh(nil, nil)
	if d.confirmCloudDelete != "" || d.confirmCloudDeleteVersion != "" {
		t.Fatal("refresh retained a stale delete confirmation")
	}
}

func TestSavedIDLinksToExactSessionAndClearsOnLogout(t *testing.T) {
	c := &cloudClient{teamID: "one"}
	settings := connectionSettings{Host: "host", Port: "22", User: "alice"}
	session := &workspaceSession{id: 3, active: settings, cloudSources: map[string]string{"other": "keep"}}
	d := &desktop{cloud: c, currentSession: 3, active: settings, sessions: []*workspaceSession{session}}
	binding := cloudSaveBinding{client: c, scope: "one", sessionID: 3, sessionSettings: settings}
	if !d.bindSavedCloudID(binding, "new", "revision-1") || session.cloudSources["one"] != "new" || session.cloudSources["other"] != "keep" || session.cloudVersions["one"] != "revision-1" {
		t.Fatal("session lost scope links")
	}
	binding.sourceID = "new"
	binding.sourceVersion = "revision-1"
	session.cloudVersions["one"] = "revision-2"
	if d.bindSavedCloudID(binding, "new", "late") {
		t.Fatal("late response replaced a newer session revision")
	}
	session.cloudVersions["one"] = "revision-1"
	session.cloudSources["one"] = "changed"
	if d.bindSavedCloudID(binding, "stale", "") {
		t.Fatal("late result overwrote a newer source")
	}
	d.clearCloudState()
	if len(session.cloudSources) != 0 || len(session.cloudVersions) != 0 {
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
	if !d.bindSavedCloudID(binding, id, "") {
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
