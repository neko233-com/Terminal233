package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/crypto/ssh"
)

const maxSavedForwardRules = 1000

type savedForwardRule struct {
	ID, Name, HostID, Mode, ListenPort, TargetHost, TargetPort string
}
type forwardRulesFile struct {
	Version int
	Items   []savedForwardRule
}
type savedForwardRun struct {
	cancel  context.CancelFunc
	client  *ssh.Client
	forward *localForward
	hostID  string
	binding [32]byte
	rule    savedForwardRule
}

func validRandomID(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == value
}
func newRandomID() (string, error) {
	var id [16]byte
	_, err := rand.Read(id[:])
	return hex.EncodeToString(id[:]), err
}

func validateForwardRule(r savedForwardRule) error {
	port, err := strconv.Atoi(r.ListenPort)
	if !validRandomID(r.ID) || !validRandomID(r.HostID) || strings.TrimSpace(r.Name) != r.Name || r.Name == "" || !validWorkspaceText(r.Name, 120) || !validWorkspaceText(r.TargetHost, 1024) || !validWorkspaceText(r.TargetPort, 8) || err != nil || port < 0 || port > 65535 || strconv.Itoa(port) != r.ListenPort {
		return errors.New("转发规则名称、主机或监听端口无效")
	}
	switch r.Mode {
	case "local", "remote":
		if _, err := parseForwardTarget(r.TargetHost, r.TargetPort); err != nil {
			return err
		}
	case "dynamic":
		if r.TargetHost != "" || r.TargetPort != "" {
			return errors.New("动态转发不能指定固定目标")
		}
	default:
		return errors.New("转发类型无效")
	}
	return nil
}

func readForwardRules(filename string) (forwardRulesFile, error) {
	if filename == "" {
		return forwardRulesFile{}, errors.New("转发规则存储不可用")
	}
	if err := checkLocalImportPending(filename); err != nil {
		return forwardRulesFile{}, err
	}
	saved, err := readForwardRulesUnchecked(filename)
	if err != nil {
		return saved, err
	}
	if err := checkLocalImportPending(filename); err != nil {
		return forwardRulesFile{}, err
	}
	return saved, nil
}

func readForwardRulesUnchecked(filename string) (forwardRulesFile, error) {
	var saved forwardRulesFile
	if filename == "" {
		return saved, errors.New("转发规则存储不可用")
	}
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return forwardRulesFile{Version: 1}, nil
	}
	if err != nil {
		return saved, err
	}
	if len(data) > 2<<20 {
		return saved, errors.New("转发规则文件过大")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&saved) != nil || dec.Decode(&struct{}{}) != io.EOF || saved.Version != 1 || len(saved.Items) > maxSavedForwardRules {
		return forwardRulesFile{}, errors.New("转发规则文件格式无效")
	}
	ids := make(map[string]bool)
	for _, r := range saved.Items {
		if validateForwardRule(r) != nil || ids[r.ID] {
			return forwardRulesFile{}, errors.New("转发规则内容无效")
		}
		ids[r.ID] = true
	}
	return saved, nil
}

func updateForwardRules(filename string, change func(*forwardRulesFile) error) (forwardRulesFile, error) {
	if filename == "" {
		return forwardRulesFile{}, errors.New("转发规则存储不可用")
	}
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return forwardRulesFile{}, err
	}
	lock := flock.New(filename + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return forwardRulesFile{}, err
	}
	if !locked {
		return forwardRulesFile{}, errors.New("另一个窗口正在保存转发规则")
	}
	defer lock.Unlock()
	saved, err := readForwardRules(filename)
	if err != nil {
		return forwardRulesFile{}, err
	}
	if err = change(&saved); err != nil {
		return forwardRulesFile{}, err
	}
	if len(saved.Items) > maxSavedForwardRules {
		return forwardRulesFile{}, errors.New("转发规则数量已达上限")
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return forwardRulesFile{}, err
	}
	f, err := os.CreateTemp(dir, ".forward-rules-*.tmp")
	if err != nil {
		return forwardRulesFile{}, err
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
		return forwardRulesFile{}, err
	}
	if closeErr != nil {
		return forwardRulesFile{}, closeErr
	}
	if err = os.Rename(f.Name(), filename); err != nil {
		return forwardRulesFile{}, err
	}
	return saved, nil
}

func (d *desktop) ensureForwardHostID(id profileID) (string, error) {
	for n, p := range d.profiles {
		if p.ID != id {
			continue
		}
		if p.StableID != "" {
			return p.StableID, nil
		}
		if d.profilePath == "" {
			return "", errors.New("本机主机存储不可用")
		}
		stable, err := newRandomID()
		if err != nil {
			return "", err
		}
		for _, other := range d.profiles {
			if other.StableID == stable {
				return "", errors.New("无法生成唯一主机标识，请重试")
			}
		}
		d.profiles[n].StableID = stable
		if err = d.saveProfiles(); err != nil {
			d.profiles[n].StableID = ""
			return "", err
		}
		return stable, nil
	}
	return "", errors.New("所选主机不存在")
}
func (d *desktop) forwardRuleHost(id string) (profile, bool) {
	for _, p := range d.profiles {
		if p.StableID == id {
			return p, true
		}
	}
	return profile{}, false
}

func (d *desktop) saveForwardRule(r savedForwardRule, old *savedForwardRule) error {
	if err := validateForwardRule(r); err != nil {
		return err
	}
	if _, ok := d.forwardRuleHost(r.HostID); !ok {
		return errors.New("所选主机不存在")
	}
	saved, err := updateForwardRules(d.forwardRulesPath, func(file *forwardRulesFile) error {
		for n, existing := range file.Items {
			if existing.ID != r.ID {
				continue
			}
			if old == nil || existing != *old {
				return errors.New("规则已被其他窗口修改，请重新载入")
			}
			file.Items[n] = r
			return nil
		}
		if old != nil {
			return errors.New("规则已被其他窗口删除，请重新载入")
		}
		file.Items = append(file.Items, r)
		return nil
	})
	if err != nil {
		return err
	}
	d.forwardRules = saved.Items
	d.reconcileForwardRules()
	return nil
}
func (d *desktop) deleteForwardRule(old savedForwardRule) error {
	saved, err := updateForwardRules(d.forwardRulesPath, func(file *forwardRulesFile) error {
		for n, r := range file.Items {
			if r.ID == old.ID {
				if r != old {
					return errors.New("规则已被其他窗口修改，请重新载入")
				}
				file.Items = append(file.Items[:n], file.Items[n+1:]...)
				return nil
			}
		}
		return errors.New("规则已被其他窗口删除，请重新载入")
	})
	if err != nil {
		return err
	}
	d.forwardRules = saved.Items
	d.reconcileForwardRules()
	return nil
}

func forwardHostBinding(p profile) [32]byte {
	return connectionDraftHash(p.Settings.Host, p.Settings.Port, p.Settings.User, p.Settings.KeyPath, p.Settings.Fingerprint)
}
func (d *desktop) stopChangedForwardRules() {
	for id, run := range d.savedForwardRuns {
		p, ok := d.forwardRuleHost(run.hostID)
		if !ok || forwardHostBinding(p) != run.binding {
			d.stopSavedForward(id)
		}
	}
}
func (d *desktop) reconcileForwardRules() {
	for id, run := range d.savedForwardRuns {
		found := false
		for _, r := range d.forwardRules {
			if r.ID == id && r == run.rule {
				found = true
				break
			}
		}
		if !found {
			d.stopSavedForward(id)
		}
	}
}

// Caller owns the returned transport and must close it on stop or failure.
func openSavedForward(ctx context.Context, rule savedForwardRule, settings connectionSettings, invalidate func()) (*ssh.Client, *localForward, error) {
	if err := validateForwardRule(rule); err != nil {
		return nil, nil, err
	}
	client, err := connectSSHClient(ctx, settings)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	done := make(chan struct{})
	go func() { _ = client.Wait(); close(done) }()
	var f *localForward
	switch rule.Mode {
	case "local":
		f, err = startLocalForward(client, done, rule.ListenPort, rule.TargetHost, rule.TargetPort, invalidate)
	case "remote":
		f, err = startRemoteForward(client, done, rule.ListenPort, rule.TargetHost, rule.TargetPort, invalidate)
	case "dynamic":
		f, err = startDynamicForward(client, done, rule.ListenPort, invalidate)
	}
	stop()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		_ = client.Close()
		return nil, nil, err
	}
	return client, f, nil
}

func (d *desktop) startSavedForward(rule savedForwardRule, invalidate func()) {
	if d.savedForwardRuns[rule.ID] != nil {
		return
	}
	current, err := readForwardRules(d.forwardRulesPath)
	if err != nil {
		d.forwardRulesStatus = "读取规则失败：" + err.Error()
		return
	}
	valid := false
	for _, r := range current.Items {
		if r == rule {
			valid = true
			break
		}
	}
	if !valid {
		d.forwardRules = current.Items
		d.reconcileForwardRules()
		d.forwardRulesStatus = "规则已被其他窗口修改或删除，已刷新。"
		return
	}
	p, ok := d.forwardRuleHost(rule.HostID)
	if !ok {
		d.forwardRulesStatus = "规则的主机不存在，请编辑规则重新选择。"
		return
	}
	settings, err := d.profileCredential(p)
	if err != nil {
		d.forwardRulesStatus = err.Error()
		return
	}
	if !strings.HasPrefix(settings.Fingerprint, "SHA256:") {
		d.forwardRulesStatus = "请先连接主机并核对主机指纹。"
		return
	}
	if !settings.canUseStoredAuth() {
		d.forwardRulesStatus = "请先为主机保存密码、私钥或配置本机 SSH agent。"
		return
	}
	if len(d.savedForwardRuns) >= 32 {
		d.forwardRulesStatus = "最多同时运行 32 条转发规则。"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	if d.savedForwardRuns == nil {
		d.savedForwardRuns = make(map[string]*savedForwardRun)
	}
	run := &savedForwardRun{cancel: cancel, hostID: rule.HostID, binding: forwardHostBinding(p), rule: rule}
	d.savedForwardRuns[rule.ID] = run
	d.forwardRulesStatus = "正在启动：" + rule.Name
	go func() {
		client, f, err := openSavedForward(ctx, rule, settings, invalidate)
		d.win.Update(func() {
			if d.savedForwardRuns[rule.ID] != run {
				if f != nil {
					f.Close()
				}
				if client != nil {
					_ = client.Close()
				}
				cancel()
				return
			}
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
				if f != nil {
					f.Close()
				}
				if client != nil {
					_ = client.Close()
				}
			}
			if err != nil {
				delete(d.savedForwardRuns, rule.ID)
				cancel()
				d.forwardRulesStatus = "启动失败：" + err.Error()
				return
			}
			run.client, run.forward = client, f
			cancel()
			d.forwardRulesStatus = "已启动：" + f.address()
			go func() {
				<-f.stopped
				_ = client.Close()
				d.win.Update(func() {
					if d.savedForwardRuns[rule.ID] == run {
						delete(d.savedForwardRuns, rule.ID)
						d.forwardRulesStatus = fmt.Sprintf("转发已停止：%s", rule.Name)
					}
				})
			}()
		})
	}()
}
func (d *desktop) stopSavedForward(id string) {
	run := d.savedForwardRuns[id]
	if run == nil {
		return
	}
	delete(d.savedForwardRuns, id)
	run.cancel()
	if run.forward != nil {
		run.forward.Close()
	}
	if run.client != nil {
		go run.client.Close()
	}
}
