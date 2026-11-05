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

func TestDesktopNativeWindow(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "terminal233-e2e.exe")
	build := exec.Command("go", "build", "-p", "1", "-tags", "e2e", "-o", exe, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("desktop build: %v %s", err, output)
	}
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("nekossh-test-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })
	app := exec.Command(exe)
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket, "APPDATA="+t.TempDir())
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
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("native automation not available: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(40 * time.Second))
	reader := bufio.NewReader(conn)
	call := func(method string, params ...map[string]any) map[string]any {
		t.Helper()
		request := map[string]any{"id": 1, "method": method}
		if len(params) > 0 {
			request["params"] = params[0]
		}
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var answer map[string]any
		if err = json.Unmarshal(line, &answer); err != nil {
			t.Fatal(err)
		}
		if answer["error"] != nil {
			t.Fatalf("automation %s: %v", method, answer["error"])
		}
		return answer
	}
	var windows map[string]any
	for time.Now().Before(deadline) {
		windows = call("windows")
		if strings.Contains(fmt.Sprint(windows), "NekoSSH") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(windows), "kind:content") {
		t.Fatalf("native window not found: %v", windows)
	}
	tree := fmt.Sprint(call("tree"))
	if !strings.Contains(tree, "连接列表") {
		t.Fatalf("missing native fields: %s", tree)
	}
	call("act", map[string]any{"key": "connection-new", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "主机地址") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "主机地址") || !strings.Contains(tree, "SSH 密码") {
		t.Fatalf("missing connection fields: %s", tree)
	}
	settings, fingerprint, attempts := testSSHServer(t)
	for key, value := range map[string]string{"host": settings.Host, "port": settings.Port, "user": settings.User, "password": settings.Password} {
		call("act", map[string]any{"key": key, "action": "setValue", "text": value})
	}
	call("act", map[string]any{"key": "profile-add", "action": "press"})
	call("act", map[string]any{"key": "inspect", "action": "press"})
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, fingerprint) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, fingerprint) {
		t.Fatalf("host fingerprint missing: %s", tree)
	}
	if attempts.Load() != 0 {
		t.Fatal("credentials sent during host inspection")
	}
	call("act", map[string]any{"key": "connect", "action": "press"})
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "已连接") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "已连接") {
		t.Fatalf("desktop failed to connect: %s", tree)
	}
	if attempts.Load() != 1 {
		t.Fatalf("authentication attempts: %d", attempts.Load())
	}
	// Let the initial SFTP directory read finish before opening another tab.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "local-terminal" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				deadline = time.Time{}
				break
			}
		}
		if deadline.IsZero() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !deadline.IsZero() {
		t.Fatal("initial file refresh did not finish")
	}
	call("act", map[string]any{"key": "nav-connections", "action": "press"})
	call("act", map[string]any{"key": "profile-local-terminal", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "本机终端已打开") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "本机终端已打开") {
		t.Fatalf("local PowerShell terminal failed to start: %s", tree)
	}
	call("act", map[string]any{"key": "nav-files", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "本机文件") && strings.Contains(tree, "file-filter") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "本机文件") {
		t.Fatalf("local file workspace missing: %s", tree)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "local-terminal" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				deadline = time.Time{}
				break
			}
		}
		if deadline.IsZero() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !deadline.IsZero() {
		t.Fatal("local directory read did not finish")
	}
	call("act", map[string]any{"key": "nav-terminal", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "toggle-file-panel") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "toggle-file-panel", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "收起文件面板") && strings.Contains(tree, "本机文件") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "收起文件面板") || !strings.Contains(tree, "本机文件") {
		t.Fatalf("terminal file panel missing: %s", tree)
	}
	call("act", map[string]any{"key": "toggle-file-panel", "action": "press"})
	call("act", map[string]any{"key": "local-terminal", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-1") && strings.Contains(tree, "session-2") && strings.Contains(tree, "session-3") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "session-1") || !strings.Contains(tree, "session-2") || !strings.Contains(tree, "session-3") {
		t.Fatalf("multiple terminal tabs missing: %s", tree)
	}
	call("act", map[string]any{"key": "session-2", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "当前会话：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "当前会话：本机终端 2") {
		t.Fatalf("failed to switch terminal tabs: %s", tree)
	}
	// Closing an inactive tab must not switch or close the active shell.
	call("act", map[string]any{"key": "session-close-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if !strings.Contains(tree, "session-1") && strings.Contains(tree, "session-2") && strings.Contains(tree, "已关闭会话：") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(tree, "session-1") || !strings.Contains(tree, "session-2") || !strings.Contains(tree, "session-3") {
		t.Fatalf("closing inactive SSH tab changed other sessions: %s", tree)
	}
	call("act", map[string]any{"key": "nav-connections", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "profile-search") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "profile-search") {
		t.Fatalf("connection list missing after navigation: %s", tree)
	}
	call("act", map[string]any{"key": "profile-search", "action": "setValue", "text": settings.Host})
	tree = fmt.Sprint(call("tree"))
	if !strings.Contains(tree, "profile-1") {
		t.Fatalf("saved connection missing from search: %s", tree)
	}
	call("act", map[string]any{"key": "profile-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "connect-trusted") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "connect-trusted") {
		t.Fatalf("trusted reconnect action missing: %s", tree)
	}
	call("act", map[string]any{"key": "password", "action": "setValue", "text": settings.Password})
	call("act", map[string]any{"key": "connect-trusted", "action": "press"})
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-4") && attempts.Load() == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "session-4") || attempts.Load() != 2 {
		t.Fatalf("trusted reconnect did not establish the SSH session: %s", tree)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "session-close-4" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				deadline = time.Time{}
				break
			}
		}
		if deadline.IsZero() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !deadline.IsZero() {
		t.Fatal("reconnected session did not become idle")
	}
	// Closing the new tab returns to a local session; restore tab 2 for the
	// active-tab close check below.
	call("act", map[string]any{"key": "session-close-4", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if !strings.Contains(tree, "session-4") && strings.Contains(tree, "已关闭会话；当前：") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(tree, "session-4") {
		t.Fatalf("reconnected tab did not close: %s", tree)
	}
	call("act", map[string]any{"key": "session-2", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "当前会话：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "当前会话：本机终端 2") {
		t.Fatalf("could not restore second tab: %s", tree)
	}
	call("act", map[string]any{"key": "disconnect", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "已关闭会话；当前：本机终端 3") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "已关闭会话；当前：本机终端 3") {
		t.Fatalf("closing one tab lost the remaining session: %s", tree)
	}
	call("act", map[string]any{"key": "disconnect", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "已断开") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "已断开") {
		t.Fatalf("local terminal failed to close: %s", tree)
	}
}
