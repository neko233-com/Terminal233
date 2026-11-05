package app

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthAttemptLimiterBlocksAndClearsOnSuccess(t *testing.T) {
	limiter := newAuthAttemptLimiter()
	now := time.Unix(100, 0)
	keys := []string{"ip:192.0.2.10", "account:alice@192.0.2.10"}
	for i := 0; i < authFailureLimit-1; i++ {
		limiter.failure(now, keys...)
		if _, ok := limiter.allow(now, keys...); !ok {
			t.Fatalf("blocked after %d failures", i+1)
		}
	}
	limiter.failure(now, keys...)
	retryAfter, ok := limiter.allow(now, keys...)
	if ok || retryAfter <= 0 {
		t.Fatalf("expected block, retryAfter=%s ok=%v", retryAfter, ok)
	}
	limiter.success(now, keys...)
	if _, ok := limiter.allow(now, keys...); !ok {
		t.Fatal("successful authentication did not clear the limiter")
	}
}

func TestRequestClientIdentityIgnoresUntrustedForwardedAddress(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("CF-Connecting-IP", "198.51.100.42")
	if got := server.requestClientIdentity(req); got != "127.0.0.1" {
		t.Fatalf("client identity=%q", got)
	}
}

func TestTunnelVisitorIdentityRequiresExplicitTrustAndLoopbackPeer(t *testing.T) {
	server := &Server{cfg: Config{TrustCloudflareTunnel: true}}
	for _, tc := range []struct {
		name, peer string
		headers    []string
		want       string
	}{
		{"visitor IPv4", "127.0.0.1:42", []string{"198.51.100.42"}, "198.51.100.42"},
		{"visitor IPv6", "[::1]:42", []string{"2001:db8::1"}, "2001:db8::1"},
		{"untrusted peer", "198.51.100.8:42", []string{"203.0.113.9"}, "198.51.100.8"},
		{"multiple headers", "127.0.0.1:42", []string{"198.51.100.1", "198.51.100.2"}, "127.0.0.1"},
		{"comma list", "127.0.0.1:42", []string{"198.51.100.1, 198.51.100.2"}, "127.0.0.1"},
		{"private address", "127.0.0.1:42", []string{"10.0.0.2"}, "127.0.0.1"},
		{"mapped private address", "127.0.0.1:42", []string{"::ffff:10.0.0.2"}, "127.0.0.1"},
		{"loopback address", "127.0.0.1:42", []string{"127.0.0.2"}, "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://localhost/", nil)
			req.RemoteAddr = tc.peer
			for _, value := range tc.headers {
				req.Header.Add("CF-Connecting-IP", value)
			}
			if got := server.requestClientIdentity(req); got != tc.want {
				t.Fatalf("identity=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestTunnelIdentitySeparatesRegistrationLimits(t *testing.T) {
	server := testServer(t)
	server.cfg.TrustCloudflareTunnel = true
	for i := 0; i < authFailureLimit; i++ {
		req := httptest.NewRequest("POST", "/api/v1/register", strings.NewReader(`{"username":"alice","password":"long-password","officialToken":"wrong"}`))
		req.RemoteAddr = "127.0.0.1:42"
		req.Header.Set("CF-Connecting-IP", "198.51.100.1")
		server.ServeHTTP(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest("POST", "/api/v1/register", strings.NewReader(`{"username":"bob","password":"long-password","officialToken":"official-token-for-tests"}`))
	req.RemoteAddr = "127.0.0.1:42"
	req.Header.Set("CF-Connecting-IP", "198.51.100.2")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != 201 {
		t.Fatalf("a different visitor was blocked: %d", res.Code)
	}
}

func TestTunnelTrustConfigurationIsExplicit(t *testing.T) {
	t.Setenv("TERMINAL233_SERVER_ADDR", "127.0.0.1:8787")
	t.Setenv("TERMINAL233_MASTER_KEY", strings.Repeat("m", 40))
	t.Setenv("TERMINAL233_OFFICIAL_TOKEN", strings.Repeat("t", 40))
	t.Setenv("TERMINAL233_ADMIN_USERNAME", "")
	t.Setenv("TERMINAL233_TRUST_CLOUDFLARE_TUNNEL", "")
	cfg, err := ConfigFromEnv()
	if err != nil || cfg.TrustCloudflareTunnel {
		t.Fatal("tunnel trust must be disabled by default")
	}
	t.Setenv("TERMINAL233_TRUST_CLOUDFLARE_TUNNEL", "true")
	cfg, err = ConfigFromEnv()
	if err != nil || !cfg.TrustCloudflareTunnel {
		t.Fatal("explicit tunnel trust was not enabled")
	}
	t.Setenv("TERMINAL233_TRUST_CLOUDFLARE_TUNNEL", "invalid")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid tunnel trust value was accepted")
	}
}
