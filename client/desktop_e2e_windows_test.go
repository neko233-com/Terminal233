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
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket)
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
		if strings.Contains(fmt.Sprint(windows), "Terminal233") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(windows), "kind:content") {
		t.Fatalf("native window not found: %v", windows)
	}
	tree := fmt.Sprint(call("tree"))
	if !strings.Contains(tree, "主机地址") || !strings.Contains(tree, "SSH 密码") || !strings.Contains(tree, "连接列表（仅本次运行）") {
		t.Fatalf("missing native fields: %s", tree)
	}
	settings, fingerprint, attempts := testSSHServer(t)
	for key, value := range map[string]string{"host": settings.Host, "port": settings.Port, "user": settings.User, "password": settings.Password} {
		call("act", map[string]any{"key": key, "action": "setValue", "text": value})
	}
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
		t.Fatalf("desktop failed to disconnect")
	}
	call("act", map[string]any{"key": "local-terminal", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "本机终端已打开") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "本机终端已打开") {
		t.Fatal("local PowerShell terminal failed to start")
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
		t.Fatal("local terminal failed to close")
	}
}
