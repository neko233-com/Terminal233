package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountJumpScopesValidationAndBackup(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "route team")
	if err != nil {
		t.Fatal(err)
	}
	fixture := func(label string) Account {
		return Account{Label: label, Host: label + ".example", Port: 22, Username: "alice", Password: "fixture-secret", HostKeyFingerprint: "SHA256:fixture"}
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			save := func(a Account, modern bool) (Account, error) {
				if scope == "team" {
					return s.store.saveTeamAccountForClient("owner", team.ID, a, modern)
				}
				return s.store.upsertAccountForClient("owner", a, modern)
			}
			remove := func(id string) error {
				if scope == "team" {
					return s.store.deleteTeamAccount("owner", team.ID, id)
				}
				return s.store.deleteAccount("owner", id)
			}
			jump, err := save(fixture(scope+"-jump"), true)
			if err != nil {
				t.Fatal(err)
			}
			target := fixture(scope + "-target")
			target.JumpID = &jump.ID
			target, err = save(target, true)
			if err != nil {
				t.Fatal(err)
			}
			if s.store.doc.Version != 6 || target.JumpID == nil || *target.JumpID != jump.ID {
				t.Fatal("jump did not persist")
			}
			legacy := target
			legacy.JumpID = nil
			legacy.Label = "legacy"
			if _, err = save(legacy, false); !errors.Is(err, errJumpClientUpgrade) {
				t.Fatal("old client updated a jump account", err)
			}
			jump.JumpID = &target.ID
			if _, err = save(jump, true); !errors.Is(err, errInvalidAccountJump) {
				t.Fatal("cycle accepted", err)
			}
			if err = remove(jump.ID); !errors.Is(err, errAccountJumpInUse) {
				t.Fatal("referenced jump deleted", err)
			}
			missing := target
			missingID := strings.Repeat("a", 48)
			missing.JumpID = &missingID
			if _, err = save(missing, true); !errors.Is(err, errInvalidAccountJump) {
				t.Fatal("missing or cross-scope jump accepted", err)
			}
		})
	}
	backup, err := s.store.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.importBackup(backup, "owner"); err != nil {
		t.Fatalf("backup lost routes: %v", err)
	}
	if err = validateAccountJumpsDocument(s.store.doc); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCloudClientsCannotReadJumpAccounts(t *testing.T) {
	s := testServer(t)
	if err := s.store.createUser("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	s.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"alice","password":"long-password"}`)))
	var token struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &token); err != nil || token.Token == "" {
		t.Fatal("login failed")
	}
	a := Account{Label: "jump", Host: "jump.example", Port: 22, Username: "alice", Password: "fixture", HostKeyFingerprint: "SHA256:fixture"}
	jump, err := s.store.upsertAccount("alice", a)
	if err != nil {
		t.Fatal(err)
	}
	a.Label = "target"
	a.Host = "target.example"
	a.JumpID = &jump.ID
	if _, err = s.store.upsertAccount("alice", a); err != nil {
		t.Fatal(err)
	}
	for _, modern := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
		req.Header.Set("Authorization", "Bearer "+token.Token)
		if modern {
			req.Header.Set("X-Neko-Jump-Support", "1")
		}
		res := httptest.NewRecorder()
		s.ServeHTTP(res, req)
		if !modern && (res.Code != http.StatusConflict || strings.Contains(res.Body.String(), "fixture")) {
			t.Fatal("legacy client received accounts", res.Code)
		}
		if modern && (res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "jumpId")) {
			t.Fatal("modern client did not receive jump", res.Code)
		}
	}
}

func TestAccountJumpRejectsAmbiguousAndTooLongGraphs(t *testing.T) {
	id := func(n int) string { return strings.Repeat("a", 47) + string("0123456789"[n]) }
	items := make([]storedAccount, 10)
	for i := range items {
		items[i].ID = id(i)
		if i > 0 {
			items[i].JumpID = id(i - 1)
		}
	}
	if err := validateAccountJumps(items[:9]); err != nil {
		t.Fatalf("eight hops should be allowed: %v", err)
	}
	if err := validateAccountJumps(items); err == nil {
		t.Fatal("nine hops accepted")
	}
	items = items[:2]
	items[0].ID = items[1].ID
	if err := validateAccountJumps(items); err == nil {
		t.Fatal("ambiguous IDs accepted")
	}
}

func TestCloudWorkspaceJumpRouteProtectsLegacyWrites(t *testing.T) {
	s := setupTeams(t)
	initial, err := s.store.listCloudWorkspaces("owner")
	if err != nil {
		t.Fatal(err)
	}
	w := cloudWorkspace{ID: "0123456789abcdef0123456789abcdef", Name: "workspace", SplitFirst: -1, SplitSecond: -1, Entries: []cloudWorkspaceEntry{{Kind: "cloud", Label: "host", Host: "host.example", Port: "22", User: "alice", AccountID: strings.Repeat("a", 48), JumpRoute: strings.Repeat("b", 64)}}}
	state, err := s.store.replaceCloudWorkspaces("owner", cloudWorkspaceMutation{Items: []cloudWorkspace{w}, ExpectedRevision: initial.Revision, SupportsJumpRoutes: true})
	if err != nil || s.store.doc.Version != 7 {
		t.Fatalf("route workspace not stored: %v", err)
	}
	if _, err = s.store.replaceCloudWorkspaces("owner", cloudWorkspaceMutation{Items: []cloudWorkspace{}, ExpectedRevision: state.Revision}); !errors.Is(err, errWorkspaceClientUpgrade) {
		t.Fatalf("legacy write accepted: %v", err)
	}
	old := s.store.doc
	old.Version = 6
	if validateCloudWorkspaceDocument(old) == nil {
		t.Fatal("old document silently accepts route")
	}
	backup, err := s.store.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.importBackup(backup, "owner"); err != nil {
		t.Fatal(err)
	}
	current, err := s.store.listCloudWorkspaces("owner")
	if err != nil || current.Items[0].Entries[0].JumpRoute != w.Entries[0].JumpRoute {
		t.Fatal("backup lost route", err)
	}
}
