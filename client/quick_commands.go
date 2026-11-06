package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofrs/flock"
)

const maxQuickCommands = 100
const maxQuickPackages = 100

type quickCommand struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	PackageID string `json:"package_id,omitempty"`
}

type quickPackage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type quickCommandFile struct {
	Version  int            `json:"version"`
	Items    []quickCommand `json:"items"`
	Packages []quickPackage `json:"packages,omitempty"`
}

func filterQuickCommands(items []quickCommand, query string) []quickCommand {
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]quickCommand, 0, len(items))
	for _, item := range items {
		if query == "" || strings.Contains(strings.ToLower(item.Name), query) || strings.Contains(strings.ToLower(item.Command), query) {
			result = append(result, item)
		}
	}
	return result
}

func validateQuickCommand(name, command string) error {
	invalidControl := func(r rune) bool { return unicode.IsControl(r) && r != '\n' }
	if !utf8.ValidString(name) || !utf8.ValidString(command) || strings.TrimSpace(name) == "" || strings.TrimSpace(command) == "" || len(name) > 120 || len(command) > 4096 || strings.IndexFunc(name, unicode.IsControl) >= 0 || strings.IndexFunc(command, invalidControl) >= 0 {
		return errors.New("请输入名称和命令或脚本；名称最多 120 字节，内容最多 4096 字节，请使用空格缩进")
	}
	return nil
}

func readQuickCommands(path string) ([]quickCommand, error) {
	saved, err := readQuickCommandFile(path)
	return saved.Items, err
}

func readQuickCommandFile(path string) (quickCommandFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return quickCommandFile{Version: 1}, nil
	}
	if err != nil {
		return quickCommandFile{}, err
	}
	if len(data) > 512<<10 {
		return quickCommandFile{}, errors.New("快速命令文件过大")
	}
	var saved quickCommandFile
	if err := json.Unmarshal(data, &saved); err != nil || (saved.Version < 1 || saved.Version > 3) || len(saved.Items) > maxQuickCommands || len(saved.Packages) > maxQuickPackages || (saved.Version < 3 && len(saved.Packages) != 0) {
		return quickCommandFile{}, errors.New("快速命令文件格式无效")
	}
	packageIDs := make(map[string]bool, len(saved.Packages))
	for i, p := range saved.Packages {
		id, err := hex.DecodeString(p.ID)
		if err != nil || len(id) != 16 || packageIDs[p.ID] || p.Name != strings.TrimSpace(p.Name) || validateQuickPackageName(p.Name) != nil {
			return quickCommandFile{}, errors.New("命令分组内容无效")
		}
		for _, previous := range saved.Packages[:i] {
			if strings.EqualFold(previous.Name, p.Name) {
				return quickCommandFile{}, errors.New("命令分组名称重复")
			}
		}
		packageIDs[p.ID] = true
	}
	seen := make(map[string]bool, len(saved.Items))
	for _, item := range saved.Items {
		id, err := hex.DecodeString(item.ID)
		if err != nil || len(id) != 16 || seen[item.ID] || validateQuickCommand(item.Name, item.Command) != nil || (saved.Version == 1 && strings.Contains(item.Command, "\n")) || (item.PackageID != "" && (saved.Version < 3 || !packageIDs[item.PackageID])) {
			return quickCommandFile{}, errors.New("快速命令文件内容无效")
		}
		seen[item.ID] = true
	}
	return saved, nil
}

// Read and update under one lock so multiple windows do not lose each other's changes.
func updateQuickCommands(path string, update func([]quickCommand) ([]quickCommand, error)) ([]quickCommand, error) {
	saved, err := updateQuickCommandFile(path, func(saved *quickCommandFile) error {
		items, err := update(saved.Items)
		if err == nil {
			saved.Items = items
		}
		return err
	})
	return saved.Items, err
}

func updateQuickCommandFile(path string, update func(*quickCommandFile) error) (quickCommandFile, error) {
	if path == "" {
		return quickCommandFile{}, errors.New("快速命令存储不可用")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return quickCommandFile{}, err
	}
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return quickCommandFile{}, err
	}
	if !locked {
		return quickCommandFile{}, errors.New("另一个窗口正在保存，请稍后重试")
	}
	defer lock.Unlock()
	saved, err := readQuickCommandFile(path)
	if err != nil {
		return quickCommandFile{}, err
	}
	if err := update(&saved); err != nil {
		return quickCommandFile{}, err
	}
	for _, item := range saved.Items {
		if strings.Contains(item.Command, "\n") {
			saved.Version = max(saved.Version, 2)
		}
		if item.PackageID != "" {
			saved.Version = 3
		}
	}
	if len(saved.Packages) > 0 {
		saved.Version = 3
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return quickCommandFile{}, err
	}
	f, err := os.CreateTemp(dir, ".quick-commands-*.tmp")
	if err != nil {
		return quickCommandFile{}, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return quickCommandFile{}, err
	}
	if closeErr != nil {
		return quickCommandFile{}, closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return quickCommandFile{}, err
	}
	return saved, nil
}

func newQuickCommand(name, command string) (quickCommand, error) {
	name = strings.TrimSpace(name)
	command = strings.ReplaceAll(command, "\r\n", "\n")
	if err := validateQuickCommand(name, command); err != nil {
		return quickCommand{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return quickCommand{}, fmt.Errorf("无法生成命令编号：%w", err)
	}
	return quickCommand{ID: hex.EncodeToString(id[:]), Name: name, Command: command}, nil
}
