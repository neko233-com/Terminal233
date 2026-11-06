package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

const localImportJournalName = ".local-import.journal"
const maxLocalImportJournal = 10 << 20

type localImportJournal struct {
	Version                         int
	OldProfilesExist, OldRulesExist bool
	OldProfilesHash, OldRulesHash   [32]byte
	NewProfiles, NewRules           []byte
	NewProfilesHash, NewRulesHash   [32]byte
}

func localImportJournalPath(storePath string) string {
	return filepath.Join(filepath.Dir(storePath), localImportJournalName)
}
func checkLocalImportPending(storePath string) error {
	_, err := os.Stat(localImportJournalPath(storePath))
	if err == nil {
		return errors.New("有尚未完成的本机导入，请重启客户端以恢复")
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func readLocalStore(path string, limit int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, false, errors.New("本机数据文件过大")
	}
	return data, true, nil
}

// Files are staged in the destination directory and synced before rename.
func writeLocalStoreAtomic(path, prefix string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), prefix)
	if err != nil {
		return err
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
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func lockLocalImport(profilePath, rulesPath string) (func(), error) {
	if profilePath == "" || rulesPath == "" || filepath.Clean(filepath.Dir(profilePath)) != filepath.Clean(filepath.Dir(rulesPath)) {
		return nil, errors.New("本机数据路径无效")
	}
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		return nil, err
	}
	profiles := flock.New(profilePath + ".lock")
	ok, err := profiles.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("另一个窗口正在保存主机")
	}
	rules := flock.New(rulesPath + ".lock")
	ok, err = rules.TryLock()
	if err != nil || !ok {
		profiles.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("另一个窗口正在保存转发规则")
	}
	return func() { _ = rules.Unlock(); _ = profiles.Unlock() }, nil
}

func decodeLocalImportJournal(data []byte) (localImportJournal, error) {
	var j localImportJournal
	if len(data) > maxLocalImportJournal {
		return j, errors.New("导入恢复数据过大")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&j) != nil || dec.Decode(&struct{}{}) != io.EOF || j.Version != 1 || len(j.NewProfiles) == 0 || len(j.NewProfiles) > 4<<20 || len(j.NewRules) == 0 || len(j.NewRules) > 2<<20 || sha256.Sum256(j.NewProfiles) != j.NewProfilesHash || sha256.Sum256(j.NewRules) != j.NewRulesHash {
		return localImportJournal{}, errors.New("导入恢复数据无效")
	}
	// Never replay arbitrary paths or unknown fields (including plaintext
	// password fields) from a corrupted recovery file.
	var profiles diskProfiles
	dec = json.NewDecoder(bytes.NewReader(j.NewProfiles))
	dec.DisallowUnknownFields()
	if dec.Decode(&profiles) != nil || dec.Decode(&struct{}{}) != io.EOF || profiles.Version < 1 || profiles.Version > 7 {
		return localImportJournal{}, errors.New("导入恢复的主机数据无效")
	}
	var rules forwardRulesFile
	dec = json.NewDecoder(bytes.NewReader(j.NewRules))
	dec.DisallowUnknownFields()
	if dec.Decode(&rules) != nil || dec.Decode(&struct{}{}) != io.EOF || rules.Version != 1 || len(rules.Items) > maxSavedForwardRules {
		return localImportJournal{}, errors.New("导入恢复的转发数据无效")
	}
	seen := map[string]bool{}
	for _, r := range rules.Items {
		if validateForwardRule(r) != nil || seen[r.ID] {
			return localImportJournal{}, errors.New("导入恢复的转发数据无效")
		}
		seen[r.ID] = true
	}
	return j, nil
}

func localImportStateMatches(data []byte, exists, oldExists bool, oldHash, newHash [32]byte) bool {
	if !exists {
		return !oldExists
	}
	hash := sha256.Sum256(data)
	return hash == newHash || (oldExists && hash == oldHash)
}

// Caller holds both locks. A conflicting external change is never overwritten.
func recoverLocalImportLocked(profilePath, rulesPath string) (bool, error) {
	journalPath := localImportJournalPath(profilePath)
	data, exists, err := readLocalStore(journalPath, maxLocalImportJournal)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	j, err := decodeLocalImportJournal(data)
	if err != nil {
		return false, err
	}
	profiles, profileExists, err := readLocalStore(profilePath, 4<<20)
	if err != nil {
		return false, err
	}
	rules, rulesExist, err := readLocalStore(rulesPath, 2<<20)
	if err != nil {
		return false, err
	}
	if !localImportStateMatches(profiles, profileExists, j.OldProfilesExist, j.OldProfilesHash, j.NewProfilesHash) || !localImportStateMatches(rules, rulesExist, j.OldRulesExist, j.OldRulesHash, j.NewRulesHash) {
		return false, errors.New("本机数据在导入中被其他程序修改；已保留恢复数据，请先核对")
	}
	if !profileExists || sha256.Sum256(profiles) != j.NewProfilesHash {
		if err = writeLocalStoreAtomic(profilePath, ".profiles-*.tmp", j.NewProfiles); err != nil {
			return false, err
		}
	}
	if !rulesExist || sha256.Sum256(rules) != j.NewRulesHash {
		if err = writeLocalStoreAtomic(rulesPath, ".forward-rules-*.tmp", j.NewRules); err != nil {
			return false, err
		}
	}
	if err = os.Remove(journalPath); err != nil {
		return false, err
	}
	return true, nil
}

func recoverLocalImport(profilePath, rulesPath string) (bool, error) {
	if _, err := os.Stat(localImportJournalPath(profilePath)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	unlock, err := lockLocalImport(profilePath, rulesPath)
	if err != nil {
		return false, err
	}
	defer unlock()
	return recoverLocalImportLocked(profilePath, rulesPath)
}

// Caller holds both locks and has checked the profile revision. The journal
// contains only the OS-protected profile data and non-secret rule metadata.
func commitLocalImportLocked(profilePath, rulesPath string, oldProfiles []byte, oldProfilesExist bool, oldRules []byte, oldRulesExist bool, newProfiles, newRules []byte, fault func(string) error) error {
	if err := checkLocalImportPending(profilePath); err != nil {
		return err
	}
	j := localImportJournal{Version: 1, OldProfilesExist: oldProfilesExist, OldRulesExist: oldRulesExist, OldProfilesHash: sha256.Sum256(oldProfiles), OldRulesHash: sha256.Sum256(oldRules), NewProfiles: newProfiles, NewRules: newRules, NewProfilesHash: sha256.Sum256(newProfiles), NewRulesHash: sha256.Sum256(newRules)}
	data, err := json.Marshal(j)
	if err != nil || len(data) > maxLocalImportJournal {
		return errors.New("导入恢复数据无法保存")
	}
	if _, err = decodeLocalImportJournal(data); err != nil {
		return err
	}
	if err = writeLocalStoreAtomic(localImportJournalPath(profilePath), ".local-import-*.tmp", data); err != nil {
		return err
	}
	step := func(name string) error {
		if fault != nil {
			return fault(name)
		}
		return nil
	}
	if err = step("journal"); err == nil {
		err = writeLocalStoreAtomic(profilePath, ".profiles-*.tmp", newProfiles)
	}
	if err == nil {
		err = step("profiles")
	}
	if err == nil {
		err = writeLocalStoreAtomic(rulesPath, ".forward-rules-*.tmp", newRules)
	}
	if err == nil {
		err = step("rules")
	}
	if err == nil {
		err = os.Remove(localImportJournalPath(profilePath))
	}
	if err != nil {
		return fmt.Errorf("导入未完成，重启客户端可尝试恢复：%w", err)
	}
	return nil
}
