package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloudWorkspacesIsolationConflictAndBackup(t *testing.T) {
	s := setupTeams(t)
	initial, err := s.store.listCloudWorkspaces("owner")
	if err != nil {
		t.Fatal(err)
	}
	w := cloudWorkspace{ID: "0123456789abcdef0123456789abcdef", Name: "private-workspace", Entries: []cloudWorkspaceEntry{{Kind: "cloud", Host: "private.example", Port: "22", User: "alice", AccountID: "account", TerminalDir: "/srv/app"}}, SplitFirst: -1, SplitSecond: -1, ViewMode: "focus"}
	input := cloudWorkspaceMutation{Items: []cloudWorkspace{w}, ExpectedRevision: initial.Revision}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.store.replaceCloudWorkspaces("owner", input); results <- e }()
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, errWorkspaceConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflicts != 1 || s.store.doc.Version != 3 {
		t.Fatal("workspace CAS or storage version failed")
	}
	other, err := s.store.listCloudWorkspaces("alice")
	if err != nil || len(other.Items) != 0 {
		t.Fatal("workspace leaked across accounts")
	}
	groups, err := s.store.listCloudGroups("owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.replaceCloudGroups("owner", "", cloudGroupMutation{Groups: []string{"team"}, ExpectedRevision: groups.Revision}); err != nil || s.store.doc.Version != 3 {
		t.Fatal("group write downgraded workspace format", err)
	}
	backup, err := s.store.exportBackup()
	if err != nil || bytes.Contains(backup, []byte("private-workspace")) {
		t.Fatal("backup failed or leaked plaintext", err)
	}
	state, err := s.store.listCloudWorkspaces("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.replaceCloudWorkspaces("owner", cloudWorkspaceMutation{Items: []cloudWorkspace{}, ExpectedRevision: state.Revision}); err != nil {
		t.Fatal(err)
	}
	if err = s.store.importBackup(backup, "owner"); err != nil {
		t.Fatal(err)
	}
	state, err = s.store.listCloudWorkspaces("owner")
	if err != nil || len(state.Items) != 1 || state.Items[0].Name != w.Name {
		t.Fatal("backup did not restore workspace", err)
	}
	path := s.store.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("private.example")) || bytes.Contains(data, []byte("private-workspace")) {
		t.Fatal("database leaked workspace plaintext", err)
	}
	reopened, err := openStore(path, "test-master-key-which-is-long")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err = reopened.listCloudWorkspaces("owner")
	if err != nil || len(state.Items) != 1 {
		t.Fatal("workspace did not persist", err)
	}
}

func TestCloudWorkspacesHTTPRejectsSecretsAndInvalidData(t *testing.T) {
	s := setupTeams(t)
	s.sessions["owner-token"] = session{Username: "owner", ExpiresAt: time.Now().Add(time.Hour)}
	call := func(method, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/workspaces", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		s.ServeHTTP(out, r)
		return out
	}
	if call(http.MethodGet, "", "").Code == 200 {
		t.Fatal("unauthenticated access")
	}
	res := call(http.MethodGet, "", "owner-token")
	var state cloudWorkspaceSnapshot
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &state) != nil {
		t.Fatal("GET failed")
	}
	bad := `{"items":[{"id":"0123456789abcdef0123456789abcdef","name":"test","entries":[{"kind":"local","password":"secret"}],"active":0,"splitFirst":-1,"splitSecond":-1}],"expectedRevision":"` + state.Revision + `"}`
	if call(http.MethodPut, bad, "owner-token").Code != 400 {
		t.Fatal("secret field accepted")
	}
	for _, entry := range []cloudWorkspaceEntry{{Kind: "local", FileDir: "C:/private"}, {Kind: "cloud", Host: "example.test", Port: "022", User: "user", AccountID: "id"}, {Kind: "cloud", Host: "example.test", Port: "22", User: "user", AccountID: "id", TerminalDir: "../secret"}} {
		w := cloudWorkspace{ID: "0123456789abcdef0123456789abcdef", Name: "test", Entries: []cloudWorkspaceEntry{entry}, SplitFirst: -1, SplitSecond: -1}
		payload, _ := json.Marshal(cloudWorkspaceMutation{Items: []cloudWorkspace{w}, ExpectedRevision: state.Revision})
		if call(http.MethodPut, string(payload), "owner-token").Code != 400 {
			t.Fatal("invalid entry accepted")
		}
	}
	if !strings.Contains(httptestHealth(s), "cloud-workspaces-v1") {
		t.Fatal("capability missing")
	}
}
