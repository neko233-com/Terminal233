package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setupTeams(t *testing.T) *Server {
	t.Helper()
	s := testServer(t)
	for _, user := range []string{"owner", "alice", "bob", "outsider"} {
		if err := s.store.createUser(user, "long-password"); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func TestTeamMembershipIsolationAndSharedAccounts(t *testing.T) {
	s := setupTeams(t)
	first, err := s.store.createTeam("owner", "公司 A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.store.createTeam("bob", "公司 B")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := s.store.createInvitation("owner", first.ID, "password-1234", time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.joinTeam("alice", invite, "wrong-password"); err == nil {
		t.Fatal("incorrect invitation password accepted")
	}
	if _, err = s.store.joinTeam("alice", invite, "password-1234"); err != nil {
		t.Fatal(err)
	}
	invite2, err := s.store.createInvitation("bob", second.ID, "password-5678", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.joinTeam("alice", invite2, "password-5678"); err != nil {
		t.Fatal(err)
	}
	if len(s.store.listTeams("alice")) != 2 {
		t.Fatal("user cannot join multiple teams")
	}
	a := Account{Label: "test", Host: "example.test", Port: 22, Username: "root", Password: "ssh-secret", HostKeyFingerprint: "SHA256:fixture"}
	if _, err = s.store.saveTeamAccount("owner", first.ID, a); err != nil {
		t.Fatal(err)
	}
	if accounts, err := s.store.listTeamAccounts("alice", first.ID); err != nil || len(accounts) != 1 || accounts[0].Password != "ssh-secret" {
		t.Fatalf("team share failed: %v", err)
	}
	if _, err = s.store.listTeamAccounts("outsider", first.ID); err == nil {
		t.Fatal("outsider read shared credential")
	}
	if _, err = s.store.saveTeamAccount("alice", first.ID, a); err == nil {
		t.Fatal("member could edit shared credential")
	}
	if err = s.store.removeTeamMember("owner", first.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.listTeamAccounts("alice", first.ID); err == nil {
		t.Fatal("removed member retained access")
	}
	if _, err = s.store.listTeamAccounts("alice", second.ID); err != nil {
		t.Fatal("removal affected other team")
	}
	data, err := os.ReadFile(s.cfg.DataPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ssh-secret") {
		t.Fatal("database leaked plaintext password")
	}
}
func TestTeamInviteExpirySingleUseAndMemberLimit(t *testing.T) {
	s := setupTeams(t)
	team, _ := s.store.createTeam("owner", "A")
	token, err := s.store.createInvitation("owner", team.ID, "password-1234", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, u := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			_, e := s.store.joinTeam(user, token, "password-1234")
			results <- e
		}(u)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("single-use invite admitted %d users", success)
	}
	token, err = s.store.createInvitation("owner", team.ID, "password-1234", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.store.mu.Lock()
	inv := s.store.doc.Invitations[invitationKey(token)]
	inv.ExpiresAt = time.Now().Add(-time.Second).Unix()
	s.store.doc.Invitations[invitationKey(token)] = inv
	s.store.mu.Unlock()
	if _, err = s.store.joinTeam("outsider", token, "password-1234"); err == nil {
		t.Fatal("expired invitation accepted")
	}
	token, err = s.store.createInvitation("owner", team.ID, "password-1234", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.store.mu.Lock()
	record := s.store.doc.Teams[team.ID]
	for len(record.Members) < maxTeamMembers {
		record.Members[fmt.Sprintf("member-%d", len(record.Members))] = "member"
	}
	s.store.doc.Teams[team.ID] = record
	s.store.mu.Unlock()
	if _, err = s.store.joinTeam("outsider", token, "password-1234"); err == nil {
		t.Fatal("team exceeded member limit")
	}
}
func TestTeamMutationRollsBackIfPersistenceFails(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "A")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := s.store.createInvitation("owner", team.ID, "password-1234", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	original := s.store.path
	s.store.path = filepath.Join(t.TempDir(), "missing", "data.sqlite")
	if _, err = s.store.joinTeam("alice", invite, "password-1234"); err == nil {
		t.Fatal("expected storage failure")
	}
	s.store.path = original
	if len(s.store.listTeams("alice")) != 0 {
		t.Fatal("failed save retained membership")
	}
	if _, err = s.store.joinTeam("alice", invite, "password-1234"); err != nil {
		t.Fatalf("failed save consumed invitation: %v", err)
	}
}
func TestSuspendedTeamRejectsMembersAndInvites(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "A")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.store.createInvitation("owner", team.ID, "password-1234", time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.joinTeam("alice", token, "password-1234"); err != nil {
		t.Fatal(err)
	}
	if err = s.store.setTeamDisabled(team.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.listTeamAccounts("owner", team.ID); err == nil {
		t.Fatal("suspended team disclosed credentials")
	}
	if _, err = s.store.teamMembers("alice", team.ID); err == nil {
		t.Fatal("suspended team exposed members")
	}
	if _, err = s.store.joinTeam("bob", token, "password-1234"); err == nil {
		t.Fatal("suspended team accepted invitation")
	}
	if _, err = s.store.createInvitation("owner", team.ID, "password-1234", time.Hour, 1); err == nil {
		t.Fatal("suspended team created invitation")
	}
	if err = s.store.setTeamDisabled(team.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.listTeamAccounts("alice", team.ID); err != nil {
		t.Fatal("re-enabled team inaccessible")
	}
}
func TestInviteRegistrationCannotClaimAdminAndIsAtomic(t *testing.T) {
	s := setupTeams(t)
	s.cfg.AdminUsername = "admin"
	s.cfg.AdminBootstrapToken = "admin-bootstrap-token-which-is-secret"
	team, err := s.store.createTeam("owner", "A")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.store.createInvitation("owner", team.ID, "invite-password", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"username": "admin", "password": "long-password", "officialToken": "", "teamInviteToken": token, "teamInvitePassword": "invite-password"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(payload))
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("invite claimed admin: %d", res.Code)
	}
	payload, _ = json.Marshal(map[string]string{"username": "newuser", "password": "long-password", "officialToken": "", "teamInviteToken": token, "teamInvitePassword": "invite-password"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(payload))
	res = httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("invite registration: %d %s", res.Code, res.Body.String())
	}
	if !s.store.userEnabled("newuser") || len(s.store.listTeams("newuser")) != 1 {
		t.Fatal("registration did not atomically join team")
	}
	if _, err = s.store.joinTeam("bob", token, "invite-password"); err == nil {
		t.Fatal("single-use invitation was not consumed")
	}
}
func TestInvitationRevocationAndMemberRoles(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "A")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.store.createInvitation("owner", team.ID, "invite-password", time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.joinTeam("alice", token, "invite-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.listInvitations("alice", team.ID); err == nil {
		t.Fatal("member listed administrative invitations")
	}
	if err = s.store.setMemberRole("alice", team.ID, "owner", "member"); err == nil {
		t.Fatal("member changed owner role")
	}
	if err = s.store.setMemberRole("owner", team.ID, "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	invites, err := s.store.listInvitations("alice", team.ID)
	if err != nil || len(invites) != 1 {
		t.Fatalf("admin invitations: %v", err)
	}
	if err = s.store.revokeInvitation("alice", team.ID, invites[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.joinTeam("bob", token, "invite-password"); err == nil {
		t.Fatal("revoked invitation accepted")
	}
}
func TestDisabledMemberCannotUseStoreAfterSessionCheck(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "A")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.store.createInvitation("owner", team.ID, "invite-password", time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.joinTeam("alice", token, "invite-password"); err != nil {
		t.Fatal(err)
	}
	if err = s.store.setDisabled("alice", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.listTeamAccounts("alice", team.ID); err == nil {
		t.Fatal("disabled member read team data")
	}
	if _, err = s.store.listAccounts("alice"); err == nil {
		t.Fatal("disabled user read personal data")
	}
	if _, err = s.store.teamMembers("alice", team.ID); err == nil {
		t.Fatal("disabled member listed team")
	}
}
