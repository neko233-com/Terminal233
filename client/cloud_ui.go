package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
			d.setCloudGroupState(loaded.groups, loaded.groupErr)
			d.setCloudWorkspaceState(loaded.workspaces, loaded.workspaceErr)
			d.teamListStale = loaded.teamErr != nil
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
		probe := *client
		probe.discoverCapabilities()
		accounts, err := client.list()
		var groups cloudGroupSnapshot
		var groupErr error
		var workspaces cloudWorkspaceSnapshot
		var workspaceErr error
		if probe.cloudWorkspaces {
			workspaces, workspaceErr = probe.listCloudWorkspaces()
		}
		if err == nil && probe.cloudGroups {
			groups, groupErr = probe.listCloudGroups()
		}
		d.win.Update(func() {
			d.busy = false
			if d.cloud == client {
				client.conditionalUpdates, client.conditionalDeletes, client.cloudGroups, client.cloudWorkspaces, client.cloudWorkspaces16, client.cloudTags, client.capabilityUnknown = probe.conditionalUpdates, probe.conditionalDeletes, probe.cloudGroups, probe.cloudWorkspaces, probe.cloudWorkspaces16, probe.cloudTags, probe.capabilityUnknown
				d.setCloudWorkspaceState(workspaces, workspaceErr)
			}
			d.finishCloudRefresh(accounts, err)
			if err == nil && d.cloud == client {
				d.setCloudGroupState(groups, groupErr)
			}
		})
	}()
}

func (d *desktop) finishCloudRefresh(accounts []cloudAccount, err error) {
	if err != nil {
		d.cloudListStale = true
		if d.cloud != nil && d.cloud.teamID != "" {
			d.teamListStale = true
		}
		d.cloudStatus = "连接列表刷新失败，请重试：" + err.Error()
		return
	}
	d.clearCloudBatch()
	d.accounts, d.selectedCloudProfile, d.cloudListStale, d.confirmCloudDelete, d.confirmCloudDeleteVersion = accounts, "", false, "", ""
	d.cloudStatus = "已刷新连接列表"
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

func (d *desktop) currentCloudVersion() string {
	if d.cloud == nil {
		return ""
	}
	for _, session := range d.sessions {
		if session.id == d.currentSession {
			return session.cloudVersions[d.cloud.teamID]
		}
	}
	return ""
}

func (d *desktop) formCloudVersion() string {
	if d.cloud == nil || !d.settingsFromCloud {
		return ""
	}
	return d.cloudFormVersions[d.cloud.teamID]
}

func (d *desktop) cloudSave() {
	binding := cloudSaveBinding{client: d.cloud, sessionID: d.currentSession, sessionSettings: d.active}
	if d.cloud != nil {
		binding.scope = d.cloud.teamID
		binding.sourceID = d.currentCloudSource()
		binding.sourceVersion = d.currentCloudVersion()
	}
	d.saveCloudConnection(d.active, "", binding.sourceID, binding)
}

func (d *desktop) cloudSaveForm() {
	// Preserve pending local edits before the form becomes bound to a cloud
	// record; cloud writes do not persist the local group or local credentials.
	if !d.settingsFromCloud && !d.prepareConnectionNavigation() {
		return
	}
	settings := d.settings
	settings.Fingerprint = d.fingerprint
	binding := cloudSaveBinding{client: d.cloud, form: true, formSettings: d.settings, formFingerprint: d.fingerprint, formLabel: d.profileLabel, formTags: d.profileTags, formGroup: d.profileGroup, formProfileID: d.selectedProfile, formFromCloud: d.settingsFromCloud}
	if d.cloud != nil {
		binding.scope = d.cloud.teamID
		binding.sourceID = d.formCloudSource()
		binding.sourceVersion = d.formCloudVersion()
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
	if !d.canWriteCloudScope() {
		d.cloudStatus, d.status = "当前团队不可写；请联系管理员或刷新团队列表。", "当前团队不可写。"
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
	if binding.form {
		tags, tagErr := parseProfileTags(binding.formTags)
		if tagErr != nil {
			d.cloudStatus, d.status = tagErr.Error(), tagErr.Error()
			return
		}
		if len(tags) > 0 && !d.cloud.cloudTags {
			d.cloudStatus = "当前同步服务尚不支持主机标签，请先更新服务。"
			d.status = d.cloudStatus
			return
		}
		if d.cloud.cloudTags {
			account.Tags = append([]string{}, tags...)
		}
	}
	if err := applyConditionalVersion(d.cloud, &account, binding.sourceVersion); err != nil {
		d.cloudStatus, d.status = err.Error(), "请重新载入云端连接。"
		return
	}
	client := d.cloud
	d.busy = true
	d.cloudStatus = "正在加密传输连接信息…"
	d.status = d.cloudStatus
	go func() {
		saved, accounts, mutationErr, refreshErr := saveCloudAndRefreshVersioned(client, account)
		d.win.Update(func() {
			d.busy = false
			bound := false
			if mutationErr == nil {
				bound = d.bindSavedCloudID(binding, saved.ID, saved.UpdatedAt)
			}
			d.finishCloudSave(accounts, mutationErr, refreshErr)
			if mutationErr == nil && !bound {
				d.cloudStatus += "；当前表单或会话已改变，请从云端列表重新选择以继续更新。"
				d.status = d.cloudStatus
			}
		})
	}()
}

func applyConditionalVersion(c *cloudClient, account *cloudAccount, version string) error {
	if c != nil && c.capabilityUnknown && account.ID != "" {
		return errors.New("无法确认服务的并发保护能力，请刷新云端连接后重试。")
	}
	if c == nil || !c.conditionalUpdates || account.ID == "" {
		return nil
	}
	if version == "" {
		return errors.New("缺少原始连接版本，请从云端列表重新载入后再同步。")
	}
	account.ExpectedUpdatedAt = version
	return nil
}

// A response may arrive after the user has moved to a different form or
// session. Bind its ID only to the exact state that initiated the write.
type cloudSaveBinding struct {
	client                                                         *cloudClient
	scope                                                          string
	sessionID                                                      int
	sessionSettings                                                connectionSettings
	form                                                           bool
	formSettings                                                   connectionSettings
	formFingerprint, formLabel, formTags, formGroup, formProfileID string
	formFromCloud                                                  bool
	sourceID, sourceVersion                                        string
}

func (d *desktop) bindSavedCloudID(binding cloudSaveBinding, id, version string) bool {
	if id == "" || d.cloud == nil || d.cloud != binding.client || d.cloud.teamID != binding.scope {
		return false
	}
	if binding.form {
		if d.settings != binding.formSettings || d.fingerprint != binding.formFingerprint || d.profileLabel != binding.formLabel || d.profileTags != binding.formTags || d.profileGroup != binding.formGroup || d.selectedProfile != binding.formProfileID || d.settingsFromCloud != binding.formFromCloud || d.formCloudSource() != binding.sourceID || d.formCloudVersion() != binding.sourceVersion {
			return false
		}
		if d.cloudFormSources == nil {
			d.cloudFormSources = make(map[string]string)
		}
		d.settingsFromCloud = true
		d.cloudFormSources[binding.scope] = id
		if d.cloudFormVersions == nil {
			d.cloudFormVersions = make(map[string]string)
		}
		d.cloudFormVersions[binding.scope] = version
		if binding.formFromCloud && binding.formGroup == "" {
			d.captureConnectionDraft()
		} else if !binding.formFromCloud && !d.hasUnsavedConnection() {
			d.profileGroup = ""
			d.captureConnectionDraft()
		}
		return true
	}
	for _, session := range d.sessions {
		if session.id == binding.sessionID && session.active == binding.sessionSettings && session.cloudSources[binding.scope] == binding.sourceID && session.cloudVersions[binding.scope] == binding.sourceVersion {
			if session.cloudSources == nil {
				session.cloudSources = make(map[string]string)
			}
			session.cloudSources[binding.scope] = id
			if session.cloudVersions == nil {
				session.cloudVersions = make(map[string]string)
			}
			session.cloudVersions[binding.scope] = version
			return true
		}
	}
	return false
}

func (d *desktop) finishCloudSave(accounts []cloudAccount, mutationErr, refreshErr error) {
	d.clearCloudBatch()
	d.invalidateCloudGroups()
	if mutationErr != nil {
		d.cloudListStale = true
		var httpErr *cloudHTTPError
		if errors.As(mutationErr, &httpErr) && httpErr.Status == http.StatusConflict && httpErr.Code == "account_conflict" {
			d.cloudStatus = "连接已被其他设备修改；本次未保存，请刷新并重新载入。"
			d.status = d.cloudStatus
			return
		}
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
	d.pendingWorkspaceHost = nil
	d.profileIdentityID = ""
	d.profileTagsCaretPending, d.profileGroupCaretPending = false, false
	d.profileSecretError = false
	d.workspace = "connections"
	d.editConnection = true
	d.connectionFocusPending = false
	d.showKeyOptions = false
	d.showConnectionTools = false
	d.selectedProfile = ""
	d.selectedCloudProfile = account.ID
	d.showProfileInspector = false
	d.profileInspectorAt = time.Time{}
	d.profileGroup = ""
	d.profileTags = strings.Join(account.Tags, ", ")
	d.profileLabel = account.Label
	d.settings = connectionSettings{Host: account.Host, Port: strconv.Itoa(account.Port), User: account.Username, Password: account.Password}
	d.settingsFromCloud = true
	d.cloudFormSources = nil
	d.cloudFormVersions = nil
	if d.cloud != nil && account.ID != "" {
		d.cloudFormSources = map[string]string{d.cloud.teamID: account.ID}
		d.cloudFormVersions = map[string]string{d.cloud.teamID: account.UpdatedAt}
	}
	d.fingerprint = ""
	d.changedFingerprint = ""
	d.expectedFingerprint = account.HostKeyFingerprint
	d.status = "已载入连接，请重新获取并核对主机指纹。"
	d.captureConnectionDraft()
}

func (d *desktop) cloudView(c *ui.Context) {
	ui.Text(c, "跨设备访问连接信息，或与团队共享凭据。").TextColor(c.Theme().TextMuted).Wrap()
	ui.Column(c).FillWidth().MaxWidth(680).Padding(20).Gap(8).Background(c.Theme().Surface).Radius(8).Children(func() {
		ui.Text(c, "同步账号").FontSize(16).Bold()
		if d.cloud != nil {
			if d.cloudUser != "" {
				ui.Text(c, "已登录："+d.cloudUser).Wrap()
			} else {
				ui.Text(c, "已登录同步服务").Wrap()
			}
			ui.Text(c, "同步服务："+d.cloudURL).FontSize(12).Wrap()
			if ui.Button(c.Key("cloudlogout"), "退出同步服务").Disabled(d.busy).Clicked() {
				d.cloudLogout()
			}
			return
		}
		ui.Text(c, "同步服务地址").FontSize(12).TextColor(c.Theme().TextMuted)
		ui.TextInput(c.Key("cloudurl"), &d.cloudURL).Label("同步服务 HTTPS 地址").Placeholder("同步服务 HTTPS 地址").Disabled(d.busy || d.cloud != nil).OnChange(d.cloudURLChanged)
		ui.Text(c, "账号").FontSize(12).TextColor(c.Theme().TextMuted)
		ui.TextInput(c.Key("clouduser"), &d.cloudUser).Label("同步账号").Placeholder("同步账号").Disabled(d.busy || d.cloud != nil)
		ui.Text(c, "密码").FontSize(12).TextColor(c.Theme().TextMuted)
		ui.TextInput(c.Key("cloudpassword"), &d.cloudPassword).Label("同步密码").Placeholder("同步密码").Password().Disabled(d.busy || d.cloud != nil)
		ui.Row(c).FillWidth().Children(func() {
			if ui.PrimaryButton(c.Key("cloudlogin"), "登录同步服务").Disabled(d.busy || d.cloud != nil).Clicked() {
				d.cloudLogin(false)
			}
		})
		ui.Text(c, "注册令牌（注册新账号时填写）").FontSize(12).TextColor(c.Theme().TextMuted)
		ui.TextInput(c.Key("cloudregistration"), &d.cloudRegistration).Label("注册令牌").Placeholder("注册令牌（仅注册时需要）").Password().Disabled(d.busy || d.cloud != nil)
		if d.cloud == nil && localRegistrationAvailable(d.cloudURL) {
			ui.Row(c).FillWidth().Children(func() {
				if ui.Button(c.Key("cloud-local-registration"), "使用本机部署注册令牌").Disabled(d.busy).Clicked() {
					if token, err := readLocalRegistrationToken(); err != nil {
						d.cloudStatus = err.Error()
					} else {
						d.cloudRegistration = token
						d.cloudRegistrationLocal = true
						d.cloudStatus = "已载入本机注册令牌；填写账号和密码后注册。"
					}
				}
			})
		}
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			if ui.Button(c.Key("cloudregister"), "注册并登录").Disabled(d.busy || d.cloud != nil).Clicked() {
				d.cloudLogin(true)
			}
		})
		if d.cloud == nil {
			label := "使用团队邀请注册…"
			if d.showInviteEntry || d.showTeams {
				label = "收起团队邀请注册"
			}
			ui.Row(c).FillWidth().Children(func() {
				if ui.Button(c.Key("cloud-invite-entry"), label).Disabled(d.busy).Clicked() {
					if d.showInviteEntry || d.showTeams {
						d.showInviteEntry, d.showTeams = false, false
					} else {
						d.showInviteEntry = true
					}
				}
			})
			if d.showInviteEntry || d.showTeams {
				d.inviteJoinView(c)
				if d.teamStatus != "" {
					ui.Text(c, d.teamStatus).FontSize(12).Wrap()
				}
			}
		}
	})
	if d.cloudStatus != "" {
		ui.Text(c, d.cloudStatus).FontSize(12).Wrap()
	}
	if d.cloud == nil {
		return
	}
	ui.Text(c, "云端连接").FontSize(16).Bold()
	if d.cloud.capabilityUnknown {
		ui.Text(c, "无法确认同步服务的并发保护能力；已暂停更新和删除已有连接，请刷新云端连接重试。").FontSize(12).Wrap()
	} else if !d.cloud.conditionalUpdates || !d.cloud.conditionalDeletes {
		ui.Text(c, "未确认当前服务支持并发修改或删除保护；多设备同时操作可能冲突，请先刷新并核对。").FontSize(12).Wrap()
	}
	if d.teamListStale {
		ui.Text(c, "团队列表可能过期，请刷新团队后再执行管理操作。").FontSize(12).Wrap()
	}
	if d.cloud != nil {
		ui.Text(c, "当前同步空间："+d.cloudScopeName()).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
		if !d.canWriteCloudScope() {
			ui.Text(c, "当前团队为只读，或角色尚未加载；请联系管理员或刷新团队列表。").FontSize(12).Wrap()
		}
	}
	if d.cloud != nil && d.active.Password != "" {
		if d.currentCloudSource() != "" {
			ui.Text(c, "同步当前会话将更新它载入的云端连接。").FontSize(12).Wrap()
		} else {
			ui.Text(c, "同步当前会话将新建云端连接，不会按地址覆盖已有记录。").FontSize(12).Wrap()
		}
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		if ui.Button(c.Key("cloudsave"), "同步当前密码连接").Disabled(d.busy || !d.canWriteCloudScope() || d.cloudListStale || d.active.Password == "" || d.active.KeyPath != "").Clicked() {
			d.cloudSave()
		}
		if ui.Button(c.Key("cloudrefresh"), "刷新云端连接").Disabled(d.busy || d.cloud == nil).Clicked() {
			d.cloudRefresh()
		}
		if ui.Button(c.Key("clouddelete"), "删除选中云端连接（需确认）").Disabled(d.busy || !d.canWriteCloudScope() || d.cloudListStale || d.confirmCloudDelete == "").Clicked() {
			d.cloudDelete()
		}
	})
	if d.cloudListStale {
		ui.Text(c, "连接列表可能过期，请先刷新后再修改或删除。").FontSize(12).Wrap()
	}
	ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
		archiveLabel := "展开连接备份"
		if d.showArchive {
			archiveLabel = "收起连接备份"
		}
		if ui.Button(c.Key("toggle-archive"), archiveLabel).Clicked() {
			d.showArchive = !d.showArchive
		}
		teamLabel := "展开团队管理"
		if d.showTeams {
			teamLabel = "收起团队管理"
		}
		if ui.Button(c.Key("toggle-teams"), teamLabel).Clicked() {
			d.showTeams = !d.showTeams
		}
		adminLabel := "展开服务器管理"
		if d.showAdmin {
			adminLabel = "收起服务器管理"
		}
		if ui.Button(c.Key("toggle-admin"), adminLabel).Clicked() {
			d.showAdmin = !d.showAdmin
		}
	})
	if d.showArchive {
		d.cloudArchiveView(c)
	}
	if d.showTeams {
		d.teamView(c)
	}
	if d.showAdmin {
		d.adminView(c)
	}
	for _, item := range d.accounts {
		account := item
		row := ui.Button(c.Key("account-"+account.ID), account.Label).BorderWidth(0).Justify(ui.Start).Disabled(d.busy || d.connectionNavigationBlocked())
		row.ContextMenu(func(menu *ui.Menu) {
			if d.canWriteCloudScope() && menu.Item("删除…").Chosen() {
				d.prepareCloudDelete(account)
			}
		})
		if row.Clicked() {
			if d.prepareConnectionNavigation() {
				d.loadCloudAccount(account)
			} else {
				d.cloudStatus = d.status
			}
		}
	}
}

func (d *desktop) prepareCloudDelete(account cloudAccount) {
	if d.busy || d.cloudListStale || !d.canWriteCloudScope() {
		return
	}
	if d.cloud.capabilityUnknown {
		d.cloudStatus = "无法确认服务的并发保护能力，请刷新云端连接后重试。"
		return
	}
	if d.cloud.conditionalDeletes {
		if _, err := time.Parse(time.RFC3339Nano, account.UpdatedAt); err != nil {
			d.confirmCloudDelete, d.confirmCloudDeleteVersion = "", ""
			d.cloudStatus = "缺少有效的连接版本，请刷新后重新选择。"
			return
		}
	}
	d.confirmCloudDelete, d.confirmCloudDeleteVersion = account.ID, account.UpdatedAt
	d.cloudStatus = "请点击删除按钮确认：" + account.Label
}

func (d *desktop) cloudURLChanged() {
	if d.cloudRegistrationLocal && !localRegistrationAvailable(d.cloudURL) {
		d.cloudRegistration, d.cloudRegistrationLocal = "", false
		d.cloudStatus = "已清除本机注册令牌；它只能用于默认同步服务。"
	}
}

func (d *desktop) cloudDelete() {
	if d.busy || !d.canWriteCloudScope() || d.cloudListStale || d.confirmCloudDelete == "" {
		return
	}
	id, revision, client := d.confirmCloudDelete, d.confirmCloudDeleteVersion, d.cloud
	d.confirmCloudDelete, d.confirmCloudDeleteVersion = "", ""
	d.busy = true
	go func() {
		accounts, mutationErr, refreshErr := mutateAndRefresh(func() error { return client.removeWithRevision(id, revision) }, client.list)
		d.win.Update(func() { d.busy = false; d.finishCloudDelete(id, accounts, mutationErr, refreshErr) })
	}()
}
func (d *desktop) finishCloudDelete(id string, accounts []cloudAccount, mutationErr, refreshErr error) {
	d.clearCloudBatch()
	d.invalidateCloudGroups()
	if mutationErr != nil {
		d.cloudListStale = true
		var httpErr *cloudHTTPError
		if errors.As(mutationErr, &httpErr) && httpErr.Status == http.StatusConflict && httpErr.Code == "account_conflict" {
			d.cloudStatus = "连接已被其他设备修改；本次未删除，请刷新并重新选择。"
			return
		}
		d.cloudStatus = "删除请求未确认，可能已生效；请先刷新：" + mutationErr.Error()
		return
	}
	if d.selectedCloudProfile == id {
		d.selectedCloudProfile = ""
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
	d.clearCloudBatch()
	d.profileTagFilters, d.profileTagQuery, d.showProfileTagFilter = nil, "", false
	d.cloudGroupSnapshot, d.cloudGroupsStale = cloudGroupSnapshot{}, false
	d.cloudWorkspaceSnapshot, d.cloudWorkspacesStale, d.confirmCloudWorkspaceRemove = cloudWorkspaceSnapshot{}, false, ""
	if d.cloud != nil {
		d.cloud.token, d.cloud.adminTOTP = "", ""
	}
	d.cloud = nil
	d.selectedCloudProfile = ""
	if d.profileSource == "cloud" {
		d.profileSource = ""
		d.profileFilter = ""
	}
	d.cloudFoldedGroups = nil
	if d.settingsFromCloud {
		d.clearCloudFormCredentials()
	}
	d.cloudPassword, d.cloudRegistration = "", ""
	d.archivePassword, d.archiveStatus, d.archiveConfirm = "", "", false
	d.cloudRegistrationLocal, d.cloudListStale, d.teamListStale = false, false, false
	d.cloudFormSources = nil
	d.cloudFormVersions = nil
	for _, session := range d.sessions {
		session.cloudSources = nil
		session.cloudVersions = nil
	}
	d.cloudStatus, d.teamStatus = "", ""
	d.accounts, d.teams, d.members, d.teamInvites = nil, nil, nil, nil
	d.teamInviteURL, d.teamInviteInput, d.teamInvitePassword = "", "", ""
	d.confirmMember, d.selectedMember, d.confirmInvite, d.confirmCloudDelete, d.confirmCloudDeleteVersion = "", "", "", "", ""
	d.adminUsers, d.adminTeams = nil, nil
	d.adminConfirmTeam, d.adminConfirmUser, d.adminStatus, d.adminTOTP = "", "", "", ""
	d.adminConfirmRestore = false
}
