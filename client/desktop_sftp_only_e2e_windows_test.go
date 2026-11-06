//go:build desktope2e && windows

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDesktopSFTPOnly(t *testing.T) {
	var interactive atomic.Int32
	connections := make(chan func() error, 16)
	settings, fingerprint, attempts := testSSHServerWithExecPolicy(t, nil, nil, nil, nil, func(kind string) bool {
		if kind == "pty-req" || kind == "shell" {
			interactive.Add(1)
			return false
		}
		return true
	}, func(closeConnection func() error) { connections <- closeConnection })
	settings.Fingerprint = fingerprint
	dir := t.TempDir()
	var profiles desktop
	if err := profiles.loadProfiles(filepath.Join(dir, "NekoSSH", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	profiles.settings, profiles.active = settings, settings
	profiles.profileLabel = "SFTP only"
	if err := profiles.saveCurrentProfile(); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "nekossh-sftp-test.exe")
	build := exec.Command("go", "build", "-p", "1", "-tags", "e2e", "-o", exe, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("nekossh-sftp-only-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })
	app := exec.Command(exe)
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket, "APPDATA="+dir, "XDG_CONFIG_HOME="+dir)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Process.Kill(); _ = app.Wait() })
	var conn net.Conn
	var err error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	reader := bufio.NewReader(conn)
	call := func(method string, params map[string]any) map[string]any {
		t.Helper()
		request := map[string]any{"id": 1, "method": method}
		if params != nil {
			request["params"] = params
		}
		data, _ := json.Marshal(request)
		if _, err := conn.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(line, &result); err != nil {
			t.Fatal(err)
		}
		if result["error"] != nil {
			t.Fatalf("automation %s: %v", method, result["error"])
		}
		return result
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("windows", nil)), "kind:content") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("windows", nil)), "kind:content") {
		t.Fatal("native window did not open")
	}
	wait := func(key string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			result := call("tree", nil)["result"]
			if state, ok := result.(map[string]any); ok {
				for _, item := range state["nodes"].([]any) {
					node := item.(map[string]any)
					if node["key"] == key && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
						return node
					}
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("control unavailable: %s", key)
		return nil
	}
	press := func(key string) { t.Helper(); wait(key); call("act", map[string]any{"key": key, "action": "press"}) }
	set := func(key, value string) {
		t.Helper()
		wait(key)
		call("act", map[string]any{"key": key, "action": "setValue", "text": value})
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if fmt.Sprint(wait(key)["value"]) == value {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("input did not update: %s", key)
	}
	press("nav-files")
	press("file-session-picker-toggle")
	press("file-picker-profile-1")
	wait("session-1")
	wait("remote-dir")
	if interactive.Load() != 0 || attempts.Load() != 1 {
		t.Fatal("file connection requested a shell or reauthenticated unexpectedly")
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "SFTP ·") {
		t.Fatal("file session is not identified as SFTP")
	}
	rightDir, leftDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(leftDir, "example.txt"), []byte("safe test"), 0600); err != nil {
		t.Fatal(err)
	}
	set("remote-dir", filepath.ToSlash(rightDir))
	press("file-refresh")
	press("secondary-picker")
	press("secondary-profile-1")
	wait("session-2")
	wait("secondary-path")
	set("secondary-path", filepath.ToSlash(leftDir))
	press("secondary-refresh")
	press("secondary-file-example.txt")
	if capturePath := os.Getenv("NEKOSSH_TEST_SFTP_CAPTURE"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, err := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(capturePath, png, 0600); err != nil {
			t.Fatal(err)
		}
	}
	press("secondary-copy-right")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(rightDir, "example.txt"))
		if err == nil && string(data) == "safe test" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(rightDir, "example.txt"))
	if err != nil || string(data) != "safe test" {
		t.Fatal("SFTP-only pane copy failed", err)
	}
	if interactive.Load() != 0 {
		t.Fatal("file workflow requested a PTY or shell")
	}
	first, second := <-connections, <-connections
	_ = first()
	press("file-reconnect")
	wait("file-example.txt")
	_ = second()
	press("secondary-reconnect")
	wait("secondary-file-example.txt")
	if interactive.Load() != 0 {
		t.Fatal("SFTP reconnect requested a PTY or shell")
	}
	press("left-pane-open-terminal")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(fmt.Sprint(call("tree", nil)), "打开终端失败") {
		time.Sleep(30 * time.Millisecond)
	}
	if interactive.Load() != 1 || !strings.Contains(fmt.Sprint(call("tree", nil)), "打开终端失败") {
		t.Fatal("left shell refusal not reported")
	}
	press("file-open-terminal")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (interactive.Load() < 2 || !strings.Contains(fmt.Sprint(call("tree", nil)), "打开终端失败")) {
		time.Sleep(30 * time.Millisecond)
	}
	if interactive.Load() != 2 || !strings.Contains(fmt.Sprint(call("tree", nil)), "打开终端失败") {
		t.Fatal("shell refusal not reported")
	}
	press("file-refresh")
	wait("file-example.txt")
}
