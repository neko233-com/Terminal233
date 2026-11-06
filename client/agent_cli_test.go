package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func agentTestStore(t *testing.T, settings connectionSettings) (string, string) {
	t.Helper()
	store := filepath.Join(t.TempDir(), "profiles.json")
	d := &desktop{}
	if err := d.loadProfiles(store); err != nil {
		t.Fatal(err)
	}
	d.settings, d.active, d.profileLabel = settings, settings, "CLI fixture"
	if err := d.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	d = &desktop{}
	if err := d.loadProfiles(store); err != nil {
		t.Fatal(err)
	}
	return store, agentHostID(d.profiles[0])
}

func agentTestCall(t *testing.T, store string, args ...string) (int, map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	code := runAgentCLI(args, store, &out)
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("stdout contains more than one JSON result")
	}
	return code, result, out.String()
}

func TestAgentCLIListsMetadataAndRejectsUntrustedHosts(t *testing.T) {
	settings, fp, attempts := testSSHServer(t)
	settings.Fingerprint = fp
	store, id := agentTestStore(t, settings)
	code, result, output := agentTestCall(t, store, "hosts")
	if code != 0 || result["ok"] != true || len(result["hosts"].([]any)) != 1 {
		t.Fatal(result)
	}
	if strings.Contains(output, settings.Password) || strings.Contains(output, "EncryptedCredentials") || strings.Contains(output, "KeyPath") {
		t.Fatal("metadata exposed credential material")
	}
	code, _, output = agentTestCall(t, store, "exec", "--host", id, "--password", "secret-argument")
	if code != 2 || strings.Contains(output, "secret-argument") {
		t.Fatal("password option was accepted or echoed")
	}
	settings.Fingerprint = ""
	untrusted, untrustedID := agentTestStore(t, settings)
	code, _, _ = agentTestCall(t, untrusted, "exec", "--host", untrustedID, "--command", "pwd")
	if code != 2 || attempts.Load() != 0 {
		t.Fatal("untrusted host reached authentication")
	}
	settings.Fingerprint = "SHA256:incorrect"
	changed, changedID := agentTestStore(t, settings)
	code, _, _ = agentTestCall(t, changed, "exec", "--host", changedID, "--command", "pwd")
	if code != 2 || attempts.Load() != 0 {
		t.Fatal("changed host key reached authentication")
	}
}

func TestAgentCLIFileRoundTripAndNoOverwrite(t *testing.T) {
	settings, fp, _ := testSSHServer(t)
	settings.Fingerprint = fp
	store, id := agentTestStore(t, settings)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	remote := filepath.Join(dir, "remote.txt")
	download := filepath.Join(dir, "download.txt")
	if err := os.WriteFile(source, []byte("agent file round trip"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"put", "--host", id, "--local", source, "--path", filepath.ToSlash(remote)},
		{"ls", "--host", id, "--path", filepath.ToSlash(dir)},
		{"get", "--host", id, "--path", filepath.ToSlash(remote), "--local", download},
	} {
		code, result, _ := agentTestCall(t, store, args...)
		if code != 0 || result["ok"] != true {
			t.Fatal(result)
		}
	}
	data, err := os.ReadFile(download)
	if err != nil || string(data) != "agent file round trip" {
		t.Fatal("roundtrip changed content", err)
	}
	if err := os.WriteFile(source, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := agentTestCall(t, store, "put", "--host", id, "--local", source, "--path", filepath.ToSlash(remote))
	if code != 2 {
		t.Fatal("upload overwrote existing target")
	}
	code, _, _ = agentTestCall(t, store, "get", "--host", id, "--path", filepath.ToSlash(remote), "--local", download)
	if code != 2 {
		t.Fatal("download overwrote existing target")
	}
	data, err = os.ReadFile(remote)
	if err != nil || string(data) != "agent file round trip" {
		t.Fatal("existing remote changed", err)
	}
}

func TestAgentCLIExecExitTimeoutAndOutputLimits(t *testing.T) {
	settings := agentExecServer(t)
	store, id := agentTestStore(t, settings)
	for _, tc := range []struct {
		command string
		code    int
		timeout string
	}{
		{"success", 0, "2s"}, {"failure", 7, "2s"}, {"large", 0, "5s"}, {"wait", 124, "50ms"},
	} {
		code, result, output := agentTestCall(t, store, "exec", "--host", id, "--command", tc.command, "--timeout", tc.timeout)
		if code != tc.code {
			t.Fatalf("%s: code %d, result %v", tc.command, code, result)
		}
		if strings.Contains(output, settings.Password) {
			t.Fatal("result exposed authentication password")
		}
		if tc.command == "success" && (result["stdout"] != "hello" || result["stderr"] != "warning") {
			t.Fatal(result)
		}
		if tc.command == "large" && (result["stdout_truncated"] != true || len(result["stdout"].(string)) != agentOutputLimit) {
			t.Fatal("output was not bounded")
		}
	}
}

func TestAgentCLIReusesBoundIdentity(t *testing.T) {
	settings := agentExecServer(t)
	store, _ := agentTestStore(t, settings)
	d := &desktop{}
	if err := d.loadProfiles(store); err != nil {
		t.Fatal(err)
	}
	if err := d.saveIdentity("", identityDraft{Name: "agent fixture", User: settings.User, Password: settings.Password}); err != nil {
		t.Fatal(err)
	}
	p := &d.profiles[0]
	p.IdentityID = d.identities[0].ID
	p.EncryptedCredentials = ""
	var err error
	p.IdentityBinding, err = sealIdentityBinding(p.Settings, p.IdentityID)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.saveProfiles(); err != nil {
		t.Fatal(err)
	}
	code, result, _ := agentTestCall(t, store, "exec", "--host", agentHostID(*p), "--command", "success")
	if code != 0 || result["stdout"] != "hello" {
		t.Fatal(result)
	}
}

func TestAgentCLIHelpDoesNotOpenGUIOrCreateStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), "unused", "profiles.json")
	code, result, _ := agentTestCall(t, store, "help")
	if code != 0 || result["output"] != "JSON" {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Dir(store)); !os.IsNotExist(err) {
		t.Fatal("help created a profile store")
	}
}

func agentExecServer(t *testing.T) connectionSettings {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if string(p) != "test-password" {
			return nil, ssh.ErrNoAuth
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			raw, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							var request struct{ Command string }
							if ssh.Unmarshal(req.Payload, &request) != nil {
								req.Reply(false, nil)
								continue
							}
							req.Reply(true, nil)
							status := uint32(0)
							switch request.Command {
							case "success":
								channel.Write([]byte("hello"))
								channel.Stderr().Write([]byte("warning"))
							case "failure":
								status = 7
							case "large":
								channel.Write(bytes.Repeat([]byte("x"), agentOutputLimit+100))
							case "wait":
								time.Sleep(250 * time.Millisecond)
							}
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	return connectionSettings{Host: host, Port: port, User: "agent", Password: "test-password", Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
}
