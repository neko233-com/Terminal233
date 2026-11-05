package main

import (
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
	d.saveCloudConnection(connectionSettings{Host: "host", Port: "22", User: "u", Password: "p", Fingerprint: "SHA256:key"}, "", "")
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
