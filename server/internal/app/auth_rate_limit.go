package app

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	authFailureWindow = 10 * time.Minute
	authBlockDuration = 15 * time.Minute
	authFailureLimit  = 8
	authLimiterCap    = 4096
)

type authAttempt struct {
	failures     int
	firstFailure time.Time
	blockedUntil time.Time
	lastSeen     time.Time
}

type authAttemptLimiter struct {
	mu      sync.Mutex
	entries map[string]authAttempt
}

func newAuthAttemptLimiter() *authAttemptLimiter {
	return &authAttemptLimiter{entries: make(map[string]authAttempt)}
}

func (l *authAttemptLimiter) allow(now time.Time, keys ...string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	for _, key := range keys {
		entry, ok := l.entries[key]
		if !ok {
			continue
		}
		if entry.blockedUntil.After(now) {
			return entry.blockedUntil.Sub(now), false
		}
	}
	return 0, true
}

func (l *authAttemptLimiter) failure(now time.Time, keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	for _, key := range keys {
		entry := l.entries[key]
		if entry.firstFailure.IsZero() || now.Sub(entry.firstFailure) > authFailureWindow {
			entry = authAttempt{firstFailure: now}
		}
		entry.failures++
		entry.lastSeen = now
		if entry.failures >= authFailureLimit {
			entry.blockedUntil = now.Add(authBlockDuration)
		}
		l.entries[key] = entry
	}
}

func (l *authAttemptLimiter) success(now time.Time, keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.entries, key)
	}
	l.pruneLocked(now)
}

func (l *authAttemptLimiter) pruneLocked(now time.Time) {
	for key, entry := range l.entries {
		if !entry.blockedUntil.After(now) && now.Sub(entry.lastSeen) > authFailureWindow {
			delete(l.entries, key)
		}
	}
	for len(l.entries) > authLimiterCap {
		var oldestKey string
		var oldest time.Time
		for key, entry := range l.entries {
			if oldestKey == "" || entry.lastSeen.Before(oldest) {
				oldestKey = key
				oldest = entry.lastSeen
			}
		}
		if oldestKey == "" {
			break
		}
		delete(l.entries, oldestKey)
	}
}

func requestClientIdentity(r *http.Request) string {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && net.ParseIP(host) != nil {
		return host
	}
	if value := strings.TrimSpace(r.RemoteAddr); value != "" {
		return value
	}
	return "unknown"
}

func authAttemptKeys(r *http.Request, username string) []string {
	client := requestClientIdentity(r)
	account := normalizeUsername(username)
	return []string{
		"ip:" + client,
		"account:" + account,
		"account:" + account + "@" + client,
	}
}
