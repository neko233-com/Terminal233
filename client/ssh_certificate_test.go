package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func certificateFixture(t *testing.T) (string, ssh.Signer, *ssh.Certificate) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	_, authority, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ssh.NewSignerFromKey(authority)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.UserCert, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	return keyPath, signer, cert
}

func TestSSHUserCertificateAndRawKeyFallback(t *testing.T) {
	keyPath, signer, cert := certificateFixture(t)
	if err := os.WriteFile(keyPath+"-cert.pub", ssh.MarshalAuthorizedKey(cert), 0600); err != nil {
		t.Fatal(err)
	}
	settings, fingerprint, attempts := testSSHServer(t, cert)
	settings.KeyPath, settings.Password, settings.Fingerprint = keyPath, "", "SHA256:incorrect"
	if client, err := connectSSHClient(context.Background(), settings); err == nil {
		_ = client.Close()
		t.Fatal("certificate authentication accepted an untrusted host")
	}
	if attempts.Load() != 0 {
		t.Fatal("certificate was offered before host verification")
	}
	settings.Fingerprint = fingerprint
	client, err := connectSSHClient(context.Background(), settings)
	if err != nil {
		t.Fatalf("certificate authentication failed: %v", err)
	}
	_ = client.Close()
	store, id := agentTestStore(t, settings)
	if code, result, _ := agentTestCall(t, store, "ls", "--host", id, "--path", "."); code != 0 || result["ok"] != true {
		t.Fatalf("agent CLI could not use the certificate: %v", result)
	}
	raw, rawFingerprint, _ := testSSHServer(t, signer.PublicKey())
	raw.KeyPath, raw.Password, raw.Fingerprint = keyPath, "", rawFingerprint
	client, err = connectSSHClient(context.Background(), raw)
	if err != nil {
		t.Fatalf("raw public key fallback failed: %v", err)
	}
	_ = client.Close()
	if err := os.Remove(keyPath + "-cert.pub"); err != nil {
		t.Fatal(err)
	}
	client, err = connectSSHClient(context.Background(), raw)
	if err != nil {
		t.Fatalf("ordinary key without a certificate regressed: %v", err)
	}
	_ = client.Close()
}

func TestSSHUserCertificateRejectsInvalidSidecar(t *testing.T) {
	keyPath, signer, cert := certificateFixture(t)
	_, other, _ := certificateFixture(t)
	wrongKeyCert := *cert
	wrongKeyCert.Key = other.PublicKey()
	hostCert := *cert
	hostCert.CertType = ssh.HostCert
	for name, content := range map[string][]byte{
		"malformed":  []byte("not a certificate"),
		"public-key": ssh.MarshalAuthorizedKey(signer.PublicKey()),
		"wrong-key":  ssh.MarshalAuthorizedKey(&wrongKeyCert),
		"host-cert":  ssh.MarshalAuthorizedKey(&hostCert),
		"extra-key":  append(ssh.MarshalAuthorizedKey(cert), ssh.MarshalAuthorizedKey(signer.PublicKey())...),
		"oversized":  make([]byte, (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(keyPath+"-cert.pub", content, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := identitySigners(keyPath, signer); err == nil {
				t.Fatal("invalid certificate sidecar was accepted")
			}
		})
	}
}
