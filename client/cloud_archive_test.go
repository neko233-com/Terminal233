package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCloudArchiveEncryptedRoundTripAndNoOverwrite(t *testing.T) {
	account := cloudAccount{ID: "original", Label: "work", Host: "example.test", Port: 22, Username: "alice", Password: "secret-ssh-value", HostKeyFingerprint: "SHA256:verified"}
	data, err := encodeCloudArchive([]cloudAccount{account}, "backup-password-long")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(account.Password)) || bytes.Contains(data, []byte(account.Host)) {
		t.Fatal("backup contains plaintext credentials or metadata")
	}
	again, err := encodeCloudArchive([]cloudAccount{account}, "backup-password-long")
	if err != nil || bytes.Equal(data, again) {
		t.Fatal("backup encryption did not use fresh randomness")
	}
	items, err := decodeCloudArchive(data, "backup-password-long")
	if err != nil || len(items) != 1 || items[0].ID != "" || items[0].Password != account.Password || items[0].HostKeyFingerprint != account.HostKeyFingerprint {
		t.Fatal("backup did not round-trip without a server ID")
	}
	if _, err := decodeCloudArchive(data, "incorrect-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	var envelope cloudArchiveEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Ciphertext[0] ^= 1
	tampered, _ := json.Marshal(envelope)
	if _, err := decodeCloudArchive(tampered, "backup-password-long"); err == nil {
		t.Fatal("tampered backup accepted")
	}
	path := filepath.Join(t.TempDir(), "archive.nekobackup")
	if err := writeCloudArchive(path, data); err != nil {
		t.Fatal(err)
	}
	if err := writeCloudArchive(path, again); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing backup was overwritten")
	}
	if loaded, err := readCloudArchive(path, "backup-password-long"); err != nil || len(loaded) != 1 {
		t.Fatal("saved backup could not be restored")
	}
}

func TestCloudArchiveRejectsInvalidInputBeforeWrite(t *testing.T) {
	account := cloudAccount{Label: "work", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:verified"}
	if _, err := encodeCloudArchive([]cloudAccount{account}, "short"); err == nil {
		t.Fatal("weak backup password accepted")
	}
	account.HostKeyFingerprint = ""
	if _, err := encodeCloudArchive([]cloudAccount{account}, "long-backup-password"); err == nil {
		t.Fatal("missing host fingerprint accepted")
	}
	if _, err := decodeCloudArchive(make([]byte, maxCloudArchiveFile+1), "long-backup-password"); err == nil {
		t.Fatal("oversized backup accepted")
	}
}

func TestCloudArchivePreservesGroupsAndRejectsInconsistentSnapshot(t *testing.T) {
	account := cloudAccount{ID: "id1", Label: "work", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:verified"}
	groups := cloudGroupSnapshot{Groups: []string{"work", "work/prod", "empty"}, Assignments: map[string]string{"id1": "work/prod"}, AccountIDs: []string{"id1"}, Revision: strings.Repeat("a", 64)}
	data, err := encodeCloudArchiveWithGroups([]cloudAccount{account}, "backup-password-long", groups)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("work/prod")) || bytes.Contains(data, []byte("secret")) {
		t.Fatal("backup leaked sensitive group or credential data")
	}
	doc, err := decodeCloudArchiveDocument(data, "backup-password-long")
	if err != nil || doc.Version != 2 || len(doc.Groups) != 3 || len(doc.AccountGroups) != 1 || doc.AccountGroups[0] != "work/prod" || doc.Accounts[0].ID != "" {
		t.Fatal("backup did not preserve portable groups")
	}
	groups.AccountIDs = []string{"different"}
	if _, err = encodeCloudArchiveWithGroups([]cloudAccount{account}, "backup-password-long", groups); err == nil {
		t.Fatal("inconsistent group snapshot accepted")
	}
	groups = cloudGroupSnapshot{Groups: []string{}, Assignments: map[string]string{}, AccountIDs: []string{}, Revision: strings.Repeat("b", 64)}
	data, err = encodeCloudArchiveWithGroups([]cloudAccount{}, "backup-password-long", groups)
	if err != nil {
		t.Fatal(err)
	}
	if doc, err = decodeCloudArchiveDocument(data, "backup-password-long"); err != nil || doc.Version != 2 || len(doc.Groups) != 0 {
		t.Fatal("empty group backup failed")
	}
}

func TestCloudGroupImportPreflightPreventsWrites(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			requests++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"groups":[],"assignments":{},"accountIds":[],"revision":"` + strings.Repeat("b", 64) + `"}`))
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token, c.cloudGroups = server.Client().Transport, "session", true
	doc := cloudArchiveDocument{Version: 2, Accounts: []cloudAccount{}, Groups: []string{"work"}, AccountGroups: []string{}}
	result := importCloudArchiveExpected(context.Background(), c, doc, strings.Repeat("a", 64))
	if result.preflightErr == nil || requests != 0 {
		t.Fatal("stale group revision allowed import writes")
	}
	c.cloudGroups = false
	result = importCloudArchive(context.Background(), c, doc)
	if result.preflightErr == nil || requests != 0 {
		t.Fatal("old service accepted a group backup")
	}
}

func TestArchivePartialImportRequiresReview(t *testing.T) {
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			var account cloudAccount
			_ = json.NewDecoder(r.Body).Decode(&account)
			if account.ID != "" {
				t.Error("import attempted to overwrite an existing ID")
			}
			if posts == 2 {
				http.Error(w, "interrupted", 503)
				return
			}
			_, _ = w.Write([]byte(`{"id":"created"}`))
			return
		}
		if posts == 0 {
			_, _ = w.Write([]byte(`[]`))
		} else {
			_, _ = w.Write([]byte(`[{"id":"created","label":"first"}]`))
		}
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "session"
	result := importCloudAccounts(context.Background(), c, []cloudAccount{{ID: "old-one"}, {ID: "old-two"}, {ID: "old-three"}})
	if result.imported != 1 || result.mutationErr == nil || result.refreshErr != nil || posts != 2 {
		t.Fatal("partial import continued or lost its outcome")
	}
	d := &desktop{cloud: c}
	d.finishArchiveImport(result)
	if !d.cloudListStale || len(d.accounts) != 1 || !strings.Contains(d.archiveStatus, "可能已生效") {
		t.Fatal("uncertain import was not clearly reported")
	}
}

func TestArchiveImportDoesNotWriteWhenPreflightFails(t *testing.T) {
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "session"
	result := importCloudAccounts(context.Background(), c, []cloudAccount{{}})
	if result.preflightErr == nil || result.imported != 0 || posts != 0 {
		t.Fatal("import wrote before checking destination")
	}
	d := &desktop{cloud: c}
	d.finishArchiveImport(result)
	if !d.cloudListStale || !strings.Contains(d.archiveStatus, "未开始导入") {
		t.Fatal("preflight failure was not reported distinctly")
	}
}

func TestArchiveImportCancellationStopsBeforeNextRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			_, _ = w.Write([]byte(`{"id":"created"}`))
			cancel()
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	c, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport, c.token = server.Client().Transport, "session"
	result := importCloudAccounts(ctx, c, []cloudAccount{{}, {}})
	if !result.cancelled || result.imported != 1 || posts != 1 || result.refreshErr != nil {
		t.Fatal("cancellation did not stop between records")
	}
	d := &desktop{cloud: c}
	d.finishArchiveImport(result)
	if !strings.Contains(d.archiveStatus, "已取消") || d.cloudListStale {
		t.Fatal("cancelled import was not reported correctly")
	}
	result = importCloudAccounts(ctx, c, []cloudAccount{{}})
	d.accounts = []cloudAccount{{ID: "existing"}}
	d.finishArchiveImport(result)
	if len(d.accounts) != 1 || d.accounts[0].ID != "existing" {
		t.Fatal("cancellation before preflight discarded the existing list")
	}
}

func TestArchiveImportRequiresConfirmationAndScopeReset(t *testing.T) {
	d := &desktop{workspace: "cloud", showArchive: true, cloud: &cloudClient{}, archivePath: "backup.nekobackup", archivePassword: "long-backup-password"}
	view := ui.NewTester(d.view, 1200, 1600)
	if err := view.Click("追加导入连接…"); err != nil {
		t.Fatal(err)
	}
	if !d.archiveConfirm || d.busy || !view.HasText("确认追加导入") {
		t.Fatal("import did not require explicit confirmation")
	}
	d.applyCloudScope("new", nil, nil)
	if d.archiveConfirm {
		t.Fatal("scope switch retained import confirmation")
	}
	d.clearCloudState()
	if d.archivePassword != "" || d.archiveConfirm {
		t.Fatal("backup password survived logout")
	}
}
