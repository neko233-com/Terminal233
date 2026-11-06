package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofrs/flock"
)

const maxWorkspaceTemplates = 100

type workspaceTemplateEntry struct {
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	Group       string `json:"group,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        string `json:"port,omitempty"`
	User        string `json:"user,omitempty"`
	KeyPath     string `json:"key_path,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	FileDir     string `json:"file_dir,omitempty"`
	LocalDir    string `json:"local_dir,omitempty"`
	CloudURL    string `json:"cloud_url,omitempty"`
	CloudUser   string `json:"cloud_user,omitempty"`
	CloudTeam   string `json:"cloud_team,omitempty"`
	CloudID     string `json:"cloud_id,omitempty"`
}

type savedWorkspace struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Entries     []workspaceTemplateEntry `json:"entries"`
	Active      int                      `json:"active"`
	SplitFirst  int                      `json:"split_first"`
	SplitSecond int                      `json:"split_second"`
	SplitLayout string                   `json:"split_layout,omitempty"`
	ViewMode    string                   `json:"view_mode,omitempty"`
	RowPerc     []float32                `json:"row_percent,omitempty"`
	ColumnPerc  []float32                `json:"column_percent,omitempty"`
}
type workspaceTemplatesFile struct {
	Version int              `json:"version"`
	Items   []savedWorkspace `json:"items"`
}

func validWorkspaceText(s string, max int) bool {
	return utf8.ValidString(s) && len(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}
func validateSavedWorkspace(w savedWorkspace) error {
	id, err := hex.DecodeString(w.ID)
	if err != nil || len(id) != 16 || strings.TrimSpace(w.Name) != w.Name || w.Name == "" || !validWorkspaceText(w.Name, 120) || len(w.Entries) == 0 || len(w.Entries) > maxWorkspaceSessions || w.Active < 0 || w.Active >= len(w.Entries) {
		return errors.New("工作区名称或会话无效")
	}
	if w.ViewMode != "" && w.ViewMode != "focus" && w.ViewMode != "grid" {
		return errors.New("工作区视图无效")
	}
	for _, values := range [][]float32{w.RowPerc, w.ColumnPerc} {
		if len(values) != 0 && len(values) != 12 {
			return errors.New("工作区分屏尺寸无效")
		}
		for _, value := range values {
			if value < 0 || value > 100 {
				return errors.New("工作区分屏尺寸无效")
			}
		}
	}
	if w.ViewMode == "" && (len(w.RowPerc) != 0 || len(w.ColumnPerc) != 0) {
		return errors.New("工作区视图无效")
	}
	if w.SplitLayout != "" && w.SplitLayout != "side" && w.SplitLayout != "stacked" {
		return errors.New("工作区布局无效")
	}
	splitNone := w.SplitFirst == -1 && w.SplitSecond == -1
	if w.ViewMode != "" && !splitNone {
		return errors.New("工作区分屏数据冲突")
	}
	if !splitNone && (w.SplitFirst < 0 || w.SplitSecond < 0 || w.SplitFirst >= len(w.Entries) || w.SplitSecond >= len(w.Entries) || w.SplitFirst == w.SplitSecond || (w.Active != w.SplitFirst && w.Active != w.SplitSecond)) {
		return errors.New("工作区分屏无效")
	}
	for _, e := range w.Entries {
		for _, s := range []string{e.Label, e.Group, e.Host, e.Port, e.User, e.KeyPath, e.Fingerprint, e.FileDir, e.LocalDir, e.CloudURL, e.CloudUser, e.CloudTeam, e.CloudID} {
			if !validWorkspaceText(s, 4096) {
				return errors.New("工作区内容无效")
			}
		}
		switch e.Kind {
		case "local":
			if e.Host != "" || e.Port != "" || e.User != "" || e.KeyPath != "" || e.Fingerprint != "" || e.CloudID != "" || e.CloudURL != "" || e.CloudUser != "" || e.CloudTeam != "" {
				return errors.New("本机会话内容无效")
			}
			if e.LocalDir != "" {
				cleaned, err := terminalDirectory(e.LocalDir, true)
				if err != nil || filepath.Clean(cleaned) != filepath.Clean(e.LocalDir) {
					return errors.New("本机终端目录无效")
				}
			}
		case "profile", "cloud":
			if _, err := (connectionSettings{Host: e.Host, Port: e.Port, User: e.User}).address(); err != nil || !strings.HasPrefix(e.Fingerprint, "SHA256:") || e.LocalDir != "" {
				return errors.New("SSH 会话信息无效")
			}
			if e.Kind == "profile" && (e.CloudURL != "" || e.CloudUser != "" || e.CloudTeam != "" || e.CloudID != "") {
				return errors.New("本机主机引用无效")
			}
			if e.Kind == "cloud" {
				if e.CloudURL == "" || e.CloudID == "" || e.CloudUser == "" || e.KeyPath != "" {
					return errors.New("云主机引用无效")
				}
				if _, err := newCloudClient(e.CloudURL); err != nil {
					return errors.New("云服务地址无效")
				}
			}
		default:
			return errors.New("不支持的会话类型")
		}
	}
	return nil
}

func readWorkspaceTemplates(filename string) (workspaceTemplatesFile, error) {
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return workspaceTemplatesFile{Version: 1}, nil
	}
	if err != nil {
		return workspaceTemplatesFile{}, err
	}
	if len(data) > 1<<20 {
		return workspaceTemplatesFile{}, errors.New("工作区文件过大")
	}
	var saved workspaceTemplatesFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&saved); err != nil {
		return workspaceTemplatesFile{}, errors.New("工作区文件格式无效")
	}
	if err = dec.Decode(&struct{}{}); err != io.EOF {
		return workspaceTemplatesFile{}, errors.New("工作区文件格式无效")
	}
	if (saved.Version != 1 && saved.Version != 2) || len(saved.Items) > maxWorkspaceTemplates {
		return workspaceTemplatesFile{}, errors.New("工作区版本或数量无效")
	}
	seen := map[string]bool{}
	for i, w := range saved.Items {
		if saved.Version == 1 && (w.ViewMode != "" || len(w.RowPerc) != 0 || len(w.ColumnPerc) != 0) {
			return workspaceTemplatesFile{}, errors.New("工作区版本与内容不符")
		}
		if validateSavedWorkspace(w) != nil || seen[w.ID] {
			return workspaceTemplatesFile{}, errors.New("工作区内容无效")
		}
		seen[w.ID] = true
		for _, previous := range saved.Items[:i] {
			if strings.EqualFold(previous.Name, w.Name) {
				return workspaceTemplatesFile{}, errors.New("工作区名称重复")
			}
		}
	}
	return saved, nil
}

func updateWorkspaceTemplates(filename string, update func(*workspaceTemplatesFile) error) (workspaceTemplatesFile, error) {
	if filename == "" {
		return workspaceTemplatesFile{}, errors.New("工作区存储不可用")
	}
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return workspaceTemplatesFile{}, err
	}
	lock := flock.New(filename + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return workspaceTemplatesFile{}, err
	}
	if !locked {
		return workspaceTemplatesFile{}, errors.New("另一个窗口正在保存工作区，请重试")
	}
	defer lock.Unlock()
	saved, err := readWorkspaceTemplates(filename)
	if err != nil {
		return workspaceTemplatesFile{}, err
	}
	if err = update(&saved); err != nil {
		return workspaceTemplatesFile{}, err
	}
	if len(saved.Items) > maxWorkspaceTemplates {
		return workspaceTemplatesFile{}, errors.New("工作区数量已达上限")
	}
	for _, w := range saved.Items {
		if w.ViewMode != "" {
			saved.Version = 2
		}
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return workspaceTemplatesFile{}, err
	}
	f, err := os.CreateTemp(dir, ".workspaces-*.tmp")
	if err != nil {
		return workspaceTemplatesFile{}, err
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
		return workspaceTemplatesFile{}, err
	}
	if closeErr != nil {
		return workspaceTemplatesFile{}, closeErr
	}
	if err = os.Rename(f.Name(), filename); err != nil {
		return workspaceTemplatesFile{}, err
	}
	return saved, nil
}

func profileCredential(p profile) (connectionSettings, error) {
	s := p.Settings
	if p.EncryptedCredentials != "" {
		password, passphrase, err := unprotectProfileCredentials(p.EncryptedCredentials, p.Settings)
		if err != nil {
			return connectionSettings{}, errors.New("无法解锁保存的主机凭据")
		}
		s.Password, s.Passphrase = password, passphrase
	}
	return s, nil
}

func profileMatchesEntry(p profile, e workspaceTemplateEntry) bool {
	s := p.Settings
	return p.Label == e.Label && p.Group == e.Group && s.Host == e.Host && s.Port == e.Port && s.User == e.User && s.KeyPath == e.KeyPath && s.Fingerprint == e.Fingerprint
}

func (d *desktop) workspaceSnapshot(name string) (savedWorkspace, error) {
	name = strings.TrimSpace(name)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return savedWorkspace{}, err
	}
	w := savedWorkspace{ID: hex.EncodeToString(id[:]), Name: name, Active: -1, SplitFirst: -1, SplitSecond: -1, SplitLayout: d.splitLayout}
	d.saveSessionState()
	sessions := d.sessions
	group := d.terminalGroupFor(d.currentSession)
	if group != nil {
		sessions = d.terminalGroupSessions(group)
		w.RowPerc = append([]float32(nil), group.rowPerc[:]...)
		w.ColumnPerc = append([]float32(nil), group.columnPerc[:]...)
		w.ViewMode = "focus"
		if group.grid {
			w.ViewMode = "grid"
		}
	}
	for i, s := range sessions {
		if s.term == nil || terminalEnded(s.term) {
			return savedWorkspace{}, fmt.Errorf("会话 %s 已结束，请先重新连接", s.label)
		}
		e := workspaceTemplateEntry{FileDir: s.fileDir}
		if s.shell == nil {
			e.Kind, e.Label = "local", s.label
			if current, err := terminalDirectory(s.term.Dir(), true); err == nil {
				e.LocalDir = current
			}
		} else if s.cloudOriginID != "" {
			e.Kind = "cloud"
			e.Host, e.Port, e.User, e.Fingerprint = s.active.Host, s.active.Port, s.active.User, s.active.Fingerprint
			e.CloudURL, e.CloudUser, e.CloudTeam, e.CloudID = s.cloudOriginURL, s.cloudOriginUser, s.cloudOriginTeam, s.cloudOriginID
		} else {
			e.Kind = "profile"
			e.Host, e.Port, e.User, e.KeyPath, e.Fingerprint = s.active.Host, s.active.Port, s.active.User, s.active.KeyPath, s.active.Fingerprint
			candidates := []profile{}
			for _, p := range d.profiles {
				if s.sourceProfileID != "" && string(p.ID) != s.sourceProfileID {
					continue
				}
				if p.Settings.Host != e.Host || p.Settings.Port != e.Port || p.Settings.User != e.User || p.Settings.KeyPath != e.KeyPath || p.Settings.Fingerprint != e.Fingerprint {
					continue
				}
				credentials, err := profileCredential(p)
				durable := p.EncryptedCredentials != "" || (credentials.KeyPath != "" && credentials.Passphrase == "")
				if err == nil && durable && credentials.Password == s.active.Password && credentials.Passphrase == s.active.Passphrase && (credentials.KeyPath != "" || credentials.Password != "") {
					candidates = append(candidates, p)
				}
			}
			if len(candidates) != 1 {
				return savedWorkspace{}, fmt.Errorf("会话 %s 需要唯一的已保存主机和可用凭据", s.label)
			}
			e.Label, e.Group = candidates[0].Label, candidates[0].Group
		}
		w.Entries = append(w.Entries, e)
		if s.id == d.currentSession {
			w.Active = i
		}
		if s.id == d.splitFirstSession {
			w.SplitFirst = i
		}
		if s.id == d.splitSession {
			w.SplitSecond = i
		}
	}
	if group != nil || d.splitSession == 0 {
		w.SplitFirst, w.SplitSecond = -1, -1
	} else if w.SplitFirst == w.SplitSecond {
		w.SplitSecond = w.Active
	} else if w.SplitFirst == -1 {
		w.SplitFirst = w.Active
	}
	if err := validateSavedWorkspace(w); err != nil {
		return savedWorkspace{}, err
	}
	return w, nil
}
