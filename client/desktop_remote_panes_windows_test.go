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
	b.Host = "localhost"
	c, cf, attempts := testSSHServer(t)
	bad := c
	bad.Fingerprint, bad.Password = cf, "incorrect-test-password"
	configDir := t.TempDir()
	var profiles desktop
	if err := profiles.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	for i, settings := range []connectionSettings{a, b, c, bad} {
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
	_ = conn.SetDeadline(time.Now().Add(120 * time.Second))
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
	setPath := func(key, value string) {
		t.Helper()
		waitKey(key)
		call("act", map[string]any{"key": key, "action": "setValue", "text": value})
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if strings.EqualFold(fmt.Sprint(waitKey(key)["value"]), value) {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("directory input %s did not update", key)
	}
	waitSelected := func(key string) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if strings.Contains(fmt.Sprint(waitKey(key)["states"]), "checked") {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("selection did not settle: %s: %v", key, waitKey(key))
	}
	connect := func(key, expected string) {
		node := waitKey(key)
		bounds := node["bounds"].(map[string]any)
		call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": bounds["x"].(float64) + bounds["width"].(float64)/2, "y": bounds["y"].(float64) + bounds["height"].(float64)/2}}})
		waitKey(expected)
	}
	connect("profile-2", "session-1")
	press("workspace-hosts-tab")
	press("nav-files")
	press("secondary-picker")
	waitKey("secondary-search")
	call("act", map[string]any{"key": "secondary-search", "action": "setValue", "text": "fixture-1"})
	press("secondary-profile-1")
	waitKey("session-2")
	waitKey("secondary-path")
	rightPicker := waitKey("file-session-picker-toggle")
	if !strings.Contains(fmt.Sprint(rightPicker["description"]), "alice@localhost") {
		t.Fatal("connecting left host changed the right session")
	}
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
	for _, item := range []struct{ root, name, text string }{
		{leftDir, "batch-one.txt", "batch one"}, {leftDir, "batch-conflict.txt", "new"},
		{leftDir, "left-action.txt", "remote edit"},
		{rightDir, "batch-conflict.txt", "existing"},
	} {
		if err := os.WriteFile(filepath.Join(item.root, item.name), []byte(item.text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ root, dir, text string }{
		{leftDir, "batch-folder", "batch folder"}, {leftDir, "left-folder", "left folder"}, {rightDir, "right-folder", "right folder"},
		{leftDir, "left-drag-folder", "left drag folder"}, {rightDir, "right-drag-folder", "right drag folder"},
		{leftDir, "left-local-folder", "remote to local folder"},
		{rightDir, "right-to-local-pane", "right to local pane"},
	} {
		if err := os.MkdirAll(filepath.Join(item.root, item.dir, "nested"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(item.root, item.dir, "nested", "data.txt"), []byte(item.text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	waitKey("secondary-path")
	setPath("secondary-path", filepath.ToSlash(leftDir))
	press("secondary-refresh")
	waitKey("secondary-file-left.txt")
	setPath("remote-dir", filepath.ToSlash(rightDir))
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
		t.Fatalf("remote copy did not finish: %s; tree: %v", filepath.Base(name), call("tree", nil))
	}
	waitFile(filepath.Join(rightDir, "left.txt"), "left to right")
	// Left-pane file management operates on its own SSH host and requires
	// explicit confirmation before deleting an item.
	press("secondary-file-left-action.txt")
	waitSelected("secondary-file-left-action.txt")
	press("left-pane-rename")
	setPath("left-action-name", "left-renamed.txt")
	press("left-action-confirm")
	waitFile(filepath.Join(leftDir, "left-renamed.txt"), "remote edit")
	press("secondary-file-left-renamed.txt")
	waitSelected("secondary-file-left-renamed.txt")
	press("left-pane-delete")
	if _, err := os.Stat(filepath.Join(leftDir, "left-renamed.txt")); err != nil {
		t.Fatal("left file disappeared before deletion confirmation")
	}
	press("left-action-confirm")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(filepath.Join(leftDir, "left-renamed.txt")); os.IsNotExist(err) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(leftDir, "left-renamed.txt")); !os.IsNotExist(err) {
		t.Fatal("left file was not deleted")
	}
	press("left-pane-mkdir")
	setPath("left-action-name", "left-created")
	press("left-action-confirm")
	waitKey("secondary-file-left-created")
	// Real pointer modifiers select a mixed batch in the native window. A
	// conflicting destination must remain untouched while other items copy.
	press("secondary-file-batch-one.txt")
	controlClick := func(key string) {
		t.Helper()
		waitKey(key)
		call("act", map[string]any{"key": key, "action": "scrollIntoView"})
		bounds := waitKey(key)["bounds"].(map[string]any)
		call("input", map[string]any{"events": []any{map[string]any{"type": "click", "mods": []string{"ctrl"}, "x": bounds["x"].(float64) + bounds["width"].(float64)/2, "y": bounds["y"].(float64) + bounds["height"].(float64)/2}}})
	}
	controlClick("secondary-file-batch-conflict.txt")
	controlClick("secondary-file-batch-folder")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "复制 3 项") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "复制 3 项") {
		t.Fatal("native Ctrl selection did not produce a three-item batch")
	}
	press("secondary-copy-right")
	waitFile(filepath.Join(rightDir, "batch-one.txt"), "batch one")
	waitFile(filepath.Join(rightDir, "batch-folder", "nested", "data.txt"), "batch folder")
	waitFile(filepath.Join(rightDir, "batch-conflict.txt"), "existing")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "成功 2，失败 1") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "成功 2，失败 1") {
		t.Fatal("native batch result did not report the conflict")
	}
	press("file-right.txt")
	press("secondary-copy-left")
	waitFile(filepath.Join(leftDir, "right.txt"), "right to left")
	drag := func(sourceKey, targetKey string) {
		t.Helper()
		waitKey(sourceKey)
		call("act", map[string]any{"key": sourceKey, "action": "scrollIntoView"})
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
	press("secondary-file-left-folder")
	press("secondary-copy-right")
	waitFile(filepath.Join(rightDir, "left-folder", "nested", "data.txt"), "left folder")
	press("file-right-folder")
	waitKey("file-open-selected")
	press("secondary-copy-left")
	waitFile(filepath.Join(leftDir, "right-folder", "nested", "data.txt"), "right folder")
	drag("secondary-file-left-drag-folder", "session-file-pane")
	waitFile(filepath.Join(rightDir, "left-drag-folder", "nested", "data.txt"), "left drag folder")
	drag("file-right-drag-folder", "local-file-pane")
	waitFile(filepath.Join(leftDir, "right-drag-folder", "nested", "data.txt"), "right drag folder")
	localPaneDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localPaneDir, "local-to-right", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localPaneDir, "local-to-right", "nested", "data.txt"), []byte("local to right pane"), 0600); err != nil {
		t.Fatal(err)
	}
	press("secondary-picker")
	press("secondary-local")
	waitKey("local-pane-path")
	if err := os.WriteFile(filepath.Join(localPaneDir, "local-action.txt"), []byte("local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	setPath("local-pane-path", localPaneDir)
	press("local-pane-open")
	waitKey("local-pane-file-local-to-right")
	press("local-pane-file-local-action.txt")
	waitSelected("local-pane-file-local-action.txt")
	press("left-pane-rename")
	setPath("left-action-name", "local-renamed.txt")
	press("left-action-confirm")
	waitFile(filepath.Join(localPaneDir, "local-renamed.txt"), "local edit")
	press("local-pane-file-local-renamed.txt")
	waitSelected("local-pane-file-local-renamed.txt")
	press("left-pane-delete")
	press("left-action-confirm")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(filepath.Join(localPaneDir, "local-renamed.txt")); os.IsNotExist(err) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(localPaneDir, "local-renamed.txt")); !os.IsNotExist(err) {
		t.Fatal("local file was not deleted")
	}
	press("local-pane-file-local-to-right")
	waitKey("local-pane-open-selected")
	press("local-pane-upload")
	waitFile(filepath.Join(rightDir, "local-to-right", "nested", "data.txt"), "local to right pane")
	press("file-right-to-local-pane")
	waitKey("file-open-selected")
	press("local-pane-download")
	waitFile(filepath.Join(localPaneDir, "right-to-local-pane", "nested", "data.txt"), "right to local pane")
	press("secondary-picker")
	press("secondary-session-2")
	waitKey("secondary-path")
	setPath("secondary-path", filepath.ToSlash(leftDir))
	press("secondary-refresh")
	waitKey("secondary-file-left.txt")
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
	if err := os.MkdirAll(filepath.Join(localDir, "local-folder", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "local-folder", "nested", "data.txt"), []byte("local folder"), 0600); err != nil {
		t.Fatal(err)
	}
	waitKey("remote-dir")
	setPath("remote-dir", localDir)
	press("file-refresh")
	waitKey("file-local-upload.txt")
	press("secondary-file-left.txt")
	press("secondary-copy-right")
	waitFile(filepath.Join(localDir, "left.txt"), "left to right")
	press("file-local-upload.txt")
	press("secondary-copy-left")
	waitFile(filepath.Join(leftDir, "local-upload.txt"), "local to remote")
	press("secondary-file-left-local-folder")
	press("secondary-copy-right")
	waitFile(filepath.Join(localDir, "left-local-folder", "nested", "data.txt"), "remote to local folder")
	press("file-local-folder")
	waitKey("file-open-selected")
	press("secondary-copy-left")
	waitFile(filepath.Join(leftDir, "local-folder", "nested", "data.txt"), "local folder")
	// A host without a locally trusted key must stop for verification, then
	// return to the left panel without replacing the existing right session.
	press("secondary-picker")
	waitKey("secondary-search")
	call("act", map[string]any{"key": "secondary-search", "action": "setValue", "text": "fixture-3"})
	press("secondary-profile-3")
	waitKey("connect")
	if attempts.Load() != 0 {
		t.Fatal("credentials were sent before host inspection")
	}
	press("connect")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if strings.Contains(fmt.Sprint(call("tree", nil)), cf) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), cf) || attempts.Load() != 0 {
		t.Fatal("new left host key was not verified before authentication")
	}
	press("connect")
	waitKey("session-4")
	waitKey("secondary-path")
	currentDir := waitKey("remote-dir")
	if !strings.EqualFold(fmt.Sprint(currentDir["value"]), localDir) {
		t.Fatal("new left host changed the right local directory")
	}
	// A failed authentication keeps the left-panel intent for a retry.
	press("secondary-picker")
	waitKey("secondary-search")
	call("act", map[string]any{"key": "secondary-search", "action": "setValue", "text": "fixture-4"})
	press("secondary-profile-4")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if strings.Contains(fmt.Sprint(call("tree", nil)), "连接失败") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "连接失败") {
		t.Fatal("failed left authentication did not remain in the editor")
	}
	call("act", map[string]any{"key": "password", "action": "setValue", "text": "test-password"})
	call("tree", nil)
	press("connect")
	waitKey("session-5")
	waitKey("secondary-path")
	currentDir = waitKey("remote-dir")
	if !strings.EqualFold(fmt.Sprint(currentDir["value"]), localDir) {
		t.Fatal("left retry changed the right directory")
	}
}
