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

func TestConditionalAccountUpdatesAreAtomic(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			save := func(a Account) (Account, error) {
				if scope == "team" {
					return s.store.saveTeamAccount("owner", team.ID, a)
				}
				return s.store.upsertAccount("owner", a)
			}
			original, err := save(Account{Label: "original", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:test"})
			if err != nil || original.UpdatedAt == "" {
				t.Fatalf("create failed: %v", err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, label := range []string{"first", "second"} {
				wg.Add(1)
				go func(label string) {
					defer wg.Done()
					a := original
					a.Label, a.ExpectedUpdatedAt = label, original.UpdatedAt
					<-start
					_, err := save(a)
					results <- err
				}(label)
			}
			close(start)
			wg.Wait()
			close(results)
			success, conflict := 0, 0
			for err := range results {
				if err == nil {
					success++
				} else if errors.Is(err, errAccountConflict) {
					conflict++
				} else {
					t.Fatalf("unexpected update failure: %v", err)
				}
			}
			if success != 1 || conflict != 1 {
				t.Fatalf("updates success=%d conflict=%d", success, conflict)
			}
			var items []Account
			if scope == "team" {
				items, err = s.store.listTeamAccounts("owner", team.ID)
			} else {
				items, err = s.store.listAccounts("owner")
			}
			if err != nil || len(items) != 1 || items[0].UpdatedAt == original.UpdatedAt || items[0].ExpectedUpdatedAt != "" {
				t.Fatalf("stored revision invalid: %v", err)
			}
			legacy := items[0]
			legacy.Label = "legacy"
			if _, err := save(legacy); err != nil {
				t.Fatalf("legacy client update failed: %v", err)
			}
		})
	}
}

func TestConditionalConflictHTTPResponses(t *testing.T) {
	s := setupTeams(t)
	health := httptest.NewRecorder()
	s.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), "conditional-account-update") || !strings.Contains(health.Body.String(), "conditional-account-delete") {
		t.Fatal("conditional update capability not advertised")
	}
	team, err := s.store.createTeam("owner", "test")
	if err != nil {
		t.Fatal(err)
	}
	s.sessions["test-session"] = session{Username: "owner", ExpiresAt: time.Now().Add(time.Hour)}
	for _, endpoint := range []string{"/api/v1/accounts", "/api/v1/teams/" + team.ID + "/accounts"} {
		account := Account{Label: "test", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:test"}
		call := func(a Account) *httptest.ResponseRecorder {
			body, _ := json.Marshal(a)
			req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-session")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			return w
		}
		created := call(account)
		if created.Code != http.StatusCreated {
			t.Fatalf("create status: %d", created.Code)
		}
		if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil {
			t.Fatal(err)
		}
		account.ExpectedUpdatedAt = "stale"
		res := call(account)
		if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "account_conflict") || strings.Contains(res.Body.String(), account.Password) {
			t.Fatalf("invalid conflict response: %d", res.Code)
		}
	}
}

func TestRevisionMonotonicWhenClockMovesBack(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	got, err := time.Parse(time.RFC3339Nano, nextAccountTimestamp(future.Format(time.RFC3339Nano)))
	if err != nil || !got.After(future) {
		t.Fatal("revision did not advance")
	}
}

func TestConditionalDeleteRejectsChangedRecords(t *testing.T) {
	s := setupTeams(t)
	team, err := s.store.createTeam("owner", "test")
	if err != nil {
		t.Fatal(err)
	}
	s.sessions["test-session"] = session{Username: "owner", ExpiresAt: time.Now().Add(time.Hour)}
	for _, endpoint := range []string{"/api/v1/accounts", "/api/v1/teams/" + team.ID + "/accounts"} {
		a := Account{Label: "test", Host: "example.test", Port: 22, Username: "alice", Password: "secret", HostKeyFingerprint: "SHA256:test"}
		body, _ := json.Marshal(a)
		create := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		create.Header.Set("Authorization", "Bearer test-session")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, create)
		if w.Code != http.StatusCreated {
			t.Fatal("create failed")
		}
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		previous := a.UpdatedAt
		a.Label, a.ExpectedUpdatedAt = "updated", previous
		body, _ = json.Marshal(a)
		update := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		update.Header.Set("Authorization", "Bearer test-session")
		w = httptest.NewRecorder()
		s.ServeHTTP(w, update)
		if w.Code != http.StatusCreated {
			t.Fatal("update failed")
		}
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		remove := func(revision string) int {
			req := httptest.NewRequest(http.MethodDelete, endpoint+"/"+a.ID, nil)
			req.Header.Set("Authorization", "Bearer test-session")
			if revision != "" {
				req.Header.Set("X-Neko-Expected-Updated-At", revision)
			}
			res := httptest.NewRecorder()
			s.ServeHTTP(res, req)
			return res.Code
		}
		if got := remove("invalid"); got != http.StatusBadRequest {
			t.Fatalf("invalid revision: %d", got)
		}
		if got := remove(previous); got != http.StatusConflict {
			t.Fatalf("stale delete: %d", got)
		}
		if got := remove(a.UpdatedAt); got != http.StatusNoContent {
			t.Fatalf("current delete: %d", got)
		}
	}
}
