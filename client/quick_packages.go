package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type quickSnippetDrag struct{ Items []quickCommand }

func (d *desktop) checkedQuickCommands() []quickCommand {
	items := make([]quickCommand, 0, len(d.quickChecked))
	for _, current := range d.quickCommands {
		if item, ok := d.quickChecked[current.ID]; ok {
			items = append(items, item)
		}
	}
	return items
}

// Move all snapshots together under the file lock. A stale selection or
// destination aborts the entire move and leaves the on-disk file untouched.
func (d *desktop) moveQuickCommands(items []quickCommand, destination *quickPackage) {
	if len(items) == 0 {
		d.quickStatus = "请先选择片段"
		return
	}
	target := ""
	if destination != nil {
		target = destination.ID
	}
	saved, err := updateQuickCommandFile(d.quickPath, func(saved *quickCommandFile) error {
		if destination != nil {
			i := quickPackageIndex(saved.Packages, destination.ID)
			if i < 0 || saved.Packages[i] != *destination {
				return errors.New("目标分组已在其他窗口修改，请刷新后重试")
			}
		}
		indices := make([]int, 0, len(items))
		seen := make(map[string]bool, len(items))
		for _, item := range items {
			i := slices.IndexFunc(saved.Items, func(current quickCommand) bool { return current.ID == item.ID })
			if seen[item.ID] || i < 0 || saved.Items[i] != item {
				return errors.New("片段已在其他窗口修改，请刷新后重试")
			}
			seen[item.ID] = true
			indices = append(indices, i)
		}
		for _, i := range indices {
			saved.Items[i].PackageID = target
		}
		return nil
	})
	if err != nil {
		d.quickStatus = "移动失败：" + err.Error()
		return
	}
	d.quickCommands, d.quickPackages = saved.Items, saved.Packages
	for _, item := range items {
		delete(d.quickChecked, item.ID)
	}
	d.quickStatus = fmt.Sprintf("已移动 %d 个片段", len(items))
}

func quickPackageIndex(packages []quickPackage, id string) int {
	return slices.IndexFunc(packages, func(p quickPackage) bool { return p.ID == id })
}

func quickPackageLabels(packages []quickPackage, all bool) []string {
	labels := []string{"未分组"}
	if all {
		labels = append([]string{"全部分组"}, labels...)
	}
	for _, p := range packages {
		labels = append(labels, "分组 / "+p.Name)
	}
	return labels
}

func quickPackageLabel(packages []quickPackage, id string, all bool) string {
	if all && id == "" {
		return "全部分组"
	}
	if i := quickPackageIndex(packages, id); i >= 0 {
		return "分组 / " + packages[i].Name
	}
	return "未分组"
}

func quickPackageForLabel(packages []quickPackage, label string, all bool) string {
	if label == "全部分组" && all {
		return ""
	}
	for _, p := range packages {
		if "分组 / "+p.Name == label {
			return p.ID
		}
	}
	if all {
		return "-"
	}
	return ""
}

func filterQuickPackage(items []quickCommand, id string) []quickCommand {
	if id == "" {
		return items
	}
	if id == "-" {
		id = ""
	}
	result := make([]quickCommand, 0, len(items))
	for _, item := range items {
		if item.PackageID == id {
			result = append(result, item)
		}
	}
	return result
}

func validateQuickPackageName(name string) error {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || len(name) > 120 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("请输入分组名称，最多 120 字节")
	}
	return nil
}

func (d *desktop) saveQuickPackage(rename bool) {
	name := strings.TrimSpace(d.quickPackageName)
	if err := validateQuickPackageName(name); err != nil {
		d.quickStatus = err.Error()
		return
	}
	var expected quickPackage
	if rename {
		i := quickPackageIndex(d.quickPackages, d.quickPackageFilter)
		if i < 0 {
			d.quickStatus = "请先选择要重命名的分组"
			return
		}
		expected = d.quickPackages[i]
	}
	var id [16]byte
	if !rename {
		if _, err := rand.Read(id[:]); err != nil {
			d.quickStatus = "无法生成分组编号"
			return
		}
	}
	newID := hex.EncodeToString(id[:])
	saved, err := updateQuickCommandFile(d.quickPath, func(saved *quickCommandFile) error {
		for _, p := range saved.Packages {
			if p.ID != expected.ID && strings.EqualFold(p.Name, name) {
				return errors.New("已有同名分组")
			}
		}
		if rename {
			i := quickPackageIndex(saved.Packages, expected.ID)
			if i < 0 || saved.Packages[i] != expected {
				return errors.New("分组已在其他窗口修改，请刷新后重试")
			}
			saved.Packages[i].Name = name
			newID = expected.ID
		} else {
			if len(saved.Packages) >= maxQuickPackages {
				return errors.New("命令分组已达到 100 个")
			}
			saved.Packages = append(saved.Packages, quickPackage{ID: newID, Name: name})
		}
		return nil
	})
	if err != nil {
		d.quickStatus = "保存分组失败：" + err.Error()
		return
	}
	d.quickCommands, d.quickPackages = saved.Items, saved.Packages
	d.quickPackageFilter, d.quickPackageName, d.quickPackageConfirm = newID, "", false
	d.quickStatus = "分组已保存"
}

func (d *desktop) deleteQuickPackage() {
	i := quickPackageIndex(d.quickPackages, d.quickPackageFilter)
	if i < 0 {
		return
	}
	if !d.quickPackageConfirm {
		d.quickPackageConfirm = true
		d.quickStatus = "再次确认移除分组；其中的片段会保留在未分组"
		return
	}
	expected := d.quickPackages[i]
	expectedItems := filterQuickPackage(d.quickCommands, expected.ID)
	saved, err := updateQuickCommandFile(d.quickPath, func(saved *quickCommandFile) error {
		i := quickPackageIndex(saved.Packages, expected.ID)
		if i < 0 || saved.Packages[i] != expected || !slices.Equal(expectedItems, filterQuickPackage(saved.Items, expected.ID)) {
			return errors.New("分组或其中片段已在其他窗口修改，请刷新后重试")
		}
		saved.Packages = append(saved.Packages[:i], saved.Packages[i+1:]...)
		for i := range saved.Items {
			if saved.Items[i].PackageID == expected.ID {
				saved.Items[i].PackageID = ""
			}
		}
		return nil
	})
	d.quickPackageConfirm = false
	if err != nil {
		d.quickStatus = "移除分组失败：" + err.Error()
		return
	}
	d.quickCommands, d.quickPackages = saved.Items, saved.Packages
	d.quickPackageFilter, d.quickPackageName = "", ""
	d.quickStatus = "分组已移除，片段保留在未分组"
}
