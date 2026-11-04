package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Starts the real Go server and reaches it through a TLS proxy, as in deployment.
func TestCloudAgainstGoServer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test builds the server")
	}
	binary := filepath.Join(t.TempDir(), "sync-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "-C", "../server", "build", "-p", "1", "-o", binary, "./cmd/server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v %s", err, output)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	command := exec.Command(binary)
	command.Env = append(os.Environ(), "TERMINAL233_SERVER_ADDR="+address, "TERMINAL233_DATA_PATH="+filepath.Join(t.TempDir(), "store.json"), "TERMINAL233_MASTER_KEY="+strings.Repeat("m", 40), "TERMINAL233_OFFICIAL_TOKEN="+strings.Repeat("t", 40))
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	base := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	target, _ := url.Parse(base)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer proxy.Close()
	client, err := newCloudClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = proxy.Client().Transport
	if err := client.register("alice", "long-password", strings.Repeat("t", 40)); err != nil {
		t.Fatal(err)
	}
	if err := client.login("alice", "long-password"); err != nil {
		t.Fatal(err)
	}
	account := cloudAccount{Label: "sample", Host: "example.test", Port: 22, Username: "me", Password: "ssh-password", HostKeyFingerprint: "SHA256:verified"}
	if err := client.save(account); err != nil {
		t.Fatal(err)
	}
	accounts, err := client.list()
	if err != nil || len(accounts) != 1 || accounts[0].Password != "ssh-password" {
		t.Fatalf("list=%v err=%v", accounts, err)
	}
	if err := client.remove(accounts[0].ID); err != nil {
		t.Fatal(err)
	}
	accounts, err = client.list()
	if err != nil || len(accounts) != 0 {
		t.Fatalf("delete list=%v err=%v", accounts, err)
	}
	if err := client.logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.list(); err == nil {
		t.Fatal("logged-out client accessed accounts")
	}
}
