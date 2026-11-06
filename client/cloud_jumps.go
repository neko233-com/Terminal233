package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func cloudTrustKey(host, port, user string) string { return fmt.Sprintf("%q/%q/%q", host, port, user) }

func (d *desktop) snapshotCloudHostTrust() map[string]string {
	result := map[string]string{}
	add := func(host, port, user, fp string) {
		if !strings.HasPrefix(fp, "SHA256:") {
			return
		}
		key := cloudTrustKey(host, port, user)
		if old, ok := result[key]; ok && old != fp {
			result[key] = ""
		} else if !ok {
			result[key] = fp
		}
	}
	for _, p := range d.profiles {
		add(p.Settings.Host, p.Settings.Port, p.Settings.User, p.Settings.Fingerprint)
	}
	for _, s := range d.sessions {
		if s.shell != nil {
			add(s.active.Host, s.active.Port, s.active.User, s.active.Fingerprint)
		}
	}
	return result
}

func trustedCloudHostFromSnapshot(trust map[string]string, a cloudAccount) (string, error) {
	fp := trust[cloudTrustKey(a.Host, strconv.Itoa(a.Port), a.Username)]
	if fp == "" {
		return "", errors.New("请先在本机核对跳板指纹")
	}
	return fp, nil
}

func cloudJumpReference(account cloudAccount) string {
	if account.JumpID == nil {
		return ""
	}
	return *account.JumpID
}

func validCloudJumpID(id string) bool {
	if id == "" {
		return true
	}
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 24 && hex.EncodeToString(b) == id
}

func resolveCloudJumpChain(target connectionSettings, accounts []cloudAccount, trusted func(cloudAccount) (string, error)) (connectionSettings, error) {
	target.route = nil
	if target.cloudJumpID == "" {
		return target, nil
	}
	if target.JumpID != "" || !validCloudJumpID(target.cloudJumpID) {
		return connectionSettings{}, errors.New("云端跳板标识无效")
	}
	address, err := target.address()
	if err != nil {
		return connectionSettings{}, err
	}
	byID := make(map[string]cloudAccount, len(accounts))
	for _, account := range accounts {
		if _, duplicate := byID[account.ID]; duplicate {
			return connectionSettings{}, errors.New("云主机编号重复")
		}
		byID[account.ID] = account
	}
	seenID := map[string]bool{}
	seenAddress := map[string]bool{strings.ToLower(address): true}
	var reverse []connectionSettings
	var components []string
	for id := target.cloudJumpID; id != ""; {
		if !validCloudJumpID(id) || seenID[id] || len(reverse) >= maxSSHJumpHosts {
			return connectionSettings{}, errors.New("云端跳板链存在循环或超过 8 跳")
		}
		account, ok := byID[id]
		if !ok {
			return connectionSettings{}, errors.New("云端跳板不存在，请刷新并重新选择")
		}
		fingerprint, err := trusted(account)
		if err != nil || fingerprint != account.HostKeyFingerprint || !strings.HasPrefix(fingerprint, "SHA256:") {
			return connectionSettings{}, fmt.Errorf("请先在本机核对跳板 %s 的主机指纹", account.Label)
		}
		step := connectionSettings{Host: account.Host, Port: strconv.Itoa(account.Port), User: account.Username, Password: account.Password, Fingerprint: fingerprint, cloudJumpID: cloudJumpReference(account)}
		address, err := step.address()
		if err != nil || step.Password == "" || seenAddress[strings.ToLower(address)] {
			return connectionSettings{}, errors.New("云端跳板包含无效或重复的主机")
		}
		seenID[id], seenAddress[strings.ToLower(address)] = true, true
		reverse = append(reverse, step)
		components = append(components, id, address, step.User, fingerprint, step.cloudJumpID, account.UpdatedAt)
		id = step.cloudJumpID
	}
	hops := make([]connectionSettings, len(reverse))
	for i := range reverse {
		hops[i] = reverse[len(reverse)-1-i]
	}
	target.route = &savedSSHRoute{hops: hops, signature: connectionDraftHash(components...), jumpID: target.cloudJumpID, cloudURL: target.cloudRouteURL, cloudUser: target.cloudRouteUser, cloudTeam: target.cloudRouteTeam}
	return target, nil
}

func (d *desktop) resolveCloudRoute(target connectionSettings) (connectionSettings, error) {
	if d.cloud == nil || d.cloud.base == nil || d.cloud.token == "" || !d.cloud.cloudJumps || d.cloudListStale || d.cloud.base.String() != target.cloudRouteURL || d.cloudUser != target.cloudRouteUser || d.cloud.teamID != target.cloudRouteTeam {
		return connectionSettings{}, errors.New("请登录并刷新此跳板所属的云空间")
	}
	return resolveCloudJumpChain(target, d.accounts, d.trustedWorkspaceCloudFingerprint)
}

func (d *desktop) resolveConnectionRoute(settings connectionSettings) (connectionSettings, error) {
	if settings.cloudJumpID != "" {
		return d.resolveCloudRoute(settings)
	}
	return d.resolveSavedRoute(settings)
}

// Local routes can only reference already synced password hosts in this exact
// scope. Match the whole route and credential, never just an address.
func (d *desktop) cloudJumpForLocalRoute(settings connectionSettings) (string, error) {
	resolved, err := d.resolveSavedRoute(settings)
	if err != nil {
		return "", err
	}
	if resolved.route == nil {
		return "", nil
	}
	previous := ""
	for _, hop := range resolved.route.hops {
		if hop.KeyPath != "" || hop.Password == "" {
			return "", errors.New("请先将每个密码跳板同步到同一云空间")
		}
		matches := []cloudAccount{}
		for _, a := range d.accounts {
			if a.Host == hop.Host && strconv.Itoa(a.Port) == hop.Port && a.Username == hop.User && a.Password == hop.Password && a.HostKeyFingerprint == hop.Fingerprint && cloudJumpReference(a) == previous {
				matches = append(matches, a)
			}
		}
		if len(matches) != 1 || matches[0].ID == "" || !validCloudJumpID(matches[0].ID) {
			return "", errors.New("请先按顺序同步各跳板到同一云空间，且避免重复主机")
		}
		previous = matches[0].ID
	}
	return previous, nil
}

func (d *desktop) prepareCloudAccountWithJump(settings connectionSettings, label, sourceID string) (cloudAccount, error) {
	if d.cloud == nil {
		return cloudAccount{}, errors.New("请先登录同步服务")
	}
	jumpID := settings.cloudJumpID
	if settings.JumpID != "" {
		if !d.cloud.cloudJumps {
			return cloudAccount{}, errors.New("当前同步服务尚不支持云端跳板")
		}
		var err error
		jumpID, err = d.cloudJumpForLocalRoute(settings)
		if err != nil {
			return cloudAccount{}, err
		}
		settings.JumpID = ""
	} else if jumpID != "" {
		if _, err := d.resolveCloudRoute(settings); err != nil {
			return cloudAccount{}, err
		}
	}
	if jumpID != "" && !d.cloud.cloudJumps {
		return cloudAccount{}, errors.New("当前同步服务尚不支持云端跳板")
	}
	account, err := prepareCloudAccount(settings, label, d.accounts, sourceID)
	if err != nil {
		return cloudAccount{}, err
	}
	if d.cloud.cloudJumps && (jumpID != "" || sourceID != "") {
		account.JumpID = &jumpID
	}
	return account, nil
}

func (d *desktop) setCloudJump(id string, markMetadataChanged func()) {
	if d.cloud == nil || !d.cloud.cloudJumps || d.cloudListStale || !validCloudJumpID(id) {
		d.status = "请先登录并刷新支持云端跳板的服务。"
		return
	}
	byID := make(map[string]cloudAccount, len(d.accounts))
	for _, a := range d.accounts {
		if _, exists := byID[a.ID]; exists {
			d.status = "云主机编号重复，请刷新。"
			return
		}
		byID[a.ID] = a
	}
	seen := map[string]bool{}
	for next, depth := id, 0; next != ""; depth++ {
		if !validCloudJumpID(next) || next == d.selectedCloudProfile || seen[next] || depth >= maxSSHJumpHosts {
			d.status = "云端跳板链循环或超过 8 跳。"
			return
		}
		a, ok := byID[next]
		if !ok {
			d.status = "云端跳板不存在，请刷新。"
			return
		}
		seen[next] = true
		next = cloudJumpReference(a)
	}
	changed := d.settings.cloudJumpID != id || d.settings.JumpID != ""
	d.settings.cloudJumpID, d.settings.JumpID, d.settings.route = id, "", nil
	d.setConnectionJump(0)
	if changed && markMetadataChanged != nil {
		markMetadataChanged()
	}
	if id != "" {
		d.status = "已选择云端跳板；每一跳都需要本机确认指纹。"
	}
}
