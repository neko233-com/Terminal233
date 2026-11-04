package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testSSHServer(t *testing.T) (connectionSettings, string, *atomic.Int32) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	attempts := &atomic.Int32{}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		attempts.Add(1)
		if string(password) != "test-password" {
			return nil, ssh.ErrNoAuth
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					if ch.ChannelType() != "session" {
						_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							switch req.Type {
							case "pty-req", "window-change":
								_ = req.Reply(true, nil)
							case "shell":
								_ = req.Reply(true, nil)
								_, _ = channel.Write([]byte("ready\r\n"))
								_, _ = io.Copy(channel, channel)
								return
							default:
								_ = req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	return connectionSettings{Host: host, Port: port, User: "alice", Password: "test-password"}, ssh.FingerprintSHA256(signer.PublicKey()), attempts
}

func TestHostConfirmationAndShell(t *testing.T) {
	settings, expected, attempts := testSSHServer(t)
	fingerprint, err := inspectHost(settings)
	if err != nil || fingerprint != expected {
		t.Fatalf("fingerprint=%q err=%v", fingerprint, err)
	}
	if attempts.Load() != 0 {
		t.Fatal("host inspection sent credentials")
	}
	settings.Fingerprint = "SHA256:incorrect"
	if shell, err := connectShell(settings); err == nil {
		_ = shell.Close()
		t.Fatal("accepted a changed host key")
	}
	if attempts.Load() != 0 {
		t.Fatal("sent credentials to a host with a mismatched key")
	}
	settings.Fingerprint = expected
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	result := make(chan string, 1)
	go func() { buf := make([]byte, 64); n, _ := shell.Read(buf); result <- string(buf[:n]) }()
	select {
	case got := <-result:
		if !strings.Contains(got, "ready") {
			t.Fatalf("unexpected output %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shell output timed out")
	}
	if attempts.Load() != 1 {
		t.Fatalf("authentication attempts=%d", attempts.Load())
	}
}

func TestInvalidConnectionSettings(t *testing.T) {
	for _, input := range []connectionSettings{{Host: "example.com", Port: "0", User: "a"}, {Host: "example.com/path", Port: "22", User: "a"}, {Host: "example.com", Port: "22"}} {
		if _, err := input.address(); err == nil {
			t.Fatalf("accepted invalid settings: %+v", input)
		}
	}
}
