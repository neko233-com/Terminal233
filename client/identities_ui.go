package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
)

func (d *desktop) identitySelector(c *ui.Context, changed func()) {
	choices := []string{"主机专用"}
	selected := choices[0]
	for _, i := range d.identities {
		label := "身份 · " + i.Name
		choices = append(choices, label)
		if d.profileIdentityID == i.ID {
			selected = label
		}
	}
	if ui.Select(c.Key("host-identity"), &selected, choices).Label("凭据来源").FillWidth().Changed() {
		id := ""
		for _, i := range d.identities {
			if selected == "身份 · "+i.Name {
				id = i.ID
				break
			}
		}
		if err := d.chooseProfileIdentity(id); err != nil {
			d.status = err.Error()
		} else {
			changed()
		}
	}
	if ui.Button(c.Key("manage-identities-from-host"), "管理身份").Disabled(d.busy).Clicked() {
		if d.closeConnectionForNavigation() {
			d.workspace = "identities"
		}
	}
}

func (d *desktop) identityView(c *ui.Context) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, "身份").FontSize(22).Bold().Grow(1)
		if ui.PrimaryButton(c.Key("identity-new"), "+ 新建身份").Disabled(d.busy || d.identityEditing != "").Clicked() {
			d.identityEditing, d.identityDraft, d.identityStatus, d.identityConfirmDelete = "new", identityDraft{}, "", ""
		}
	})
	ui.Text(c, "在多个本机主机中复用用户名、密码和私钥。").FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
	if d.identityStatus != "" {
		ui.Text(c, d.identityStatus).Wrap()
	}
	if d.identityEditing != "" {
		draft := &d.identityDraft
		ui.Column(c).FillWidth().MaxWidth(560).Padding(16).Gap(8).Background(c.Theme().Surface).Radius(10).Children(func() {
			title := "编辑身份"
			if d.identityEditing == "new" {
				title = "新建身份"
			}
			ui.Text(c, title).FontSize(16).Bold()
			ui.TextInput(c.Key("identity-name"), &draft.Name).Label("身份名称").Placeholder("例如 工作账号").FillWidth()
			ui.TextInput(c.Key("identity-user"), &draft.User).Label("身份用户名").Placeholder("用户名").FillWidth()
			ui.TextInput(c.Key("identity-password"), &draft.Password).Label("身份密码").Placeholder("密码（可选）").Password().FillWidth()
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				ui.TextInput(c.Key("identity-key"), &draft.KeyPath).Label("身份私钥路径").Placeholder("私钥文件路径（可选）").Grow(1)
				if ui.Button(c.Key("identity-key-pick"), "选择…").Disabled(d.busy).Clicked() {
					d.pickLocalPath("identity-key")
				}
			})
			ui.TextInput(c.Key("identity-passphrase"), &draft.Passphrase).Label("身份私钥口令").Placeholder("私钥口令（可选）").Password().FillWidth()
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c.Key("identity-save"), "保存身份").Disabled(d.busy).Clicked() {
					id := d.identityEditing
					if id == "new" {
						id = ""
					}
					if err := d.saveIdentity(id, *draft); err != nil {
						d.identityStatus = "保存失败：" + err.Error()
					} else {
						d.identityDraft, d.identityEditing, d.identityStatus = identityDraft{}, "", "身份已保存"
					}
				}
				if ui.Button(c.Key("identity-cancel"), "取消").Clicked() {
					d.identityDraft, d.identityEditing, d.identityStatus = identityDraft{}, "", ""
				}
			})
		})
	}
	if len(d.identities) == 0 && d.identityEditing == "" {
		ui.Text(c, "还没有身份").TextColor(c.Theme().TextMuted)
	}
	for _, identity := range d.identities {
		i := identity
		count := 0
		for _, p := range d.profiles {
			if p.IdentityID == i.ID {
				count++
			}
		}
		ui.Row(c.Key("identity-row-" + i.ID)).FillWidth().MaxWidth(700).Padding(12).Gap(10).AlignItems(ui.Center).Background(c.Theme().Surface).Radius(8).Children(func() {
			ui.Column(c).Grow(1).Gap(3).Children(func() {
				ui.Text(c, i.Name).Bold()
				ui.Text(c, fmt.Sprintf("%s · %d 个主机", i.User, count)).FontSize(12).TextColor(c.Theme().TextMuted)
			})
			if ui.Button(c.Key("identity-edit-"+i.ID), "编辑").Disabled(d.busy || d.identityEditing != "").Clicked() {
				password, phrase, err := identityCredentials(i)
				if err != nil {
					d.identityStatus = "无法解锁身份，原数据已保留。"
				} else {
					d.identityDraft = identityDraft{Name: i.Name, User: i.User, KeyPath: i.KeyPath, Password: password, Passphrase: phrase}
					d.identityEditing, d.identityConfirmDelete, d.identityStatus = i.ID, "", ""
				}
			}
			label := "删除"
			if d.identityConfirmDelete == i.ID {
				label = "确认删除"
			}
			if ui.Button(c.Key("identity-delete-"+i.ID), label).Disabled(d.busy || d.identityEditing != "" || count > 0).Tooltip("使用中的身份需要先解除主机关联").Clicked() {
				if d.identityConfirmDelete != i.ID {
					d.identityConfirmDelete = i.ID
				} else if err := d.deleteIdentity(i.ID); err != nil {
					d.identityStatus = "删除失败：" + err.Error()
				} else {
					d.identityStatus, d.identityConfirmDelete = "身份已删除", ""
				}
			}
		})
	}
}
