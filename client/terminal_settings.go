package main

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
)

const defaultTerminalFontSize = 13
const minTerminalFontSize = 9
const maxTerminalFontSize = 32

var terminalThemes = []struct{ id, label, light, dark string }{
	{"", "跟随界面", "", ""},
	{"catppuccin", "Catppuccin", "Catppuccin Latte", "Catppuccin Mocha"},
	{"dracula", "Dracula", "Dracula", "Dracula"},
	{"nord", "Nord", "Nord", "Nord"},
}

func validTerminalTheme(id string) bool {
	for _, theme := range terminalThemes {
		if theme.id == id {
			return true
		}
	}
	return false
}

func terminalThemeLabel(id string) string {
	for _, theme := range terminalThemes {
		if theme.id == id {
			return theme.label
		}
	}
	return terminalThemes[0].label
}

func terminalThemeID(label string) string {
	for _, theme := range terminalThemes {
		if theme.label == label {
			return theme.id
		}
	}
	return ""
}

func terminalThemeLabels() []string {
	labels := make([]string, 0, len(terminalThemes))
	for _, theme := range terminalThemes {
		labels = append(labels, theme.label)
	}
	return labels
}

func terminalThemePair(id string) (*terminal.Theme, *terminal.Theme, error) {
	for _, choice := range terminalThemes {
		if choice.id != id {
			continue
		}
		if choice.id == "" {
			return nil, nil, nil
		}
		light, err := terminal.GhosttyTheme(choice.light)
		if err != nil {
			return nil, nil, fmt.Errorf("无法加载终端配色：%w", err)
		}
		if choice.dark == choice.light {
			return light, light, nil
		}
		dark, err := terminal.GhosttyTheme(choice.dark)
		if err != nil {
			return nil, nil, fmt.Errorf("无法加载终端配色：%w", err)
		}
		return light, dark, nil
	}
	return nil, nil, fmt.Errorf("终端配色无效")
}

func (d *desktop) changeTerminalTheme(id string) {
	light, dark, err := terminalThemePair(id)
	if err != nil {
		d.preferencesStatus = err.Error()
		return
	}
	d.terminalTheme, d.terminalLightTheme, d.terminalDarkTheme = id, light, dark
	for _, session := range d.sessions {
		if session.term != nil {
			session.term.SetTheme(light, dark)
		}
	}
	if d.preferencesPath != "" {
		if err := writeTerminalThemePreference(d.preferencesPath, id); err != nil {
			d.preferencesStatus = "终端配色未保存：" + err.Error()
		} else {
			d.preferencesStatus = ""
		}
	}
}

func (d *desktop) currentTerminalFontSize() int {
	if d.terminalFontSize < minTerminalFontSize || d.terminalFontSize > maxTerminalFontSize {
		return defaultTerminalFontSize
	}
	return d.terminalFontSize
}

func (d *desktop) changeTerminalFont(z terminal.Zoom) bool {
	size := d.currentTerminalFontSize()
	if z.Reset {
		size = defaultTerminalFontSize
	} else {
		size += z.Steps
	}
	if size < minTerminalFontSize {
		size = minTerminalFontSize
	}
	if size > maxTerminalFontSize {
		size = maxTerminalFontSize
	}
	if size == d.currentTerminalFontSize() {
		return true
	}
	d.terminalFontSize = size
	for _, session := range d.sessions {
		if session.term != nil {
			session.term.SetFont(terminal.Font{Size: float32(size)})
		}
	}
	if d.preferencesPath != "" {
		if err := writeFontPreference(d.preferencesPath, size); err != nil {
			d.preferencesStatus = "界面设置未保存：" + err.Error()
		} else {
			d.preferencesStatus = ""
		}
	}
	return true
}

func (d *desktop) terminalOptions() terminal.Options {
	return terminal.Options{NoClipboardWrite: true, ImageLimit: -1, Font: terminal.Font{Size: float32(d.currentTerminalFontSize())}, Theme: d.terminalLightTheme, DarkTheme: d.terminalDarkTheme, OnZoom: d.changeTerminalFont}
}
