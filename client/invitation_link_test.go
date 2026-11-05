package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCreateInviteRejectsInvalidTokenAfterServerResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token":"bad","url":"https://attacker.test/#join=bad"}`))
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token, c.teamID = server.Client().Transport, "session", "team"
	if link, err := c.createInvite("password", 3600, 1); err == nil || link != "" || !strings.Contains(err.Error(), "已创建") {
		t.Fatal("malformed response was accepted or its uncertain outcome was hidden")
	}
}

func TestInviteServiceBindingAndValidation(t *testing.T) {
	token := strings.Repeat("a", 48)
	link := "https://sync.example.test/prefix#join=" + token
	got, service, err := parseInvite(link)
	if err != nil || got != token || service != "https://sync.example.test/prefix" {
		t.Fatalf("could not parse invitation: %q %q %v", got, service, err)
	}
	if _, err = inviteTokenForService(link, "https://other.example.test/prefix"); err == nil {
		t.Fatal("accepted invitation for another service")
	}
	if _, err = inviteTokenForService(link, "https://sync.example.test/other"); err == nil {
		t.Fatal("accepted invitation for another base path")
	}
	if _, err = inviteTokenForService(link, "https://SYNC.example.test:443/prefix/"); err != nil {
		t.Fatalf("equivalent HTTPS service rejected: %v", err)
	}
	if got, err = inviteTokenForService(token, "https://other.example.test"); err != nil || got != token {
		t.Fatal("bare invitation token rejected")
	}
	for _, invalid := range []string{"http://sync.example.test/#join=" + token, "https://user:secret@sync.example.test/#join=" + token, "https://sync.example.test/?x=1#join=" + token, "https://sync.example.test/#join=" + token + "&join=" + token, "https://sync.example.test/#join=broken"} {
		if _, _, err := parseInvite(invalid); err == nil {
			t.Fatalf("accepted invalid invitation %q", invalid)
		}
	}
}

func TestInviteMismatchKeepsCredentialsAndCanSelectService(t *testing.T) {
	token := strings.Repeat("b", 48)
	d := &desktop{workspace: "cloud", showTeams: true, cloudURL: "https://default.test", cloudUser: "alice", cloudPassword: "password", teamInvitePassword: "invite-password", teamInviteInput: "https://custom.test/base#join=" + token}
	d.registerWithInvite()
	if d.busy || d.cloudPassword != "password" || d.teamInvitePassword != "invite-password" || !strings.Contains(d.teamStatus, "请先切换") {
		t.Fatal("mismatched invite started a request or discarded credentials")
	}
	view := ui.NewTester(d.view, 1200, 2200)
	if err := view.Click("使用邀请链接中的服务地址"); err != nil {
		t.Fatal(err)
	}
	if d.cloudURL != "https://custom.test/base" || d.cloudPassword != "password" {
		t.Fatal("explicit service selection failed")
	}
}

func TestInviteRegistrationAccessibleFromSignIn(t *testing.T) {
	d := &desktop{workspace: "cloud", cloudURL: defaultCloudURL}
	view := ui.NewTester(d.view, 1200, 1400)
	if view.HasText("邀请链接或令牌") {
		t.Fatal("invitation entry should initially be collapsed")
	}
	if err := view.Click("使用团队邀请注册…"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("邀请链接或令牌") || !view.HasText("通过邀请注册并加入") || d.showTeams {
		t.Fatal("sign-in invitation entry did not open")
	}
	if err := view.Click("收起团队邀请注册"); err != nil {
		t.Fatal(err)
	}
	if view.HasText("邀请链接或令牌") {
		t.Fatal("invitation entry did not close")
	}
}
