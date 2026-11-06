package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (d *desktop) quickPackageCards(c *ui.Context) {
	if len(d.quickPackages) == 0 || d.quickEditorOpen {
		return
	}
	ui.Row(c).FillWidth().Wrap().Gap(10).Children(func() {
		for _, group := range d.quickPackages {
			p := group
			count := len(filterQuickPackage(d.quickCommands, p.ID))
			card := ui.Button(c.Key("quick-package-card-"+p.ID), "").Label("打开片段分组：" + p.Name).Width(200).Height(66).Padding(10).Justify(ui.Start).Background(c.Theme().Surface).BorderWidth(0).Radius(10)
			if d.quickPackageFilter == p.ID {
				card = card.BorderWidth(1).BorderColor(c.Theme().Accent)
			}
			card.Children(func() {
				ui.Icon(c, fileIcon).Width(19).Height(19).TextColor(c.Theme().Accent)
				ui.Column(c).Grow(1).Padding(0, 8).Gap(3).Children(func() {
					ui.Text(c, p.Name).Bold().Wrap()
					ui.Text(c, fmt.Sprintf("%d 个片段", count)).FontSize(12).TextColor(c.Theme().TextMuted)
				})
			})
			if card.Clicked() {
				d.quickPackageFilter, d.quickPackageConfirm, d.quickPackageName = p.ID, false, ""
			}
			if dragged, ok := ui.Drop[quickSnippetDrag](card); ok {
				d.moveQuickCommands(dragged.Items, &p)
			}
		}
		card := ui.Button(c.Key("quick-package-card-ungrouped"), "").Label("打开未分组片段").Width(200).Height(66).Padding(10).Justify(ui.Start).Background(c.Theme().Surface).BorderWidth(0).Radius(10)
		if d.quickPackageFilter == "-" {
			card = card.BorderWidth(1).BorderColor(c.Theme().Accent)
		}
		card.Children(func() {
			ui.Icon(c, fileIcon).Width(19).Height(19).TextColor(c.Theme().TextMuted)
			ui.Column(c).Grow(1).Padding(0, 8).Gap(3).Children(func() {
				ui.Text(c, "未分组").Bold()
				ui.Text(c, fmt.Sprintf("%d 个片段", len(filterQuickPackage(d.quickCommands, "-")))).FontSize(12).TextColor(c.Theme().TextMuted)
			})
		})
		if card.Clicked() {
			d.quickPackageFilter, d.quickPackageConfirm, d.quickPackageName = "-", false, ""
		}
		if dragged, ok := ui.Drop[quickSnippetDrag](card); ok {
			d.moveQuickCommands(dragged.Items, nil)
		}
	})
}

func (d *desktop) quickBatchControls(c *ui.Context) {
	if !d.quickBatch {
		return
	}
	ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("已选择 %d 个片段", len(d.quickChecked))).FontSize(12)
		label := quickPackageLabel(d.quickPackages, d.quickMoveTarget, false)
		if ui.Select(c.Key("quick-move-target"), &label, quickPackageLabels(d.quickPackages, false)).Label("选择片段移动目标").Changed() {
			d.quickMoveTarget = quickPackageForLabel(d.quickPackages, label, false)
		}
		if ui.Button(c.Key("quick-move-selected"), "移动").Disabled(len(d.quickChecked) == 0).Clicked() {
			var target *quickPackage
			if d.quickMoveTarget != "" {
				if i := quickPackageIndex(d.quickPackages, d.quickMoveTarget); i >= 0 {
					p := d.quickPackages[i]
					target = &p
				} else {
					d.quickStatus = "目标分组不存在，请刷新后重试"
					return
				}
			}
			items := d.checkedQuickCommands()
			if len(items) != len(d.quickChecked) {
				d.quickStatus = "所选片段已变化，请刷新后重试"
				return
			}
			d.moveQuickCommands(items, target)
		}
	})
}
