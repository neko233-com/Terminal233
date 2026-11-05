//go:build desktope2e && windows

package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
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
	configDir := t.TempDir()
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket, "APPDATA="+configDir)
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
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "test-key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0600); err != nil {
		t.Fatal(err)
	}
	authorizedKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	var slowSFTP atomic.Bool
	settings, fingerprint, attempts := testSSHServerWithSlowSFTP(t, &slowSFTP, authorizedKey)
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
	// Exercise the native forwarding controls through the in-process SSH test server.
	forwardTarget, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer forwardTarget.Close()
	go func() {
		for {
			connection, acceptErr := forwardTarget.Accept()
			if acceptErr != nil {
				return
			}
			go func() { defer connection.Close(); _, _ = io.Copy(connection, connection) }()
		}
	}()
	_, targetPort, _ := net.SplitHostPort(forwardTarget.Addr().String())
	call("act", map[string]any{"key": "nav-forward", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "forward-port") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "forward-port") {
		t.Fatalf("forward controls missing: %s", tree)
	}
	call("act", map[string]any{"key": "forward-port", "action": "setValue", "text": targetPort})
	call("act", map[string]any{"key": "forward-start", "action": "press"})
	var address string
	forwardPattern := regexp.MustCompile(`已启动：(127\.0\.0\.1:[0-9]+)`)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if match := forwardPattern.FindStringSubmatch(tree); len(match) == 2 {
			address = match[1]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if address == "" {
		t.Fatalf("forward did not start: %s", tree)
	}
	forwardConn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = forwardConn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = forwardConn.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	forwardReply := make([]byte, 4)
	if _, err = io.ReadFull(forwardConn, forwardReply); err != nil || string(forwardReply) != "test" {
		t.Fatalf("forward relay failed: %q %v", forwardReply, err)
	}
	_ = forwardConn.Close()
	call("act", map[string]any{"key": "forward-stop-0", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "转发已停止") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "转发已停止") {
		t.Fatalf("forward did not stop: %s", tree)
	}
	if conn, dialErr := net.DialTimeout("tcp", address, time.Second); dialErr == nil {
		conn.Close()
		t.Fatal("stopped forward still listening")
	}
	call("act", map[string]any{"key": "nav-connections", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "profile-local-terminal") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "profile-local-terminal") {
		t.Fatalf("connection list did not load: %s", tree)
	}
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
	// Set a real PowerShell directory before enabling follow in the file view.
	followDir := filepath.Join(t.TempDir(), "目录 with spaces")
	if err := os.Mkdir(followDir, 0700); err != nil {
		t.Fatal(err)
	}
	focusTree := call("tree")["result"].(map[string]any)
	var localTerminalID string
	for _, value := range focusTree["nodes"].([]any) {
		node := value.(map[string]any)
		if node["label"] == "Terminal" {
			localTerminalID, _ = node["id"].(string)
			break
		}
	}
	if localTerminalID == "" {
		t.Fatalf("local terminal missing: %s", tree)
	}
	call("act", map[string]any{"id": localTerminalID, "action": "focus"})
	command := "Set-Location -LiteralPath '" + strings.ReplaceAll(followDir, "'", "''") + "'"
	call("input", map[string]any{"events": []any{map[string]any{"type": "text", "text": command}, map[string]any{"type": "key", "key": "Enter"}}})
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
	call("act", map[string]any{"key": "file-follow-terminal", "action": "press"})
	deadline = time.Now().Add(15 * time.Second)
	followed := false
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "remote-dir" && strings.EqualFold(fmt.Sprint(node["value"]), followDir) {
				followed = true
				break
			}
		}
		if followed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !followed {
		t.Fatal("file panel did not follow the real PowerShell working directory")
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "file-follow-terminal" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
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
		t.Fatal("followed directory did not finish loading")
	}
	call("act", map[string]any{"key": "file-follow-terminal", "action": "press"})
	// Exercise the native file workspace against an isolated directory.
	localDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDirectory, "original.txt"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDirectory, ".hidden"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}

	call("act", map[string]any{"key": "remote-dir", "action": "setValue", "text": localDirectory})
	call("act", map[string]any{"key": "file-refresh", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	localReady := false
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		ready := false
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "file-refresh" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				ready = true
				break
			}
		}
		if ready {
			localReady = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !localReady {
		t.Fatal("isolated local directory did not finish loading")
	}
	call("act", map[string]any{"key": "file-toggle-dotfiles", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "显示点文件") && !strings.Contains(tree, "file-.hidden") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "显示点文件") || strings.Contains(tree, "file-.hidden") {
		t.Fatalf("dotfile toggle failed: %s", tree)
	}
	call("act", map[string]any{"key": "file-sort-size", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "按大小 ↑") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "按大小 ↑") {
		t.Fatalf("file sort control failed: %s", tree)
	}
	call("act", map[string]any{"key": "file-original.txt", "action": "press"})
	call("act", map[string]any{"key": "rename-name", "action": "setValue", "text": "renamed.txt"})
	call("act", map[string]any{"key": "rename-selected", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(filepath.Join(localDirectory, "renamed.txt")); readErr == nil && string(data) == "content" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data, readErr := os.ReadFile(filepath.Join(localDirectory, "renamed.txt")); readErr != nil || string(data) != "content" {
		t.Fatal("native rename did not finish")
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "file-refresh" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
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
		t.Fatal("rename refresh did not finish")
	}
	call("act", map[string]any{"key": "new-directory-name", "action": "setValue", "text": "新目录"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "new-directory-create" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
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
		t.Fatal("directory create action did not become available")
	}
	call("act", map[string]any{"key": "new-directory-create", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, statErr := os.Stat(filepath.Join(localDirectory, "新目录")); statErr == nil && info.IsDir() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if info, statErr := os.Stat(filepath.Join(localDirectory, "新目录")); statErr != nil || !info.IsDir() {
		t.Fatal("native file workspace did not create the directory")
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "file-refresh" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
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
		t.Fatal("directory refresh after creation did not finish")
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
	panelTree := call("tree")["result"].(map[string]any)
	listVisible := false
	for _, value := range panelTree["nodes"].([]any) {
		node := value.(map[string]any)
		if node["key"] == "file-list" {
			bounds := node["bounds"].(map[string]any)
			listVisible = bounds["y"].(float64) < 700 && bounds["height"].(float64) <= 220
		}
	}
	if !listVisible {
		t.Fatalf("compact file list was not visible and bounded: %s", tree)
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
	// Run a deliberately slowed, isolated SFTP upload while switching to a
	// different terminal, then cancel it from that other session.
	transferDir := t.TempDir()
	transferSource := filepath.Join(t.TempDir(), "background.bin")
	if err = os.WriteFile(transferSource, make([]byte, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	call("act", map[string]any{"key": "session-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "当前会话：alice@") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "当前会话：alice@") {
		t.Fatalf("could not activate SSH tab for transfer: %s", tree)
	}
	call("act", map[string]any{"key": "nav-files", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "key:remote-dir") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "remote-dir", "action": "setValue", "text": filepath.ToSlash(transferDir)})
	call("act", map[string]any{"key": "local-file", "action": "setValue", "text": transferSource})
	slowSFTP.Store(true)
	call("act", map[string]any{"key": "upload", "action": "press"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "cancel-transfer") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "cancel-transfer") {
		t.Fatalf("slow transfer did not start: %s", tree)
	}
	call("act", map[string]any{"key": "nav-terminal", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
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
		t.Fatalf("could not switch during transfer: %s", tree)
	}
	call("act", map[string]any{"key": "nav-files", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "取消后台传输") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "取消后台传输") {
		t.Fatalf("background cancel action missing: %s", tree)
	}
	call("act", map[string]any{"key": "cancel-transfer", "action": "press"})
	deadline = time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if !strings.Contains(tree, "cancel-transfer") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	slowSFTP.Store(false)
	if strings.Contains(tree, "cancel-transfer") {
		t.Fatalf("background cancellation did not finish: %s", tree)
	}
	if _, err = os.Stat(filepath.Join(transferDir, "background.bin")); !os.IsNotExist(err) {
		t.Fatalf("background cancellation left an output file: %v", err)
	}
	call("act", map[string]any{"key": "nav-terminal", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	sshTabReady := false
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		ready := false
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "session-1" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				ready = true
				break
			}
		}
		if ready {
			sshTabReady = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sshTabReady {
		t.Fatal("SSH tab stayed unavailable after cancelling the transfer")
	}
	call("act", map[string]any{"key": "session-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "当前会话：alice@") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "当前会话：alice@") {
		t.Fatal("could not return to the SSH tab after cancelling the transfer")
	}
	call("act", map[string]any{"key": "nav-files", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "传输已取消") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "传输已取消") {
		t.Fatalf("cancelled result was not kept on the original session: %s", tree)
	}
	call("act", map[string]any{"key": "nav-terminal", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
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
	// Both independently running terminals must remain visible and switchable.
	call("act", map[string]any{"key": "toggle-split", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "split-focus-2") && strings.Contains(tree, "split-focus-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "split-focus-2") || !strings.Contains(tree, "split-focus-1") {
		t.Fatalf("split terminal panes missing: %s", tree)
	}
	// Focusing the inactive terminal must also change the current session.
	result := call("tree")["result"].(map[string]any)
	var terminalID string
	for _, value := range result["nodes"].([]any) {
		node := value.(map[string]any)
		if node["label"] == "终端："+settings.User+"@"+settings.Host {
			terminalID, _ = node["id"].(string)
			break
		}
	}
	if terminalID == "" {
		t.Fatalf("inactive terminal not exposed: %s", tree)
	}
	call("act", map[string]any{"id": terminalID, "action": "focus"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "切换到：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "切换到：本机终端 2") {
		t.Fatalf("terminal focus did not select its session: %s", tree)
	}
	result = call("tree")["result"].(map[string]any)
	var bounds map[string]any
	for _, value := range result["nodes"].([]any) {
		node := value.(map[string]any)
		if node["label"] == "终端：本机终端 2" {
			bounds, _ = node["bounds"].(map[string]any)
			break
		}
	}
	if bounds == nil {
		t.Fatalf("local terminal bounds missing: %s", tree)
	}
	x := bounds["x"].(float64) + bounds["width"].(float64)/2
	y := bounds["y"].(float64) + bounds["height"].(float64)/2
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "x": x, "y": y}, map[string]any{"type": "pointerMove", "x": 1, "y": 1}}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "当前会话：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "当前会话：本机终端 2") {
		t.Fatalf("clicking terminal did not select it: %s", tree)
	}
	call("act", map[string]any{"key": "split-focus-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "切换到：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "切换到：本机终端 2") {
		t.Fatalf("split pane switch failed: %s", tree)
	}
	call("act", map[string]any{"key": "toggle-split", "action": "press"})
	call("act", map[string]any{"key": "session-2", "action": "press"})
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
	call("act", map[string]any{"key": "profile-edit-1", "action": "press"})
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
	// Save a key path on the already trusted profile, then connect from the
	// list. The test server accepts only the temporary public key above.
	call("act", map[string]any{"key": "nav-connections", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "profile-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "profile-1") {
		t.Fatalf("profile missing before key setup: %s", tree)
	}
	call("act", map[string]any{"key": "profile-edit-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "connect-trusted") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "key", "action": "setValue", "text": keyPath})
	call("act", map[string]any{"key": "profile-add", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "连接已保存") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "连接已保存") {
		t.Fatalf("key profile was not saved: %s", tree)
	}
	call("act", map[string]any{"key": "nav-connections", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "profile-connect-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "profile-connect-1") {
		t.Fatalf("profile connect action missing: %s", tree)
	}
	call("act", map[string]any{"key": "profile-connect-1", "action": "press"})
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-5") && attempts.Load() == 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "session-5") || attempts.Load() != 3 {
		t.Fatalf("key profile did not connect from the list: %s", tree)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "session-close-5" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
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
		t.Fatal("key session did not become idle")
	}
	call("act", map[string]any{"key": "session-close-5", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if !strings.Contains(tree, "session-5") && strings.Contains(tree, "已关闭会话；当前：") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(tree, "session-5") {
		t.Fatalf("key session did not close: %s", tree)
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
	call("act", map[string]any{"key": "font-larger", "action": "press"})
	call("act", map[string]any{"key": "theme-toggle", "action": "press"})
	call("act", map[string]any{"key": "toggle-sidebar", "action": "press"})
	var prefs workspacePreferences
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		prefs, err = readPreferences(filepath.Join(configDir, "NekoSSH", "preferences.json"))
		if err == nil && prefs.Light && prefs.SidebarCollapsed && prefs.TerminalFontSize == 14 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || !prefs.Light || !prefs.SidebarCollapsed || prefs.TerminalFontSize != 14 {
		t.Fatalf("native appearance controls did not persist: %+v, %v", prefs, err)
	}
}
