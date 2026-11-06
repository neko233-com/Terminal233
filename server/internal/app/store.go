package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/crypto/scrypt"
	_ "modernc.org/sqlite"
)

const maxAccountsPerUser = 1000

type storeDocument struct {
	Version     int                            `json:"version"`
	Users       map[string]userRecord          `json:"users"`
	Accounts    map[string][]storedAccount     `json:"accounts"`
	Teams       map[string]teamRecord          `json:"teams,omitempty"`
	Invitations map[string]teamInvitation      `json:"invitations,omitempty"`
	Groups      map[string]cloudGroupState     `json:"groups,omitempty"`
	Workspaces  map[string]cloudWorkspaceState `json:"workspaces,omitempty"`
}

type userRecord struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	CreatedAt    string `json:"createdAt"`
	Disabled     bool   `json:"disabled,omitempty"`
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
	path     string
	key      []byte
	mu       sync.Mutex
	doc      storeDocument
	revision int64
	hasState bool
	fileLock *flock.Flock
}

// openStore keeps only authenticated ciphertext in SQLite. Existing encrypted
// v1 files are migrated after verification; their original bytes are retained.
func openStore(path, masterKey string) (result *Store, err error) {
	key := sha256.Sum256([]byte(masterKey))
	s := &Store{path: path, key: key[:], doc: storeDocument{Version: 1, Users: map[string]userRecord{}, Accounts: map[string][]storedAccount{}}}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	acquired, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !acquired {
		_ = lock.Close()
		return nil, errors.New("数据文件已由其他服务实例使用")
	}
	s.fileLock = lock
	defer func() {
		if err != nil {
			_ = lock.Close()
		}
	}()
	file, err := os.Open(path)
	var data []byte
	if errors.Is(err, os.ErrNotExist) {
		if _, backupErr := os.Stat(path + ".legacy-backup"); backupErr == nil {
			return nil, errors.New("数据库缺失但旧数据备份存在，请先恢复备份后重试")
		} else if !errors.Is(backupErr, os.ErrNotExist) {
			return nil, backupErr
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取数据文件失败: %w", err)
	} else {
		header := make([]byte, 16)
		n, readErr := io.ReadFull(file, header)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			_ = file.Close()
			return nil, readErr
		}
		if n > 0 && string(header[:n]) != "SQLite format 3\x00" {
			if _, err = file.Seek(0, io.SeekStart); err != nil {
				_ = file.Close()
				return nil, err
			}
			data, err = io.ReadAll(io.LimitReader(file, maxBackupBytes+65))
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			if len(data) > maxBackupBytes+64 {
				_ = file.Close()
				return nil, errors.New("旧数据文件过大")
			}
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
	}
	if len(data) > 0 {
		plain, err := decrypt(s.key, data)
		if err != nil {
			return nil, fmt.Errorf("旧数据解密失败: %w", err)
		}
		if err = json.Unmarshal(plain, &s.doc); err != nil {
			return nil, fmt.Errorf("旧数据格式无效: %w", err)
		}
		if err = s.validateDocument(); err != nil {
			return nil, err
		}
		backup := path + ".legacy-backup"
		if _, err = os.Stat(backup); err == nil {
			return nil, errors.New("旧数据备份已存在，拒绝覆盖")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		tmp := path + ".migration-" + newID()
		migrated := &Store{path: tmp, key: s.key, doc: s.doc}
		if err = migrated.saveLocked(); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		if err = os.Rename(path, backup); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		if err = os.Rename(tmp, path); err != nil {
			restoreErr := os.Rename(backup, path)
			_ = os.Remove(tmp)
			if restoreErr != nil {
				return nil, errors.Join(err, fmt.Errorf("旧数据仍保存在备份文件，恢复失败: %w", restoreErr))
			}
			return nil, err
		}
		s.revision = migrated.revision
		s.hasState = migrated.hasState
		return s, nil
	}
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var payload []byte
	err = db.QueryRow("SELECT payload, revision FROM encrypted_state WHERE id=1").Scan(&payload, &s.revision)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	s.hasState = true
	plain, err := decrypt(s.key, payload)
	if err != nil {
		return nil, fmt.Errorf("数据文件解密失败，可能是 master key 不匹配: %w", err)
	}
	if err = json.Unmarshal(plain, &s.doc); err != nil {
		return nil, fmt.Errorf("数据文件格式错误: %w", err)
	}
	if err = s.validateDocument(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) validateDocument() error {
	if (s.doc.Version < 1 || s.doc.Version > 3) || s.doc.Users == nil || s.doc.Accounts == nil {
		return errors.New("数据文件版本不受支持")
	}
	if s.doc.Teams == nil {
		s.doc.Teams = map[string]teamRecord{}
	}
	if s.doc.Invitations == nil {
		s.doc.Invitations = map[string]teamInvitation{}
	}
	if err := validateCloudGroupDocument(s.doc); err != nil {
		return err
	}
	return validateCloudWorkspaceDocument(s.doc)
}
func openSQLite(path string) (*sql.DB, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("CREATE TABLE IF NOT EXISTS encrypted_state (id INTEGER PRIMARY KEY CHECK (id=1), payload BLOB NOT NULL, revision INTEGER NOT NULL DEFAULT 0)"); err != nil {
		db.Close()
		return nil, err
	}
	rows, err := db.Query("PRAGMA table_info(encrypted_state)")
	if err != nil {
		db.Close()
		return nil, err
	}
	hasRevision := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		if name == "revision" {
			hasRevision = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		db.Close()
		return nil, err
	}
	rows.Close()
	if !hasRevision {
		if _, err = db.Exec("ALTER TABLE encrypted_state ADD COLUMN revision INTEGER NOT NULL DEFAULT 0"); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}
func (s *Store) saveLocked() error {
	plain, err := json.Marshal(s.doc)
	if err != nil {
		return err
	}
	if len(plain) > maxBackupBytes {
		return errors.New("storage capacity reached")
	}
	data, err := encrypt(s.key, plain)
	if err != nil {
		return err
	}
	db, err := openSQLite(s.path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result sql.Result
	if s.hasState {
		result, err = db.Exec("UPDATE encrypted_state SET payload=?,revision=revision+1 WHERE id=1 AND revision=?", data, s.revision)
	} else {
		result, err = db.Exec("INSERT INTO encrypted_state (id,payload,revision) VALUES (1,?,1) ON CONFLICT(id) DO NOTHING", data)
	}
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("数据库已由其他实例修改，拒绝覆盖；请重启服务")
	}
	if s.hasState {
		s.revision++
	} else {
		s.hasState = true
		s.revision = 1
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
	if err := s.saveLocked(); err != nil {
		delete(s.doc.Users, username)
		delete(s.doc.Accounts, username)
		return err
	}
	return nil
}

func (s *Store) authenticateUser(username, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.doc.Users[normalizeUsername(username)]
	return ok && !record.Disabled && verifyPassword(record.PasswordHash, password)
}

func (s *Store) listAccounts(username string) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeUserLocked(username) {
		return nil, errTeamUnavailable
	}
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
	if !s.activeUserLocked(owner) {
		return Account{}, errTeamUnavailable
	}
	items := append([]storedAccount(nil), s.doc.Accounts[owner]...)
	original := s.doc.Accounts[owner]
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if account.ID == "" {
		account.ID = newID()
		account.CreatedAt = now
	} else {
		found := false
		for _, item := range items {
			if item.ID == account.ID {
				if account.ExpectedUpdatedAt != "" && account.ExpectedUpdatedAt != item.UpdatedAt {
					return Account{}, errAccountConflict
				}
				now = nextAccountTimestamp(item.UpdatedAt)
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
	account.ExpectedUpdatedAt = ""
	ciphertext, err := encryptString(s.key, account.Password)
	if err != nil {
		return Account{}, err
	}
	record := storedAccount{ID: account.ID, Label: account.Label, Host: account.Host, Port: account.Port, Username: account.Username, EncryptedPassword: ciphertext, HostKeyFingerprint: account.HostKeyFingerprint, CreatedAt: account.CreatedAt, UpdatedAt: account.UpdatedAt}
	if account.ID != "" {
		for i, item := range items {
			if item.ID == account.ID {
				items[i] = record
				s.doc.Accounts[owner] = items
				if err := s.saveLocked(); err != nil {
					s.doc.Accounts[owner] = original
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
		s.doc.Accounts[owner] = original
		return Account{}, err
	}
	return account, nil
}

// Under the store lock, every update must produce a distinct revision even
// when two writes occur in the same clock tick or the system clock moves back.
func nextAccountTimestamp(previous string) string {
	now := time.Now().UTC()
	if old, err := time.Parse(time.RFC3339Nano, previous); err == nil && !now.After(old) {
		now = old.Add(time.Nanosecond)
	}
	return now.Format(time.RFC3339Nano)
}

func (s *Store) deleteAccount(owner, id string) error {
	return s.deleteAccountIfVersion(owner, id, "")
}

func (s *Store) deleteAccountIfVersion(owner, id, expected string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeUserLocked(owner) {
		return errTeamUnavailable
	}
	items := s.doc.Accounts[normalizeUsername(owner)]
	for i, item := range items {
		if item.ID == id {
			if expected != "" && expected != item.UpdatedAt {
				return errAccountConflict
			}
			key := normalizeUsername(owner)
			updated := append([]storedAccount(nil), items[:i]...)
			updated = append(updated, items[i+1:]...)
			return s.transactionalLocked(func() error {
				s.doc.Accounts[key] = updated
				state := s.doc.Groups[key]
				if state.Assignments != nil {
					delete(state.Assignments, id)
					s.doc.Groups[key] = state
				}
				return nil
			})
		}
	}
	return errAccountNotFound
}

type UserSummary struct {
	Username     string `json:"username"`
	AccountCount int    `json:"accountCount"`
	CreatedAt    string `json:"createdAt"`
	Disabled     bool   `json:"disabled"`
}

func (s *Store) summaries() []UserSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]UserSummary, 0, len(s.doc.Users))
	for username, user := range s.doc.Users {
		result = append(result, UserSummary{Username: username, AccountCount: len(s.doc.Accounts[username]), CreatedAt: user.CreatedAt, Disabled: user.Disabled})
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
	errAccountConflict = errors.New("account was modified")
	errAccountLimit    = errors.New("account limit reached")
)

func (s *Store) Close() error {
	if s.fileLock == nil {
		return nil
	}
	return s.fileLock.Close()
}

func (s *Store) activeUserLocked(name string) bool {
	u, ok := s.doc.Users[normalizeUsername(name)]
	return ok && !u.Disabled
}
