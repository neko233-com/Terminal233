package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
