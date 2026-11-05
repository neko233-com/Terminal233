//go:build desktope2e && windows

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopCloudArchive(t *testing.T) {
	temp := t.TempDir()
	serverBinary := filepath.Join(temp, "sync-server.exe")
	buildServer := exec.Command("go", "-C", "../server", "build", "-p", "1", "-o", serverBinary, "./cmd/server")
	if output, err := buildServer.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v %s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	serverProcess := exec.Command(serverBinary)
	serverProcess.Env = append(os.Environ(), "TERMINAL233_SERVER_ADDR="+address, "TERMINAL233_DATA_PATH="+filepath.Join(temp, "store.sqlite"), "TERMINAL233_MASTER_KEY="+strings.Repeat("m", 40), "TERMINAL233_OFFICIAL_TOKEN="+strings.Repeat("t", 40), "TERMINAL233_ADMIN_USERNAME=")
	if err := serverProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverProcess.Process.Kill(); _ = serverProcess.Wait() })
	base := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := http.Get(base + "/healthz")
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("test server did not start: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	target, _ := url.Parse(base)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer proxy.Close()
	client, err := newCloudClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = proxy.Client().Transport
	if err := client.register("archive-test", "test-cloud-password", strings.Repeat("t", 40)); err != nil {
		t.Fatal(err)
	}
	if err := client.login("archive-test", "test-cloud-password"); err != nil {
		t.Fatal(err)
	}
	account := cloudAccount{Label: "隔离测试连接", Host: "example.test", Port: 22, Username: "alice", Password: "test-ssh-password", HostKeyFingerprint: "SHA256:test"}
	if err := client.save(account); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(temp, "test-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	appBinary := filepath.Join(temp, "terminal233-e2e.exe")
	buildApp := exec.Command("go", "build", "-p", "1", "-tags", "e2e", "-o", appBinary, ".")
	if output, err := buildApp.CombinedOutput(); err != nil {
		t.Fatalf("build desktop: %v %s", err, output)
	}
	socket := filepath.Join(temp, "automation.sock")
	app := exec.Command(appBinary)
	app.Env = append(os.Environ(), "MYGO_AUTOMATION="+socket, "APPDATA="+filepath.Join(temp, "appdata"), "SSL_CERT_FILE="+caPath, "GODEBUG=x509sslcertoverrideplatform=1")
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Process.Kill(); _ = app.Wait() })
	var conn net.Conn
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("native automation unavailable: %v", err)
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
		data, _ := json.Marshal(request)
		if _, err := conn.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var answer map[string]any
		if err := json.Unmarshal(line, &answer); err != nil || answer["error"] != nil {
			t.Fatalf("automation %s failed: %v %v", method, answer["error"], err)
		}
		return answer
	}
	waitText := func(needle string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(fmt.Sprint(call("tree")), needle) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("native window did not show %q", needle)
	}
	waitText("nav-cloud")
	call("act", map[string]any{"key": "nav-cloud", "action": "press"})
	waitText("cloudurl")
	call("act", map[string]any{"key": "cloudurl", "action": "setValue", "text": proxy.URL})
	call("act", map[string]any{"key": "clouduser", "action": "setValue", "text": "archive-test"})
	call("act", map[string]any{"key": "cloudpassword", "action": "setValue", "text": "test-cloud-password"})
	call("act", map[string]any{"key": "cloudlogin", "action": "press"})
	waitText("同步服务已登录")
	call("act", map[string]any{"key": "toggle-archive", "action": "press"})
	waitText("archive-path")
	backupPath := filepath.Join(temp, "connections.nekobackup")
	call("act", map[string]any{"key": "archive-path", "action": "setValue", "text": backupPath})
	call("act", map[string]any{"key": "archive-password", "action": "setValue", "text": "test-backup-password"})
	call("act", map[string]any{"key": "archive-export", "action": "press"})
	waitText("已加密导出 1 条连接")
	data, err := os.ReadFile(backupPath)
	if err != nil || bytes.Contains(data, []byte(account.Password)) {
		t.Fatal("native export was missing or contained plaintext")
	}
	if decoded, err := decodeCloudArchive(data, "test-backup-password"); err != nil || len(decoded) != 1 || decoded[0].Password != account.Password {
		t.Fatal("native export could not be decrypted")
	}
	call("act", map[string]any{"key": "archive-password", "action": "setValue", "text": "wrong-backup-password"})
	call("act", map[string]any{"key": "archive-import", "action": "press"})
	waitText("确认追加导入")
	call("act", map[string]any{"key": "archive-import", "action": "press"})
	waitText("备份密码错误或文件已损坏")
	if existing, err := client.list(); err != nil || len(existing) != 1 {
		t.Fatal("wrong backup password changed cloud connections")
	}
	call("act", map[string]any{"key": "archive-password", "action": "setValue", "text": "test-backup-password"})
	call("act", map[string]any{"key": "archive-import", "action": "press"})
	waitText("确认追加导入")
	call("act", map[string]any{"key": "archive-import", "action": "press"})
	waitText("已追加导入 1 条连接")
	if accounts, err := client.list(); err != nil || len(accounts) != 2 || accounts[0].ID == accounts[1].ID {
		t.Fatal("native import did not append a separate connection")
	}
}
