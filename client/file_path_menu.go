package main

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

type parentDirectory struct {
	label string
	path  string
}

// Nearest ancestors come first. Paths are derived only from a successfully
// opened directory and never from a partially typed address.
func parentDirectories(dir string, local bool) []parentDirectory {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	clean, parent, base := path.Clean, path.Dir, path.Base
	if local {
		clean, parent, base = filepath.Clean, filepath.Dir, filepath.Base
	}
	current := clean(dir)
	var result []parentDirectory
	for len(result) < 64 {
		next := parent(current)
		if next == current {
			break
		}
		label := base(next)
		if parent(next) == next {
			label = next
		}
		result = append(result, parentDirectory{label: label, path: next})
		current = next
	}
	return result
}

// This narrow menu keeps the editable path visible while allowing a single
// click to any ancestor, without adding a permanent row to the file pane.
func fileParentMenu(c *ui.Context, key, directory string, local, disabled bool, open *bool, navigate func(string)) {
	parents := parentDirectories(directory, local)
	if disabled || len(parents) == 0 {
		*open = false
	}
	button := ui.Button(c.Key(key+"-toggle"), "▾").Label("选择上级目录").Tooltip("选择上级目录").Width(32).Padding(0).Disabled(disabled || len(parents) == 0)
	if button.Clicked() {
		*open = !*open
	}
	ui.PopoverBase(c, button, open, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(4, 0, 0, 0).Children(func() {
			ui.Column(c).Width(260).Padding(6).Gap(2).Background(c.Theme().SurfaceHover).Radius(8).Children(func() {
				ui.Text(c, "上级目录").FontSize(12).TextColor(c.Theme().TextMuted).Padding(4, 8)
				ui.Scroll(c.Key(key + "-list")).Label("上级目录列表").Height(min(float32(len(parents))*36, 288)).FillWidth().Children(func() {
					for i, item := range parents {
						item := item
						row := ui.Button(c.Key(key+"-"+strconv.Itoa(i)), "").Label("前往目录："+item.path).Tooltip(item.path).FillWidth().Height(36).Padding(4, 8).BorderWidth(0).Justify(ui.Start).Background(ui.Transparent)
						row.Children(func() {
							ui.Icon(c, fileIcon).Width(16).Height(16).Shrink(0).TextColor(c.Theme().TextMuted)
							ui.Text(c, item.label).SingleLine().Grow(1)
						})
						if row.Clicked() {
							*open = false
							navigate(item.path)
						}
					}
				})
			})
		})
	})
}
