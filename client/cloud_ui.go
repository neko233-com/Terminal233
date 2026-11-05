package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) cloudLogin(register bool) {
	if d.busy {
		return
	}
	if d.cloudRegistrationLocal && !localRegistrationAvailable(d.cloudURL) {
		d.cloudRegistration, d.cloudRegistrationLocal = "", false
		d.cloudStatus = "本机部署令牌仅可用于默认同步服务，请重新填写注册令牌。"
		return
	}
	client, err := newCloudClient(d.cloudURL)
	if err != nil {
		d.cloudStatus = err.Error()
		return
	}
	user, password, registration := d.cloudUser, d.cloudPassword, d.cloudRegistration
	d.cloudPassword = ""
	d.cloudRegistration = ""
	d.cloudRegistrationLocal = false
	d.busy = true
	d.cloudStatus = "正在连接同步服务…"
	go func() {
		registered := false
		var err error
		if register {
			err = client.register(user, password, registration)
			registered = err == nil
		}
		if err == nil {
			err = client.login(user, password)
		}
		var loaded cloudInitialState
		if err == nil {
			loaded = loadCloudInitialState(client, false)
		}
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				if registered {
					d.cloudStatus = "注册已完成，但登录失败；请直接登录：" + err.Error()
				} else if register {
					d.cloudStatus = "注册请求未确认；请先尝试登录或核对账号：" + err.Error()
				} else {
					d.cloudStatus = fmt.Sprintf("同步服务：%v", err)
				}
				return
			}
			d.cloud, d.accounts, d.teams = client, loaded.accounts, loaded.teams
			d.cloudListStale = loaded.accountErr != nil
			d.cloudStatus = "同步服务已登录"
			if loaded.accountErr != nil {
				d.cloudStatus = "已登录，但连接列表加载失败；请刷新后再修改：" + loaded.accountErr.Error()
			}
			d.teamStatus = ""
			if loaded.teamErr != nil {
				d.teamStatus = "团队列表加载失败，请刷新团队：" + loaded.teamErr.Error()
			}
		})
	}()
}
func (d *desktop) cloudRefresh() {
	if d.busy || d.cloud == nil {
		return
	}
	d.busy = true
	client := d.cloud
	go func() {
		accounts, err := client.list()
		d.win.Update(func() {
			d.busy = false
			if err != nil {
				d.cloudStatus = err.Error()
				return
			}
			d.accounts = accounts
			d.cloudListStale = false
			d.confirmCloudDelete = ""
			d.cloudStatus = "已刷新连接列表"
		})
	}()
}
func cloneCloudSources(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for scope, id := range source {
		clone[scope] = id
	}
	return clone
}

func (d *desktop) currentCloudSource() string {
	if d.cloud == nil {
		return ""
	}
	for _, session := range d.sessions {
		if session.id == d.currentSession {
			return session.cloudSources[d.cloud.teamID]
		}
	}
	return ""
}

func (d *desktop) formCloudSource() string {
	if d.cloud == nil || !d.settingsFromCloud {
		return ""
	}
	return d.cloudFormSources[d.cloud.teamID]
}

func (d *desktop) cloudSave() {
	binding := cloudSaveBinding{client: d.cloud, sessionID: d.currentSession, sessionSettings: d.active}
	if d.cloud != nil {
		binding.scope = d.cloud.teamID
		binding.sourceID = d.currentCloudSource()
	}
	d.saveCloudConnection(d.active, "", binding.sourceID, binding)
}

func (d *desktop) cloudSaveForm() {
	settings := d.settings
	settings.Fingerprint = d.fingerprint
	binding := cloudSaveBinding{client: d.cloud, form: true, formSettings: d.settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel, formProfileID: d.selectedProfile, formFromCloud: d.settingsFromCloud}
	if d.cloud != nil {
		binding.scope = d.cloud.teamID
		binding.sourceID = d.formCloudSource()
	}
	d.saveCloudConnection(settings, d.profileLabel, binding.sourceID, binding)
}

// Updates require an explicitly loaded ID; matching an address is not proof
// that two cloud entries represent the same credential.
func prepareCloudAccount(settings connectionSettings, label string, existing []cloudAccount, selectedID string) (cloudAccount, error) {
	if _, err := settings.address(); err != nil {
		return cloudAccount{}, err
	}
	if settings.KeyPath != "" || settings.Password == "" || !strings.HasPrefix(settings.Fingerprint, "SHA256:") {
		return cloudAccount{}, fmt.Errorf("仅支持同步已核对指纹的密码连接")
	}
	port, _ := strconv.Atoi(strings.TrimSpace(settings.Port))
	label = strings.TrimSpace(label)
	if selectedID != "" {
		found := false
		for _, item := range existing {
			if item.ID == selectedID {
				found = true
				if label == "" {
					label = item.Label
				}
				break
			}
		}
		if !found {
			return cloudAccount{}, fmt.Errorf("原云端连接不存在或已不可见，请刷新并重新选择")
		}
	}
	if label == "" {
		label = settings.User + "@" + settings.Host
	}
	return cloudAccount{ID: selectedID, Label: label, Host: strings.TrimSpace(settings.Host), Port: port, Username: strings.TrimSpace(settings.User), Password: settings.Password, HostKeyFingerprint: settings.Fingerprint}, nil
}

func (d *desktop) saveCloudConnection(settings connectionSettings, label, sourceID string, binding cloudSaveBinding) {
	if d.busy || d.cloud == nil {
		return
	}
	if d.cloudListStale {
		d.cloudStatus, d.status = "连接列表需要先刷新，避免重复或覆盖错误连接。", "连接列表需要先刷新。"
		return
	}
	account, err := prepareCloudAccount(settings, label, d.accounts, sourceID)
	if err != nil {
		d.cloudStatus, d.status = err.Error(), err.Error()
		return
	}
	client := d.cloud
	d.busy = true
	d.cloudStatus = "正在加密传输连接信息…"
	d.status = d.cloudStatus
	go func() {
		id, accounts, mutationErr, refreshErr := saveCloudAndRefresh(client, account)
		d.win.Update(func() {
			d.busy = false
			bound := false
			if mutationErr == nil {
				bound = d.bindSavedCloudID(binding, id)
			}
			d.finishCloudSave(accounts, mutationErr, refreshErr)
			if mutationErr == nil && !bound {
				d.cloudStatus += "；当前表单或会话已改变，请从云端列表重新选择以继续更新。"
				d.status = d.cloudStatus
			}
		})
	}()
}

// A response may arrive after the user has moved to a different form or
// session. Bind its ID only to the exact state that initiated the write.
type cloudSaveBinding struct {
	client                                    *cloudClient
	scope                                     string
	sessionID                                 int
	sessionSettings                           connectionSettings
	form                                      bool
	formSettings                              connectionSettings
	formFingerprint, formLabel, formProfileID string
	formFromCloud                             bool
	sourceID                                  string
}

func (d *desktop) bindSavedCloudID(binding cloudSaveBinding, id string) bool {
	if id == "" || d.cloud == nil || d.cloud != binding.client || d.cloud.teamID != binding.scope {
		return false
	}
	if binding.form {
		if d.settings != binding.formSettings || d.fingerprint != binding.formFingerprint || d.profileLabel != binding.formLabel || d.selectedProfile != binding.formProfileID || d.settingsFromCloud != binding.formFromCloud || d.formCloudSource() != binding.sourceID {
			return false
		}
		if d.cloudFormSources == nil {
			d.cloudFormSources = make(map[string]string)
		}
		d.settingsFromCloud = true
		d.cloudFormSources[binding.scope] = id
		return true
	}
	for _, session := range d.sessions {
		if session.id == binding.sessionID && session.active == binding.sessionSettings && session.cloudSources[binding.scope] == binding.sourceID {
			if session.cloudSources == nil {
				session.cloudSources = make(map[string]string)
			}
			session.cloudSources[binding.scope] = id
			return true
		}
	}
	return false
}

func (d *desktop) finishCloudSave(accounts []cloudAccount, mutationErr, refreshErr error) {
	if mutationErr != nil {
		d.cloudListStale = true
		d.cloudStatus = "同步请求未确认，可能已生效；请先刷新：" + mutationErr.Error()
		d.status = d.cloudStatus
		return
	}
	if refreshErr != nil {
		d.cloudListStale = true
		d.cloudStatus = "连接已同步，但列表刷新失败；请先刷新：" + refreshErr.Error()
		d.status = d.cloudStatus
		return
	}
	d.accounts, d.cloudListStale = accounts, false
	d.cloudStatus = "连接信息已同步"
	d.status = d.cloudStatus
}

func (d *desktop) loadCloudAccount(account cloudAccount) {
	d.workspace = "connections"
	d.editConnection = true
	d.selectedProfile = ""
	d.profileGroup = ""
	d.profileLabel = account.Label
	d.settings = connectionSettings{Host: account.Host, Port: strconv.Itoa(account.Port), User: account.Username, Password: account.Password}
	d.settingsFromCloud = true
	d.cloudFormSources = nil
	if d.cloud != nil && account.ID != "" {
		d.cloudFormSources = map[string]string{d.cloud.teamID: account.ID}
	}
	d.fingerprint = ""
	d.changedFingerprint = ""
	d.expectedFingerprint = account.HostKeyFingerprint
	d.status = "已载入连接，请重新获取并核对主机指纹。"
}

func (d *desktop) cloudView(c *ui.Context) {
	ui.Text(c, "跨设备访问连接信息，或与团队共享凭据。").TextColor(c.Theme().TextMuted).Wrap()
	ui.Column(c).FillWidth().Padding(18).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() {
		ui.Text(c, "同步账号").FontSize(16).Bold()
		ui.TextInput(c.Key("cloudurl"), &d.cloudURL).Label("同步服务 HTTPS 地址").Placeholder("同步服务 HTTPS 地址").Disabled(d.busy || d.cloud != nil).OnChange(d.cloudURLChanged)
		ui.TextInput(c.Key("clouduser"), &d.cloudUser).Label("同步账号").Placeholder("同步账号").Disabled(d.busy || d.cloud != nil)
		ui.TextInput(c.Key("cloudpassword"), &d.cloudPassword).Label("同步密码").Placeholder("同步密码").Password().Disabled(d.busy || d.cloud != nil)
		ui.TextInput(c.Key("cloudregistration"), &d.cloudRegistration).Label("注册令牌").Placeholder("注册令牌（仅注册时需要）").Password().Disabled(d.busy || d.cloud != nil)
		if d.cloud == nil && localRegistrationAvailable(d.cloudURL) {
			if ui.Button(c.Key("cloud-local-registration"), "使用本机部署注册令牌").Disabled(d.busy).Clicked() {
				if token, err := readLocalRegistrationToken(); err != nil {
					d.cloudStatus = err.Error()
				} else {
					d.cloudRegistration = token
					d.cloudRegistrationLocal = true
					d.cloudStatus = "已载入本机注册令牌；填写账号和密码后注册。"
				}
			}
		}
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			if ui.PrimaryButton(c.Key("cloudlogin"), "登录同步服务").Disabled(d.busy || d.cloud != nil).Clicked() {
				d.cloudLogin(false)
			}
			if ui.Button(c.Key("cloudregister"), "注册并登录").Disabled(d.busy || d.cloud != nil).Clicked() {
				d.cloudLogin(true)
			}
			if ui.Button(c.Key("cloudlogout"), "退出同步服务").Disabled(d.busy || d.cloud == nil).Clicked() {
				d.cloudLogout()
			}
		})
	})
	ui.Text(c, "云端连接").FontSize(16).Bold()
	if d.cloud != nil {
		ui.Text(c, "当前同步空间："+d.cloudScopeName()).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	}
	if d.cloud != nil && d.active.Password != "" {
		if d.currentCloudSource() != "" {
			ui.Text(c, "同步当前会话将更新它载入的云端连接。").FontSize(12).Wrap()
		} else {
			ui.Text(c, "同步当前会话将新建云端连接，不会按地址覆盖已有记录。").FontSize(12).Wrap()
		}
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		if ui.Button(c.Key("cloudsave"), "同步当前密码连接").Disabled(d.busy || d.cloud == nil || d.cloudListStale || d.active.Password == "" || d.active.KeyPath != "").Clicked() {
			d.cloudSave()
		}
		if ui.Button(c.Key("cloudrefresh"), "刷新云端连接").Disabled(d.busy || d.cloud == nil).Clicked() {
			d.cloudRefresh()
		}
		if ui.Button(c.Key("clouddelete"), "删除选中云端连接（需确认）").Disabled(d.busy || d.cloudListStale || d.confirmCloudDelete == "").Clicked() {
			d.cloudDelete()
		}
	})
	if d.cloudListStale {
		ui.Text(c, "连接列表可能过期，请先刷新后再修改或删除。").FontSize(12).Wrap()
	}
	ui.Text(c, d.cloudStatus).FontSize(12).Wrap()
	teamLabel := "展开团队管理"
	if d.showTeams {
		teamLabel = "收起团队管理"
	}
	if ui.Button(c.Key("toggle-teams"), teamLabel).Clicked() {
		d.showTeams = !d.showTeams
	}
	if d.showTeams {
		d.teamView(c)
	}
	adminLabel := "展开服务器管理"
	if d.showAdmin {
		adminLabel = "收起服务器管理"
	}
	if ui.Button(c.Key("toggle-admin"), adminLabel).Clicked() {
		d.showAdmin = !d.showAdmin
	}
	if d.showAdmin {
		d.adminView(c)
	}
	for _, item := range d.accounts {
		account := item
		row := ui.Button(c.Key("account-"+account.ID), account.Label).Disabled(d.busy)
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("删除…").Chosen() {
				d.confirmCloudDelete = account.ID
				d.cloudStatus = "请点击删除按钮确认：" + account.Label
			}
		})
		if row.Clicked() {
			d.loadCloudAccount(account)
		}
	}
}

func (d *desktop) cloudURLChanged() {
	if d.cloudRegistrationLocal && !localRegistrationAvailable(d.cloudURL) {
		d.cloudRegistration, d.cloudRegistrationLocal = "", false
		d.cloudStatus = "已清除本机注册令牌；它只能用于默认同步服务。"
	}
}

func (d *desktop) cloudDelete() {
	if d.busy || d.cloud == nil || d.cloudListStale || d.confirmCloudDelete == "" {
		return
	}
	id, client := d.confirmCloudDelete, d.cloud
	d.confirmCloudDelete = ""
	d.busy = true
	go func() {
		accounts, mutationErr, refreshErr := mutateAndRefresh(func() error { return client.remove(id) }, client.list)
		d.win.Update(func() { d.busy = false; d.finishCloudDelete(id, accounts, mutationErr, refreshErr) })
	}()
}
func (d *desktop) finishCloudDelete(id string, accounts []cloudAccount, mutationErr, refreshErr error) {
	if mutationErr != nil {
		d.cloudListStale = true
		d.cloudStatus = "删除请求未确认，可能已生效；请先刷新：" + mutationErr.Error()
		return
	}
	if refreshErr != nil {
		kept := d.accounts[:0]
		for _, account := range d.accounts {
			if account.ID != id {
				kept = append(kept, account)
			}
		}
		d.accounts, d.cloudListStale = kept, true
		d.cloudStatus = "连接已删除，但列表刷新失败；请先刷新：" + refreshErr.Error()
		return
	}
	d.accounts, d.cloudListStale = accounts, false
	d.cloudStatus = "云端连接已删除"
}

func (d *desktop) cloudLogout() {
	if d.busy || d.cloud == nil {
		return
	}
	client := d.cloud
	d.busy = true
	go func() {
		err := client.logout()
		d.win.Update(func() {
			d.busy = false
			d.clearCloudState()
			if err != nil {
				d.cloudStatus = "本地已退出，服务端注销失败：" + err.Error()
			} else {
				d.cloudStatus = "已退出同步服务"
			}
		})
	}()
}

// Logging out and restoring a backup both invalidate the cloud session.
func (d *desktop) clearCloudState() {
	if d.cloud != nil {
		d.cloud.token, d.cloud.adminTOTP = "", ""
	}
	d.cloud = nil
	if d.profileSource == "cloud" {
		d.profileSource = ""
	}
	if d.settingsFromCloud {
		d.settings.Password, d.settings.Passphrase = "", ""
		d.settingsFromCloud = false
	}
	d.cloudPassword, d.cloudRegistration = "", ""
	d.cloudRegistrationLocal, d.cloudListStale = false, false
	d.cloudFormSources = nil
	for _, session := range d.sessions {
		session.cloudSources = nil
	}
	d.cloudStatus, d.teamStatus = "", ""
	d.accounts, d.teams, d.members, d.teamInvites = nil, nil, nil, nil
	d.teamInviteURL, d.teamInviteInput, d.teamInvitePassword = "", "", ""
	d.confirmMember, d.confirmInvite, d.confirmCloudDelete = "", "", ""
	d.adminUsers, d.adminTeams = nil, nil
	d.adminConfirmTeam, d.adminConfirmUser, d.adminStatus, d.adminTOTP = "", "", "", ""
	d.adminConfirmRestore = false
}
