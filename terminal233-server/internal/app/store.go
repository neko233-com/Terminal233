package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

const maxAccountsPerUser = 1000

type storeDocument struct {
	Version  int                        `json:"version"`
	Users    map[string]userRecord      `json:"users"`
	Accounts map[string][]storedAccount `json:"accounts"`
}

type userRecord struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	CreatedAt    string `json:"createdAt"`
}

type storedAccount struct {
	ID                 string `json:"id"`
	Label              string `json:"label"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	EncryptedPassword  string `json:"encryptedPassword"`
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

type Store struct {
	path string
	key  []byte
	mu   sync.Mutex
	doc  storeDocument
}

func openStore(path, masterKey string) (*Store, error) {
	key := sha256.Sum256([]byte(masterKey))
	s := &Store{path: path, key: key[:], doc: storeDocument{Version: 1, Users: map[string]userRecord{}, Accounts: map[string][]storedAccount{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取数据文件失败: %w", err)
	}
	plain, err := decrypt(key[:], data)
	if err != nil {
		return nil, fmt.Errorf("数据文件解密失败，可能是 master key 不匹配: %w", err)
	}
	if err := json.Unmarshal(plain, &s.doc); err != nil {
		return nil, fmt.Errorf("数据文件格式错误: %w", err)
	}
	if s.doc.Version != 1 || s.doc.Users == nil || s.doc.Accounts == nil {
		return nil, errors.New("数据文件版本不受支持")
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	plain, err := json.Marshal(s.doc)
	if err != nil {
		return err
	}
	data, err := encrypt(s.key, plain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Store) createUser(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	username = normalizeUsername(username)
	if _, exists := s.doc.Users[username]; exists {
		return errUserExists
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.doc.Users[username] = userRecord{Username: username, PasswordHash: hash, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	s.doc.Accounts[username] = []storedAccount{}
	return s.saveLocked()
}

func (s *Store) authenticateUser(username, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.doc.Users[normalizeUsername(username)]
	return ok && verifyPassword(record.PasswordHash, password)
}

func (s *Store) listAccounts(username string) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := append([]storedAccount(nil), s.doc.Accounts[normalizeUsername(username)]...)
	result := make([]Account, 0, len(stored))
	for _, item := range stored {
		password, err := decryptString(s.key, item.EncryptedPassword)
		if err != nil {
			return nil, err
		}
		result = append(result, Account{ID: item.ID, Label: item.Label, Host: item.Host, Port: item.Port, Username: item.Username, Password: password, HostKeyFingerprint: item.HostKeyFingerprint, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Label) < strings.ToLower(result[j].Label) })
	return result, nil
}

func (s *Store) upsertAccount(owner string, account Account) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner = normalizeUsername(owner)
	items := s.doc.Accounts[owner]
	now := time.Now().UTC().Format(time.RFC3339)
	if account.ID == "" {
		account.ID = newID()
		account.CreatedAt = now
	} else {
		found := false
		for _, item := range items {
			if item.ID == account.ID {
				found = true
				account.CreatedAt = item.CreatedAt
				break
			}
		}
		if !found {
			return Account{}, errAccountNotFound
		}
	}
	if account.CreatedAt == "" {
		account.CreatedAt = now
	}
	account.UpdatedAt = now
	ciphertext, err := encryptString(s.key, account.Password)
	if err != nil {
		return Account{}, err
	}
	record := storedAccount{ID: account.ID, Label: account.Label, Host: account.Host, Port: account.Port, Username: account.Username, EncryptedPassword: ciphertext, HostKeyFingerprint: account.HostKeyFingerprint, CreatedAt: account.CreatedAt, UpdatedAt: account.UpdatedAt}
	if account.ID != "" {
		for i, item := range items {
			if item.ID == account.ID {
				items[i] = record
				if err := s.saveLocked(); err != nil {
					return Account{}, err
				}
				return account, nil
			}
		}
	}
	if len(items) >= maxAccountsPerUser {
		return Account{}, errAccountLimit
	}
	s.doc.Accounts[owner] = append(items, record)
	if err := s.saveLocked(); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (s *Store) deleteAccount(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.doc.Accounts[normalizeUsername(owner)]
	for i, item := range items {
		if item.ID == id {
			s.doc.Accounts[normalizeUsername(owner)] = append(items[:i], items[i+1:]...)
			return s.saveLocked()
		}
	}
	return errAccountNotFound
}

type UserSummary struct {
	Username     string `json:"username"`
	AccountCount int    `json:"accountCount"`
	CreatedAt    string `json:"createdAt"`
}

func (s *Store) summaries() []UserSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]UserSummary, 0, len(s.doc.Users))
	for username, user := range s.doc.Users {
		result = append(result, UserSummary{Username: username, AccountCount: len(s.doc.Accounts[username]), CreatedAt: user.CreatedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Username < result[j].Username })
	return result
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(salt) + "." + base64.RawURLEncoding.EncodeToString(hash), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return false
	}
	salt, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	expected, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil || len(expected) != 32 {
		return false
	}
	actual, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return false
	}
	var diff byte
	for i := range expected {
		diff |= expected[i] ^ actual[i]
	}
	return diff == 0
}

func encrypt(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plain, nil)...), nil
}

func decrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

func encryptString(key []byte, value string) (string, error) {
	data, err := encrypt(key, []byte(value))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
func decryptString(key []byte, value string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	plain, err := decrypt(key, data)
	return string(plain), err
}
func normalizeUsername(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

var (
	errUserExists      = errors.New("user exists")
	errAccountNotFound = errors.New("account not found")
	errAccountLimit    = errors.New("account limit reached")
)
