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
	"testing"
	"time"
)

func TestDesktopRemotePanes(t *testing.T) {
	workspaceCommands := make(chan string, 8)
	a, af, _ := testSSHServerWithExec(t, nil, nil, nil, func(command string) { workspaceCommands <- command })
	a.Fingerprint = af
	b, bf, _ := testSSHServer(t)
	b.Fingerprint = bf
	b.Host = "localhost"
	c, cf, attempts := testSSHServer(t)
	bad := c
	bad.Fingerprint, bad.Password = cf, "incorrect-test-password"
	configDir := t.TempDir()
	multiItem, err := newQuickCommand("多终端验证", "echo NEKO_MULTI_TEST")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = updateQuickCommands(filepath.Join(configDir, "NekoSSH", "quick-commands.json"), func(items []quickCommand) ([]quickCommand, error) { return append(items, multiItem), nil }); err != nil {
		t.Fatal(err)
	}
	var profiles desktop
	if err := profiles.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	profiles.profileGroups = []string{"batch-target"}
	for i, settings := range []connectionSettings{a, b, c, bad, bad} {
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
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket, "APPDATA="+configDir, "XDG_CONFIG_HOME="+configDir, "NEKOSSH_E2E_NO_EDITOR=1")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Process.Kill(); _ = app.Wait() })
	var conn net.Conn
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
			t.Fatalf("automation %s %v: %v", method, params, result["error"])
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
		t.Fatalf("control unavailable: %s; tree: %v", key, call("tree", nil))
		return nil
	}
	press := func(key string) { t.Helper(); waitKey(key); call("act", map[string]any{"key": key, "action": "press"}) }
	waitGone := func(key string) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			found := false
			result := call("tree", nil)["result"].(map[string]any)
			for _, item := range result["nodes"].([]any) {
				if item.(map[string]any)["key"] == key {
					found = true
					break
				}
			}
			if !found {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("control did not disappear: %s", key)
	}
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
	visibleBounds := func(key string) map[string]any {
		t.Helper()
		waitKey(key)
		call("act", map[string]any{"key": key, "action": "scrollIntoView"})
		var previous string
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			node := waitKey(key)
			bounds := node["bounds"].(map[string]any)
			current := fmt.Sprint(bounds)
			if current == previous && !strings.Contains(fmt.Sprint(node["states"]), "offscreen") {
				return bounds
			}
			previous = current
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("element did not settle on screen: %s", key)
		return nil
	}
	connect := func(key, expected string) {
		node := waitKey(key)
		bounds := node["bounds"].(map[string]any)
		call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": bounds["x"].(float64) + bounds["width"].(float64)/2, "y": bounds["y"].(float64) + bounds["height"].(float64)/2}}})
		waitKey(expected)
	}
	connect("profile-2", "session-1")
	press("workspace-hosts-tab")
	connect("profile-1", "session-2")
	press("workspace-hosts-tab")
	press("nav-files")
	press("file-session-picker-toggle")
	press("file-picker-session-1")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(waitKey("file-session-picker-toggle")["description"]), "alice@localhost") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(waitKey("file-session-picker-toggle")["description"]), "alice@localhost") {
		t.Fatal("right session did not switch")
	}
	press("secondary-picker")
	press("secondary-session-2")
	waitKey("secondary-path")
	rightPicker := waitKey("file-session-picker-toggle")
	if !strings.Contains(fmt.Sprint(rightPicker["description"]), "alice@localhost") {
		t.Fatalf("connecting left host changed the right session: %v", rightPicker["description"])
	}
	leftDir, rightDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(leftDir, "left.txt"), []byte("left to right"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftDir, ".hidden-fixture"), []byte("hidden"), 0600); err != nil {
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
	waitGone("secondary-file-.hidden-fixture")
	press("left-pane-options")
	press("left-pane-toggle-dotfiles")
	waitKey("secondary-file-.hidden-fixture")
	press("left-pane-options")
	if capturePath := os.Getenv("NEKOSSH_TEST_CAPTURE"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, err := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(capturePath, png, 0600); err != nil {
			t.Fatal(err)
		}
	}
	press("left-pane-sort-size")
	press("left-pane-options")
	press("left-pane-toggle-dotfiles")
	waitGone("secondary-file-.hidden-fixture")
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
	waitSelected("secondary-file-batch-one.txt")
	controlClick := func(key string) {
		t.Helper()
		bounds := visibleBounds(key)
		call("input", map[string]any{"events": []any{map[string]any{"type": "click", "mods": []string{"ctrl"}, "x": bounds["x"].(float64) + bounds["width"].(float64)/2, "y": bounds["y"].(float64) + bounds["height"].(float64)/2}}})
	}
	controlClick("secondary-file-batch-conflict.txt")
	waitSelected("secondary-file-batch-conflict.txt")
	controlClick("secondary-file-batch-folder")
	waitSelected("secondary-file-batch-folder")
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
		source := visibleBounds(sourceKey)
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
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "打开输入的目录后才能传输") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "打开输入的目录后才能传输") {
		t.Fatal("local path change was not applied")
	}
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
	waitSelected("local-pane-file-local-to-right")
	waitKey("local-pane-open-selected")
	press("local-pane-upload")
	waitFile(filepath.Join(rightDir, "local-to-right", "nested", "data.txt"), "local to right pane")
	press("file-right-to-local-pane")
	waitSelected("file-right-to-local-pane")
	waitKey("file-open-selected")
	press("local-pane-download")
	waitFile(filepath.Join(localPaneDir, "right-to-local-pane", "nested", "data.txt"), "right to local pane")
	if err := os.WriteFile(filepath.Join(rightDir, "edit-roundtrip.txt"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	press("file-refresh")
	// An external editor receives a local copy; saving uploads after the
	// snapshot is stable and a conflicting remote change stops the monitor.
	editBounds := visibleBounds("file-edit-roundtrip.txt")
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": editBounds["x"].(float64) + editBounds["width"].(float64)/2, "y": editBounds["y"].(float64) + editBounds["height"].(float64)/2}}})
	editPath := fmt.Sprint(waitKey("external-edit-path-1")["value"])
	editDir := filepath.Dir(editPath)
	rel, relErr := filepath.Rel(os.TempDir(), editDir)
	if relErr != nil || filepath.Dir(rel) != "." || !strings.HasPrefix(rel, "nekossh-edit-") {
		t.Fatalf("unexpected test edit directory: %s", editDir)
	}
	t.Cleanup(func() { _ = os.Remove(editPath); _ = os.Remove(editDir) })
	if err := os.WriteFile(editPath, []byte("edited locally"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFile(filepath.Join(rightDir, "edit-roundtrip.txt"), "edited locally")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "已上传，继续监控") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "已上传，继续监控") {
		t.Fatal("external edit upload did not settle")
	}
	if err := os.WriteFile(filepath.Join(rightDir, "edit-roundtrip.txt"), []byte("other writer"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editPath, []byte("unsent edit"), 0600); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "远程文件已变化") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "远程文件已变化") {
		t.Fatal("external edit conflict did not stop monitoring")
	}
	waitFile(filepath.Join(rightDir, "edit-roundtrip.txt"), "other writer")
	waitFile(editPath, "unsent edit")
	press("external-edit-close-1")
	press("secondary-picker")
	press("secondary-session-2")
	waitKey("secondary-path")
	setPath("secondary-path", filepath.ToSlash(leftDir))
	press("secondary-refresh")
	waitKey("secondary-file-left.txt")
	if err := os.WriteFile(filepath.Join(leftDir, "left-edit.txt"), []byte("left initial"), 0600); err != nil {
		t.Fatal(err)
	}
	press("secondary-refresh")
	bounds := visibleBounds("secondary-file-left-edit.txt")
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": bounds["x"].(float64) + bounds["width"].(float64)/2, "y": bounds["y"].(float64) + bounds["height"].(float64)/2}}})
	leftEditPath := fmt.Sprint(waitKey("external-edit-path-2")["value"])
	leftEditDir := filepath.Dir(leftEditPath)
	leftRel, leftRelErr := filepath.Rel(os.TempDir(), leftEditDir)
	if leftRelErr != nil || filepath.Dir(leftRel) != "." || !strings.HasPrefix(leftRel, "nekossh-edit-") {
		t.Fatalf("unexpected left edit directory: %s", leftEditDir)
	}
	t.Cleanup(func() { _ = os.Remove(leftEditPath); _ = os.Remove(leftEditDir) })
	if err := os.WriteFile(leftEditPath, []byte("left edited"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFile(filepath.Join(leftDir, "left-edit.txt"), "left edited")
	press("external-edit-stop-2")
	waitKey("external-edit-close-2")
	press("external-edit-close-2")
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
	// Save and restore a workspace with two SSH connections and one local
	// terminal; the template must never contain their passwords.
	press("session-close-5")
	waitGone("session-close-5")
	press("session-close-4")
	waitGone("session-close-4")
	press("session-3")
	press("toggle-split")
	waitKey("split-focus-1")
	press("local-terminal")
	press("workspace-templates-toggle")
	setPath("workspace-template-name", "测试工作区")
	press("workspace-template-save")
	workspaceFile := filepath.Join(configDir, "NekoSSH", "workspaces.json")
	until = time.Now().Add(10 * time.Second)
	var saved workspaceTemplatesFile
	for time.Now().Before(until) {
		saved, err = readWorkspaceTemplates(workspaceFile)
		if err == nil && len(saved.Items) == 1 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil || len(saved.Items) != 1 || len(saved.Items[0].Entries) != 3 {
		t.Fatalf("workspace was not saved: %v; tree: %v", err, call("tree", nil))
	}
	if saved.Version != 3 || saved.Items[0].Entries[1].TerminalDir != "/fixture work" {
		t.Fatalf("remote terminal directory was not saved: %+v", saved.Items[0].Entries)
	}
	data, err := os.ReadFile(workspaceFile)
	if err != nil || strings.Contains(string(data), "test-password") {
		t.Fatal("workspace serialized a password")
	}
	id := saved.Items[0].ID
	press("session-close-3")
	waitGone("session-close-3")
	press("session-close-2")
	waitGone("session-close-2")
	press("session-close-1")
	waitGone("session-close-1")
	press("local-terminal")
	waitKey("workspace-open-" + id)
	press("workspace-open-" + id)
	waitKey("group-tab-1")
	select {
	case got := <-workspaceCommands:
		want, _ := remoteDirectoryCommand("/fixture work")
		if got != want {
			t.Fatalf("workspace restored wrong remote directory: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace did not restore the remote directory")
	}
	waitKey("group-focus-6")
	waitKey("group-focus-7")
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "已恢复工作区") {
		t.Fatal("restored workspace status missing")
	}
	press("toggle-split")
	waitKey("group-rail-1")
	waitKey("group-save-1")
	if capturePath := os.Getenv("NEKOSSH_TEST_WORKSPACE_CAPTURE"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, err := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(capturePath, png, 0600); err != nil {
			t.Fatal(err)
		}
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "key", "key": "Ctrl+S"}}})
	waitGone("group-save-1")
	updatedWorkspace, err := readWorkspaceTemplates(workspaceFile)
	if err != nil || len(updatedWorkspace.Items) != 1 || updatedWorkspace.Items[0].ID != id || updatedWorkspace.Items[0].ViewMode != "focus" {
		t.Fatal("saved workspace was not updated", err)
	}
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_FOCUS"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, decodeErr := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if writeErr := os.WriteFile(capturePath, png, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	press("group-focus-7")
	waitSelected("group-focus-7")
	press("group-add-1")
	press("group-add-local-1")
	waitSelected("group-focus-9")
	press("toggle-split")
	waitKey("group-grid-1")
	waitKey("group-focus-6")
	waitKey("group-focus-7")
	waitKey("group-focus-8")
	press("group-close-member-9")
	waitKey("group-tab-1")
	// Save the grouped grid, close it, and restore the version-2 template.
	press("local-terminal")
	setPath("workspace-template-name", "分屏工作区")
	press("workspace-template-save")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		saved, err = readWorkspaceTemplates(workspaceFile)
		if err == nil && len(saved.Items) == 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil || saved.Version != 3 || len(saved.Items) != 2 || saved.Items[1].ViewMode != "grid" || len(saved.Items[1].Entries) != 3 || len(saved.Items[1].ColumnPerc) != 12 {
		t.Fatal("grouped workspace layout was not saved")
	}
	groupTemplateID := saved.Items[1].ID
	press("group-close-1")
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(waitKey("group-close-1")["label"]), "确认关闭工作区") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(waitKey("group-close-1")["label"]), "确认关闭工作区") {
		t.Fatal("group closure was not confirmed")
	}
	press("group-close-1")
	waitGone("group-tab-1")
	waitKey("workspace-open-" + groupTemplateID)
	press("workspace-open-" + groupTemplateID)
	waitKey("group-tab-2")
	waitKey("group-grid-2")
	waitKey("group-focus-10")
	waitKey("group-focus-11")
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, decodeErr := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if writeErr := os.WriteFile(capturePath, png, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	drag("group-focus-10", "group-focus-11")
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		a := waitKey("group-focus-10")["bounds"].(map[string]any)
		b := waitKey("group-focus-11")["bounds"].(map[string]any)
		if b["x"].(float64) < a["x"].(float64) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	firstBounds := waitKey("group-focus-10")["bounds"].(map[string]any)
	secondBounds := waitKey("group-focus-11")["bounds"].(map[string]any)
	if secondBounds["x"].(float64) >= firstBounds["x"].(float64) {
		t.Fatal("workspace terminal drag did not reorder panes")
	}
	// Create another workspace from existing standalone sessions in the UI.
	press("local-terminal")
	press("new-tab-local")
	waitKey("session-13")
	press("local-terminal")
	press("new-tab-local")
	waitKey("session-14")
	// Dropping a terminal tab on another creates a split workspace.
	drag("session-14", "session-13")
	waitKey("group-tab-3")
	waitKey("group-grid-3")
	waitKey("group-focus-13")
	press("group-focus-13")
	waitSelected("group-focus-13")
	// Create a workspace directly from saved hosts and the local terminal.
	press("local-terminal")
	setPath("workspace-template-name", "主机工作区")
	press("workspace-hosts-toggle")
	press("new-tab-local")
	press("new-tab-profile-1")
	press("new-tab-profile-2")
	waitSelected("new-tab-local")
	waitSelected("new-tab-profile-1")
	waitSelected("new-tab-profile-2")
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_HOSTS"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, decodeErr := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if writeErr := os.WriteFile(capturePath, png, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	press("workspace-hosts-open")
	waitKey("group-tab-4")
	waitKey("group-focus-15")
	waitKey("group-focus-16")
	waitKey("group-focus-17")
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "已创建工作区") {
		t.Fatal("workspace was not created from selected hosts")
	}
	press("local-terminal")
	waitKey("workspace-open-" + id)
	if strings.Contains(fmt.Sprint(call("tree", nil)), "workspace-delete-"+id) {
		t.Fatal("workspace delete action should be hidden until management is opened")
	}
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_TEMPLATES"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, decodeErr := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if writeErr := os.WriteFile(capturePath, png, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	// Run a saved snippet on two explicitly selected SSH terminals.
	press("group-tab-4")
	press("quick-toggle")
	press("quick-select-" + multiItem.ID)
	press("quick-multi-" + multiItem.ID)
	waitKey("quick-multi-target-" + multiItem.ID + "-16")
	call("act", map[string]any{"key": "quick-multi-target-" + multiItem.ID + "-16", "action": "scrollIntoView"})
	press("quick-multi-target-" + multiItem.ID + "-16")
	call("act", map[string]any{"key": "quick-multi-target-" + multiItem.ID + "-17", "action": "scrollIntoView"})
	press("quick-multi-target-" + multiItem.ID + "-17")
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "选择目标 · 2") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "选择目标 · 2") {
		t.Fatal("multi-terminal selection did not settle")
	}
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_MULTI"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, decodeErr := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if writeErr := os.WriteFile(capturePath, png, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	press("quick-multi-send-" + multiItem.ID)
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "已向 2 个终端提交并回车") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "已向 2 个终端提交并回车") {
		t.Fatal("multi-terminal execution was not reported")
	}
	press("quick-toggle")
	waitGone("quick-filter")
	for _, session := range []string{"16", "17"} {
		press("group-focus-" + session)
		waitSelected("group-focus-" + session)
		press("terminal-search-toggle")
		setPath("terminal-search-input", "NEKO_MULTI_TEST")
		waitKey("terminal-search-next")
		press("terminal-search-close")
		waitGone("terminal-search-input")
	}
	// Real-time input in focus mode is sent to the other workspace terminals.
	press("toggle-broadcast")
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(waitKey("toggle-broadcast")["description"]), "关闭广播输入") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(waitKey("toggle-broadcast")["description"]), "关闭广播输入") {
		t.Fatal("broadcast did not enable")
	}
	press("group-broadcast-member-15")
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(waitKey("group-broadcast-member-15")["label"]), "加入广播") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(waitKey("group-broadcast-member-15")["label"]), "加入广播") {
		t.Fatal("local terminal was not excluded from broadcast")
	}
	result := call("tree", nil)["result"].(map[string]any)
	var terminalID string
	for _, entry := range result["nodes"].([]any) {
		node := entry.(map[string]any)
		if node["label"] == "终端：fixture-2" {
			terminalID, _ = node["id"].(string)
			break
		}
	}
	if terminalID == "" {
		t.Fatal("focused broadcast terminal not found")
	}
	call("act", map[string]any{"id": terminalID, "action": "focus"})
	call("input", map[string]any{"events": []any{map[string]any{"type": "text", "text": "echo NEKO_BROADCAST_TEST"}, map[string]any{"type": "key", "key": "Enter"}}})
	for _, session := range []string{"16", "17"} {
		press("group-focus-" + session)
		waitSelected("group-focus-" + session)
		press("terminal-search-toggle")
		setPath("terminal-search-input", "NEKO_BROADCAST_TEST")
		waitKey("terminal-search-next")
		press("terminal-search-close")
		waitGone("terminal-search-input")
	}
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_BROADCAST"); capturePath != "" {
		result := call("capture", nil)["result"].(map[string]any)
		png, decodeErr := base64.StdEncoding.DecodeString(fmt.Sprint(result["png"]))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if writeErr := os.WriteFile(capturePath, png, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	press("group-focus-15")
	waitSelected("group-focus-15")
	press("terminal-search-toggle")
	setPath("terminal-search-input", "NEKO_BROADCAST_TEST")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "没有匹配结果") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "没有匹配结果") {
		t.Fatal("excluded local terminal received the broadcast")
	}
	press("terminal-search-close")
	waitGone("terminal-search-input")
	press("toggle-broadcast")
	// Native Ctrl-click selection and dragging a selected host move the whole
	// selection in one persisted update.
	press("workspace-hosts-tab")
	controlClick("profile-1")
	controlClick("profile-2")
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "已勾选 2 个本机连接") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "已勾选 2 个本机连接") {
		t.Fatal("native host multi-selection failed")
	}
	drag("profile-1", "group-card-batch-target")
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		var saved desktop
		if saved.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")) == nil && len(saved.profiles) >= 2 && saved.profiles[0].Group == "batch-target" && saved.profiles[1].Group == "batch-target" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	var restoredProfiles desktop
	if err := restoredProfiles.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")); err != nil || len(restoredProfiles.profiles) < 2 || restoredProfiles.profiles[0].Group != "batch-target" || restoredProfiles.profiles[1].Group != "batch-target" {
		t.Fatal("native batch drag did not persist", err)
	}
	// A previously verified saved host can be dropped into an existing workspace.
	beforeDrop := attempts.Load()
	visibleBounds("group-tab-4")
	drag("profile-3", "group-tab-4")
	waitKey("group-focus-18")
	waitKey("group-grid-4")
	if attempts.Load() != beforeDrop+1 {
		t.Fatal("saved host was not authenticated exactly once")
	}
	// Authentication failure keeps the same target for an explicit retry.
	press("workspace-hosts-tab")
	visibleBounds("group-tab-4")
	drag("profile-5", "group-tab-4")
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !strings.Contains(fmt.Sprint(call("tree", nil)), "连接失败") {
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree", nil)), "连接失败") {
		t.Fatal("failed authentication was not reported")
	}
	waitKey("password")
	call("act", map[string]any{"key": "password", "action": "setValue", "text": "test-password"})
	press("connect")
	waitKey("group-focus-19")
}
