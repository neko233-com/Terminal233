package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	maxBodyBytes = 256 * 1024
	sessionTTL   = 12 * time.Hour
)

type Account struct {
	ID                 string `json:"id,omitempty"`
	Label              string `json:"label"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
	CreatedAt          string `json:"createdAt,omitempty"`
	UpdatedAt          string `json:"updatedAt,omitempty"`
}

type Server struct {
	cfg             Config
	store           *Store
	sessions        map[string]session
	loginLimiter    *authAttemptLimiter
	registerLimiter *authAttemptLimiter
	mu              sync.Mutex
}

type session struct {
	Username  string
	ExpiresAt time.Time
}

func New(cfg Config) (*Server, error) {
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
		s.landingPage(w)
	case r.URL.Path == "/docs.html" && r.Method == http.MethodGet:
		s.docsPage(w)
	case r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		s.json(w, http.StatusOK, map[string]any{"status": "ok", "service": "terminal233-server", "version": Version})
	case r.URL.Path == "/api/v1/register" && r.Method == http.MethodPost:
		s.register(w, r)
	case r.URL.Path == "/api/v1/login" && r.Method == http.MethodPost:
		s.login(w, r)
	case r.URL.Path == "/api/v1/logout" && r.Method == http.MethodPost:
		s.logout(w, r)
	case r.URL.Path == "/api/v1/accounts" && r.Method == http.MethodGet:
		s.withUser(w, r, s.listAccounts)
	case r.URL.Path == "/api/v1/accounts" && r.Method == http.MethodPost:
		s.withUser(w, r, s.saveAccount)
	case strings.HasPrefix(r.URL.Path, "/api/v1/accounts/") && r.Method == http.MethodDelete:
		s.withUser(w, r, s.deleteAccount)
	case r.URL.Path == "/admin" && r.Method == http.MethodGet:
		s.adminPage(w, r)
	case r.URL.Path == "/admin/login" && r.Method == http.MethodPost:
		s.adminLogin(w, r)
	case r.URL.Path == "/admin/logout" && r.Method == http.MethodPost:
		s.adminLogout(w, r)
	default:
		s.error(w, http.StatusNotFound, "not_found", "接口不存在")
	}
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		OfficialToken string `json:"officialToken"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	registerKeys := []string{"register@" + requestClientIdentity(r)}
	if retryAfter, ok := s.registerLimiter.allow(time.Now(), registerKeys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
		s.error(w, http.StatusTooManyRequests, "rate_limited", "注册尝试过于频繁，请稍后再试")
		return
	}
	if !constantTimeEqual(input.OfficialToken, s.cfg.OfficialToken) {
		s.registerLimiter.failure(time.Now(), registerKeys...)
		s.error(w, http.StatusForbidden, "official_token_required", "需要有效的官方注册 token")
		return
	}
	if len(input.Username) < 3 || len(input.Username) > 64 || len(input.Password) < 8 || len(input.Password) > 256 {
		s.error(w, http.StatusBadRequest, "invalid_registration", "账号或密码格式无效")
		return
	}
	if err := s.store.createUser(input.Username, input.Password); err != nil {
		if errors.Is(err, errUserExists) {
			s.error(w, http.StatusConflict, "user_exists", "账号已存在")
			return
		}
		s.error(w, http.StatusInternalServerError, "storage_failed", "账号创建失败")
		return
	}
	s.registerLimiter.success(time.Now(), registerKeys...)
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
	loginKeys := authAttemptKeys(r, input.Username)
	if retryAfter, ok := s.loginLimiter.allow(time.Now(), loginKeys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
		s.error(w, http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后再试")
		return
	}
	if !s.store.authenticateUser(input.Username, input.Password) {
		s.loginLimiter.failure(time.Now(), loginKeys...)
		s.error(w, http.StatusUnauthorized, "invalid_credentials", "账号或密码错误")
		return
	}
	s.loginLimiter.success(time.Now(), loginKeys...)
	token := newID()
	s.mu.Lock()
	s.sessions[token] = session{Username: normalizeUsername(input.Username), ExpiresAt: time.Now().Add(sessionTTL)}
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

func (s *Server) listAccounts(w http.ResponseWriter, _ *http.Request, username string) {
	accounts, err := s.store.listAccounts(username)
	if err != nil {
		s.error(w, http.StatusInternalServerError, "storage_failed", "读取账号失败")
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
	if err := verifySSH(account, s.cfg.SSHTimeout); err != nil {
		s.error(w, http.StatusBadGateway, "ssh_verification_failed", "SSH 密码校验失败，账号未保存")
		return
	}
	saved, err := s.store.upsertAccount(username, account)
	if err != nil {
		switch {
		case errors.Is(err, errAccountLimit):
			s.error(w, http.StatusConflict, "account_limit", "每个账号最多保存 1000 条 SSH 凭据")
		case errors.Is(err, errAccountNotFound):
			s.error(w, http.StatusNotFound, "account_not_found", "SSH 账号不存在")
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
	if err := s.store.deleteAccount(username, id); err != nil {
		s.error(w, http.StatusNotFound, "account_not_found", "SSH 账号不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if !ok {
		s.error(w, http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录")
		return
	}
	handler(w, r, current.Username)
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthenticated(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, adminLoginHTML)
		return
	}
	data := struct {
		Username string
		Users    []UserSummary
	}{Username: s.cfg.AdminUsername, Users: s.store.summaries()}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminTemplate.Execute(w, data); err != nil {
		log.Printf("admin render failed: %v", err)
	}
}

func (s *Server) landingPage(w http.ResponseWriter) {
	writeHTMLPageHeaders(w)
	_ = landingTemplate.Execute(w, struct{ Version string }{Version: Version})
}

func (s *Server) docsPage(w http.ResponseWriter) {
	writeHTMLPageHeaders(w)
	_, _ = io.WriteString(w, docsHTML)
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.FormValue("username") != s.cfg.AdminUsername || r.FormValue("password") != s.cfg.AdminPassword {
		http.Error(w, "账号或密码错误", http.StatusUnauthorized)
		return
	}
	token := newID()
	s.mu.Lock()
	s.sessions["admin:"+token] = session{Username: "admin", ExpiresAt: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "terminal233_admin", Value: token, HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/", MaxAge: int(sessionTTL.Seconds())})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("terminal233_admin"); err == nil {
		s.mu.Lock()
		delete(s.sessions, "admin:"+cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "terminal233_admin", Value: "", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/"})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("terminal233_admin")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions["admin:"+cookie.Value]
	return ok && current.Username == "admin" && current.ExpiresAt.After(time.Now())
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
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

func writeHTMLPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
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
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
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
		fallback := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
		return hex.EncodeToString(fallback[:])
	}
	return hex.EncodeToString(bytes)
}

func validAccountInput(a Account) bool {
	return a.Label != "" && len(a.Label) <= 128 && a.Host != "" && len(a.Host) <= 253 && !strings.ContainsAny(a.Host, "/\\@ \t\r\n") && a.Port > 0 && a.Port <= 65535 && a.Username != "" && len(a.Username) <= 128 && !strings.ContainsAny(a.Username, "/\\@ \t\r\n") && a.Password != "" && len(a.Password) <= 64*1024
}

func verifySSH(account Account, timeout time.Duration) error {
	address := net.JoinHostPort(account.Host, strconv.Itoa(account.Port))
	var seenFingerprint string
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		seenFingerprint = ssh.FingerprintSHA256(key)
		if account.HostKeyFingerprint != "" && account.HostKeyFingerprint != seenFingerprint {
			return errors.New("host key mismatch")
		}
		return nil
	}
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	config := &ssh.ClientConfig{User: account.Username, Auth: []ssh.AuthMethod{ssh.Password(account.Password)}, HostKeyCallback: callback, Timeout: timeout}
	clientConn, chans, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		return err
	}
	client := ssh.NewClient(clientConn, chans, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	if err := session.Run("true"); err != nil {
		return err
	}
	_ = seenFingerprint
	return nil
}

var adminLoginHTML = `<!doctype html><meta charset="utf-8"><title>Terminal233 Server</title><style>body{font:16px system-ui;max-width:420px;margin:12vh auto;padding:24px;background:#111827;color:#e5e7eb}input,button{box-sizing:border-box;width:100%;padding:10px;margin:7px 0;border-radius:6px;border:1px solid #374151;background:#1f2937;color:#fff}button{background:#2563eb;border:0;cursor:pointer}</style><h1>Terminal233 Server</h1><p>管理后台</p><form method="post" action="/admin/login"><input name="username" placeholder="账号" value="root" autocomplete="username"><input name="password" type="password" placeholder="密码" autocomplete="current-password"><button>登录</button></form>`

var landingTemplate = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Terminal233 Cloud</title><style>
:root{color-scheme:dark;font-family:system-ui,-apple-system,"Segoe UI","Microsoft YaHei",sans-serif;background:#07111b;color:#e7f2ff}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(circle at 18% 12%,rgba(77,211,255,.22),transparent 34%),radial-gradient(circle at 86% 78%,rgba(107,242,171,.16),transparent 32%),#07111b}.card{width:min(720px,calc(100% - 32px));padding:38px;border:1px solid rgba(174,216,255,.16);border-radius:28px;background:rgba(10,25,39,.9);box-shadow:0 24px 80px rgba(0,0,0,.32)}.eyebrow{letter-spacing:.18em;text-transform:uppercase;font-size:12px;color:#76e8b0}h1{margin:12px 0 10px;font-size:clamp(38px,8vw,68px);letter-spacing:-.055em}p{color:#a6bacb;line-height:1.7}.status{display:flex;align-items:center;gap:10px;margin:24px 0;padding:14px 16px;border-radius:16px;background:rgba(118,232,176,.09);color:#baffd8}.dot{width:10px;height:10px;border-radius:50%;background:#76e8b0;box-shadow:0 0 18px #76e8b0}.links{display:flex;gap:12px;flex-wrap:wrap;margin-top:24px}a{padding:12px 16px;border-radius:999px;text-decoration:none;font-weight:700;color:#07111b;background:linear-gradient(135deg,#6ee7ff,#76e8b0)}a.secondary{color:#e7f2ff;background:rgba(255,255,255,.08)}small{display:block;margin-top:26px;color:#71879a}</style></head><body><main class="card"><div class="eyebrow">terminal233 cloud</div><h1>SSH 凭据同步服务</h1><p>这是你自部署的 Terminal233 Server。客户端通过 HTTPS 使用它同步加密保存的 SSH 连接信息。</p><div class="status"><span class="dot"></span><strong>服务在线 · 版本 {{.Version}}</strong></div><div class="links"><a href="/docs.html">查看 API 说明</a><a class="secondary" href="/healthz">健康检查</a><a class="secondary" href="/admin">管理后台</a></div><small>SSH 密码不会显示在此页面；注册必须携带官方 token。</small></main></body></html>`))

var docsHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Terminal233 Cloud API</title><style>body{margin:0;background:#07111b;color:#e7f2ff;font:15px/1.7 system-ui,-apple-system,"Segoe UI","Microsoft YaHei",sans-serif}main{width:min(900px,calc(100% - 32px));margin:48px auto}h1{font-size:42px;letter-spacing:-.04em}p,li{color:#a6bacb}code{padding:3px 6px;border-radius:6px;background:#132639;color:#baffd8}section{margin:24px 0;padding:20px;border:1px solid rgba(174,216,255,.16);border-radius:18px;background:rgba(10,25,39,.82)}a{color:#76e8ff}</style></head><body><main><a href="/">← 返回 Terminal233 Cloud</a><h1>API 说明</h1><p>所有 API 均使用 HTTPS；响应不会缓存。SSH 密码只在服务端加密存储，不会显示在管理后台。</p><section><h2>注册与登录</h2><ul><li><code>POST /api/v1/register</code>：需要 <code>username</code>、<code>password</code>、<code>officialToken</code>。</li><li><code>POST /api/v1/login</code>：返回短期 Bearer 会话 token。</li><li>登录和注册失败会触发短时冷却；成功登录后会清除失败计数。</li></ul></section><section><h2>凭据管理</h2><ul><li><code>GET /api/v1/accounts</code>：读取当前用户的 SSH 连接。</li><li><code>POST /api/v1/accounts</code>：验证 SSH 后保存或更新连接。</li><li><code>DELETE /api/v1/accounts/:id</code>：删除连接。</li></ul></section><section><h2>运维</h2><ul><li><code>GET /healthz</code>：服务健康检查。</li><li><code>GET /admin</code>：管理员概览，不展示 SSH 密码。</li></ul></section></main></body></html>`

var adminTemplate = template.Must(template.New("admin").Parse(`<!doctype html><meta charset="utf-8"><title>Terminal233 Server 管理后台</title><style>body{font:15px system-ui;max-width:1000px;margin:32px auto;padding:0 20px;background:#f4f6f8;color:#111827}table{border-collapse:collapse;width:100%;background:white}th,td{padding:10px;border-bottom:1px solid #e5e7eb;text-align:left}button{padding:7px 12px}</style><h1>Terminal233 Server</h1><p>当前管理员：{{.Username}}　<a href="/admin/logout" onclick="event.preventDefault();document.getElementById('logout').submit()">退出</a></p><form id="logout" method="post" action="/admin/logout"></form><table><thead><tr><th>用户</th><th>SSH 凭据数</th><th>创建时间</th></tr></thead><tbody>{{range .Users}}<tr><td>{{.Username}}</td><td>{{.AccountCount}} / 1000</td><td>{{.CreatedAt}}</td></tr>{{else}}<tr><td colspan="3">暂无注册用户</td></tr>{{end}}</tbody></table>`))
