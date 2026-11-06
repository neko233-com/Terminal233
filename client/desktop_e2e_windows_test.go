//go:build desktope2e && windows

package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket, "APPDATA="+configDir, "XDG_CONFIG_HOME="+configDir)
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
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
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
	navigate := func(key string) {
		t.Helper()
		if !strings.Contains(fmt.Sprint(call("tree")), "key:"+key+" ") {
			call("act", map[string]any{"key": "toggle-sidebar", "action": "press"})
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(fmt.Sprint(call("tree")), "key:"+key+" ") {
				call("act", map[string]any{"key": key, "action": "press"})
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("navigation control %s did not appear", key)
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
	if !strings.Contains(tree, "key:host-entry-address ") {
		t.Fatal("fresh native workspace should show the centered host entry")
	}
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_HOST_ENTRY"); capturePath != "" {
		capture := call("capture")["result"].(map[string]any)
		pixels, err := base64.StdEncoding.DecodeString(fmt.Sprint(capture["png"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(capturePath, pixels, 0600); err != nil {
			t.Fatal(err)
		}
	}
	call("act", map[string]any{"key": "host-entry-address", "action": "setValue", "text": "onboarding.example.invalid"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ready := false
		for _, entry := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
			node := entry.(map[string]any)
			if node["key"] == "host-entry-continue" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "host-entry-continue", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "key:host ") && strings.Contains(tree, "onboarding.example.invalid") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "key:host ") || !strings.Contains(tree, "onboarding.example.invalid") {
		t.Fatalf("host entry did not open its native editor: %s", tree)
	}
	// The prefilled address is an unfinished draft and must require an explicit
	// second close before it can be discarded.
	call("act", map[string]any{"key": "connection-back", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "再次点击返回可放弃修改") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "再次点击返回可放弃修改") {
		t.Fatal("host entry discarded its incomplete draft without confirmation")
	}
	call("act", map[string]any{"key": "connection-back", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "key:host-entry-address ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "key:host-entry-address ") {
		t.Fatal("host entry did not return after closing the draft")
	}
	if !strings.Contains(tree, "key:workspace-rail-compact ") {
		t.Fatal("fresh installs should show the text navigation")
	}
	call("act", map[string]any{"key": "workspace-rail-compact", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "key:workspace-rail-expand ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "key:workspace-rail-expand ") {
		t.Fatal("compact icon navigation is missing")
	}
	call("act", map[string]any{"key": "workspace-rail-expand", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:workspace-rail-compact ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:workspace-rail-compact ") {
		t.Fatal("text navigation did not open")
	}
	call("act", map[string]any{"key": "workspace-rail-compact", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:workspace-rail-expand ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:workspace-rail-expand ") {
		t.Fatal("icon navigation did not return")
	}
	call("act", map[string]any{"key": "nav-snippets", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "quick-new") && strings.Contains(tree, "命令目标：未选择终端") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "quick-new") || !strings.Contains(tree, "命令目标：未选择终端") {
		t.Fatalf("snippet workspace not available before connecting: %s", tree)
	}
	call("act", map[string]any{"key": "quick-packages", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "quick-package-name") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "quick-package-name", "action": "setValue", "text": "测试分组"})
	call("act", map[string]any{"key": "quick-package-add", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "分组 / 测试分组") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "分组 / 测试分组") {
		t.Fatal("snippet group was not created")
	}
	call("act", map[string]any{"key": "quick-new", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "quick-cancel-edit") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "quick-cancel-edit") {
		t.Fatal("snippet editor did not open before connecting")
	}
	call("act", map[string]any{"key": "quick-name", "action": "setValue", "text": "临时测试"})
	call("act", map[string]any{"key": "quick-text", "action": "setValue", "text": "pwd"})
	call("act", map[string]any{"key": "quick-save", "action": "press"})
	var snippetID string
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if key, ok := node["key"].(string); ok && strings.HasPrefix(key, "snippet-select-") {
				snippetID = strings.TrimPrefix(key, "snippet-select-")
				break
			}
		}
		if snippetID != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snippetID == "" {
		t.Fatal("saved snippet did not appear")
	}
	var packageKey string
	for _, value := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
		node := value.(map[string]any)
		if key, ok := node["key"].(string); ok && strings.HasPrefix(key, "quick-package-card-") && key != "quick-package-card-ungrouped" {
			packageKey = key
			break
		}
	}
	if packageKey == "" {
		t.Fatal("snippet group card missing")
	}
	call("act", map[string]any{"key": "quick-batch-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "snippet-check-"+snippetID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "snippet-check-" + snippetID, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "已选择 1 个片段") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "已选择 1 个片段") {
		t.Fatal("native batch selection was not reflected in the UI")
	}
	call("act", map[string]any{"key": "quick-move-selected", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "已移动 1 个片段") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "已移动 1 个片段") {
		t.Fatal("native batch move failed")
	}
	call("act", map[string]any{"key": "quick-package-card-ungrouped", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "snippet-check-"+snippetID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "quick-batch-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "snippet-select-"+snippetID) && !strings.Contains(tree, "snippet-check-"+snippetID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "snippet-select-"+snippetID) || strings.Contains(tree, "snippet-check-"+snippetID) {
		t.Fatal("snippet selection did not exit batch mode")
	}
	call("act", map[string]any{"key": "snippet-select-" + snippetID, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "snippet-delete-"+snippetID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "snippet-delete-"+snippetID) {
		t.Fatal("selected snippet actions did not appear")
	}
	snippetTree := call("tree")["result"].(map[string]any)
	insertDisabled := false
	for _, value := range snippetTree["nodes"].([]any) {
		node := value.(map[string]any)
		if node["key"] == "snippet-insert-"+snippetID && strings.Contains(fmt.Sprint(node["states"]), "disabled") {
			insertDisabled = true
		}
	}
	if !insertDisabled {
		t.Fatal("snippet insertion was enabled without a terminal")
	}
	call("act", map[string]any{"key": "snippet-delete-" + snippetID, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "确认删除") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "snippet-delete-" + snippetID, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(fmt.Sprint(call("tree")), "snippet-select-"+snippetID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(fmt.Sprint(call("tree")), "snippet-select-"+snippetID) {
		t.Fatal("confirmed snippet deletion did not complete")
	}
	call("act", map[string]any{"key": packageKey, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "分组 / 测试分组") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "quick-packages", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "quick-package-delete") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "quick-package-delete", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "确认移除分组") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "quick-package-delete", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "分组已移除") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "分组已移除") {
		t.Fatal("confirmed snippet group removal did not complete")
	}
	navigate("nav-cloud")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "cloud-invite-entry") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "cloud-invite-entry") {
		t.Fatal("invitation entry missing at sign-in")
	}
	call("act", map[string]any{"key": "cloud-invite-entry", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "team-register") && strings.Contains(tree, "team-invite-input") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "team-register") || !strings.Contains(tree, "team-invite-input") {
		t.Fatal("invitation registration form did not open")
	}
	navigate("nav-files")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "files-open-connections") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	navigate("nav-connections")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "connection-new") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "connection-new") {
		t.Fatal("connection workspace did not reopen")
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
	focusedHost := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "host" && node["id"] == result["focus"] {
				focusedHost = true
				break
			}
		}
		if focusedHost {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !focusedHost {
		t.Fatal("new host address did not receive native focus")
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
	var saved []byte
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, err = os.ReadFile(filepath.Join(configDir, "NekoSSH", "profiles.json"))
		if err == nil && strings.Contains(string(saved), settings.Host) && strings.Contains(string(saved), settings.User) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || !strings.Contains(string(saved), settings.Host) || !strings.Contains(string(saved), settings.User) || strings.Contains(string(saved), settings.Password) {
		t.Fatal("native editor did not autosave metadata safely")
	}
	var savedProfiles diskProfiles
	if json.Unmarshal(saved, &savedProfiles) != nil || savedProfiles.Version != 3 || len(savedProfiles.Items) != 1 {
		t.Fatal("native editor did not protect its saved credential")
	}
	password, _, secretErr := unprotectProfileCredentials(savedProfiles.Items[0].EncryptedCredentials, settings)
	if secretErr != nil || password != settings.Password {
		t.Fatal("native editor credential cannot be restored")
	}
	call("act", map[string]any{"key": "host-layout-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "key:host-list-header-local ") && strings.Contains(tree, "切换为网格视图") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "key:host-list-header-local ") {
		t.Fatalf("native host list layout did not appear: %s", tree)
	}
	listRowWidth := float64(0)
	for _, entry := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
		node := entry.(map[string]any)
		if node["key"] == "profile-1" {
			listRowWidth = node["bounds"].(map[string]any)["width"].(float64)
		}
	}
	if listRowWidth < 600 {
		t.Fatalf("native host row did not fill the list: %v", listRowWidth)
	}
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_HOST_LIST"); capturePath != "" {
		capture := call("capture")["result"].(map[string]any)
		pixels, err := base64.StdEncoding.DecodeString(fmt.Sprint(capture["png"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(capturePath, pixels, 0600); err != nil {
			t.Fatal(err)
		}
	}
	call("act", map[string]any{"key": "host-layout-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if !strings.Contains(tree, "key:host-list-header-local ") && strings.Contains(tree, "切换为列表视图") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(tree, "key:host-list-header-local ") {
		t.Fatal("native grid layout did not return")
	}
	if capturePath := os.Getenv("NEKOSSH_E2E_CAPTURE_WORKSPACE"); capturePath != "" {
		capture := call("capture")["result"].(map[string]any)
		pixels, err := base64.StdEncoding.DecodeString(fmt.Sprint(capture["png"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(capturePath, pixels, 0600); err != nil {
			t.Fatal(err)
		}
	}
	call("act", map[string]any{"key": "profile-tags", "action": "setValue", "text": "production, database"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, err = os.ReadFile(filepath.Join(configDir, "NekoSSH", "profiles.json"))
		if err == nil && json.Unmarshal(saved, &savedProfiles) == nil && savedProfiles.Version == 4 && len(savedProfiles.Items[0].Tags) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || savedProfiles.Version != 4 || len(savedProfiles.Items[0].Tags) != 2 || strings.Contains(string(saved), settings.Password) {
		t.Fatal("native tag editor did not save safely")
	}
	call("act", map[string]any{"key": "connection-options", "action": "press"})
	call("act", map[string]any{"key": "profile-add", "action": "press"})
	// The primary action inspects an unknown host before it can authenticate.
	call("act", map[string]any{"key": "connect", "action": "press"})
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
		if strings.Contains(fmt.Sprint(call("tree")), "session-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	navigate("nav-files")
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "所属会话：alice@") && strings.Contains(tree, "remote-dir") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "所属会话：alice@") || !strings.Contains(tree, "remote-dir") {
		t.Fatalf("file workspace did not open after connecting: %s", tree)
	}
	if attempts.Load() != 1 {
		t.Fatalf("authentication attempts: %d", attempts.Load())
	}
	navigate("nav-terminal")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "toggle-split") {
			break
		}
		time.Sleep(50 * time.Millisecond)
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
	call("act", map[string]any{"key": "monitor-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "5.0 / 8.0 GiB") && strings.Contains(tree, "根磁盘 3.0 / 10.0 GiB") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "5.0 / 8.0 GiB") || !strings.Contains(tree, "1.25") || !strings.Contains(tree, "根磁盘 3.0 / 10.0 GiB") {
		t.Fatalf("SSH monitor did not display test data: %s", tree)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "CPU 50.0%") && strings.Contains(tree, "KiB/s") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "CPU 50.0%") || !strings.Contains(tree, "KiB/s") {
		t.Fatalf("SSH monitor rates did not update: %s", tree)
	}
	navigate("nav-connections")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "profile-search") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "profile-search") {
		t.Fatal("could not leave terminal workspace")
	}
	navigate("nav-terminal")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "monitor-toggle") && strings.Contains(tree, "服务器状态") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "服务器状态") || strings.Contains(tree, "根磁盘 3.0 / 10.0 GiB") {
		t.Fatal("monitor did not stop when leaving terminal")
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
	navigate("nav-forward")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		ready, open := false, false
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "forward-rule-name" {
				open = true
			}
			if node["key"] == "forward-rule-new" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				ready = true
			}
		}
		if open {
			break
		}
		if ready {
			call("act", map[string]any{"key": "forward-rule-new", "action": "press"})
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:forward-rule-name ") {
		t.Fatal("forward rule editor did not open")
	}
	call("act", map[string]any{"key": "forward-rule-name", "action": "setValue", "text": "测试转发"})
	call("act", map[string]any{"key": "forward-rule-port", "action": "setValue", "text": targetPort})
	call("act", map[string]any{"key": "forward-rule-save", "action": "press"})
	var savedRule savedForwardRule
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		file, readErr := readForwardRules(filepath.Join(configDir, "NekoSSH", "forward-rules.json"))
		if readErr == nil && len(file.Items) == 1 {
			savedRule = file.Items[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if savedRule.ID == "" {
		t.Fatal("native forwarding rule was not saved")
	}
	call("act", map[string]any{"key": "forward-rule-toggle-" + savedRule.ID, "action": "press"})
	var ruleAddress string
	rulePattern := regexp.MustCompile(`已启动：(127\.0\.0\.1:[0-9]+)`)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if match := rulePattern.FindStringSubmatch(tree); len(match) == 2 {
			ruleAddress = match[1]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ruleAddress == "" {
		t.Fatalf("saved rule did not start: %s", tree)
	}
	storedConn, err := net.DialTimeout("tcp", ruleAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = storedConn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = storedConn.Write([]byte("rule"))
	storedReply := make([]byte, 4)
	if _, err = io.ReadFull(storedConn, storedReply); err != nil || string(storedReply) != "rule" {
		t.Fatalf("saved rule relay: %q %v", storedReply, err)
	}
	_ = storedConn.Close()
	call("act", map[string]any{"key": "forward-rule-toggle-" + savedRule.ID, "action": "press"})
	call("act", map[string]any{"key": "forward-rules-reload", "action": "press"})
	if !strings.Contains(fmt.Sprint(call("tree")), "测试转发") {
		t.Fatal("saved rule was not present after reload")
	}
	var ruleX, ruleY float64
	for _, value := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
		node := value.(map[string]any)
		if node["label"] == savedRule.Name {
			bounds := node["bounds"].(map[string]any)
			ruleX = bounds["x"].(float64) + bounds["width"].(float64)/2
			ruleY = bounds["y"].(float64) + bounds["height"].(float64)/2
		}
	}
	if ruleX == 0 || ruleY == 0 {
		t.Fatal("saved rule row missing")
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": ruleX, "y": ruleY}}})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "运行中 · 127.0.0.1:") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "运行中 · 127.0.0.1:") {
		t.Fatal("double-click did not start saved rule")
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": ruleX, "y": ruleY}}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "已停止") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "已停止") {
		t.Fatal("double-click did not stop saved rule")
	}
	call("act", map[string]any{"key": "forward-rule-delete-" + savedRule.ID, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "确认删除") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "forward-rule-delete-" + savedRule.ID, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		file, readErr := readForwardRules(filepath.Join(configDir, "NekoSSH", "forward-rules.json"))
		if readErr == nil && len(file.Items) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if file, readErr := readForwardRules(filepath.Join(configDir, "NekoSSH", "forward-rules.json")); readErr != nil || len(file.Items) != 0 {
		t.Fatal("saved rule was not deleted")
	}
	call("act", map[string]any{"key": "forward-temporary-toggle", "action": "press"})
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
		if strings.Contains(tree, "转发已停止") && !strings.Contains(tree, "key:forward-stop-0 ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "转发已停止") || strings.Contains(tree, "key:forward-stop-0 ") {
		t.Fatalf("forward did not stop: %s", tree)
	}
	if conn, dialErr := net.DialTimeout("tcp", address, time.Second); dialErr == nil {
		conn.Close()
		t.Fatal("stopped forward still listening")
	}
	call("act", map[string]any{"key": "forward-mode-dynamic", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "本机 SOCKS5 代理") && !strings.Contains(tree, "key:forward-host ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "本机 SOCKS5 代理") || strings.Contains(tree, "key:forward-host ") {
		t.Fatalf("dynamic forward form: %s", tree)
	}
	call("act", map[string]any{"key": "forward-start", "action": "press"})
	address = ""
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
		t.Fatalf("dynamic forward did not start: %s", tree)
	}
	proxyConn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = proxyConn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = proxyConn.Write([]byte{5, 1, 0})
	method := make([]byte, 2)
	if _, err = io.ReadFull(proxyConn, method); err != nil || method[1] != 0 {
		t.Fatalf("SOCKS handshake: %v %v", method, err)
	}
	portNumber, err := strconv.Atoi(targetPort)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = proxyConn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, byte(portNumber >> 8), byte(portNumber)})
	proxyReply := make([]byte, 10)
	if _, err = io.ReadFull(proxyConn, proxyReply); err != nil || proxyReply[1] != 0 {
		t.Fatalf("SOCKS connect: %v %v", proxyReply, err)
	}
	_, _ = proxyConn.Write([]byte("test"))
	if _, err = io.ReadFull(proxyConn, forwardReply); err != nil || string(forwardReply) != "test" {
		t.Fatalf("SOCKS relay: %q %v", forwardReply, err)
	}
	_ = proxyConn.Close()
	call("act", map[string]any{"key": "forward-stop-0", "action": "press"})
	call("act", map[string]any{"key": "forward-mode-remote", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "远程监听端口") && strings.Contains(tree, "本机可访问的目标主机") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "远程监听端口") || !strings.Contains(tree, "本机可访问的目标主机") {
		t.Fatalf("remote forward form: %s", tree)
	}
	// Simulate a dropped SSH shell and reconnect the same tab.
	navigate("nav-terminal")
	deadline = time.Now().Add(5 * time.Second)
	var sshTerminalID string
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["label"] == "Terminal" {
				sshTerminalID, _ = node["id"].(string)
				break
			}
		}
		if sshTerminalID != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sshTerminalID == "" {
		t.Fatal("SSH terminal not found")
	}
	call("act", map[string]any{"id": sshTerminalID, "action": "focus"})
	call("input", map[string]any{"events": []any{map[string]any{"type": "text", "text": "__NEKOSSH_TEST_DISCONNECT__"}}})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-reconnect") && strings.Contains(tree, "SSH 会话已断开") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "session-reconnect") || !strings.Contains(tree, "SSH 会话已断开") {
		t.Fatalf("disconnect was not visible: %s", tree)
	}
	previousAttemptCount := attempts.Load()
	call("act", map[string]any{"key": "session-reconnect", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "已重新连接") && attempts.Load() == previousAttemptCount+1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "已重新连接") || attempts.Load() != previousAttemptCount+1 {
		t.Fatalf("reconnect failed: %s", tree)
	}
	navigate("nav-connections")
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
	call("input", map[string]any{"events": []any{map[string]any{"type": "text", "text": "Write-Output 'NekoFindMarker98765'"}, map[string]any{"type": "key", "key": "Enter"}}})
	call("act", map[string]any{"key": "terminal-search-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "terminal-search-input") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "terminal-search-input", "action": "setValue", "text": "NekoFindMarker98765"})
	searchCount := regexp.MustCompile(`^[1-9][0-9]*/[1-9][0-9]*$`)
	searchFound := false
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if searchCount.MatchString(fmt.Sprint(node["label"])) {
				searchFound = true
				break
			}
		}
		if searchFound {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !searchFound {
		t.Fatalf("native terminal search did not find output: %v", call("tree"))
	}
	call("act", map[string]any{"key": "terminal-search-next", "action": "press"})
	call("act", map[string]any{"key": "terminal-search-previous", "action": "press"})
	call("act", map[string]any{"key": "terminal-search-close", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(fmt.Sprint(call("tree")), "terminal-search-input") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "quick-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	localTerminalID = ""
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "quick-new") && strings.Contains(tree, "命令目标：本机终端 2（会话 2）") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "命令目标：本机终端 2（会话 2）") {
		t.Fatal("quick command target not visible")
	}
	call("act", map[string]any{"key": "quick-new", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "quick-name") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	commandResult := filepath.Join(t.TempDir(), "quick-command-result.txt")
	quickPath := strings.ReplaceAll(commandResult, "'", "''")
	quickText := "Set-Content -LiteralPath '" + quickPath + "' -Value 'quick-command-ok'\nAdd-Content -LiteralPath '" + quickPath + "' -Value 'quick-command-line-two'"
	call("act", map[string]any{"key": "quick-name", "action": "setValue", "text": "验证命令"})
	call("act", map[string]any{"key": "quick-text", "action": "setValue", "text": quickText})
	call("act", map[string]any{"key": "quick-save", "action": "press"})
	var quickRunKey string
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if key, ok := node["key"].(string); ok && strings.HasPrefix(key, "quick-run-") {
				quickRunKey = key
				break
			}
		}
		if quickRunKey != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if quickRunKey == "" {
		t.Fatalf("saved quick command missing: %v", call("tree"))
	}
	call("act", map[string]any{"key": "quick-filter", "action": "setValue", "text": "not-a-match"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "没有匹配的快速命令") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "没有匹配的快速命令") {
		t.Fatal("quick command search did not filter the native list")
	}
	call("act", map[string]any{"key": "quick-filter", "action": "setValue", "text": "验证"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), quickRunKey) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": quickRunKey, "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(commandResult); err == nil && strings.Contains(string(data), "quick-command-ok") && strings.Contains(string(data), "quick-command-line-two") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data, err := os.ReadFile(commandResult); err != nil || !strings.Contains(string(data), "quick-command-ok") || !strings.Contains(string(data), "quick-command-line-two") {
		t.Fatal("quick command did not execute in the selected local terminal")
	}
	call("act", map[string]any{"key": "quick-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		if strings.Contains(fmt.Sprint(result), "terminal-search-input") || strings.Contains(fmt.Sprint(result), "quick-side-scroll") || strings.Contains(fmt.Sprint(result), "quick-compact-scroll") {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["label"] == "Terminal" {
				localTerminalID, _ = node["id"].(string)
			}
		}
		if localTerminalID != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if localTerminalID == "" {
		t.Fatalf("terminal not visible after closing quick commands: %v", call("tree"))
	}
	call("act", map[string]any{"id": localTerminalID, "action": "focus"})
	command := "Set-Location -LiteralPath '" + strings.ReplaceAll(followDir, "'", "''") + "'"
	call("input", map[string]any{"events": []any{map[string]any{"type": "text", "text": command}, map[string]any{"type": "key", "key": "Enter"}}})
	navigate("nav-files")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "本机文件") && strings.Contains(tree, "file-tools-toggle") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "本机文件") {
		t.Fatalf("local file workspace missing: %s", tree)
	}
	// The picker can be outside the viewport even while the tools are open.
	// Read the toggle's state instead of treating a missing picker as closed.
	deadline = time.Now().Add(5 * time.Second)
	var nextFileToggle time.Time
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "pick-batch-upload") {
			break
		}
		if !strings.Contains(tree, "收起文件操作") && time.Now().After(nextFileToggle) {
			call("act", map[string]any{"key": "file-tools-toggle", "action": "press"})
			nextFileToggle = time.Now().Add(300 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "pick-batch-upload") || !strings.Contains(tree, "选择多文件并复制到此") {
		t.Fatal("native batch picker entry missing")
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
	call("act", map[string]any{"key": "file-options-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "file-toggle-dotfiles") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	tree = fmt.Sprint(call("tree"))
	if !strings.Contains(tree, "显示点文件") || strings.Contains(tree, "file-.hidden") {
		t.Fatalf("dotfiles should initially be hidden: %s", tree)
	}
	call("act", map[string]any{"key": "file-toggle-dotfiles", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "隐藏点文件") && strings.Contains(tree, "file-.hidden") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "隐藏点文件") || !strings.Contains(tree, "file-.hidden") {
		t.Fatalf("dotfile show failed: %s", tree)
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
	deadline = time.Now().Add(5 * time.Second)
	selectedReady := false
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "rename-name" && strings.Contains(fmt.Sprint(node), "original.txt") {
				selectedReady = true
				break
			}
		}
		if selectedReady {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !selectedReady {
		t.Fatal("file selection did not populate rename field")
	}
	call("act", map[string]any{"key": "rename-name", "action": "setValue", "text": "renamed.txt"})
	deadline = time.Now().Add(5 * time.Second)
	renameReady := false
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "rename-name" && strings.Contains(fmt.Sprint(node), "renamed.txt") {
				renameReady = true
				break
			}
		}
		if renameReady {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !renameReady {
		t.Fatal("rename field did not update")
	}
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
	// Navigate through the actual native controls, including a failed read.
	childDirectory := filepath.Join(localDirectory, "新目录")
	if err := os.WriteFile(filepath.Join(childDirectory, "history-marker.txt"), []byte("child"), 0600); err != nil {
		t.Fatal(err)
	}
	waitDirectory := func(dir, marker string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			result := call("tree")["result"].(map[string]any)
			matches, ready, found := false, false, false
			for _, value := range result["nodes"].([]any) {
				node := value.(map[string]any)
				if node["key"] == "remote-dir" && strings.EqualFold(fmt.Sprint(node["value"]), dir) {
					matches = true
				}
				if node["key"] == "file-refresh" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
					ready = true
				}
				if node["key"] == "file-"+marker {
					found = true
				}
			}
			if matches && ready && found {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("directory navigation did not load %q with %q: %v", dir, marker, call("tree"))
	}
	call("act", map[string]any{"key": "file-新目录", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "file-open-selected") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "file-open-selected", "action": "press"})
	waitDirectory(childDirectory, "history-marker.txt")
	call("act", map[string]any{"key": "file-back", "action": "press"})
	waitDirectory(localDirectory, "renamed.txt")
	resultForDirectory := call("tree")["result"].(map[string]any)
	var directoryBounds map[string]any
	for _, value := range resultForDirectory["nodes"].([]any) {
		node := value.(map[string]any)
		if node["key"] == "file-新目录" {
			directoryBounds, _ = node["bounds"].(map[string]any)
			break
		}
	}
	if directoryBounds == nil {
		t.Fatal("directory row missing before double click")
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": directoryBounds["x"].(float64) + directoryBounds["width"].(float64)/2, "y": directoryBounds["y"].(float64) + directoryBounds["height"].(float64)/2}}})
	waitDirectory(childDirectory, "history-marker.txt")
	call("act", map[string]any{"key": "file-back", "action": "press"})
	waitDirectory(localDirectory, "renamed.txt")
	call("act", map[string]any{"key": "file-forward", "action": "press"})
	waitDirectory(childDirectory, "history-marker.txt")
	call("act", map[string]any{"key": "remote-dir", "action": "setValue", "text": filepath.Join(localDirectory, "does-not-exist")})
	call("act", map[string]any{"key": "file-refresh", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "读取文件失败") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Fatal("failed directory read was not reported")
	}
	call("act", map[string]any{"key": "file-back", "action": "press"})
	waitDirectory(localDirectory, "renamed.txt")
	navigate("nav-terminal")
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
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "new-tab-search") && strings.Contains(tree, "new-tab-local") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "new-tab-search") || !strings.Contains(tree, "new-tab-local") {
		t.Fatal("new tab search did not open")
	}
	call("act", map[string]any{"key": "new-tab-search", "action": "setValue", "text": "alice"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "new-tab-session-1") && !strings.Contains(tree, "new-tab-local") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "new-tab-session-1") || strings.Contains(tree, "new-tab-local") {
		t.Fatal("new tab did not filter sessions")
	}
	selectionExposed := false
	for _, value := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
		node := value.(map[string]any)
		if node["key"] == "new-tab-session-1" && strings.Contains(fmt.Sprint(node["states"]), "checked") {
			selectionExposed = true
		}
	}
	if !selectionExposed {
		t.Fatal("highlighted search result was not exposed to accessibility")
	}
	call("act", map[string]any{"key": "new-tab-search", "action": "focus"})
	call("input", map[string]any{"events": []any{map[string]any{"type": "key", "key": "Enter"}}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "当前会话：alice@") && strings.Contains(tree, "new-tab-session ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "当前会话：alice@") || !strings.Contains(tree, "new-tab-session ") {
		t.Fatal("keyboard selection did not switch to the matching session")
	}
	call("act", map[string]any{"key": "new-tab-session", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "new-tab-search") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "new-tab-search", "action": "setValue", "text": ""})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "new-tab-local") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "new-tab-local", "action": "press"})
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
	navigate("nav-files")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "key:remote-dir") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "remote-dir", "action": "setValue", "text": filepath.ToSlash(transferDir)})
	for _, name := range []string{"batch-one.txt", "batch-existing.txt"} {
		if err := os.WriteFile(filepath.Join(transferDir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	batchTarget := t.TempDir()
	if err := os.WriteFile(filepath.Join(batchTarget, "batch-existing.txt"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	call("act", map[string]any{"key": "file-refresh", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "file-batch-one.txt") && strings.Contains(tree, "file-batch-existing.txt") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "file-batch-one.txt") || !strings.Contains(tree, "file-batch-existing.txt") {
		t.Fatalf("batch SFTP source files did not appear: %s", tree)
	}
	call("act", map[string]any{"key": "file-select-visible", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "多选 2/100") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "多选 2/100") {
		t.Fatal("native file selection did not select both files")
	}
	call("act", map[string]any{"key": "batch-target", "action": "setValue", "text": batchTarget})
	call("act", map[string]any{"key": "batch-download", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "批量传输完成：成功 1，失败 1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "批量传输完成：成功 1，失败 1") {
		t.Fatalf("native SFTP batch download did not finish: %s", tree)
	}
	if data, err := os.ReadFile(filepath.Join(batchTarget, "batch-one.txt")); err != nil || string(data) != "batch-one.txt" {
		t.Fatal("native SFTP batch download did not copy the new file")
	}
	if data, err := os.ReadFile(filepath.Join(batchTarget, "batch-existing.txt")); err != nil || string(data) != "protected" {
		t.Fatal("native SFTP batch download overwrote the existing file")
	}
	// Exercise the two independent panes with isolated local and SFTP paths.
	paneDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(paneDir, "pane-upload.txt"), []byte("pane upload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paneDir, "pane-drag.txt"), []byte("pane drag"), 0600); err != nil {
		t.Fatal(err)
	}
	call("act", map[string]any{"key": "local-pane-path", "action": "setValue", "text": paneDir})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		found := false
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "local-pane-path" && strings.EqualFold(fmt.Sprint(node["value"]), paneDir) {
				found = true
				break
			}
		}
		// The control value can update before its OnChange callback. Wait for
		// the callback too, otherwise it can cancel the just-started read.
		if found && strings.Contains(fmt.Sprint(result), "打开输入的目录后才能传输") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !time.Now().Before(deadline) {
		t.Fatal("local transfer path change was not applied")
	}
	call("act", map[string]any{"key": "local-pane-open", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "local-pane-file-pane-upload.txt") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "local-pane-file-pane-upload.txt") {
		t.Fatalf("local transfer pane did not load the isolated directory: %s", tree)
	}
	waitPaneAction := func(key string) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			result := call("tree")["result"].(map[string]any)
			for _, value := range result["nodes"].([]any) {
				node := value.(map[string]any)
				if node["key"] == key && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("dual-pane action %s did not become available", key)
	}
	call("act", map[string]any{"key": "local-pane-file-pane-upload.txt", "action": "press"})
	waitPaneAction("local-pane-upload")
	call("act", map[string]any{"key": "local-pane-upload", "action": "press"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(filepath.Join(transferDir, "pane-upload.txt")); readErr == nil && string(data) == "pane upload" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data, readErr := os.ReadFile(filepath.Join(transferDir, "pane-upload.txt")); readErr != nil || string(data) != "pane upload" {
		t.Fatal("dual-pane upload failed")
	}
	waitPaneAction("local-pane-file-pane-drag.txt")
	waitPaneAction("file-refresh")
	waitDragEndpoints := func(sourceKey, targetKey string) (map[string]any, map[string]any) {
		t.Helper()
		call("act", map[string]any{"key": sourceKey, "action": "scrollIntoView"})
		var previous string
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			tree := call("tree")["result"].(map[string]any)
			var source, target map[string]any
			offscreen := true
			for _, value := range tree["nodes"].([]any) {
				node := value.(map[string]any)
				if node["key"] == sourceKey {
					source, _ = node["bounds"].(map[string]any)
					offscreen = strings.Contains(fmt.Sprint(node["states"]), "offscreen")
				}
				if node["key"] == targetKey {
					target, _ = node["bounds"].(map[string]any)
				}
			}
			if source != nil && target != nil && !offscreen {
				current := fmt.Sprint(source, target)
				if current == previous {
					return source, target
				}
				previous = current
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("dual-pane drag endpoints did not settle: %s, %s", sourceKey, targetKey)
		return nil, nil
	}
	sourceBounds, targetBounds := waitDragEndpoints("local-pane-file-pane-drag.txt", "session-file-pane")
	sx := sourceBounds["x"].(float64) + sourceBounds["width"].(float64)/2
	sy := sourceBounds["y"].(float64) + sourceBounds["height"].(float64)/2
	tx := targetBounds["x"].(float64) + targetBounds["width"].(float64)/2
	ty := targetBounds["y"].(float64) + 120
	call("input", map[string]any{"events": []any{map[string]any{"type": "pointerDown", "x": sx, "y": sy}}})
	call("input", map[string]any{"events": []any{map[string]any{"type": "pointerMove", "x": tx, "y": ty}}})
	call("tree")

	call("input", map[string]any{"events": []any{map[string]any{"type": "pointerUp", "x": tx, "y": ty}}})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(filepath.Join(transferDir, "pane-drag.txt")); readErr == nil && string(data) == "pane drag" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data, readErr := os.ReadFile(filepath.Join(transferDir, "pane-drag.txt")); readErr != nil || string(data) != "pane drag" {
		t.Fatalf("dual-pane drag upload failed: %s", fmt.Sprint(call("tree")))
	}
	waitPaneAction("file-batch-existing.txt")
	sourceBounds, targetBounds = waitDragEndpoints("file-batch-existing.txt", "local-file-pane")
	sx = sourceBounds["x"].(float64) + sourceBounds["width"].(float64)/2
	sy = sourceBounds["y"].(float64) + sourceBounds["height"].(float64)/2
	tx = targetBounds["x"].(float64) + targetBounds["width"].(float64)/2
	ty = targetBounds["y"].(float64) + 120
	call("input", map[string]any{"events": []any{map[string]any{"type": "pointerDown", "x": sx, "y": sy}}})
	call("input", map[string]any{"events": []any{map[string]any{"type": "pointerMove", "x": tx, "y": ty}}})
	call("tree")
	call("input", map[string]any{"events": []any{map[string]any{"type": "pointerUp", "x": tx, "y": ty}}})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(filepath.Join(paneDir, "batch-existing.txt")); readErr == nil && string(data) == "batch-existing.txt" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data, readErr := os.ReadFile(filepath.Join(paneDir, "batch-existing.txt")); readErr != nil || string(data) != "batch-existing.txt" {
		t.Fatal("dual-pane drag download failed")
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		ready := false
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "file-batch-one.txt" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "file-batch-one.txt", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	selectedPaneFile := false
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "rename-name" && fmt.Sprint(node["value"]) == "batch-one.txt" {
				selectedPaneFile = true
				break
			}
		}
		if selectedPaneFile {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !selectedPaneFile {
		t.Fatal("dual-pane remote selection did not update")
	}
	waitPaneAction("local-pane-download")
	call("act", map[string]any{"key": "local-pane-download", "action": "press"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(filepath.Join(paneDir, "batch-one.txt")); readErr == nil && string(data) == "batch-one.txt" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data, readErr := os.ReadFile(filepath.Join(paneDir, "batch-one.txt")); readErr != nil || string(data) != "batch-one.txt" {
		result := call("tree")["result"].(map[string]any)
		var statuses []string
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			label := fmt.Sprint(node["label"])
			if strings.Contains(label, "传输") || strings.Contains(label, "文件") || strings.Contains(label, "目录") {
				statuses = append(statuses, label)
			}
		}
		t.Fatalf("dual-pane download failed: read=%v contents=%q status=%v", readErr, data, statuses)
	}
	waitPaneAction("local-file")
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
	call("act", map[string]any{"key": "file-session-picker-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "file-picker-session-2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "file-picker-session-2", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "所属会话：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "所属会话：本机终端 2") {
		t.Fatalf("could not switch during transfer: %s", tree)
	}
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
	navigate("nav-terminal")
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
	navigate("nav-files")
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
	navigate("nav-terminal")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "toggle-split") {
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
	call("input", map[string]any{"events": []any{map[string]any{"type": "key", "key": "Ctrl+Tab"}}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "当前会话：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "当前会话：本机终端 2") {
		t.Fatalf("next session shortcut failed: %s", tree)
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "key", "key": "Ctrl+Shift+Tab"}}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "切换到：本机终端 2") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "切换到：本机终端 2") {
		t.Fatalf("previous session shortcut failed: %s", tree)
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
	navigate("nav-connections")
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
	call("act", map[string]any{"key": "profile-search", "action": "setValue", "text": "production"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "profile-1") && !strings.Contains(tree, "profile-local-terminal") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "profile-1") || strings.Contains(tree, "profile-local-terminal") {
		t.Fatalf("tagged connection missing from search: %s", tree)
	}
	call("act", map[string]any{"key": "profile-search", "action": "setValue", "text": settings.Host})
	profileBoundsForClick := func() map[string]any {
		t.Helper()
		call("act", map[string]any{"key": "profile-1", "action": "scrollIntoView"})
		var previous string
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			result := call("tree")["result"].(map[string]any)
			for _, value := range result["nodes"].([]any) {
				node := value.(map[string]any)
				if node["key"] != "profile-1" || strings.Contains(fmt.Sprint(node["states"]), "offscreen") {
					continue
				}
				bounds, ok := node["bounds"].(map[string]any)
				if !ok {
					continue
				}
				current := fmt.Sprint(bounds)
				if current == previous {
					return bounds
				}
				previous = current
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("saved connection did not settle on screen")
		return nil
	}
	profileBounds := profileBoundsForClick()
	previousAttemptCount = attempts.Load()
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": profileBounds["x"].(float64) + profileBounds["width"].(float64)/2, "y": profileBounds["y"].(float64) + profileBounds["height"].(float64)/2}}})
	// A double-click uses the protected password and the previously verified
	// host key without asking for the password again.
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-4") && attempts.Load() == previousAttemptCount+1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "session-4") || attempts.Load() != previousAttemptCount+1 {
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
	navigate("nav-connections")
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
	call("act", map[string]any{"key": "profile-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "connection-back") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "使用已信任指纹连接") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Switching immediately after an edit must save before replacing the form.
	call("act", map[string]any{"key": "profile-label", "action": "setValue", "text": "native-edited-host"})
	call("act", map[string]any{"key": "profile-group", "action": "setValue", "text": "fixture-group"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "profile-label" && fmt.Sprint(node["value"]) == "native-edited-host" {
				deadline = time.Time{}
				break
			}
		}
		if deadline.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !deadline.IsZero() {
		t.Fatal("native host edit was not applied")
	}
	call("act", map[string]any{"key": "connection-new", "action": "press"})
	newEditor := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "host" && (node["value"] == nil || fmt.Sprint(node["value"]) == "") {
				newEditor = true
				break
			}
		}
		if newEditor {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, readErr := os.ReadFile(filepath.Join(configDir, "NekoSSH", "profiles.json"))
	if !newEditor || readErr != nil || !strings.Contains(string(data), "native-edited-host") {
		t.Fatal("native host navigation did not flush the pending edit")
	}
	call("act", map[string]any{"key": "profile-1", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "使用已信任指纹连接") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "key-options-toggle", "action": "scrollIntoView"})
	call("act", map[string]any{"key": "key-options-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:pick-key") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "key", "action": "setValue", "text": keyPath})
	call("act", map[string]any{"key": "connection-options", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:profile-add") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:profile-add") {
		t.Fatal("host save action did not become available")
	}
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
	navigate("nav-connections")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "profile-1") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "profile-1") {
		t.Fatalf("profile missing after editing: %s", tree)
	}
	profileBounds = profileBoundsForClick()
	previousAttemptCount = attempts.Load()
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": profileBounds["x"].(float64) + profileBounds["width"].(float64)/2, "y": profileBounds["y"].(float64) + profileBounds["height"].(float64)/2}}})
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "session-5") && attempts.Load() == previousAttemptCount+1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "session-5") || attempts.Load() != previousAttemptCount+1 {
		t.Fatalf("key profile did not connect from the list (authentication attempts: %d): %s", attempts.Load(), tree)
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
	call("act", map[string]any{"key": "session-close-2", "action": "press"})
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
	call("act", map[string]any{"key": "session-close-3", "action": "press"})
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
	call("act", map[string]any{"key": "settings-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "font-larger") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "font-larger") {
		t.Fatalf("terminal settings did not open: %s", tree)
	}
	call("act", map[string]any{"key": "font-larger", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		prefs, readErr := readPreferences(filepath.Join(configDir, "NekoSSH", "preferences.json"))
		if readErr == nil && prefs.TerminalFontSize == 14 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if prefs, readErr := readPreferences(filepath.Join(configDir, "NekoSSH", "preferences.json")); readErr != nil || prefs.TerminalFontSize != 14 {
		t.Fatalf("terminal font change was not persisted: %+v, %v", prefs, readErr)
	}
	call("act", map[string]any{"key": "terminal-theme", "action": "press"})
	var nordX, nordY float64
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["label"] == "Nord" {
				bounds := node["bounds"].(map[string]any)
				nordX = bounds["x"].(float64) + bounds["width"].(float64)/2
				nordY = bounds["y"].(float64) + bounds["height"].(float64)/2
				break
			}
		}
		if nordX != 0 && nordY != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if nordX == 0 || nordY == 0 {
		t.Fatal("terminal theme options did not open")
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "x": nordX, "y": nordY}}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		prefs, readErr := readPreferences(filepath.Join(configDir, "NekoSSH", "preferences.json"))
		if readErr == nil && prefs.TerminalTheme == "nord" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if prefs, readErr := readPreferences(filepath.Join(configDir, "NekoSSH", "preferences.json")); readErr != nil || prefs.TerminalTheme != "nord" {
		t.Fatalf("terminal theme was not persisted: %+v, %v", prefs, readErr)
	}
	call("act", map[string]any{"key": "theme-toggle", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		prefs, readErr := readPreferences(filepath.Join(configDir, "NekoSSH", "preferences.json"))
		if readErr == nil && prefs.Light {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if prefs, readErr := readPreferences(filepath.Join(configDir, "NekoSSH", "preferences.json")); readErr != nil || !prefs.Light {
		t.Fatalf("theme change was not persisted: %+v, %v", prefs, readErr)
	}
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
	// The isolated SSH fixture has already been verified on this device.
	navigate("nav-connections")
	call("act", map[string]any{"key": "profile-search", "action": "setValue", "text": ""})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:group-card-fixture-group ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "group-card-fixture-group", "action": "press"})
	beforeAttempts := attempts.Load()
	call("act", map[string]any{"key": "group-quick-connect", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "分组连接完成：1 个已连接") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(tree, "分组连接完成：1 个已连接") || attempts.Load() != beforeAttempts+1 {
		t.Fatal("native group quick connection failed")
	}
	// Create an encrypted reusable identity and attach it to a new host.
	navigate("nav-identities")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		ready, open := false, false
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == "identity-name" {
				open = true
			}
			if node["key"] == "identity-new" && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				ready = true
			}
		}
		if open {
			break
		}
		if ready {
			call("act", map[string]any{"key": "identity-new", "action": "press"})
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:identity-name ") {
		t.Fatal("identity editor did not open")
	}
	for key, value := range map[string]string{"identity-name": "测试身份", "identity-user": settings.User, "identity-password": settings.Password} {
		call("act", map[string]any{"key": key, "action": "setValue", "text": value})
	}
	call("act", map[string]any{"key": "identity-save", "action": "press"})
	var identityID string
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(filepath.Join(configDir, "NekoSSH", "profiles.json"))
		var disk diskProfiles
		if readErr == nil && json.Unmarshal(data, &disk) == nil && len(disk.Identities) == 1 {
			if strings.Contains(string(data), settings.Password) {
				t.Fatal("identity password persisted as plaintext")
			}
			identityID = disk.Identities[0].ID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if identityID == "" {
		t.Fatal("identity was not saved")
	}
	navigate("nav-connections")
	call("act", map[string]any{"key": "connection-new", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:host ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:host ") {
		t.Fatalf("new host editor did not become ready: %v", call("tree"))
	}
	call("act", map[string]any{"key": "host", "action": "setValue", "text": settings.Host})
	call("act", map[string]any{"key": "port", "action": "setValue", "text": settings.Port})
	call("act", map[string]any{"key": "host-identity", "action": "press"})
	var identityX, identityY float64
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["label"] == "身份 · 测试身份" {
				bounds := node["bounds"].(map[string]any)
				identityX = bounds["x"].(float64) + bounds["width"].(float64)/2
				identityY = bounds["y"].(float64) + bounds["height"].(float64)/2
				break
			}
		}
		if identityX != 0 && identityY != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if identityX == 0 || identityY == 0 {
		t.Fatal("identity selection option missing")
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "x": identityX, "y": identityY}}})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(filepath.Join(configDir, "NekoSSH", "profiles.json"))
		var disk diskProfiles
		if readErr == nil && json.Unmarshal(data, &disk) == nil {
			for _, item := range disk.Items {
				if item.IdentityID == identityID && item.IdentityBinding != "" {
					identityX = -1
					break
				}
			}
		}
		if identityX == -1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if identityX != -1 {
		t.Fatal("host identity association was not saved")
	}
	var identities desktop
	if err := identities.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	resolved := false
	for _, p := range identities.profiles {
		if p.IdentityID == identityID {
			s, err := identities.profileCredential(p)
			if err == nil && s.Password == settings.Password {
				resolved = true
			}
		}
	}
	if !resolved {
		t.Fatal("saved host did not resolve its shared identity")
	}
	beforeAttempts = attempts.Load()
	call("act", map[string]any{"key": "connect", "action": "press"})
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() == beforeAttempts {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "确认指纹并连接") {
			call("act", map[string]any{"key": "connect", "action": "press"})
		}
		time.Sleep(50 * time.Millisecond)
	}
	if attempts.Load() != beforeAttempts+1 {
		t.Fatal("shared identity did not authenticate to the isolated SSH fixture")
	}
	// Revoke the fixture's local host key only after all connection checks.
	navigate("nav-known-hosts")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		tree = fmt.Sprint(call("tree"))
		if strings.Contains(tree, "key:known-host-search ") && strings.Contains(tree, fingerprint) {
			break
		}
		if strings.Contains(tree, "key:nav-known-hosts ") {
			call("act", map[string]any{"key": "nav-known-hosts", "action": "press"})
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(tree, fingerprint) {
		t.Fatal("verified fixture key missing from known hosts")
	}
	keyID := knownHostKeyID(knownHostKey{Host: settings.Host, Port: settings.Port, Fingerprint: fingerprint})
	forgetKey := "known-host-forget-" + keyID
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		ready := false
		for _, value := range result["nodes"].([]any) {
			node := value.(map[string]any)
			if node["key"] == forgetKey && !strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": forgetKey, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "确认撤销") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": forgetKey, "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "下次连接需要重新核对指纹") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "下次连接需要重新核对指纹") {
		t.Fatal("known-host revocation did not complete")
	}
	var afterRevocation desktop
	if err := afterRevocation.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	for _, p := range afterRevocation.profiles {
		if p.Settings.Host == settings.Host && p.Settings.Port == settings.Port && p.Settings.Fingerprint == fingerprint {
			t.Fatal("known-host revocation did not persist")
		}
	}
	// Export and preview an encrypted local backup, then append it to the
	// isolated test profile store without trusting imported host keys.
	navigate("nav-connections")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:profile-tools-toggle ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:local-archive-open ") {
		call("act", map[string]any{"key": "profile-tools-toggle", "action": "press"})
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:local-archive-open ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	call("act", map[string]any{"key": "local-archive-open", "action": "press"})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:local-archive-path ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	backupPath := filepath.Join(t.TempDir(), "local.nekobackup")
	call("act", map[string]any{"key": "local-archive-path", "action": "setValue", "text": backupPath})
	call("act", map[string]any{"key": "local-archive-password", "action": "setValue", "text": "test-backup-password"})
	call("act", map[string]any{"key": "local-archive-export", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "已加密导出") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil || strings.Contains(string(backup), settings.Password) {
		t.Fatal("local backup was not safely exported")
	}
	doc, err := decodeLocalArchive(backup, "test-backup-password")
	if err != nil || len(doc.Profiles) == 0 || len(doc.Identities) == 0 {
		t.Fatalf("local backup contents invalid: %v", err)
	}
	call("act", map[string]any{"key": "local-archive-password", "action": "setValue", "text": "test-backup-password"})
	call("act", map[string]any{"key": "local-archive-inspect", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "key:local-archive-apply ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "key:local-archive-apply ") {
		t.Fatal("local import preview missing")
	}
	call("act", map[string]any{"key": "local-archive-apply", "action": "press"})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("tree")), "已导入 ") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("tree")), "已导入 ") {
		t.Fatal("local backup import did not finish")
	}
	var afterImport desktop
	if err := afterImport.loadProfiles(filepath.Join(configDir, "NekoSSH", "profiles.json")); err != nil {
		t.Fatal(err)
	}
	if len(afterImport.profiles) != len(afterRevocation.profiles)+len(doc.Profiles) || len(afterImport.identities) != len(afterRevocation.identities)+len(doc.Identities) {
		t.Fatal("local backup was not appended")
	}
	for _, p := range afterImport.profiles[len(afterRevocation.profiles):] {
		if p.Settings.Fingerprint != "" {
			t.Fatal("imported host key was trusted automatically")
		}
	}
}
