package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	binenc "encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Starts the real Go server and reaches it through a TLS proxy, as in deployment.
func TestCloudAgainstGoServer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test builds the server")
	}
	binary := filepath.Join(t.TempDir(), "sync-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "-C", "../server", "build", "-p", "1", "-o", binary, "./cmd/server")
	build.Env = append(os.Environ(), "GOFLAGS=-p=1") // The server is a separate module.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v %s", err, output)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	command := exec.Command(binary)
	command.Env = append(os.Environ(), "TERMINAL233_SERVER_ADDR="+address, "TERMINAL233_DATA_PATH="+filepath.Join(t.TempDir(), "store.json"), "TERMINAL233_MASTER_KEY="+strings.Repeat("m", 40), "TERMINAL233_OFFICIAL_TOKEN="+strings.Repeat("t", 40), "TERMINAL233_ADMIN_USERNAME=platform-admin", "TERMINAL233_ADMIN_BOOTSTRAP_TOKEN="+strings.Repeat("a", 40), "TERMINAL233_ADMIN_TOTP_SECRET=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	base := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	target, _ := url.Parse(base)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer proxy.Close()
	client, err := newCloudClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = proxy.Client().Transport
	if err := client.register("alice", "long-password", strings.Repeat("t", 40)); err != nil {
		t.Fatal(err)
	}
	if err := client.login("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	client.discoverCapabilities()
	if !client.conditionalUpdates || !client.conditionalDeletes || !client.cloudGroups || !client.cloudWorkspaces || !client.cloudTags {
		t.Fatal("server conditional-update capability missing")
	}
	account := cloudAccount{Label: "sample", Host: "example.test", Port: 22, Username: "me", Password: "ssh-password", HostKeyFingerprint: "SHA256:verified", Tags: []string{"production", "database"}}
	if err := client.save(account); err != nil {
		t.Fatal(err)
	}
	accounts, err := client.list()
	if err != nil || len(accounts) != 1 || accounts[0].Password != "ssh-password" || len(accounts[0].Tags) != 2 {
		t.Fatalf("list=%v err=%v", accounts, err)
	}
	legacy := accounts[0]
	legacy.Tags = nil
	legacy.ExpectedUpdatedAt = legacy.UpdatedAt
	if err = client.request(http.MethodPost, client.accountsPath(), legacy, nil); err != nil {
		t.Fatal("legacy update failed", err)
	}
	accounts, err = client.list()
	if err != nil || len(accounts) != 1 || len(accounts[0].Tags) != 2 {
		t.Fatal("legacy client removed tags", err)
	}
	clearTags := accounts[0]
	clearTags.Tags = []string{}
	clearTags.ExpectedUpdatedAt = clearTags.UpdatedAt
	if _, err = client.saveReturningRecord(clearTags); err != nil {
		t.Fatal("clear tags failed", err)
	}
	accounts, err = client.list()
	if err != nil || len(accounts) != 1 || len(accounts[0].Tags) != 0 {
		t.Fatal("tags were not cleared", err)
	}
	workspaceState, err := client.listCloudWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	staleWorkspaces := workspaceState
	workspaceState.Items = []cloudWorkspace{{ID: "0123456789abcdef0123456789abcdef", Name: "example", Entries: []cloudWorkspaceEntry{{Kind: "cloud", Host: "example.test", Port: "22", User: "me", AccountID: accounts[0].ID}}, SplitFirst: -1, SplitSecond: -1}}
	workspaceState, err = client.replaceCloudWorkspaces(workspaceState)
	if err != nil || len(workspaceState.Items) != 1 {
		t.Fatal("cloud workspace save failed", err)
	}
	if _, err = client.replaceCloudWorkspaces(staleWorkspaces); err == nil {
		t.Fatal("stale workspace update accepted")
	}
	groupState, err := client.listCloudGroups()
	if err != nil {
		t.Fatal(err)
	}
	staleGroups := groupState
	groupState.Groups = []string{"work", "work/prod", "empty"}
	groupState.Assignments = map[string]string{accounts[0].ID: "work/prod"}
	savedGroups, err := client.replaceCloudGroups(groupState)
	if err != nil || savedGroups.Assignments[accounts[0].ID] != "work/prod" {
		t.Fatalf("cloud groups were not saved: %v", err)
	}
	staleGroups.Groups = []string{"stale"}
	if _, err = client.replaceCloudGroups(staleGroups); err == nil {
		t.Fatal("stale group revision accepted")
	}
	// The same endpoint can have separate cloud records; only an explicit ID
	// may update one of them.
	settings := connectionSettings{Host: account.Host, Port: "22", User: account.Username, Password: "second-secret", Fingerprint: account.HostKeyFingerprint}
	second, err := prepareCloudAccount(settings, "second", accounts, "")
	if err != nil || second.ID != "" {
		t.Fatal("new record selected an existing ID")
	}
	secondID, both, mutationErr, refreshErr := saveCloudAndRefresh(client, second)
	if mutationErr != nil || refreshErr != nil || secondID == "" || len(both) != 2 {
		t.Fatalf("expected two independent records: %v %v", mutationErr, refreshErr)
	}
	groupState, err = client.listCloudGroups()
	if err != nil {
		t.Fatal(err)
	}
	batch, changed, err := movedCloudGroupState(groupState, both, []string{accounts[0].ID, secondID}, "empty")
	if err != nil || changed != 2 {
		t.Fatal("batch group move failed to prepare", err)
	}
	savedGroups, err = client.replaceCloudGroups(batch)
	if err != nil || savedGroups.Assignments[accounts[0].ID] != "empty" || savedGroups.Assignments[secondID] != "empty" {
		t.Fatal("batch group move failed", err)
	}
	if _, err = client.replaceCloudGroups(batch); err == nil {
		t.Fatal("stale batch group update accepted")
	}
	settings.Password = "updated-first"
	update, err := prepareCloudAccount(settings, "", both, accounts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyConditionalVersion(client, &update, accounts[0].UpdatedAt); err != nil {
		t.Fatal(err)
	}
	stale := update
	stale.Password = "stale-overwrite"
	updatedID, both, mutationErr, refreshErr := saveCloudAndRefresh(client, update)
	if mutationErr != nil || refreshErr != nil || updatedID != accounts[0].ID || len(both) != 2 {
		t.Fatal("cloud update changed the record count or ID")
	}
	_, _, conflict, _ := saveCloudAndRefresh(client, stale)
	var httpErr *cloudHTTPError
	if !errors.As(conflict, &httpErr) || httpErr.Status != http.StatusConflict || httpErr.Code != "account_conflict" {
		t.Fatalf("stale update was not rejected: %v", conflict)
	}
	err = client.removeWithRevision(accounts[0].ID, accounts[0].UpdatedAt)
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusConflict || httpErr.Code != "account_conflict" {
		t.Fatalf("stale delete was not rejected: %v", err)
	}
	for _, item := range both {
		if item.ID == accounts[0].ID && item.Password != "updated-first" {
			t.Fatal("selected record not updated")
		}
		if item.ID != accounts[0].ID && item.Password != "second-secret" {
			t.Fatal("other same-address record was overwritten")
		}
		if err := client.remove(item.ID); err != nil {
			t.Fatal(err)
		}
	}
	accounts, err = client.list()
	if err != nil || len(accounts) != 0 {
		t.Fatalf("delete list=%v err=%v", accounts, err)
	}
	// The portable encrypted backup imports records as new entries without
	// replacing an existing record in the destination scope.
	archive, err := encodeCloudArchive([]cloudAccount{account}, "backup-password-long")
	if err != nil {
		t.Fatal(err)
	}
	portable, err := decodeCloudArchive(archive, "backup-password-long")
	if err != nil {
		t.Fatal(err)
	}
	if err = client.save(account); err != nil {
		t.Fatal(err)
	}
	currentAccounts, err := client.list()
	if err != nil {
		t.Fatal(err)
	}
	currentGroups, err := client.listCloudGroups()
	if err != nil {
		t.Fatal(err)
	}
	currentGroups.Assignments[currentAccounts[0].ID] = "work/prod"
	currentGroups, err = client.replaceCloudGroups(currentGroups)
	if err != nil {
		t.Fatal(err)
	}
	groupArchive, err := encodeCloudArchiveWithGroups(currentAccounts, "backup-password-long", currentGroups)
	if err != nil {
		t.Fatal(err)
	}
	groupDoc, err := decodeCloudArchiveDocument(groupArchive, "backup-password-long")
	if err != nil || len(portable) != 1 {
		t.Fatal("backup format compatibility failed")
	}
	result := importCloudArchive(context.Background(), client, groupDoc)
	if result.mutationErr != nil || result.refreshErr != nil || result.imported != 1 || len(result.accounts) != 2 || result.accounts[0].ID == result.accounts[1].ID {
		t.Fatal("portable import did not preserve the existing connection")
	}
	if !result.groupsApplied || result.groupErr != nil || result.groupState.Assignments[result.createdIDs[0]] != "work/prod" {
		t.Fatalf("cloud group backup was not restored: %v", result.groupErr)
	}
	for _, item := range result.accounts {
		if err := client.remove(item.ID); err != nil {
			t.Fatal(err)
		}
	}
	team, err := client.createTeam("开发组")
	if err != nil {
		t.Fatal(err)
	}
	client.teamID = team.ID
	if err := client.save(account); err != nil {
		t.Fatal(err)
	}
	invite, err := client.createInvite("invite-pass-123", 3600, 1)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(invite)
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress, _ := url.Parse(proxy.URL)
	if parsed.Host != proxyAddress.Host {
		t.Fatal("self-hosted invite used another service's host")
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := newCloudClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	bob.http.Transport = proxy.Client().Transport
	if err = bob.registerWithInvite("bobby", "long-password", fragment.Get("join"), "invite-pass-123"); err != nil {
		t.Fatal(err)
	}
	if err = bob.login("bobby", "long-password"); err != nil {
		t.Fatal(err)
	}
	bob.teamID = team.ID
	shared, err := bob.list()
	if err != nil || len(shared) != 1 || shared[0].Password != "ssh-password" {
		t.Fatalf("team share failed: %v", err)
	}
	if err = bob.save(account); err == nil {
		t.Fatal("team member modified credentials")
	}
	secondTeam, err := client.createTeam("运维组")
	if err != nil {
		t.Fatal(err)
	}
	client.teamID = secondTeam.ID
	secondAccount := account
	secondAccount.Label, secondAccount.Password = "other team", "second-team-secret"
	if err = client.save(secondAccount); err != nil {
		t.Fatal(err)
	}
	secondInvite, err := client.createInvite("second-invite-pass", 3600, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bob.joinTeam(extractInviteToken(secondInvite), "second-invite-pass"); err != nil {
		t.Fatal(err)
	}
	teams, err := bob.teams()
	if err != nil || len(teams) != 2 {
		t.Fatalf("one account did not join two teams: %d %v", len(teams), err)
	}
	bob.teamID = secondTeam.ID
	shared, err = bob.list()
	if err != nil || len(shared) != 1 || shared[0].Password != "second-team-secret" {
		t.Fatal("second team did not return its own credentials")
	}
	bob.teamID = team.ID
	shared, err = bob.list()
	if err != nil || len(shared) != 1 || shared[0].Password != "ssh-password" {
		t.Fatal("returning to first team mixed up credential scopes")
	}
	client.teamID = team.ID
	if err = client.removeMember("bobby"); err != nil {
		t.Fatal(err)
	}
	if _, err = bob.list(); err == nil {
		t.Fatal("removed member retained team access")
	}
	admin, err := newCloudClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	admin.http.Transport = proxy.Client().Transport
	if err = admin.register("platform-admin", "long-password", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if err = admin.login("platform-admin", "long-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.adminUsers(); err == nil {
		t.Fatal("admin action without TOTP succeeded")
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	var counter [8]byte
	binenc.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	admin.adminTOTP = fmt.Sprintf("%06d", (binenc.BigEndian.Uint32(digest[offset:offset+4])&0x7fffffff)%1000000)
	users, err := admin.adminUsers()
	if err != nil || len(users) != 3 {
		t.Fatalf("admin users: %d %v", len(users), err)
	}
	backup, err := admin.exportBackup()
	if err != nil || backup.Format != "nekossh-encrypted-v1" || strings.Contains(backup.Data, "ssh-password") {
		t.Fatalf("backup export failed: %v", err)
	}
	if err = admin.importBackup(backup); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.adminUsers(); err == nil {
		t.Fatal("restore did not invalidate admin session")
	}
	if err := client.logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.list(); err == nil {
		t.Fatal("logged-out client accessed accounts")
	}
}
