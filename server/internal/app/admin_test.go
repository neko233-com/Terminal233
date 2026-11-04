package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminBackupRestoreAndRevocation(t *testing.T) {
	s := testServer(t)
	s.cfg.AdminUsername = "admin"
	s.cfg.AdminTOTPSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, name := range []string{"admin", "alice"} {
		if err := s.store.createUser(name, "long-password"); err != nil {
			t.Fatal(err)
		}
	}
	s.sessions["admin-token"] = session{Username: "admin", ExpiresAt: time.Now().Add(time.Hour)}
	s.sessions["alice-token"] = session{Username: "alice", ExpiresAt: time.Now().Add(time.Hour)}
	request := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		key, _ := decodeTOTPSecret(s.cfg.AdminTOTPSecret)
		r.Header.Set("X-Admin-TOTP", totpAt(key, time.Now().Unix()/30))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if got := request("GET", "/api/v1/admin/users", "alice-token", nil); got.Code != http.StatusNotFound {
		t.Fatalf("non-admin access: %d", got.Code)
	}
	export := request("GET", "/api/v1/admin/export", "admin-token", nil)
	if export.Code != 200 {
		t.Fatalf("export: %d", export.Code)
	}
	var payload struct{ Format, Data string }
	if err := json.Unmarshal(export.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(export.Body.Bytes(), []byte("alice")) {
		t.Fatal("export leaked username")
	}
	if err := s.store.setDisabled("alice", true); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", "/api/v1/accounts", "alice-token", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("disabled session accepted: %d", got.Code)
	}
	bad, _ := json.Marshal(map[string]string{"format": payload.Format, "data": payload.Data + "broken", "confirm": "RESTORE"})
	if got := request("POST", "/api/v1/admin/import", "admin-token", bad); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid backup accepted: %d", got.Code)
	}
	if s.store.userEnabled("alice") {
		t.Fatal("failed import changed state")
	}
	good, _ := json.Marshal(map[string]string{"format": payload.Format, "data": payload.Data, "confirm": "RESTORE"})
	if got := request("POST", "/api/v1/admin/import", "admin-token", good); got.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", got.Code, got.Body.String())
	}
	if !s.store.userEnabled("alice") {
		t.Fatal("restore lost user")
	}
	if got := request("GET", "/api/v1/admin/users", "admin-token", nil); got.Code != http.StatusUnauthorized {
		t.Fatal("restore did not revoke sessions")
	}
}
func TestAdminBootstrapUsesSeparateTokenAndRegistrationThrottle(t *testing.T) {
	s := testServer(t)
	s.cfg.AdminUsername = "admin"
	s.cfg.AdminBootstrapToken = "separate-admin-bootstrap-token-for-test"
	makeRegister := func(name, token string) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]string{"username": name, "password": "long-password", "officialToken": token})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(payload))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := makeRegister("admin", s.cfg.OfficialToken); w.Code != http.StatusForbidden {
		t.Fatalf("ordinary token registered admin: %d", w.Code)
	}
	if w := makeRegister("admin", s.cfg.AdminBootstrapToken); w.Code != http.StatusCreated {
		t.Fatalf("admin bootstrap failed: %d", w.Code)
	}
	for i := 0; i < authFailureLimit-2; i++ {
		makeRegister(fmt.Sprintf("user%d", i), s.cfg.OfficialToken)
	}
	if w := makeRegister("another", s.cfg.OfficialToken); w.Code != http.StatusTooManyRequests {
		t.Fatalf("registration attempts not limited: %d", w.Code)
	}
}
func TestTOTPValidationAndAdminRequiresIt(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := totpAt(key, 1); got != "287082" {
		t.Fatalf("RFC TOTP vector mismatch: %s", got)
	}
	now := time.Unix(1234567890, 0)
	code := totpAt(key, now.Unix()/30)
	if !verifyTOTP(secret, code, now) || verifyTOTP(secret, "000000", now) || verifyTOTP(secret, code, now.Add(2*time.Minute)) {
		t.Fatal("TOTP verification incorrect")
	}
	s := testServer(t)
	s.cfg.AdminUsername = "admin"
	s.cfg.AdminTOTPSecret = secret
	if err = s.store.createUser("admin", "long-password"); err != nil {
		t.Fatal(err)
	}
	s.sessions["token"] = session{Username: "admin", ExpiresAt: time.Now().Add(time.Hour)}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
	req.Header.Set("Authorization", "Bearer token")
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("admin without TOTP accepted: %d", res.Code)
	}
}
