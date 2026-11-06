package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxBackupBytes = 16 << 20

func (s *Store) userEnabled(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasState {
		return false
	}
	if _, err := os.Stat(s.path); err != nil {
		return false
	}
	db, err := openSQLite(s.path)
	if err != nil {
		return false
	}
	defer db.Close()
	var revision int64
	if err = db.QueryRow("SELECT revision FROM encrypted_state WHERE id=1").Scan(&revision); err != nil || revision != s.revision {
		return false
	}
	u, ok := s.doc.Users[normalizeUsername(user)]
	return ok && !u.Disabled
}
func (s *Store) setDisabled(user string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user = normalizeUsername(user)
	rec, ok := s.doc.Users[user]
	if !ok {
		return errTeamUnavailable
	}
	return s.transactionalLocked(func() error { rec.Disabled = disabled; s.doc.Users[user] = rec; return nil })
}
func (s *Store) exportBackup() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain, err := json.Marshal(s.doc)
	if err != nil {
		return nil, err
	}
	if len(plain) > maxBackupBytes {
		return nil, errors.New("backup exceeds size limit")
	}
	return encrypt(s.key, plain)
}
func (s *Store) importBackup(data []byte, admin string) error {
	if len(data) > maxBackupBytes+64 {
		return errors.New("backup exceeds size limit")
	}
	plain, err := decrypt(s.key, data)
	if err != nil {
		return err
	}
	var doc storeDocument
	if err = json.Unmarshal(plain, &doc); err != nil {
		return err
	}
	if (doc.Version < 1 || doc.Version > 7) || doc.Users == nil || doc.Accounts == nil {
		return errors.New("invalid backup")
	}
	if err = validateCloudGroupDocument(doc); err != nil {
		return errors.New("invalid backup group state")
	}
	if err = validateCloudWorkspaceDocument(doc); err != nil {
		return errors.New("invalid backup workspace state")
	}
	if err = validateAccountTagsDocument(doc); err != nil {
		return errors.New("invalid backup account tags")
	}
	if err = validateAccountJumpsDocument(doc); err != nil {
		return errors.New("invalid backup account jumps")
	}
	adminRecord, ok := doc.Users[normalizeUsername(admin)]
	if !ok || adminRecord.Disabled {
		return errors.New("backup does not contain active admin")
	}
	for id, t := range doc.Teams {
		if id == "" || t.ID != id || len(t.Members) > maxTeamMembers {
			return errors.New("invalid team")
		}
		owners := 0
		for username, role := range t.Members {
			if _, ok = doc.Users[username]; !ok {
				return errors.New("unknown member")
			}
			switch role {
			case "owner":
				owners++
			case "admin", "member":
			default:
				return errors.New("invalid role")
			}
		}
		if owners != 1 {
			return errors.New("invalid team owner")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.doc
	s.doc = doc
	if err = s.saveLocked(); err != nil {
		s.doc = previous
		return err
	}
	return nil
}
func (s *Server) adminResource(w http.ResponseWriter, r *http.Request, user string) {
	if s.cfg.AdminUsername == "" || normalizeUsername(user) != s.cfg.AdminUsername {
		s.error(w, http.StatusNotFound, "not_found", "接口不存在")
		return
	}
	keys := []string{"admin:" + user, "admin-ip:" + s.requestClientIdentity(r)}
	if retry, ok := s.adminLimiter.allow(time.Now(), keys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
		s.error(w, http.StatusTooManyRequests, "rate_limited", "管理员验证尝试过于频繁")
		return
	}
	if !verifyTOTP(s.cfg.AdminTOTPSecret, r.Header.Get("X-Admin-TOTP"), time.Now()) {
		s.adminLimiter.failure(time.Now(), keys...)
		s.error(w, http.StatusForbidden, "admin_verification_failed", "管理员动态验证码无效")
		return
	}
	s.adminLimiter.success(time.Now(), keys...)
	switch {
	case r.URL.Path == "/api/v1/admin/users" && r.Method == http.MethodGet:
		s.json(w, http.StatusOK, s.store.summaries())
	case r.URL.Path == "/api/v1/admin/teams" && r.Method == http.MethodGet:
		s.json(w, http.StatusOK, s.store.adminTeams())
	case r.URL.Path == "/api/v1/admin/team-status" && r.Method == http.MethodPost:
		var body struct {
			ID       string `json:"id"`
			Disabled bool   `json:"disabled"`
		}
		if !s.decode(w, r, &body) {
			return
		}
		if err := s.store.setTeamDisabled(body.ID, body.Disabled); err != nil {
			s.error(w, http.StatusNotFound, "not_found", "团队不存在")
			return
		}
		s.json(w, http.StatusOK, map[string]bool{"ok": true})
	case r.URL.Path == "/api/v1/admin/user-status" && r.Method == http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Disabled bool   `json:"disabled"`
		}
		if !s.decode(w, r, &body) {
			return
		}
		if normalizeUsername(body.Username) == s.cfg.AdminUsername {
			s.error(w, http.StatusForbidden, "admin_protected", "不能禁用管理员自身")
			return
		}
		if err := s.store.setDisabled(body.Username, body.Disabled); err != nil {
			s.error(w, http.StatusNotFound, "not_found", "用户不存在")
			return
		}
		s.json(w, http.StatusOK, map[string]bool{"ok": true})
	case r.URL.Path == "/api/v1/admin/export" && r.Method == http.MethodGet:
		data, err := s.store.exportBackup()
		if err != nil {
			s.error(w, http.StatusInternalServerError, "export_failed", "导出失败")
			return
		}
		s.json(w, http.StatusOK, map[string]string{"format": "nekossh-encrypted-v1", "data": base64.StdEncoding.EncodeToString(data)})
	case r.URL.Path == "/api/v1/admin/import" && r.Method == http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 24<<20)
		defer r.Body.Close()
		var body struct {
			Format  string `json:"format"`
			Data    string `json:"data"`
			Confirm string `json:"confirm"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.Format != "nekossh-encrypted-v1" || body.Confirm != "RESTORE" {
			s.error(w, http.StatusBadRequest, "invalid_backup", "备份格式或恢复确认无效")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			s.error(w, http.StatusBadRequest, "invalid_backup", "备份格式无效")
			return
		}
		data, err := base64.StdEncoding.DecodeString(body.Data)
		if err != nil || s.store.importBackup(data, s.cfg.AdminUsername) != nil {
			s.error(w, http.StatusBadRequest, "invalid_backup", "备份无效，或主密钥不匹配")
			return
		}
		s.mu.Lock()
		s.sessions = map[string]session{}
		s.mu.Unlock()
		s.json(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
			s.error(w, http.StatusNotFound, "not_found", "接口不存在")
		}
	}
}
