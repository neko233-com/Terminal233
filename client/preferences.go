package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// Preferences contain only presentation choices, never connection credentials.
type workspacePreferences struct {
	Version                int     `json:"version"`
	Light                  bool    `json:"light"`
	SidebarCollapsed       bool    `json:"sidebar_collapsed"`
	ShowFiles              bool    `json:"show_files"`
	TerminalFontSize       int     `json:"terminal_font_size"`
	TerminalTheme          string  `json:"terminal_theme,omitempty"`
	ExternalEditor         string  `json:"external_editor,omitempty"`
	SplitLayout            string  `json:"split_layout,omitempty"`
	TerminalFilePercent    float32 `json:"terminal_file_percent,omitempty"`
	SplitHorizontalPercent float32 `json:"split_horizontal_percent,omitempty"`
	SplitVerticalPercent   float32 `json:"split_vertical_percent,omitempty"`
}

func readPreferences(path string) (workspacePreferences, error) {
	p := workspacePreferences{Version: 1, TerminalFontSize: defaultTerminalFontSize}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if len(data) > 64<<10 {
		return p, errors.New("设置文件过大")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("设置格式无效：%w", err)
	}
	if p.Version != 1 {
		return p, errors.New("不支持的设置版本")
	}
	if p.TerminalFontSize < minTerminalFontSize || p.TerminalFontSize > maxTerminalFontSize {
		return p, errors.New("终端字号无效")
	}
	if !validTerminalTheme(p.TerminalTheme) {
		return p, errors.New("终端配色无效")
	}
	if p.ExternalEditor != "" && !validEditorPath(p.ExternalEditor) {
		return p, errors.New("外部编辑器路径无效")
	}
	if p.SplitLayout != "" && p.SplitLayout != "side" && p.SplitLayout != "stacked" {
		return p, errors.New("分屏布局无效")
	}
	for _, value := range []float32{p.TerminalFilePercent, p.SplitHorizontalPercent, p.SplitVerticalPercent} {
		if value < 0 || value > 100 {
			return p, errors.New("面板尺寸无效")
		}
	}
	return p, nil
}

func (d *desktop) loadPreferences(path string) error {
	d.preferencesPath = path
	p, err := readPreferences(path)
	if err != nil {
		return err
	}
	d.lightTheme, d.sidebarCollapsed, d.showFilePanel = p.Light, p.SidebarCollapsed, p.ShowFiles
	d.externalEditorPath = p.ExternalEditor
	d.terminalFontSize = p.TerminalFontSize
	d.splitLayout = p.SplitLayout
	d.terminalFilePercent, d.splitHorizontalPercent, d.splitVerticalPercent = p.TerminalFilePercent, p.SplitHorizontalPercent, p.SplitVerticalPercent
	light, dark, err := terminalThemePair(p.TerminalTheme)
	if err != nil {
		return err
	}
	d.terminalTheme, d.terminalLightTheme, d.terminalDarkTheme = p.TerminalTheme, light, dark
	return nil
}

// Layout dimensions are saved explicitly, rather than synchronously writing
// preferences for every pointer movement while a divider is dragged.
func (d *desktop) savePanelSizes() {
	if d.preferencesPath == "" {
		d.preferencesStatus = "界面设置位置不可用，无法保存面板尺寸。"
		return
	}
	err := updatePreferences(d.preferencesPath, func(p *workspacePreferences) error {
		for _, value := range []float32{d.terminalFilePercent, d.splitHorizontalPercent, d.splitVerticalPercent} {
			if value < 0 || value > 100 {
				return errors.New("面板尺寸无效")
			}
		}
		p.TerminalFilePercent, p.SplitHorizontalPercent, p.SplitVerticalPercent = d.terminalFilePercent, d.splitHorizontalPercent, d.splitVerticalPercent
		return nil
	})
	if err != nil {
		d.preferencesStatus = "面板尺寸未保存：" + err.Error()
	} else {
		d.preferencesStatus = "面板尺寸已保存"
	}
}

// Merge just the changed field while holding a process lock, so two windows
// changing different options cannot silently overwrite each other's choices.
func (d *desktop) savePreference(field string, value bool) {
	if d.preferencesPath == "" {
		return
	}
	if err := writePreference(d.preferencesPath, field, value); err != nil {
		d.preferencesStatus = "界面设置未保存：" + err.Error()
	} else {
		d.preferencesStatus = ""
	}
}

func writePreference(path, field string, value bool) error {
	return updatePreferences(path, func(p *workspacePreferences) error {
		switch field {
		case "light":
			p.Light = value
		case "sidebar":
			p.SidebarCollapsed = value
		case "files":
			p.ShowFiles = value
		default:
			return errors.New("未知设置")
		}
		return nil
	})
}

func writeFontPreference(path string, size int) error {
	if size < minTerminalFontSize || size > maxTerminalFontSize {
		return errors.New("终端字号无效")
	}
	return updatePreferences(path, func(p *workspacePreferences) error { p.TerminalFontSize = size; return nil })
}

func writeTerminalThemePreference(path, theme string) error {
	if !validTerminalTheme(theme) {
		return errors.New("终端配色无效")
	}
	return updatePreferences(path, func(p *workspacePreferences) error { p.TerminalTheme = theme; return nil })
}

func updatePreferences(path string, change func(*workspacePreferences) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("另一个窗口正在保存，请稍后重试")
	}
	defer lock.Unlock()
	p, err := readPreferences(path)
	if err != nil {
		return err
	}
	if err = change(&p); err != nil {
		return err
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".preferences-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
