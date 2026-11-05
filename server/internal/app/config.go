package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

var Version = "0.3.1"

type Config struct {
	AdminUsername       string
	AdminBootstrapToken string
	AdminTOTPSecret     string
	Address             string
	DataPath            string
	MasterKey           string
	OfficialToken       string
	AllowedOrigins      map[string]struct{}
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{
		AdminUsername:       normalizeUsername(os.Getenv("TERMINAL233_ADMIN_USERNAME")),
		AdminBootstrapToken: os.Getenv("TERMINAL233_ADMIN_BOOTSTRAP_TOKEN"),
		AdminTOTPSecret:     os.Getenv("TERMINAL233_ADMIN_TOTP_SECRET"),
		Address:             envOr("TERMINAL233_SERVER_ADDR", "127.0.0.1:8787"),
		DataPath:            envOr("TERMINAL233_DATA_PATH", "data/terminal233-server.json"),
		MasterKey:           os.Getenv("TERMINAL233_MASTER_KEY"),
		OfficialToken:       os.Getenv("TERMINAL233_OFFICIAL_TOKEN"),
	}
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return Config{}, errors.New("服务必须绑定到回环地址，并通过受信任的 HTTPS 反向代理公开")
	}
	if len(cfg.MasterKey) < 32 {
		return Config{}, errors.New("TERMINAL233_MASTER_KEY 至少需要 32 个随机字符，不能使用默认值")
	}
	if cfg.AdminUsername != "" && (len(cfg.AdminBootstrapToken) < 32 || cfg.AdminBootstrapToken == cfg.OfficialToken) {
		return Config{}, errors.New("管理员注册令牌必须独立且至少 32 个字符")
	}
	if cfg.AdminUsername != "" {
		secret, err := decodeTOTPSecret(cfg.AdminTOTPSecret)
		if err != nil || len(secret) < 20 {
			return Config{}, errors.New("管理员必须设置至少 20 字节随机 Base32 TOTP 密钥")
		}
	}
	if len(cfg.OfficialToken) < 32 {
		return Config{}, errors.New("TERMINAL233_OFFICIAL_TOKEN 至少需要 32 个随机字符")
	}
	cfg.AllowedOrigins = parseOrigins(os.Getenv("TERMINAL233_ALLOWED_ORIGINS"))
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func parseOrigins(raw string) map[string]struct{} {
	origins := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(item); value != "" {
			origins[value] = struct{}{}
		}
	}
	return origins
}

func (c Config) String() string {
	return fmt.Sprintf("address=%s data=%s", c.Address, c.DataPath)
}
