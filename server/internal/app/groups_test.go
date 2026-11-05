package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloudGroupsAtomicIsolationAndCleanup(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "test")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := s.store.createInvitation("owner", team.ID, "password-1234", time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.joinTeam("alice", invite, "password-1234"); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", team.ID} {
		t.Run(scope, func(t *testing.T) {
			a := Account{Label: "server", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:test"}
			var saved Account
			if scope == "" {
				saved, err = s.store.upsertAccount("owner", a)
			} else {
				saved, err = s.store.saveTeamAccount("owner", scope, a)
			}
			if err != nil {
				t.Fatal(err)
			}
			initial, err := s.store.listCloudGroups("owner", scope)
			if err != nil {
				t.Fatal(err)
			}
			input := cloudGroupMutation{Groups: []string{"project", "project/prod", "empty"}, Assignments: map[string]string{saved.ID: "project/prod"}, ExpectedRevision: initial.Revision}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _, e := s.store.replaceCloudGroups("owner", scope, input); results <- e }()
			}
			wg.Wait()
			close(results)
			ok, conflict := 0, 0
			for e := range results {
				if e == nil {
					ok++
				} else if errors.Is(e, errGroupConflict) {
					conflict++
				} else {
					t.Fatal(e)
				}
			}
			if ok != 1 || conflict != 1 || s.store.doc.Version != 2 {
				t.Fatal("group updates were not atomic or did not protect storage version")
			}
			state, err := s.store.listCloudGroups("owner", scope)
			if err != nil || len(state.Groups) != 3 || state.Assignments[saved.ID] != "project/prod" {
				t.Fatal("groups not preserved")
			}
			if scope != "" {
				if _, err = s.store.listCloudGroups("alice", scope); err != nil {
					t.Fatal("member cannot read groups")
				}
				if _, err = s.store.replaceCloudGroups("alice", scope, cloudGroupMutation{ExpectedRevision: state.Revision}); !errors.Is(err, errTeamUnavailable) {
					t.Fatal("read-only member could modify groups")
				}
				if _, err = s.store.listCloudGroups("outsider", scope); !errors.Is(err, errTeamUnavailable) {
					t.Fatal("outsider could read groups")
				}
			}
			// Legacy clients can update account fields without erasing group metadata.
			saved.Label = "legacy edit"
			if scope == "" {
				_, err = s.store.upsertAccount("owner", saved)
			} else {
				_, err = s.store.saveTeamAccount("owner", scope, saved)
			}
			if err != nil {
				t.Fatal(err)
			}
			state, _ = s.store.listCloudGroups("owner", scope)
			if state.Assignments[saved.ID] != "project/prod" {
				t.Fatal("legacy update removed group assignment")
			}
			if scope == "" {
				err = s.store.deleteAccount("owner", saved.ID)
			} else {
				err = s.store.deleteTeamAccount("owner", scope, saved.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			state, _ = s.store.listCloudGroups("owner", scope)
			if len(state.Assignments) != 0 || len(state.Groups) != 3 {
				t.Fatal("delete did not clean assignment or preserve empty groups")
			}
		})
	}
	path := s.store.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(path, "test-master-key-which-is-long")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if state, err := reopened.listCloudGroups("owner", team.ID); err != nil || len(state.Groups) != 3 {
		t.Fatal("group state did not persist")
	}
}

func TestCloudGroupValidationAndHTTP(t *testing.T) {
	s := setupTeams(t)
	s.sessions["owner-token"] = session{Username: "owner", ExpiresAt: time.Now().Add(time.Hour)}
	call := func(method, path string, input any) *httptest.ResponseRecorder {
		var body []byte
		if input != nil {
			body, _ = json.Marshal(input)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer owner-token")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	res := call(http.MethodGet, "/api/v1/groups", nil)
	var state cloudGroupSnapshot
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &state) != nil {
		t.Fatal("group GET failed")
	}
	for _, bad := range []cloudGroupMutation{
		{Groups: []string{"parent/child"}, ExpectedRevision: state.Revision},
		{Groups: []string{"a", "a"}, ExpectedRevision: state.Revision},
		{Groups: []string{"a"}, Assignments: map[string]string{"nonexistent": "a"}, ExpectedRevision: state.Revision},
	} {
		if r := call(http.MethodPut, "/api/v1/groups", bad); r.Code != 400 {
			t.Fatalf("invalid group input accepted: %d", r.Code)
		}
	}
	valid := cloudGroupMutation{Groups: []string{"parent", "parent/child"}, ExpectedRevision: state.Revision}
	if r := call(http.MethodPut, "/api/v1/groups", valid); r.Code != 200 {
		t.Fatalf("valid group update failed: %d %s", r.Code, r.Body.String())
	}
	if r := call(http.MethodPut, "/api/v1/groups", valid); r.Code != 409 || !strings.Contains(r.Body.String(), "group_conflict") {
		t.Fatal("stale group revision accepted")
	}
	if !strings.Contains(httptestHealth(s), "cloud-groups-v1") {
		t.Fatal("group capability missing")
	}
}

func TestCloudGroupRevisionDoesNotRepeatAfterRevert(t *testing.T) {
	s := setupTeams(t)
	initial, err := s.store.listCloudGroups("owner", "")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.store.replaceCloudGroups("owner", "", cloudGroupMutation{Groups: []string{"one"}, ExpectedRevision: initial.Revision})
	if err != nil {
		t.Fatal(err)
	}
	reverted, err := s.store.replaceCloudGroups("owner", "", cloudGroupMutation{ExpectedRevision: changed.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if reverted.Revision == initial.Revision {
		t.Fatal("reverting the visible state reused a stale revision")
	}
	if _, err := s.store.replaceCloudGroups("owner", "", cloudGroupMutation{Groups: []string{"stale"}, ExpectedRevision: initial.Revision}); !errors.Is(err, errGroupConflict) {
		t.Fatal("stale revision accepted after revert")
	}
}

func TestCloudGroupBackupRestoresEmptyGroups(t *testing.T) {
	s := setupTeams(t)
	initial, err := s.store.listCloudGroups("owner", "")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.store.replaceCloudGroups("owner", "", cloudGroupMutation{Groups: []string{"empty"}, ExpectedRevision: initial.Revision})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := s.store.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(backup, []byte("empty")) {
		t.Fatal("encrypted backup leaked group name")
	}
	if _, err = s.store.replaceCloudGroups("owner", "", cloudGroupMutation{ExpectedRevision: saved.Revision}); err != nil {
		t.Fatal(err)
	}
	if err = s.store.importBackup(backup, "owner"); err != nil {
		t.Fatal(err)
	}
	restored, err := s.store.listCloudGroups("owner", "")
	if err != nil || len(restored.Groups) != 1 || restored.Groups[0] != "empty" {
		t.Fatal("backup lost empty group")
	}
}

func httptestHealth(s *Server) string {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return w.Body.String()
}
