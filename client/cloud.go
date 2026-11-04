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
	base  *url.URL
	http  *http.Client
	token string
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
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("同步连接失败: %w", err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 2*1024*1024+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(data) > 2*1024*1024 {
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
	err := c.request(http.MethodGet, "/api/v1/accounts", nil, &result)
	return result, err
}
func (c *cloudClient) save(a cloudAccount) error {
	if c.token == "" {
		return errors.New("请先登录同步服务")
	}
	return c.request(http.MethodPost, "/api/v1/accounts", a, nil)
}
func (c *cloudClient) logout() error {
	if c.token == "" {
		return nil
	}
	err := c.request(http.MethodPost, "/api/v1/logout", nil, nil)
	c.token = ""
	return err
}
