package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const cloudArchiveFormat = "nekossh-accounts-v1"
const maxCloudArchivePlain = 2 << 20
const maxCloudArchiveFile = 3 << 20
const maxCloudArchiveAccounts = 1000

type cloudArchiveEnvelope struct {
	Format     string `json:"format"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type cloudArchiveDocument struct {
	Version       int            `json:"version"`
	Accounts      []cloudAccount `json:"accounts"`
	Groups        []string       `json:"groups"`
	AccountGroups []string       `json:"accountGroups,omitempty"`
	JumpIndices   []int          `json:"jumpIndices,omitempty"`
}

func validateArchivePassword(password string) error {
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("备份密码需为 12–1024 字节")
	}
	return nil
}

func normalizeArchiveAccounts(accounts []cloudAccount) ([]cloudAccount, error) {
	if len(accounts) > maxCloudArchiveAccounts {
		return nil, errors.New("备份连接数量超过上限")
	}
	result := make([]cloudAccount, 0, len(accounts))
	for i, account := range accounts {
		if cloudJumpReference(account) != "" {
			return nil, fmt.Errorf("备份第 %d 条连接包含云端跳板，当前备份格式不能保存该路径", i+1)
		}
		settings := connectionSettings{Host: account.Host, Port: fmt.Sprint(account.Port), User: account.Username, Password: account.Password, Fingerprint: account.HostKeyFingerprint}
		if len(account.Label) > 128 || len(account.Host) > 253 || len(account.Username) > 128 || len(account.Password) > 64<<10 || len(account.HostKeyFingerprint) > 100 || strings.ContainsAny(account.Username, "/\\@ \t\r\n") {
			return nil, fmt.Errorf("备份第 %d 条连接无效", i+1)
		}
		item, err := prepareCloudAccount(settings, account.Label, nil, "")
		if err != nil {
			return nil, fmt.Errorf("备份第 %d 条连接无效", i+1)
		}
		if !validStoredProfileTags(account.Tags) {
			return nil, fmt.Errorf("备份第 %d 条连接的标签无效", i+1)
		}
		item.Tags = append([]string(nil), account.Tags...)
		result = append(result, item)
	}
	return result, nil
}

func archiveCipher(password string, salt []byte) (cipher.AEAD, error) {
	if err := validateArchivePassword(password); err != nil {
		return nil, err
	}
	if len(salt) != 16 {
		return nil, errors.New("备份格式无效")
	}
	key, err := scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encodeCloudArchive(accounts []cloudAccount, password string) ([]byte, error) {
	parents, clean, err := archiveJumpReferences(accounts)
	if err != nil {
		return nil, err
	}
	items, err := normalizeArchiveAccounts(clean)
	if err != nil {
		return nil, err
	}
	doc := cloudArchiveDocument{Version: 1, Accounts: items}
	for _, item := range items {
		if len(item.Tags) > 0 {
			doc.Version = 3
			doc.Groups = []string{}
			doc.AccountGroups = make([]string, len(items))
			break
		}
	}
	if parents != nil {
		doc.Version = 4
		doc.JumpIndices = parents
		doc.Groups = []string{}
		doc.AccountGroups = make([]string, len(items))
	}
	return encodeCloudArchiveDocument(doc, password)
}

func encodeCloudArchiveWithGroups(accounts []cloudAccount, password string, groups cloudGroupSnapshot) ([]byte, error) {
	parents, clean, err := archiveJumpReferences(accounts)
	if err != nil {
		return nil, err
	}
	items, err := normalizeArchiveAccounts(clean)
	if err != nil {
		return nil, err
	}
	if !cloudGroupAccountsMatch(groups, accounts) {
		return nil, errors.New("连接列表与云端分组不一致，请刷新后重试")
	}
	paths := make([]string, len(accounts))
	for i, a := range accounts {
		paths[i] = groups.Assignments[a.ID]
	}
	version := 2
	for _, item := range items {
		if len(item.Tags) > 0 {
			version = 3
			break
		}
	}
	if parents != nil {
		version = 4
	}
	return encodeCloudArchiveDocument(cloudArchiveDocument{Version: version, Accounts: items, Groups: append([]string{}, groups.Groups...), AccountGroups: paths, JumpIndices: parents}, password)
}

func encodeCloudArchiveDocument(document cloudArchiveDocument, password string) ([]byte, error) {
	plain, err := json.Marshal(document)
	if err != nil || len(plain) > maxCloudArchivePlain {
		return nil, errors.New("备份内容无法编码或过大")
	}
	defer clear(plain)
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := archiveCipher(password, salt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	envelope := cloudArchiveEnvelope{Format: cloudArchiveFormat, Salt: salt, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plain, []byte(cloudArchiveFormat))}
	return json.Marshal(envelope)
}

func decodeCloudArchive(data []byte, password string) ([]cloudAccount, error) {
	doc, err := decodeCloudArchiveDocument(data, password)
	if err == nil && len(doc.JumpIndices) != 0 {
		return nil, errors.New("该备份包含跳板，请使用完整导入流程")
	}
	return doc.Accounts, err
}

func decodeCloudArchiveDocument(data []byte, password string) (cloudArchiveDocument, error) {
	var empty cloudArchiveDocument
	if len(data) > maxCloudArchiveFile {
		return empty, errors.New("备份文件过大")
	}
	var envelope cloudArchiveEnvelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Format != cloudArchiveFormat || len(envelope.Salt) != 16 || len(envelope.Nonce) != 12 || len(envelope.Ciphertext) < 16 || len(envelope.Ciphertext) > maxCloudArchivePlain+16 {
		return empty, errors.New("备份格式无效")
	}
	aead, err := archiveCipher(password, envelope.Salt)
	if err != nil {
		return empty, err
	}
	plain, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, []byte(cloudArchiveFormat))
	if err != nil {
		return empty, errors.New("备份密码错误或文件已损坏")
	}
	defer clear(plain)
	var document cloudArchiveDocument
	if json.Unmarshal(plain, &document) != nil || (document.Version < 1 || document.Version > 4) || document.Accounts == nil {
		return empty, errors.New("备份内容无效")
	}
	for _, account := range document.Accounts {
		if account.JumpID != nil {
			return empty, errors.New("备份包含未经映射的跳板编号")
		}
	}
	if document.Version < 4 && len(document.JumpIndices) != 0 {
		return empty, errors.New("备份版本与跳板路径不符")
	}
	if document.Version == 4 {
		if _, err := archiveJumpOrder(document.JumpIndices, len(document.Accounts)); err != nil {
			return empty, err
		}
	}
	items, err := normalizeArchiveAccounts(document.Accounts)
	if err != nil {
		return empty, err
	}
	document.Accounts = items
	if document.Version < 3 {
		for _, item := range document.Accounts {
			if len(item.Tags) > 0 {
				return empty, errors.New("备份版本与标签不符")
			}
		}
	}
	if document.Version >= 2 {
		if !validCloudArchiveGroups(document) {
			return empty, errors.New("备份分组无效")
		}
	}
	return document, nil
}

func validCloudArchiveGroups(document cloudArchiveDocument) bool {
	if len(document.AccountGroups) != len(document.Accounts) || document.Groups == nil {
		return false
	}
	state := cloudGroupSnapshot{Groups: document.Groups, Assignments: map[string]string{}, AccountIDs: []string{}, Revision: strings.Repeat("0", 64)}
	for i, path := range document.AccountGroups {
		id := fmt.Sprint(i)
		state.AccountIDs = append(state.AccountIDs, id)
		if path != "" {
			state.Assignments[id] = path
		}
	}
	return validCloudGroupSnapshot(state)
}

func writeCloudArchive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("写入失败且无法清理不完整备份：%w", err)
		}
		return errors.Join(writeErr, closeErr)
	}
	return nil
}

func readCloudArchive(path, password string) ([]cloudAccount, error) {
	doc, err := readCloudArchiveDocument(path, password)
	if err == nil && len(doc.JumpIndices) != 0 {
		return nil, errors.New("该备份包含跳板，请使用完整导入流程")
	}
	return doc.Accounts, err
}

func readCloudArchiveDocument(path, password string) (cloudArchiveDocument, error) {
	var empty cloudArchiveDocument
	f, err := os.Open(path)
	if err != nil {
		return empty, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCloudArchiveFile+1))
	if err != nil {
		return empty, err
	}
	return decodeCloudArchiveDocument(data, password)
}
