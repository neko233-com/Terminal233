package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func echoFixture(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l
}

func TestDynamicForwardSOCKS5(t *testing.T) {
	target := echoFixture(t)
	_, p, _ := net.SplitHostPort(target.Addr().String())
	port, _ := strconv.Atoi(p)
	settings, fingerprint, _ := testSSHServer(t)
	settings.Fingerprint = fingerprint
	shell, err := connectShell(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	f, err := startDynamicForward(shell.client, shell.done, "0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if host, _, _ := net.SplitHostPort(f.address()); host != "127.0.0.1" {
		t.Fatalf("unexpected listener %s", f.address())
	}
	conn, err := net.DialTimeout("tcp", f.address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write([]byte{5, 1, 0})
	b := make([]byte, 2)
	if _, err = io.ReadFull(conn, b); err != nil || b[1] != 0 {
		t.Fatalf("method: %v %v", b, err)
	}
	_, _ = conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)})
	response := make([]byte, 10)
	if _, err = io.ReadFull(conn, response); err != nil || response[1] != 0 {
		t.Fatalf("connect: %v %v", response, err)
	}
	_, _ = conn.Write([]byte("hello"))
	data := make([]byte, 5)
	if _, err = io.ReadFull(conn, data); err != nil || string(data) != "hello" {
		t.Fatalf("relay: %q %v", data, err)
	}
	f.Close()
	if _, err = conn.Read(data); err == nil {
		t.Fatal("active SOCKS connection survived stop")
	}
}

func TestSOCKS5RejectsUnsupportedCommand(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() { _, err := socks5Target(server); done <- err }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_, _ = client.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	_, _ = io.ReadFull(client, reply)
	_, _ = client.Write([]byte{5, 2, 0, 1})
	result := make([]byte, 10)
	if _, err := io.ReadFull(client, result); err != nil || result[1] != 7 {
		t.Fatalf("reply %v: %v", result, err)
	}
	if <-done == nil {
		t.Fatal("unsupported command accepted")
	}
}

func TestSOCKS5PreservesDomainForRemoteResolution(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	result := make(chan string, 1)
	go func() { target, _ := socks5Target(server); result <- target }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_, _ = client.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	_, _ = io.ReadFull(client, reply)
	request := append([]byte{5, 1, 0, 3, 11}, []byte("example.org")...)
	request = append(request, 1, 187)
	_, _ = client.Write(request)
	if got := <-result; got != "example.org:443" {
		t.Fatalf("target = %q", got)
	}
}

// This isolated SSH server handles only loopback remote forwarding.
func remoteForwardFixture(t *testing.T) (*ssh.Client, <-chan struct{}) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		sc, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			conn.Close()
			return
		}
		defer sc.Close()
		go func() {
			for ch := range channels {
				_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
			}
		}()
		var listener net.Listener
		defer func() {
			if listener != nil {
				listener.Close()
			}
		}()
		for req := range requests {
			var request struct {
				Host string
				Port uint32
			}
			if ssh.Unmarshal(req.Payload, &request) != nil || request.Host != "127.0.0.1" || request.Port > 65535 {
				req.Reply(false, nil)
				continue
			}
			switch req.Type {
			case "tcpip-forward":
				listener, err = net.Listen("tcp4", net.JoinHostPort(request.Host, strconv.Itoa(int(request.Port))))
				if err != nil {
					req.Reply(false, nil)
					continue
				}
				_, p, _ := net.SplitHostPort(listener.Addr().String())
				port, _ := strconv.Atoi(p)
				req.Reply(true, ssh.Marshal(struct{ Port uint32 }{uint32(port)}))
				go func(l net.Listener) {
					for {
						c, err := l.Accept()
						if err != nil {
							return
						}
						go func() {
							defer c.Close()
							payload := ssh.Marshal(struct {
								Host       string
								Port       uint32
								Origin     string
								OriginPort uint32
							}{"127.0.0.1", uint32(port), "127.0.0.1", 1234})
							ch, reqs, err := sc.OpenChannel("forwarded-tcpip", payload)
							if err != nil {
								return
							}
							defer ch.Close()
							go ssh.DiscardRequests(reqs)
							go io.Copy(ch, c)
							_, _ = io.Copy(c, ch)
						}()
					}
				}(listener)
			case "cancel-tcpip-forward":
				if listener != nil {
					listener.Close()
				}
				req.Reply(true, nil)
			default:
				req.Reply(false, nil)
			}
		}
	}()
	client, err := ssh.Dial("tcp", l.Addr().String(), &ssh.ClientConfig{User: "fixture", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	done := make(chan struct{})
	go func() { _ = client.Wait(); close(done) }()
	return client, done
}

func TestRemoteForwardRelaysAndStops(t *testing.T) {
	target := echoFixture(t)
	_, port, _ := net.SplitHostPort(target.Addr().String())
	client, done := remoteForwardFixture(t)
	f, err := startRemoteForward(client, done, "0", "127.0.0.1", port, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	conn, err := net.DialTimeout("tcp", f.address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write([]byte("hello"))
	data := make([]byte, 5)
	if _, err = io.ReadFull(conn, data); err != nil || string(data) != "hello" {
		t.Fatalf("relay %q: %v", data, err)
	}
	f.Close()
	if _, err = conn.Read(data); err == nil {
		t.Fatal("connection survived stop")
	}
}
