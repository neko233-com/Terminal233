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
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDesktopCloudWorkflows(t *testing.T) {
	temp := t.TempDir()
	serverBinary := filepath.Join(temp, "sync-server.exe")
	buildServer := exec.Command("go", "-C", "../server", "build", "-p", "1", "-o", serverBinary, "./cmd/server")
	buildServer.Env = append(os.Environ(), "GOFLAGS=-p=1") // The server is a separate module.
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
	client.discoverCapabilities()
	account := cloudAccount{Label: "隔离测试连接", Host: "example.test", Port: 22, Username: "alice", Password: "test-ssh-password", HostKeyFingerprint: "SHA256:test", Tags: []string{"fixture-prod", "fixture-db"}}
	if err := client.save(account); err != nil {
		t.Fatal(err)
	}
	initialGroups, err := client.listCloudGroups()
	if err != nil {
		t.Fatal(err)
	}
	initialGroups.Groups = []string{"production-group"}
	if _, err = client.replaceCloudGroups(initialGroups); err != nil {
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
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(fmt.Sprint(call("windows")), "kind:content") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(fmt.Sprint(call("windows")), "kind:content") {
		t.Fatal("native cloud test window did not open")
	}
	waitText("nav-cloud")
	call("act", map[string]any{"key": "nav-cloud", "action": "press"})
	waitText("cloudurl")
	call("act", map[string]any{"key": "cloudurl", "action": "setValue", "text": proxy.URL})
	call("act", map[string]any{"key": "clouduser", "action": "setValue", "text": "archive-test"})
	call("act", map[string]any{"key": "cloudpassword", "action": "setValue", "text": "test-cloud-password"})
	call("act", map[string]any{"key": "cloudlogin", "action": "press"})
	waitText("同步服务已登录")
	call("act", map[string]any{"key": "nav-connections", "action": "press"})
	waitText("隔离测试连接 · alice@example.test")
	waitText("按云端标签筛选：fixture-prod")
	call("act", map[string]any{"key": "profile-tags-filter", "action": "press"})
	waitText("筛选标签：fixture-db")
	call("act", map[string]any{"key": "profile-tags-option-0", "action": "press"})
	call("act", map[string]any{"key": "profile-tags-option-1", "action": "press"})
	waitText("标签筛选（已选 2）")
	waitText("隔离测试连接 · alice@example.test")
	call("act", map[string]any{"key": "profile-tags-clear", "action": "press"})
	call("act", map[string]any{"key": "profile-tags-filter", "action": "press"})
	call("act", map[string]any{"key": "profile-search", "action": "setValue", "text": "fixture-prod"})
	waitText("隔离测试连接 · alice@example.test")
	call("act", map[string]any{"key": "profile-search", "action": "setValue", "text": ""})
	stored, err := client.list()
	if err != nil || len(stored) != 1 {
		t.Fatal("isolated cloud connection missing")
	}
	call("act", map[string]any{"key": "profile-cloud-" + stored[0].ID, "action": "press"})
	waitText("已载入连接，请重新获取并核对主机指纹")
	cloudHostTree := fmt.Sprint(call("tree"))
	if strings.Contains(cloudHostTree, "使用已信任指纹连接") || !strings.Contains(cloudHostTree, "连接并核验主机指纹") {
		t.Fatal("cloud fingerprint was silently trusted")
	}
	var groupFieldID string
	for _, entry := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
		node := entry.(map[string]any)
		if node["label"] == "云端主机分组" {
			groupFieldID, _ = node["id"].(string)
			break
		}
	}
	if groupFieldID == "" {
		t.Fatal("cloud group editor missing from accessibility tree")
	}
	call("act", map[string]any{"id": groupFieldID, "action": "scrollIntoView"})
	var fieldX, fieldY float64
	for _, entry := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
		node := entry.(map[string]any)
		if node["id"] == groupFieldID {
			bounds := node["bounds"].(map[string]any)
			fieldX = bounds["x"].(float64) + bounds["width"].(float64)/2
			fieldY = bounds["y"].(float64) + bounds["height"].(float64)/2
			if strings.Contains(fmt.Sprint(node["states"]), "disabled") {
				t.Fatalf("cloud group editor disabled: %v", node["states"])
			}
			break
		}
	}
	if fieldX == 0 {
		t.Fatal("cloud group editor is not visible")
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "x": fieldX, "y": fieldY}}})
	waitText("分组 / production-group")
	var groupX, groupY float64
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result := call("tree")["result"].(map[string]any)
		for _, entry := range result["nodes"].([]any) {
			node := entry.(map[string]any)
			if node["label"] == "分组 / production-group" {
				bounds := node["bounds"].(map[string]any)
				groupX = bounds["x"].(float64) + bounds["width"].(float64)/2
				groupY = bounds["y"].(float64) + bounds["height"].(float64)/2
				break
			}
		}
		if groupX != 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if groupX == 0 {
		t.Fatal("cloud group option unavailable")
	}
	call("input", map[string]any{"events": []any{map[string]any{"type": "click", "x": groupX, "y": groupY}}})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		groups, e := client.listCloudGroups()
		if e == nil && groups.Assignments[stored[0].ID] == "production-group" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	groups, e := client.listCloudGroups()
	if e != nil || groups.Assignments[stored[0].ID] != "production-group" {
		t.Fatal("native cloud group editor did not save", e)
	}
	call("act", map[string]any{"key": "nav-cloud", "action": "press"})
	waitText("toggle-archive")
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
	if decoded, err := decodeCloudArchive(data, "test-backup-password"); err != nil || len(decoded) != 1 || decoded[0].Password != account.Password || len(decoded[0].Tags) != 2 || decoded[0].Tags[0] != "fixture-prod" {
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
	if accounts, err := client.list(); err != nil || len(accounts) != 2 || accounts[0].ID == accounts[1].ID || len(accounts[0].Tags) != 2 || len(accounts[1].Tags) != 2 {
		t.Fatal("native import did not append a separate connection")
	}
	// Exercise the team controls against the same isolated Go service.
	call("act", map[string]any{"key": "toggle-archive", "action": "press"})
	call("act", map[string]any{"key": "toggle-teams", "action": "press"})
	waitText("team-name")
	call("act", map[string]any{"key": "team-name", "action": "setValue", "text": "桌面测试团队"})
	call("act", map[string]any{"key": "team-create", "action": "press"})
	waitText("团队操作已完成")
	teams, err := client.teams()
	if err != nil || len(teams) != 1 {
		t.Fatal("native team creation did not reach the test service")
	}
	teamID := teams[0].ID
	call("act", map[string]any{"key": "team-" + teamID, "action": "press"})
	waitText("当前团队角色：所有者")
	call("act", map[string]any{"key": "team-invite-password", "action": "setValue", "text": "test-invite-password"})
	call("act", map[string]any{"key": "team-invite", "action": "press"})
	waitText("邀请已创建")
	invitePattern := regexp.MustCompile(regexp.QuoteMeta(proxy.URL) + `/#join=([a-f0-9]{48})`)
	inviteMatch := invitePattern.FindStringSubmatch(fmt.Sprint(call("tree")))
	if len(inviteMatch) != 2 {
		t.Fatal("native invitation did not use the selected service address")
	}
	bob, err := newCloudClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	bob.http.Transport = proxy.Client().Transport
	if err := bob.registerWithInvite("test-member", "test-member-password", inviteMatch[1], "test-invite-password"); err != nil {
		t.Fatal(err)
	}
	if err := bob.login("test-member", "test-member-password"); err != nil {
		t.Fatal(err)
	}
	client.teamID, bob.teamID = teamID, teamID
	teamAccount := account
	teamAccount.Label = "团队独立连接"
	if err := client.save(teamAccount); err != nil {
		t.Fatal(err)
	}
	call("act", map[string]any{"key": "team-" + teamID, "action": "press"})
	waitText("test-member（普通成员）")
	call("act", map[string]any{"key": "member-test-member", "action": "press"})
	waitText("test-member（普通成员） · 已选择")
	call("act", map[string]any{"key": "member-admin", "action": "press"})
	waitText("test-member（管理员） · 已选择")
	if err := bob.save(cloudAccount{Label: "管理员连接", Host: "admin.example.test", Port: 22, Username: "admin", Password: "test-password", HostKeyFingerprint: "SHA256:test"}); err != nil {
		t.Fatal("promoted team administrator could not add a connection")
	}
	call("act", map[string]any{"key": "member-demote", "action": "press"})
	waitText("test-member（普通成员） · 已选择")
	if err := bob.save(teamAccount); err == nil {
		t.Fatal("demoted team member retained write access")
	}
	secondTeam, err := client.createTeam("另一个测试团队")
	if err != nil {
		t.Fatal(err)
	}
	client.teamID = secondTeam.ID
	otherAccount := account
	otherAccount.Label, otherAccount.Password = "另一团队连接", "other-team-password"
	if err := client.save(otherAccount); err != nil {
		t.Fatal(err)
	}
	secondInvite, err := client.createInvite("test-second-invite", 3600, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.joinTeam(extractInviteToken(secondInvite), "test-second-invite"); err != nil {
		t.Fatal(err)
	}
	call("act", map[string]any{"key": "team-refresh", "action": "press"})
	waitText("team-" + secondTeam.ID)
	call("act", map[string]any{"key": "team-" + secondTeam.ID, "action": "press"})
	waitText("当前同步空间：另一个测试团队")
	waitText("另一团队连接")
	call("act", map[string]any{"key": "team-" + teamID, "action": "press"})
	waitText("当前同步空间：桌面测试团队")
	waitText("团队独立连接")
	call("act", map[string]any{"key": "member-test-member", "action": "press"})
	waitText("test-member（普通成员） · 已选择")
	call("act", map[string]any{"key": "member-remove", "action": "press"})
	waitText("确认移除成员")
	call("act", map[string]any{"key": "member-remove", "action": "press"})
	waitText("成员已移除")
	if _, err := bob.list(); err == nil {
		t.Fatal("removed member retained team access")
	}
	bob.teamID = secondTeam.ID
	if accounts, err := bob.list(); err != nil || len(accounts) != 1 || accounts[0].Password != "other-team-password" {
		t.Fatal("removal from one team changed membership in the other")
	}
	call("act", map[string]any{"key": "team-personal", "action": "press"})
	waitText("当前同步空间：个人空间")
	client.teamID = ""
	if personal, err := client.list(); err != nil || len(personal) != 2 {
		t.Fatal("team operations modified personal-space connections")
	}
}
