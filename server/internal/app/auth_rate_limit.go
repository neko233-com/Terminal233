package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strconv"
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

func (s *Server) requestClientIdentity(r *http.Request) string {
	peer := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	ip, err := netip.ParseAddr(peer)
	if err == nil {
		ip = ip.Unmap()
		if (s.cfg.TrustCloudflareTunnel || len(s.cfg.EdgeHMACKey) != 0) && ip.IsLoopback() {
			values := r.Header.Values("CF-Connecting-IP")
			if len(values) == 1 {
				visitor, err := netip.ParseAddr(strings.TrimSpace(values[0]))
				if err == nil {
					visitor = visitor.Unmap()
					if visitor.Zone() == "" && visitor.IsGlobalUnicast() && !visitor.IsPrivate() && (len(s.cfg.EdgeHMACKey) == 0 || validEdgeSignature(r, values[0], s.cfg.EdgeHMACKey, time.Now())) {
						return visitor.String()
					}
				}
			}
		}
		return ip.String()
	}
	if value := strings.TrimSpace(r.RemoteAddr); value != "" {
		return value
	}
	return "unknown"
}

// The edge signature authenticates the IP header even against other local
// processes that can connect to the server's loopback listener.
func validEdgeSignature(r *http.Request, ip string, key []byte, now time.Time) bool {
	values := r.Header.Values("X-Nekossh-Visitor-Signature")
	if len(values) != 1 {
		return false
	}
	timestamp, signature, ok := strings.Cut(values[0], ":")
	if !ok || len(timestamp) > 12 || len(signature) != 64 {
		return false
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds < now.Unix()-60 || seconds > now.Unix()+60 {
		return false
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(timestamp + "\n" + r.Method + "\n" + r.URL.EscapedPath() + "\n" + ip))
	return hmac.Equal(provided, mac.Sum(nil))
}

func (s *Server) authAttemptKeys(r *http.Request, username string) []string {
	client := s.requestClientIdentity(r)
	account := normalizeUsername(username)
	return []string{
		"ip:" + client,
		"account:" + account,
		"account:" + account + "@" + client,
	}
}
