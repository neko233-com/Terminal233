package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudRequiresHTTPSAndRefusesCredentialURLs(t *testing.T) {
	for _, value := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/?token=secret", "https://example.com/#secret"} {
		if _, err := newCloudClient(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
func TestCloudLoginAndAuthenticatedList(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/login":
			_, _ = w.Write([]byte(`{"token":"test-session"}`))
		case "/api/v1/accounts":
			if r.Header.Get("Authorization") != "Bearer test-session" {
				http.Error(w, "missing auth", 401)
				return
			}
			_, _ = w.Write([]byte(`[{"id":"1","label":"test","host":"example.com","port":22,"username":"alice","password":"secret","hostKeyFingerprint":"SHA256:abc"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = server.Client().Transport
	if err := client.login("alice", "password"); err != nil {
		t.Fatal(err)
	}
	accounts, err := client.list()
	if err != nil || len(accounts) != 1 || accounts[0].Host != "example.com" {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
}
func TestCloudDoesNotFollowRedirect(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("credentials followed redirect") }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := newCloudClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = server.Client().Transport
	if err := client.login("alice", "secret"); err == nil {
		t.Fatal("accepted redirect")
	}
}
