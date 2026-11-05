package app

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) teamRoot(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method == http.MethodGet {
		s.json(w, http.StatusOK, s.store.listTeams(user))
		return
	}
	if r.Method != http.MethodPost {
		s.error(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法无效")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	team, err := s.store.createTeam(user, body.Name)
	if err != nil {
		s.error(w, http.StatusBadRequest, "team_create_failed", "无法创建团队，请检查团队名称和数量")
		return
	}
	s.json(w, http.StatusCreated, team)
}
func (s *Server) teamJoin(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		s.error(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法无效")
		return
	}
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	keys := []string{"invite:" + s.requestClientIdentity(r), "invite-user:" + user}
	if retry, ok := s.inviteLimiter.allow(time.Now(), keys...); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
		s.error(w, http.StatusTooManyRequests, "rate_limited", "尝试过于频繁，请稍后再试")
		return
	}
	if !s.acquireAuth(w) {
		return
	}
	defer s.releaseAuth()
	team, err := s.store.joinTeam(user, body.Token, body.Password)
	if err != nil {
		s.inviteLimiter.failure(time.Now(), keys...)
		s.error(w, http.StatusForbidden, "invitation_invalid", "邀请不可用、密码错误或团队已满")
		return
	}
	s.inviteLimiter.success(time.Now(), keys...)
	s.json(w, http.StatusOK, team)
}
func (s *Server) teamResource(w http.ResponseWriter, r *http.Request, user string) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/teams/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" {
		s.error(w, http.StatusNotFound, "not_found", "接口不存在")
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "groups":
		s.groupResource(w, r, user, id)
	case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodGet:
		result, err := s.store.teamMembers(user, id)
		if err != nil {
			s.error(w, http.StatusNotFound, "not_found", "团队不存在或无权访问")
			return
		}
		s.json(w, http.StatusOK, result)
	case len(parts) == 3 && parts[1] == "members" && parts[2] == "role" && r.Method == http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		}
		if !s.decode(w, r, &body) {
			return
		}
		if err := s.store.setMemberRole(user, id, body.Username, body.Role); err != nil {
			s.error(w, http.StatusForbidden, "role_failed", "无权修改成员角色")
			return
		}
		s.json(w, http.StatusOK, map[string]bool{"ok": true})
	case len(parts) == 3 && parts[1] == "members" && r.Method == http.MethodDelete:
		if err := s.store.removeTeamMember(user, id, parts[2]); err != nil {
			s.error(w, http.StatusNotFound, "not_found", "成员不存在或无权操作")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 2 && parts[1] == "invitations" && r.Method == http.MethodGet:
		result, err := s.store.listInvitations(user, id)
		if err != nil {
			s.error(w, http.StatusNotFound, "not_found", "团队不存在或无权访问")
			return
		}
		s.json(w, http.StatusOK, result)
	case len(parts) == 3 && parts[1] == "invitations" && r.Method == http.MethodDelete:
		if err := s.store.revokeInvitation(user, id, parts[2]); err != nil {
			s.error(w, http.StatusNotFound, "not_found", "邀请不存在或无权操作")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 2 && parts[1] == "invitations" && r.Method == http.MethodPost:
		var body struct {
			Password         string `json:"password"`
			ExpiresInSeconds int    `json:"expiresInSeconds"`
			Uses             int    `json:"uses"`
		}
		if !s.decode(w, r, &body) {
			return
		}
		if body.ExpiresInSeconds < 60 || body.ExpiresInSeconds > 7*24*60*60 || body.Uses < 1 || body.Uses > 100 {
			s.error(w, http.StatusBadRequest, "invalid_invitation", "邀请期限或使用次数无效")
			return
		}
		if !s.acquireAuth(w) {
			return
		}
		defer s.releaseAuth()
		token, err := s.store.createInvitation(user, id, body.Password, time.Duration(body.ExpiresInSeconds)*time.Second, body.Uses)
		if err != nil {
			s.error(w, http.StatusBadRequest, "invitation_failed", "邀请创建失败：请检查权限、密码和数量")
			return
		}
		result := map[string]string{"token": token}
		if s.cfg.PublicURL != "" {
			link, _ := url.Parse(s.cfg.PublicURL)
			if link.Path == "" {
				link.Path = "/"
			}
			link.Fragment = url.Values{"join": {token}}.Encode()
			result["url"] = link.String()
		}
		s.json(w, http.StatusCreated, result)
	case len(parts) == 2 && parts[1] == "accounts" && r.Method == http.MethodGet:
		accounts, err := s.store.listTeamAccounts(user, id)
		if err != nil {
			s.error(w, http.StatusNotFound, "not_found", "团队不存在或无权访问")
			return
		}
		s.json(w, http.StatusOK, accounts)
	case len(parts) == 2 && parts[1] == "accounts" && r.Method == http.MethodPost:
		var a Account
		if !s.decode(w, r, &a) {
			return
		}
		if !validAccountInput(a) {
			s.error(w, http.StatusBadRequest, "invalid_account", "SSH 账号参数无效")
			return
		}
		result, err := s.store.saveTeamAccount(user, id, a)
		if err != nil {
			if errors.Is(err, errAccountConflict) {
				s.error(w, http.StatusConflict, "account_conflict", "连接已被其他设备修改，请刷新后重试")
				return
			}
			s.error(w, http.StatusForbidden, "save_failed", "无权保存或团队凭据数量已达上限")
			return
		}
		s.json(w, http.StatusCreated, result)
	case len(parts) == 3 && parts[1] == "accounts" && r.Method == http.MethodDelete:
		expected, valid := s.expectedDeleteRevision(w, r)
		if !valid {
			return
		}
		if err := s.store.deleteTeamAccountIfVersion(user, id, parts[2], expected); err != nil {
			if errors.Is(err, errAccountConflict) {
				s.error(w, http.StatusConflict, "account_conflict", "连接已被其他设备修改，请刷新后重试")
				return
			}
			s.error(w, http.StatusNotFound, "not_found", "凭据不存在或无权操作")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		s.error(w, http.StatusNotFound, "not_found", "接口不存在")
	}
}
