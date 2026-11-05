package main

import "github.com/egoist/mygo/plugins/terminal"

const defaultTerminalFontSize = 13
const minTerminalFontSize = 9
const maxTerminalFontSize = 32

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
	return terminal.Options{NoClipboardWrite: true, ImageLimit: -1, Font: terminal.Font{Size: float32(d.currentTerminalFontSize())}, OnZoom: d.changeTerminalFont}
}
