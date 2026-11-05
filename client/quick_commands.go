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

type quickCommand struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

type quickCommandFile struct {
	Version int            `json:"version"`
	Items   []quickCommand `json:"items"`
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
	if !utf8.ValidString(name) || !utf8.ValidString(command) || strings.TrimSpace(name) == "" || strings.TrimSpace(command) == "" || len(name) > 120 || len(command) > 4096 || strings.IndexFunc(name, unicode.IsControl) >= 0 || strings.IndexFunc(command, unicode.IsControl) >= 0 {
		return errors.New("请输入名称和单行命令；名称最多 120 字节，命令最多 4096 字节")
	}
	return nil
}

func readQuickCommands(path string) ([]quickCommand, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 512<<10 {
		return nil, errors.New("快速命令文件过大")
	}
	var saved quickCommandFile
	if err := json.Unmarshal(data, &saved); err != nil || saved.Version != 1 || len(saved.Items) > maxQuickCommands {
		return nil, errors.New("快速命令文件格式无效")
	}
	seen := make(map[string]bool, len(saved.Items))
	for _, item := range saved.Items {
		id, err := hex.DecodeString(item.ID)
		if err != nil || len(id) != 16 || seen[item.ID] || validateQuickCommand(item.Name, item.Command) != nil {
			return nil, errors.New("快速命令文件内容无效")
		}
		seen[item.ID] = true
	}
	return saved.Items, nil
}

// Read and update under one lock so multiple windows do not lose each other's changes.
func updateQuickCommands(path string, update func([]quickCommand) ([]quickCommand, error)) ([]quickCommand, error) {
	if path == "" {
		return nil, errors.New("快速命令存储不可用")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("另一个窗口正在保存，请稍后重试")
	}
	defer lock.Unlock()
	items, err := readQuickCommands(path)
	if err != nil {
		return nil, err
	}
	items, err = update(items)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(quickCommandFile{Version: 1, Items: items}, "", "  ")
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".quick-commands-*.tmp")
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return nil, err
	}
	return items, nil
}

func newQuickCommand(name, command string) (quickCommand, error) {
	name = strings.TrimSpace(name)
	if err := validateQuickCommand(name, command); err != nil {
		return quickCommand{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return quickCommand{}, fmt.Errorf("无法生成命令编号：%w", err)
	}
	return quickCommand{ID: hex.EncodeToString(id[:]), Name: name, Command: command}, nil
}
