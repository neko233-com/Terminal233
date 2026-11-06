package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

var errQuickConnection = errors.New("请输入 用户@主机 或 ssh 用户@主机 -p 端口；不支持密码和其他选项")

func looksLikeQuickConnection(input string) bool {
	input = strings.TrimSpace(input)
	return strings.HasPrefix(input, "ssh ") || strings.Contains(input, "@")
}

func (d *desktop) submitConnectionSearch() {
	if d.busy || !looksLikeQuickConnection(d.profileSearch) {
		return
	}
	if !d.prepareConnectionNavigation() {
		return
	}
	d.quickConnect = d.profileSearch
	d.openQuickConnection()
	if d.editConnection {
		d.profileSearch = ""
	}
}

// parseQuickConnection treats the input as data, never as a shell command.
func parseQuickConnection(input string) (connectionSettings, error) {
	if len(input) > 512 || strings.ContainsAny(input, "\r\n\x00") {
		return connectionSettings{}, errQuickConnection
	}
	parts := strings.Fields(input)
	if len(parts) > 0 && parts[0] == "ssh" {
		parts = parts[1:]
	}
	port, target, portSeen := "22", "", false
	for i := 0; i < len(parts); i++ {
		if parts[i] == "-p" && !portSeen && i+1 < len(parts) {
			portSeen = true
			i++
			port = parts[i]
		} else if target == "" && !strings.HasPrefix(parts[i], "-") {
			target = parts[i]
		} else {
			return connectionSettings{}, errQuickConnection
		}
	}
	user, host, ok := strings.Cut(target, "@")
	if !ok || user == "" || host == "" || strings.Contains(host, "@") {
		return connectionSettings{}, errQuickConnection
	}
	for _, r := range user {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return connectionSettings{}, errQuickConnection
		}
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
		if ip := net.ParseIP(host); ip == nil || !strings.Contains(host, ":") {
			return connectionSettings{}, errQuickConnection
		}
	} else {
		for _, r := range host {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
				return connectionSettings{}, errQuickConnection
			}
		}
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return connectionSettings{}, errQuickConnection
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return connectionSettings{}, errQuickConnection
	}
	return connectionSettings{Host: host, User: user, Port: strconv.Itoa(n)}, nil
}

func (d *desktop) openQuickConnection() {
	settings, err := parseQuickConnection(d.quickConnect)
	if err != nil {
		d.quickConnect = ""
		d.status = err.Error()
		return
	}
	d.settings = settings
	d.profileTagsCaretPending, d.profileGroupCaretPending = false, false
	d.profileSecretError = false
	d.connectionFocusPending = false
	d.showKeyOptions = false
	d.showConnectionTools = false
	d.settingsFromCloud = false
	d.cloudFormSources = nil
	d.cloudFormVersions = nil
	d.quickConnect, d.profileGroup, d.profileLabel, d.profileTags, d.selectedProfile = "", "", "", "", ""
	d.profileIdentityID = ""
	d.fingerprint, d.expectedFingerprint = "", ""
	d.changedFingerprint = ""
	d.editConnection = true
	d.status = "请填写认证信息，获取并核对服务器指纹。"
	d.captureConnectionDraft()
	d.connectionDraft.metadata = connectionDraftHash("", "", "", "", "", "", "")
	d.profileAutoDirty = true
}
