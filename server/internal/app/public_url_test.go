package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPublicURLValidation(t *testing.T) {
	for _, invalid := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/?x=1", "https://example.test/#fragment", "https://", "https://example.test:bad"} {
		if _, err := normalizePublicURL(invalid); err == nil {
			t.Fatalf("accepted invalid public URL %q", invalid)
		}
	}
	for input, expected := range map[string]string{"": "", "https://sync.example.test/": "https://sync.example.test", "https://sync.example.test/base/": "https://sync.example.test/base"} {
		if got, err := normalizePublicURL(input); err != nil || got != expected {
			t.Fatalf("normalize %q: %q, %v", input, got, err)
		}
	}
}

func TestPublicURLIsLoadedFromEnvironment(t *testing.T) {
	t.Setenv("TERMINAL233_MASTER_KEY", strings.Repeat("m", 40))
	t.Setenv("TERMINAL233_OFFICIAL_TOKEN", strings.Repeat("t", 40))
	t.Setenv("TERMINAL233_SERVER_ADDR", "127.0.0.1:8787")
	t.Setenv("TERMINAL233_ADMIN_USERNAME", "")
	t.Setenv("TERMINAL233_PUBLIC_URL", "https://sync.example.test/base/")
	cfg, err := ConfigFromEnv()
	if err != nil || cfg.PublicURL != "https://sync.example.test/base" {
		t.Fatalf("public URL not loaded: %q, %v", cfg.PublicURL, err)
	}
	t.Setenv("TERMINAL233_PUBLIC_URL", "http://example.test")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid configured public URL was accepted")
	}
}

func TestInviteURLUsesConfiguredServiceNeverRequestHost(t *testing.T) {
	server := testServer(t)
	if err := server.store.createUser("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	team, err := server.store.createTeam("alice", "test")
	if err != nil {
		t.Fatal(err)
	}
	server.sessions["test-session"] = session{Username: "alice", ExpiresAt: time.Now().Add(time.Hour)}
	for _, base := range []string{"", "https://selfhost.example.test/base"} {
		server.cfg.PublicURL = base
		req := httptest.NewRequest(http.MethodPost, "https://attacker.example.test/api/v1/teams/"+team.ID+"/invitations", strings.NewReader(`{"password":"valid-password-123","expiresInSeconds":3600,"uses":1}`))
		req.Header.Set("Authorization", "Bearer test-session")
		req.Header.Set("X-Forwarded-Host", "attacker.example.test")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != http.StatusCreated {
			t.Fatalf("create invite: %d %s", response.Code, response.Body.String())
		}
		var result map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result["token"]) != 48 {
			t.Fatal("invalid invitation response")
		}
		if base == "" {
			if _, ok := result["url"]; ok {
				t.Fatal("unconfigured service invented an invitation URL")
			}
			continue
		}
		link, err := url.Parse(result["url"])
		if err != nil || link.Scheme != "https" || link.Host != "selfhost.example.test" || link.Path != "/base" || link.Fragment != "join="+result["token"] {
			t.Fatalf("invalid configured invitation link: %q", result["url"])
		}
	}
}
