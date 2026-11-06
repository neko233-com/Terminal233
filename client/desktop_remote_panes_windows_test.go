//go:build desktope2e && windows

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopRemotePanes(t *testing.T) {
	a, af, _ := testSSHServer(t)
	a.Fingerprint = af
	b, bf, _ := testSSHServer(t)
	b.Fingerprint = bf
	configDir := t.TempDir()
	var profiles desktop
	if err := profiles.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	for i, settings := range []connectionSettings{a, b} {
		profiles.settings, profiles.active = settings, settings
		profiles.selectedProfile, profiles.profileLabel = "", fmt.Sprintf("fixture-%d", i+1)
		if err := profiles.saveCurrentProfile(); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(t.TempDir(), "terminal233-remote-panes.exe")
	build := exec.Command("go", "build", "-p", "1", "-tags", "e2e", "-o", exe, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("desktop build: %v %s", err, out)
	}
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("nekossh-remote-panes-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })
	app := exec.Command(exe)
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket, "APPDATA="+configDir, "XDG_CONFIG_HOME="+configDir)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Process.Kill(); _ = app.Wait() })
	var conn net.Conn
	var err error
	until := time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		conn, err = net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("native automation: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
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
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if strings.Contains(fmt.Sprint(call("windows", nil)), "kind:content") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("windows", nil)), "kind:content") {
		t.Fatal("native window did not open")
	}
	waitKey := func(key string) map[string]any {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			result := call("tree", nil)["result"].(map[string]any)
			for _, item := range result["nodes"].([]any) {
				node := item.(map[string]any)
				if node["key"] == key && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
					return node
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("control unavailable: %s", key)
		return nil
	}
	press := func(key string) { waitKey(key); call("act", map[string]any{"key": key, "action": "press"}) }
	connect := func(key, expected string) {
		node := waitKey(key)
		bounds := node["bounds"].(map[string]any)
		call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": bounds["x"].(float64) + bounds["width"].(float64)/2, "y": bounds["y"].(float64) + bounds["height"].(float64)/2}}})
		waitKey(expected)
	}
	connect("profile-1", "session-1")
	press("workspace-hosts-tab")
	connect("profile-2", "session-2")
	press("workspace-hosts-tab")
	press("nav-files")
	press("secondary-picker")
	press("secondary-session-1")
	leftDir, rightDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(leftDir, "left.txt"), []byte("left to right"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rightDir, "right.txt"), []byte("right to left"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftDir, "left-drag.txt"), []byte("drag left to right"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rightDir, "right-drag.txt"), []byte("drag right to left"), 0600); err != nil {
		t.Fatal(err)
	}
	waitKey("secondary-path")
	call("act", map[string]any{"key": "secondary-path", "action": "setValue", "text": filepath.ToSlash(leftDir)})
	press("secondary-refresh")
	waitKey("secondary-file-left.txt")
	call("act", map[string]any{"key": "remote-dir", "action": "setValue", "text": filepath.ToSlash(rightDir)})
	press("file-refresh")
	waitKey("file-right.txt")
	press("secondary-file-left.txt")
	press("secondary-copy-right")
	waitFile := func(name, contents string) {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if data, err := os.ReadFile(name); err == nil && string(data) == contents {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("remote copy did not finish: %s", filepath.Base(name))
	}
	waitFile(filepath.Join(rightDir, "left.txt"), "left to right")
	press("file-right.txt")
	press("secondary-copy-left")
	waitFile(filepath.Join(leftDir, "right.txt"), "right to left")
	drag := func(sourceKey, targetKey string) {
		t.Helper()
		source := waitKey(sourceKey)["bounds"].(map[string]any)
		target := waitKey(targetKey)["bounds"].(map[string]any)
		sx, sy := source["x"].(float64)+source["width"].(float64)/2, source["y"].(float64)+source["height"].(float64)/2
		tx, ty := target["x"].(float64)+target["width"].(float64)/2, target["y"].(float64)+18
		call("input", map[string]any{"events": []any{map[string]any{"type": "pointerDown", "x": sx, "y": sy}}})
		call("input", map[string]any{"events": []any{map[string]any{"type": "pointerMove", "x": tx, "y": ty}}})
		call("tree", nil)
		call("input", map[string]any{"events": []any{map[string]any{"type": "pointerUp", "x": tx, "y": ty}}})
	}
	drag("secondary-file-left-drag.txt", "session-file-pane")
	waitFile(filepath.Join(rightDir, "left-drag.txt"), "drag left to right")
	drag("file-right-drag.txt", "local-file-pane")
	waitFile(filepath.Join(leftDir, "right-drag.txt"), "drag right to left")
	// The right pane may also be a local session while the left stays remote.
	press("local-terminal")
	press("new-tab-local")
	waitKey("session-3")
	press("workspace-hosts-tab")
	press("nav-files")
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "local-upload.txt"), []byte("local to remote"), 0600); err != nil {
		t.Fatal(err)
	}
	waitKey("remote-dir")
	call("act", map[string]any{"key": "remote-dir", "action": "setValue", "text": localDir})
	press("file-refresh")
	waitKey("file-local-upload.txt")
	press("secondary-file-left.txt")
	press("secondary-copy-right")
	waitFile(filepath.Join(localDir, "left.txt"), "left to right")
	press("file-local-upload.txt")
	press("secondary-copy-left")
	waitFile(filepath.Join(leftDir, "local-upload.txt"), "local to remote")
}
