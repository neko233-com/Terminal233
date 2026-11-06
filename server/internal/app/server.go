package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	maxBodyBytes = 256 * 1024
	maxSessions  = 10000
	sessionTTL   = 12 * time.Hour
)

type Account struct {
	ID                 string   `json:"id,omitempty"`
	Label              string   `json:"label"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	Username           string   `json:"username"`
	Password           string   `json:"password"`
	HostKeyFingerprint string   `json:"hostKeyFingerprint,omitempty"`
	CreatedAt          string   `json:"createdAt,omitempty"`
	UpdatedAt          string   `json:"updatedAt,omitempty"`
	ExpectedUpdatedAt  string   `json:"expectedUpdatedAt,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	JumpID             *string  `json:"jumpId,omitempty"`
}

type Server struct {
	cfg             Config
	store           *Store
	sessions        map[string]session
	loginLimiter    *authAttemptLimiter
	registerLimiter *authAttemptLimiter
	inviteLimiter   *authAttemptLimiter
	adminLimiter    *authAttemptLimiter
	authSlots       chan struct{}
	mu              sync.Mutex
}

type session struct {
	Username  string
	ExpiresAt time.Time
}

func New(cfg Config) (*Server, error) {
	var err error
	if cfg.PublicURL, err = normalizePublicURL(cfg.PublicURL); err != nil {
		return nil, err
	}
	store, err := openStore(cfg.DataPath, cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:             cfg,
		store:           store,
		sessions:        map[string]session{},
		loginLimiter:    newAuthAttemptLimiter(),
		registerLimiter: newAuthAttemptLimiter(),
		inviteLimiter:   newAuthAttemptLimiter(),
		adminLimiter:    newAuthAttemptLimiter(),
		authSlots:       make(chan struct{}, 2),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.applyCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		s.json(w, http.StatusOK, map[string]string{"service": "terminal233-sync", "message": "团队邀请请复制完整链接到 NekoSSH 客户端，在团队共享中粘贴并输入邀请密码。"})
	case r.URL.Path == "/docs.html" && r.Method == http.MethodGet:
		s.error(w, http.StatusNotFound, "not_found", "接口不存在")
	case r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		s.json(w, http.StatusOK, map[string]any{"status": "ok", "service": "terminal233-server", "version": Version, "capabilities": []string{"conditional-account-update", "conditional-account-delete", "cloud-groups-v1", "cloud-workspaces-v1", "cloud-workspaces-16", "cloud-workspace-tab-names-v1", "account-tags-v1", "account-jumps-v1"}})
	case r.URL.Path == "/api/v1/register" && r.Method == http.MethodPost:
		s.register(w, r)
	case r.URL.Path == "/api/v1/login" && r.Method == http.MethodPost:
		s.login(w, r)
	case r.URL.Path == "/api/v1/logout" && r.Method == http.MethodPost:
		s.logout(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/admin/"):
		s.withUser(w, r, s.adminResource)
	case r.URL.Path == "/api/v1/teams":
		s.withUser(w, r, s.teamRoot)
	case r.URL.Path == "/api/v1/teams/join":
		s.withUser(w, r, s.teamJoin)
	case r.URL.Path == "/api/v1/groups":
		s.withUser(w, r, func(w http.ResponseWriter, r *http.Request, user string) { s.groupResource(w, r, user, "") })
	case r.URL.Path == "/api/v1/workspaces":
		s.withUser(w, r, s.workspaceResource)
	case strings.HasPrefix(r.URL.Path, "/api/v1/teams/"):
		s.withUser(w, r, s.teamResource)
	case r.URL.Path == "/api/v1/accounts" && r.Method == http.MethodGet:
		s.withUser(w, r, s.listAccounts)
	case r.URL.Path == "/api/v1/accounts" && r.Method == http.MethodPost:
		s.withUser(w, r, s.saveAccount)
	case strings.HasPrefix(r.URL.Path, "/api/v1/accounts/") && r.Method == http.MethodDelete:
		s.withUser(w, r, s.deleteAccount)
	default:
		s.error(w, http.StatusNotFound, "not_found", "接口不存在")
	}
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username           string `json:"username"`
		Password           string `json:"password"`
		OfficialToken      string `json:"officialToken"`
		TeamInviteToken    string `json:"teamInviteToken,omitempty"`
		TeamInvitePassword string `json:"teamInvitePassword,omitempty"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	registerKeys := []string{"register@" + s.requestClientIdentity(r)}
	if retryAfter, ok := s.registerLimiter.allow(time.Now(), registerKeys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
		s.error(w, http.StatusTooManyRequests, "rate_limited", "注册尝试过于频繁，请稍后再试")
		return
	}
	registrationToken := s.cfg.OfficialToken
	adminRegistration := s.cfg.AdminUsername != "" && normalizeUsername(input.Username) == s.cfg.AdminUsername
	if adminRegistration {
		registrationToken = s.cfg.AdminBootstrapToken
	}
	useInvite := input.TeamInviteToken != "" && !adminRegistration
	if !useInvite && (registrationToken == "" || !constantTimeEqual(input.OfficialToken, registrationToken)) {
		s.registerLimiter.failure(time.Now(), registerKeys...)
		s.error(w, http.StatusForbidden, "registration_token_required", "注册令牌或团队邀请无效")
		return
	}
	if !validRegistrationUsername(input.Username) || len(input.Password) < 12 || len(input.Password) > 256 {
		s.registerLimiter.failure(time.Now(), registerKeys...)
		s.error(w, http.StatusBadRequest, "invalid_registration", "账号或密码格式无效")
		return
	}
	if !s.acquireAuth(w) {
		return
	}
	defer s.releaseAuth()
	var createErr error
	if useInvite {
		createErr = s.store.registerWithInvitation(input.Username, input.Password, input.TeamInviteToken, input.TeamInvitePassword)
	} else {
		createErr = s.store.createUser(input.Username, input.Password)
	}
	if err := createErr; err != nil {
		s.registerLimiter.failure(time.Now(), registerKeys...)
		if errors.Is(err, errInvalidInvitation) {
			s.error(w, http.StatusForbidden, "invitation_invalid", "邀请不可用、密码错误或团队已满")
			return
		}
		if errors.Is(err, errUserExists) {
			s.error(w, http.StatusConflict, "user_exists", "账号已存在")
			return
		}
		s.error(w, http.StatusInternalServerError, "storage_failed", "账号创建失败")
		return
	}
	s.registerLimiter.failure(time.Now(), registerKeys...)
	s.json(w, http.StatusCreated, map[string]any{"ok": true, "username": normalizeUsername(input.Username)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	loginKeys := s.authAttemptKeys(r, input.Username)
	if retryAfter, ok := s.loginLimiter.allow(time.Now(), loginKeys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
		s.error(w, http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后再试")
		return
	}
	if !s.acquireAuth(w) {
		return
	}
	defer s.releaseAuth()
	if len(input.Username) < 3 || len(input.Username) > 64 || len(input.Password) < 8 || len(input.Password) > 256 || !s.store.authenticateUser(input.Username, input.Password) {
		s.loginLimiter.failure(time.Now(), loginKeys...)
		s.error(w, http.StatusUnauthorized, "invalid_credentials", "账号或密码错误")
		return
	}
	s.loginLimiter.success(time.Now(), loginKeys...)
	token := newID()
	s.mu.Lock()
	now := time.Now()
	for key, current := range s.sessions {
		if !current.ExpiresAt.After(now) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		s.error(w, http.StatusServiceUnavailable, "session_limit", "会话数量已达上限，请稍后重试")
		return
	}
	s.sessions[token] = session{Username: normalizeUsername(input.Username), ExpiresAt: now.Add(sessionTTL)}
	s.mu.Unlock()
	s.json(w, http.StatusOK, map[string]any{"token": token, "expiresInSeconds": int(sessionTTL.Seconds())})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := bearer(r); token != "" {
		s.mu.Lock()
		delete(s.sessions, token)
		s.mu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request, username string) {
	accounts, err := s.store.listAccounts(username)
	if err != nil {
		s.error(w, http.StatusInternalServerError, "storage_failed", "读取账号失败")
		return
	}
	if !jumpClientSupported(r) && accountJumpsPresent(accounts) {
		s.error(w, http.StatusConflict, "client_upgrade_required", "此空间包含跳板连接，请升级客户端")
		return
	}
	s.json(w, http.StatusOK, accounts)
}

func (s *Server) saveAccount(w http.ResponseWriter, r *http.Request, username string) {
	var account Account
	if !s.decode(w, r, &account) {
		return
	}
	if !validAccountInput(account) {
		s.error(w, http.StatusBadRequest, "invalid_account", "SSH 账号参数无效")
		return
	}
	saved, err := s.store.upsertAccountForClient(username, account, jumpClientSupported(r))
	if err != nil {
		switch {
		case errors.Is(err, errAccountConflict):
			s.error(w, http.StatusConflict, "account_conflict", "连接已被其他设备修改，请刷新后重试")
		case errors.Is(err, errAccountLimit):
			s.error(w, http.StatusConflict, "account_limit", "每个账号最多保存 1000 条 SSH 凭据")
		case errors.Is(err, errAccountNotFound):
			s.error(w, http.StatusNotFound, "account_not_found", "SSH 账号不存在")
		case errors.Is(err, errInvalidAccountJump):
			s.error(w, http.StatusBadRequest, "invalid_jump", "跳板不存在、循环或超过跳数限制")
		case errors.Is(err, errJumpClientUpgrade):
			s.error(w, http.StatusConflict, "client_upgrade_required", "此连接包含跳板，请升级客户端")
		default:
			s.error(w, http.StatusInternalServerError, "storage_failed", "保存 SSH 账号失败")
		}
		return
	}
	s.json(w, http.StatusCreated, saved)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, username string) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/accounts/")
	if id == "" || strings.Contains(id, "/") {
		s.error(w, http.StatusBadRequest, "invalid_account", "账号 ID 无效")
		return
	}
	expected, ok := s.expectedDeleteRevision(w, r)
	if !ok {
		return
	}
	if err := s.store.deleteAccountIfVersion(username, id, expected); err != nil {
		if errors.Is(err, errAccountJumpInUse) {
			s.error(w, http.StatusConflict, "jump_in_use", "其他连接正在使用此跳板，请先修改这些连接")
			return
		}
		if errors.Is(err, errAccountConflict) {
			s.error(w, http.StatusConflict, "account_conflict", "连接已被其他设备修改，请刷新后重试")
			return
		}
		s.error(w, http.StatusNotFound, "account_not_found", "SSH 账号不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) expectedDeleteRevision(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("X-Neko-Expected-Updated-At")
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 || len(values[0]) > 64 {
		s.error(w, http.StatusBadRequest, "invalid_revision", "连接版本无效")
		return "", false
	}
	if _, err := time.Parse(time.RFC3339Nano, values[0]); err != nil {
		s.error(w, http.StatusBadRequest, "invalid_revision", "连接版本无效")
		return "", false
	}
	return values[0], true
}

func (s *Server) withUser(w http.ResponseWriter, r *http.Request, handler func(http.ResponseWriter, *http.Request, string)) {
	token := bearer(r)
	s.mu.Lock()
	current, ok := s.sessions[token]
	if ok && current.ExpiresAt.Before(time.Now()) {
		delete(s.sessions, token)
		ok = false
	}
	s.mu.Unlock()
	if !ok || !s.store.userEnabled(current.Username) {
		s.error(w, http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录")
		return
	}
	handler(w, r, current.Username)
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.error(w, http.StatusBadRequest, "invalid_json", "请求内容无效")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		s.error(w, http.StatusBadRequest, "invalid_json", "请求内容无效")
		return false
	}
	return true
}
func (s *Server) json(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) error(w http.ResponseWriter, status int, code, message string) {
	s.json(w, status, map[string]string{"code": code, "message": message})
}

func retryAfterSeconds(duration time.Duration) int {
	seconds := int(duration.Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}
func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if _, ok := s.cfg.AllowedOrigins[origin]; ok {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Neko-Jump-Support")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	}
}

func bearer(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func newID() string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(bytes)
}

func validAccountInput(a Account) bool {
	return validAccountTags(a.Tags) && (a.ExpectedUpdatedAt == "" || (a.ID != "" && len(a.ExpectedUpdatedAt) <= 64)) && a.Label != "" && len(a.Label) <= 128 && a.Host != "" && len(a.Host) <= 253 && !strings.ContainsAny(a.Host, "/\\@ \t\r\n") && a.Port > 0 && a.Port <= 65535 && a.Username != "" && len(a.Username) <= 128 && !strings.ContainsAny(a.Username, "/\\@ \t\r\n") && a.Password != "" && len(a.Password) <= 64*1024 && strings.HasPrefix(a.HostKeyFingerprint, "SHA256:") && len(a.HostKeyFingerprint) <= 100
}

func (s *Server) acquireAuth(w http.ResponseWriter) bool {
	select {
	case s.authSlots <- struct{}{}:
		return true
	default:
		s.error(w, http.StatusServiceUnavailable, "authentication_busy", "认证请求繁忙，请稍后重试")
		return false
	}
}
func (s *Server) releaseAuth() { <-s.authSlots }

func validRegistrationUsername(value string) bool {
	value = normalizeUsername(value)
	if len(value) < 3 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-@", r) {
			return false
		}
	}
	return true
}

func (s *Server) Close() error { return s.store.Close() }
