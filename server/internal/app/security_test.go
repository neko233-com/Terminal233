package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLimiterKeepsBlockForFullDuration(t *testing.T) {
	limiter := newAuthAttemptLimiter()
	start := time.Unix(100, 0)
	for i := 0; i < authFailureLimit; i++ {
		limiter.failure(start, "ip:test")
	}
	if _, allowed := limiter.allow(start.Add(authFailureWindow+time.Second), "ip:test"); allowed {
		t.Fatal("block expired before block duration")
	}
	if _, allowed := limiter.allow(start.Add(authBlockDuration+time.Second), "ip:test"); !allowed {
		t.Fatal("block did not expire")
	}
}

func TestSyncRequiresAuthenticationAndFingerprint(t *testing.T) {
	server := testServer(t)
	if err := server.store.createUser("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	server.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"alice","password":"long-password"}`)))
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil || body.Token == "" {
		t.Fatalf("login failed: %s", login.Body.String())
	}
	payload := `{"label":"test","host":"127.0.0.1","port":22,"username":"root","password":"secret"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+body.Token)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing fingerprint accepted: %d", res.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated access: %d", res.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("admin endpoint exposed: %d", res.Code)
	}
}

func TestConfigRejectsPublicListen(t *testing.T) {
	t.Setenv("TERMINAL233_MASTER_KEY", strings.Repeat("a", 32))
	t.Setenv("TERMINAL233_OFFICIAL_TOKEN", strings.Repeat("b", 32))
	t.Setenv("TERMINAL233_SERVER_ADDR", "0.0.0.0:8787")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("accepted public plaintext listener")
	}
	t.Setenv("TERMINAL233_SERVER_ADDR", "127.0.0.1:8787")
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestAccountRoundTripAndUserIsolation(t *testing.T) {
	server := testServer(t)
	for _, user := range []string{"alice", "bob"} {
		if err := server.store.createUser(user, "long-password"); err != nil {
			t.Fatal(err)
		}
	}
	login := func(user string) string {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"`+user+`","password":"long-password"}`)))
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" {
			t.Fatalf("login %s: %s", user, rec.Body.String())
		}
		return body.Token
	}
	alice, bob := login("alice"), login("bob")
	payload := `{"label":"host","host":"example.test","port":22,"username":"me","password":"ssh-secret","hostKeyFingerprint":"SHA256:fixture"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+alice)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, request)
	if rec.Code != http.StatusCreated {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	for _, test := range []struct {
		token      string
		wantSecret bool
	}{{alice, true}, {bob, false}} {
		request = httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		rec = httptest.NewRecorder()
		server.ServeHTTP(rec, request)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "ssh-secret") != test.wantSecret {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
	}
	disk, err := os.ReadFile(server.cfg.DataPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), "ssh-secret") || strings.Contains(string(disk), "example.test") {
		t.Fatal("store contains plaintext account")
	}
}
