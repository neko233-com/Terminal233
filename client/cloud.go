package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type cloudAccount struct {
	ID                 string `json:"id,omitempty"`
	Label              string `json:"label"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	CreatedAt          string `json:"createdAt,omitempty"`
	UpdatedAt          string `json:"updatedAt,omitempty"`
}

type cloudClient struct {
	base      *url.URL
	http      *http.Client
	token     string
	teamID    string
	adminTOTP string
}

func newCloudClient(raw string) (*cloudClient, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("同步服务地址必须是 HTTPS 地址，且不能包含账号、参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return &cloudClient{base: u, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *cloudClient) request(method, path string, input, output any) error {
	u := *c.base
	u.Path += path
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if strings.HasPrefix(path, "/api/v1/admin/") && c.adminTOTP != "" {
		req.Header.Set("X-Admin-TOTP", c.adminTOTP)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("同步连接失败: %w", err)
	}
	defer resp.Body.Close()
	limit := int64(2 * 1024 * 1024)
	if path == "/api/v1/admin/export" {
		limit = 24 * 1024 * 1024
	}
	limited := io.LimitReader(resp.Body, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("同步服务响应过大")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("同步服务拒绝请求（%d）：%s", resp.StatusCode, e.Message)
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return errors.New("同步服务响应无效")
		}
	}
	return nil
}
func (c *cloudClient) register(user, password, registrationToken string) error {
	return c.request(http.MethodPost, "/api/v1/register", map[string]string{"username": user, "password": password, "officialToken": registrationToken}, nil)
}
func (c *cloudClient) login(user, password string) error {
	var result struct {
		Token string `json:"token"`
	}
	if err := c.request(http.MethodPost, "/api/v1/login", map[string]string{"username": user, "password": password}, &result); err != nil {
		return err
	}
	if result.Token == "" {
		return errors.New("同步服务没有返回会话")
	}
	c.token = result.Token
	return nil
}
func (c *cloudClient) list() ([]cloudAccount, error) {
	var result []cloudAccount
	if c.token == "" {
		return nil, errors.New("请先登录同步服务")
	}
	err := c.request(http.MethodGet, c.accountsPath(), nil, &result)
	return result, err
}
func (c *cloudClient) save(a cloudAccount) error {
	if c.token == "" {
		return errors.New("请先登录同步服务")
	}
	return c.request(http.MethodPost, c.accountsPath(), a, nil)
}

// saveReturningID uses the server's exact response ID, avoiding endpoint-based
// guesses when several credentials have the same address.
func (c *cloudClient) saveReturningID(a cloudAccount) (string, error) {
	if c.token == "" {
		return "", errors.New("请先登录同步服务")
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := c.request(http.MethodPost, c.accountsPath(), a, &result); err != nil {
		return "", err
	}
	if result.ID == "" || len(result.ID) > 128 || strings.ContainsAny(result.ID, "/\\\x00") || (a.ID != "" && result.ID != a.ID) {
		return "", errors.New("服务未返回可确认的连接编号")
	}
	return result.ID, nil
}

func (c *cloudClient) logout() error {
	if c.token == "" {
		return nil
	}
	err := c.request(http.MethodPost, "/api/v1/logout", nil, nil)
	c.token = ""
	return err
}

func (c *cloudClient) remove(id string) error {
	if c.token == "" {
		return errors.New("请先登录同步服务")
	}
	if id == "" || strings.ContainsAny(id, "/\\") {
		return errors.New("无效的连接编号")
	}
	return c.request(http.MethodDelete, c.accountsPath()+"/"+url.PathEscape(id), nil, nil)
}

func (c *cloudClient) accountsPath() string {
	if c.teamID != "" {
		return "/api/v1/teams/" + url.PathEscape(c.teamID) + "/accounts"
	}
	return "/api/v1/accounts"
}

type cloudTeam struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	MemberCount int    `json:"memberCount"`
	Disabled    bool   `json:"disabled"`
}
type cloudTeamMember struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (c *cloudClient) teams() ([]cloudTeam, error) {
	var teams []cloudTeam
	err := c.request(http.MethodGet, "/api/v1/teams", nil, &teams)
	return teams, err
}
func (c *cloudClient) createTeam(name string) (cloudTeam, error) {
	var t cloudTeam
	err := c.request(http.MethodPost, "/api/v1/teams", map[string]string{"name": name}, &t)
	return t, err
}
func (c *cloudClient) joinTeam(token, password string) (cloudTeam, error) {
	var t cloudTeam
	err := c.request(http.MethodPost, "/api/v1/teams/join", map[string]string{"token": token, "password": password}, &t)
	return t, err
}
func (c *cloudClient) createInvite(password string, seconds, uses int) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := c.request(http.MethodPost, "/api/v1/teams/"+url.PathEscape(c.teamID)+"/invitations", map[string]any{"password": password, "expiresInSeconds": seconds, "uses": uses}, &out)
	return out.URL, err
}
func (c *cloudClient) members() ([]cloudTeamMember, error) {
	var members []cloudTeamMember
	err := c.request(http.MethodGet, "/api/v1/teams/"+url.PathEscape(c.teamID)+"/members", nil, &members)
	return members, err
}
func (c *cloudClient) removeMember(username string) error {
	return c.request(http.MethodDelete, "/api/v1/teams/"+url.PathEscape(c.teamID)+"/members/"+url.PathEscape(username), nil, nil)
}

type cloudAdminUser struct {
	Username     string `json:"username"`
	AccountCount int    `json:"accountCount"`
	CreatedAt    string `json:"createdAt"`
	Disabled     bool   `json:"disabled"`
}
type cloudBackup struct {
	Format  string `json:"format"`
	Data    string `json:"data"`
	Confirm string `json:"confirm,omitempty"`
}

func (c *cloudClient) adminUsers() ([]cloudAdminUser, error) {
	var users []cloudAdminUser
	err := c.request(http.MethodGet, "/api/v1/admin/users", nil, &users)
	return users, err
}
func (c *cloudClient) setUserDisabled(user string, disabled bool) error {
	return c.request(http.MethodPost, "/api/v1/admin/user-status", map[string]any{"username": user, "disabled": disabled}, nil)
}
func (c *cloudClient) exportBackup() (cloudBackup, error) {
	var backup cloudBackup
	err := c.request(http.MethodGet, "/api/v1/admin/export", nil, &backup)
	return backup, err
}
func (c *cloudClient) importBackup(backup cloudBackup) error {
	backup.Confirm = "RESTORE"
	return c.request(http.MethodPost, "/api/v1/admin/import", backup, nil)
}

func (c *cloudClient) adminTeams() ([]cloudTeam, error) {
	var teams []cloudTeam
	err := c.request(http.MethodGet, "/api/v1/admin/teams", nil, &teams)
	return teams, err
}
func (c *cloudClient) setTeamDisabled(id string, disabled bool) error {
	return c.request(http.MethodPost, "/api/v1/admin/team-status", map[string]any{"id": id, "disabled": disabled}, nil)
}

func (c *cloudClient) registerWithInvite(user, password, invite, invitePassword string) error {
	return c.request(http.MethodPost, "/api/v1/register", map[string]string{"username": user, "password": password, "officialToken": "", "teamInviteToken": invite, "teamInvitePassword": invitePassword}, nil)
}

type cloudInvite struct {
	ID        string `json:"id"`
	ExpiresAt int64  `json:"expiresAt"`
	Remaining int    `json:"remaining"`
}

func (c *cloudClient) invitations() ([]cloudInvite, error) {
	var inv []cloudInvite
	err := c.request(http.MethodGet, "/api/v1/teams/"+url.PathEscape(c.teamID)+"/invitations", nil, &inv)
	return inv, err
}
func (c *cloudClient) revokeInvitation(id string) error {
	return c.request(http.MethodDelete, "/api/v1/teams/"+url.PathEscape(c.teamID)+"/invitations/"+url.PathEscape(id), nil, nil)
}
func (c *cloudClient) setMemberRole(user, role string) error {
	return c.request(http.MethodPost, "/api/v1/teams/"+url.PathEscape(c.teamID)+"/members/role", map[string]string{"username": user, "role": role}, nil)
}
