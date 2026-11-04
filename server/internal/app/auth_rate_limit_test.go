package app

import (
	"net/http/httptest"
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
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("CF-Connecting-IP", "198.51.100.42")
	if got := requestClientIdentity(req); got != "127.0.0.1" {
		t.Fatalf("client identity=%q", got)
	}
}
