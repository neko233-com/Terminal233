package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.json")
	server, err := New(Config{Address: "127.0.0.1:0", DataPath: path, MasterKey: "test-master-key-which-is-long", OfficialToken: "official-token-for-tests", AdminUsername: "root", AdminPassword: "root", SSHTimeout: 3 * time.Second, AllowedOrigins: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestRegisterRequiresOfficialToken(t *testing.T) {
	server := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(`{"username":"alice","password":"long-password","officialToken":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestRegisterAndLoginRoundTrip(t *testing.T) {
	server := testServer(t)
	register := httptest.NewRequest(http.MethodPost, "/api/v1/register", strings.NewReader(`{"username":"Alice","password":"long-password","officialToken":"official-token-for-tests"}`))
	register.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, register)
	if res.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", res.Code, res.Body.String())
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"alice","password":"long-password"}`))
	login.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	server.ServeHTTP(res, login)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "token") {
		t.Fatalf("login status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestBadSSHCredentialIsNotSaved(t *testing.T) {
	server := testServer(t)
	if err := server.store.createUser("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"alice","password":"long-password"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, login)
	var loginBody struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&loginBody); err != nil {
		t.Fatal(err)
	}
	if loginBody.Token == "" {
		t.Fatal("login response did not contain a token")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{"label":"bad","host":"127.0.0.1","port":1,"username":"root","password":"wrong"}`))
	req.Header.Set("Authorization", "Bearer "+loginBody.Token)
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code == http.StatusCreated {
		t.Fatal("bad SSH credentials were accepted")
	}
	accounts, err := server.store.listAccounts("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts=%d", len(accounts))
	}
}

func TestEncryptedStoreDoesNotContainPlaintext(t *testing.T) {
	server := testServer(t)
	if err := server.store.createUser("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(server.cfg.DataPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "long-password") {
		t.Fatal("plaintext password leaked into store")
	}
}
