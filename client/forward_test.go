package main

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestLocalForwardRelaysAndStopsWithSession(t *testing.T) {
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	_, targetPort, err := net.SplitHostPort(target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	f, err := startLocalForward(shell.client, shell.done, "0", "127.0.0.1", targetPort, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !strings.HasPrefix(f.address(), "127.0.0.1:") {
		t.Fatalf("non-loopback listener: %s", f.address())
	}
	conn, err := net.DialTimeout("tcp", f.address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err = io.ReadFull(conn, b); err != nil || string(b) != "hello" {
		t.Fatalf("relay %q: %v", b, err)
	}
	_, localPort, _ := net.SplitHostPort(f.address())
	if _, err := startLocalForward(shell.client, shell.done, localPort, "127.0.0.1", targetPort, nil); err == nil {
		t.Fatal("accepted occupied local port")
	}
	f.Close()
	if _, err := conn.Read(b); err == nil {
		t.Fatal("stopped forward left an active connection")
	}
	f, err = startLocalForward(shell.client, shell.done, "0", "127.0.0.1", targetPort, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	conn, err = net.DialTimeout("tcp", f.address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(conn, b); err != nil {
		t.Fatal(err)
	}
	if err = shell.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !f.isClosed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !f.isClosed() {
		t.Fatal("SSH disconnect did not stop listener")
	}
	if _, err = net.DialTimeout("tcp", f.address(), time.Second); err == nil {
		t.Fatal("listener survived session")
	}
	if _, err = conn.Read(b); err == nil {
		t.Fatal("active connection survived session")
	}
}

func TestForwardValidation(t *testing.T) {
	for _, value := range []string{"", "a/b", "user@host", "example.com:22", "host name"} {
		if _, err := parseForwardTarget(value, "22"); err == nil {
			t.Fatalf("invalid host accepted: %q", value)
		}
	}
	if _, err := parseForwardTarget("::1", "22"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseForwardTarget("localhost", "0"); err == nil {
		t.Fatal("invalid port accepted")
	}
	if _, err := startLocalForward(nil, nil, "0", "localhost", "22", nil); err == nil {
		t.Fatal("accepted absent SSH session")
	}
}
