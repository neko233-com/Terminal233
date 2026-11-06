package main

import (
	"fmt"
	"sort"

	"github.com/egoist/mygo/ui"
)

type paneSelection struct {
	names  map[string]bool
	types  map[string]bool
	anchor string
}

type paneCopyItem struct {
	name      string
	directory bool
}

// Normal click selects one item; Ctrl toggles and Shift selects a visible
// range. Work on a copy so a selection over the limit leaves the old one.
func (s *paneSelection) choose(name string, files []remoteFile, mods ui.Modifiers) (string, error) {
	selected := make(map[string]bool, len(s.names)+1)
	types := make(map[string]bool, len(s.names)+1)
	if mods&ui.Cmd != 0 {
		for key, value := range s.names {
			if value {
				selected[key] = true
				types[key] = s.types[key]
			}
		}
	}
	index, anchor := -1, -1
	for i, f := range files {
		if f.Name == name {
			index = i
		}
		if f.Name == s.anchor {
			anchor = i
		}
	}
	if index < 0 {
		return "", fmt.Errorf("文件列表已改变，请重新选择")
	}
	if mods&ui.Shift != 0 && anchor >= 0 {
		lo, hi := anchor, index
		if lo > hi {
			lo, hi = hi, lo
		}
		for _, f := range files[lo : hi+1] {
			if eligiblePaneItem(f) {
				selected[f.Name] = true
				types[f.Name] = f.IsDir
			}
		}
	} else if eligiblePaneItem(files[index]) {
		if mods&ui.Cmd != 0 && selected[name] {
			delete(selected, name)
			delete(types, name)
		} else {
			selected[name] = true
			types[name] = files[index].IsDir
		}
	}
	if len(selected) > maxBatchFiles {
		return "", fmt.Errorf("每次最多选择 %d 项", maxBatchFiles)
	}
	s.names = selected
	s.types = types
	if mods&ui.Shift == 0 || anchor < 0 {
		s.anchor = name
	}
	if mods == 0 && !eligiblePaneItem(files[index]) {
		return name, nil
	}
	if selected[name] {
		return name, nil
	}
	for _, f := range files {
		if selected[f.Name] {
			return f.Name, nil
		}
	}
	for key := range selected {
		return key, nil
	}
	return "", nil
}

func (s paneSelection) itemsFor(name string, files []remoteFile) []paneCopyItem {
	for _, f := range files {
		if f.Name == name {
			return s.itemsForFile(f)
		}
	}
	return nil
}

func (s paneSelection) itemsForFile(file remoteFile) []paneCopyItem {
	if !eligiblePaneItem(file) {
		return nil
	}
	if !s.names[file.Name] || len(s.names) < 2 {
		return []paneCopyItem{{name: file.Name, directory: file.IsDir}}
	}
	items := make([]paneCopyItem, 0, len(s.names))
	for name := range s.names {
		directory, ok := s.types[name]
		if !ok {
			return nil
		}
		items = append(items, paneCopyItem{name: name, directory: directory})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	return items
}

func paneItemsMatch(items []paneCopyItem, files []remoteFile) bool {
	if len(items) == 0 || len(items) > maxBatchFiles {
		return false
	}
	seen := make(map[string]bool, len(items))
	available := make(map[string]bool, len(files))
	for _, file := range files {
		if eligiblePaneItem(file) {
			available[file.Name] = file.IsDir
		}
	}
	for _, item := range items {
		if seen[item.name] {
			return false
		}
		seen[item.name] = true
		directory, found := available[item.name]
		if !found || directory != item.directory {
			return false
		}
	}
	return true
}

func paneItemsInclude(items []paneCopyItem, name string) bool {
	for _, item := range items {
		if item.name == name {
			return true
		}
	}
	return false
}
