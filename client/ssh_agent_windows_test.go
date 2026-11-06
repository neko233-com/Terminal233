//go:build windows

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestSSHAgentAuthenticationAndHostKey(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := `\\.\pipe\nekossh-agent-test-` + hex.EncodeToString(nonce[:])
	listener, err := winio.ListenPipe(name, nil)
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
			go func(conn net.Conn) { defer conn.Close(); _ = agent.ServeAgent(keyring, conn) }(conn)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", name)
	settings, fingerprint, attempts := testSSHServer(t, signer.PublicKey())
	settings.Password = ""
	settings.Fingerprint = "SHA256:incorrect"
	if conn, err := connectSSHClient(context.Background(), settings); err == nil {
		_ = conn.Close()
		t.Fatal("accepted an untrusted host")
	}
	if attempts.Load() != 0 {
		t.Fatal("authentication was attempted before host key validation")
	}
	settings.Fingerprint = fingerprint
	conn, err := connectSSHClient(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if attempts.Load() == 0 {
		t.Fatal("agent public key was not offered")
	}
	store, id := agentTestStore(t, settings)
	code, result, _ := agentTestCall(t, store, "ls", "--host", id, "--path", ".")
	if code != 0 || result["ok"] != true {
		t.Fatalf("agent CLI could not use the SSH agent: %v", result)
	}
}

func TestSSHAgentCertificateAuthentication(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ssh.NewSignerFromKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.UserCert, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key, Certificate: cert}); err != nil {
		t.Fatal(err)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := `\\.\pipe\nekossh-agent-test-` + hex.EncodeToString(nonce[:])
	listener, err := winio.ListenPipe(name, nil)
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
			go func(conn net.Conn) { defer conn.Close(); _ = agent.ServeAgent(keyring, conn) }(conn)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", name)
	settings, fingerprint, _ := testSSHServer(t, cert)
	settings.Password, settings.Fingerprint = "", fingerprint
	client, err := connectSSHClient(context.Background(), settings)
	if err != nil {
		t.Fatalf("SSH agent certificate authentication failed: %v", err)
	}
	_ = client.Close()
}
