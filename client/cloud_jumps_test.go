package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCloudJumpChainPinsEachHostAndNeverFallsBack(t *testing.T) {
	jump, jumpFP, _ := testSSHServer(t)
	target, targetFP, _ := testSSHServer(t)
	jumpID := strings.Repeat("a", 48)
	port, _ := strconv.Atoi(jump.Port)
	account := cloudAccount{ID: jumpID, Label: "jump", Host: jump.Host, Port: port, Username: jump.User, Password: jump.Password, HostKeyFingerprint: jumpFP, UpdatedAt: "revision"}
	target.Fingerprint, target.cloudJumpID = targetFP, jumpID
	trust := func(a cloudAccount) (string, error) { return jumpFP, nil }
	resolved, err := resolveCloudJumpChain(target, []cloudAccount{account}, trust)
	if err != nil || resolved.route == nil || len(resolved.route.hops) != 1 {
		t.Fatalf("route resolution failed: %v", err)
	}
	if got, err := inspectHostThrough(resolved, nil); err != nil || got != targetFP {
		t.Fatalf("inspection through route: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := connectSSHClient(ctx, resolved)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	unresolved := target
	unresolved.route = nil
	if direct, err := connectSSHClient(ctx, unresolved); err == nil {
		_ = direct.Close()
		t.Fatal("missing cloud route fell back to direct")
	}
	account.HostKeyFingerprint = "SHA256:changed"
	if _, err = resolveCloudJumpChain(target, []cloudAccount{account}, trust); err == nil {
		t.Fatal("untrusted cloud fingerprint accepted")
	}
	account.HostKeyFingerprint = jumpFP
	account.JumpID = &jumpID
	if _, err = resolveCloudJumpChain(target, []cloudAccount{account}, trust); err == nil {
		t.Fatal("cyclic cloud jump accepted")
	}
	if _, err = resolveCloudJumpChain(target, nil, trust); err == nil {
		t.Fatal("missing cloud jump accepted")
	}
}

func TestCloudJumpArchiveRoundTripAndImport(t *testing.T) {
	jumpID, targetID := strings.Repeat("a", 48), strings.Repeat("b", 48)
	items := []cloudAccount{
		{ID: targetID, Label: "target", Host: "target.example", Port: 22, Username: "alice", Password: "fixture-target", HostKeyFingerprint: "SHA256:target", JumpID: &jumpID},
		{ID: jumpID, Label: "jump", Host: "jump.example", Port: 22, Username: "alice", Password: "fixture-jump", HostKeyFingerprint: "SHA256:jump"},
	}
	data, err := encodeCloudArchive(items, "fixture-backup-password")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("fixture-target")) || bytes.Contains(data, []byte(jumpID)) {
		t.Fatal("backup leaked plaintext")
	}
	doc, err := decodeCloudArchiveDocument(data, "fixture-backup-password")
	if err != nil || doc.Version != 4 || !reflect.DeepEqual(doc.JumpIndices, []int{1, -1}) {
		t.Fatalf("route roundtrip: %+v %v", doc.JumpIndices, err)
	}
	if _, err := decodeCloudArchive(data, "fixture-backup-password"); err == nil {
		t.Fatal("legacy reader discarded route")
	}
	var posts []cloudAccount
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var a cloudAccount
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			t.Error(err)
			return
		}
		posts = append(posts, a)
		id := strings.Repeat("c", 48)
		if len(posts) == 2 {
			id = strings.Repeat("d", 48)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token, c.cloudJumps = server.Client().Transport, "session", true
	result := importCloudAccountsWithJumps(context.Background(), c, doc.Accounts, doc.JumpIndices)
	if result.preflightErr != nil || result.mutationErr != nil || result.refreshErr != nil || result.imported != 2 {
		t.Fatalf("import failed: %+v", result)
	}
	if len(posts) != 2 || posts[0].Label != "jump" || posts[1].Label != "target" || posts[1].JumpID == nil || *posts[1].JumpID != strings.Repeat("c", 48) || result.createdIDs[0] != strings.Repeat("d", 48) {
		t.Fatal("import did not remap route in dependency order")
	}
	c.cloudJumps = false
	if got := importCloudAccountsWithJumps(context.Background(), c, doc.Accounts, doc.JumpIndices); got.preflightErr == nil || len(posts) != 2 {
		t.Fatal("unsupported destination accepted route")
	}
}

func TestCloudJumpArchiveRejectsInvalidGraphsAndLocalSave(t *testing.T) {
	for _, parents := range [][]int{{1, 0}, {2, -1}, {1, 2, 3, 4, 5, 6, 7, 8, 9, -1}} {
		if _, err := archiveJumpOrder(parents, len(parents)); err == nil {
			t.Fatalf("accepted invalid graph %v", parents)
		}
	}
	d := &desktop{settings: connectionSettings{Host: "host.example", Port: "22", User: "alice", cloudJumpID: strings.Repeat("a", 48)}}
	if err := d.addProfile(); err == nil || len(d.profiles) != 0 {
		t.Fatal("local save lost cloud route")
	}
}

func TestCloudJumpWorkspaceRejectsChangedOrUntrustedRoute(t *testing.T) {
	jumpID, targetID := strings.Repeat("a", 48), strings.Repeat("b", 48)
	jump := cloudAccount{ID: jumpID, Label: "jump", Host: "jump.example", Port: 22, Username: "alice", Password: "fixture-jump", HostKeyFingerprint: "SHA256:jump", UpdatedAt: "v1"}
	target := cloudAccount{ID: targetID, Host: "target.example", Port: 22, Username: "alice", Password: "fixture-target", JumpID: &jumpID}
	settings := connectionSettings{Host: target.Host, Port: "22", User: target.Username, Fingerprint: "SHA256:target", cloudJumpID: jumpID}
	route, err := resolveCloudJumpChain(settings, []cloudAccount{jump, target}, func(cloudAccount) (string, error) { return "SHA256:jump", nil })
	if err != nil {
		t.Fatal(err)
	}
	e := workspaceTemplateEntry{Kind: "cloud", Host: target.Host, Port: "22", User: target.Username, Fingerprint: settings.Fingerprint, CloudID: targetID, JumpRoute: route.savedRouteSignature()}
	trust := map[string]string{cloudTrustKey(jump.Host, "22", jump.Username): jump.HostKeyFingerprint}
	targets := []workspaceRestoreTarget{{entry: e, settings: settings, cloudTrust: trust}}
	load := func(string) ([]cloudAccount, error) { return []cloudAccount{jump, target}, nil }
	if _, err = hydrateWorkspaceCloudTargets(context.Background(), targets, load); err != nil {
		t.Fatal(err)
	}
	jump.UpdatedAt = "v2"
	if _, err = hydrateWorkspaceCloudTargets(context.Background(), targets, load); err == nil {
		t.Fatal("changed jump route accepted")
	}
	jump.UpdatedAt = "v1"
	delete(trust, cloudTrustKey(jump.Host, "22", jump.Username))
	if _, err = hydrateWorkspaceCloudTargets(context.Background(), targets, load); err == nil {
		t.Fatal("untrusted jump accepted")
	}
	trust[cloudTrustKey(jump.Host, "22", jump.Username)] = jump.HostKeyFingerprint
	target.JumpID = nil
	if _, err = hydrateWorkspaceCloudTargets(context.Background(), targets, load); err == nil {
		t.Fatal("changed direct route accepted")
	}
}

func TestCloudJumpArchiveCannotSilentlyDropRoute(t *testing.T) {
	id := strings.Repeat("b", 48)
	accounts := []cloudAccount{{ID: strings.Repeat("a", 48), Label: "target", Host: "target.example", Port: 22, Username: "alice", Password: "fixture", HostKeyFingerprint: "SHA256:fixture", JumpID: &id}}
	if _, err := normalizeArchiveAccounts(accounts); err == nil {
		t.Fatal("archive silently stripped jump")
	}
}
