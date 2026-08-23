package app

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const Version = "0.1.0"

type Config struct {
	Address        string
	DataPath       string
	MasterKey      string
	OfficialToken  string
	AdminUsername  string
	AdminPassword  string
	SSHTimeout     time.Duration
	AllowedOrigins map[string]struct{}
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Address:       envOr("TERMINAL233_SERVER_ADDR", "127.0.0.1:8787"),
		DataPath:      envOr("TERMINAL233_DATA_PATH", "data/terminal233-server.json"),
		MasterKey:     os.Getenv("TERMINAL233_MASTER_KEY"),
		OfficialToken: os.Getenv("TERMINAL233_OFFICIAL_TOKEN"),
		AdminUsername: envOr("TERMINAL233_ADMIN_USERNAME", "root"),
		AdminPassword: envOr("TERMINAL233_ADMIN_PASSWORD", "root"),
		SSHTimeout:    15 * time.Second,
	}
	if len(cfg.MasterKey) < 16 {
		return Config{}, errors.New("TERMINAL233_MASTER_KEY 至少需要 16 个字符，不能使用默认值")
	}
	if len(cfg.OfficialToken) < 16 {
		return Config{}, errors.New("TERMINAL233_OFFICIAL_TOKEN 至少需要 16 个字符")
	}
	if cfg.AdminUsername == "" || cfg.AdminPassword == "" {
		return Config{}, errors.New("管理后台账号密码不能为空")
	}
	if raw := strings.TrimSpace(os.Getenv("TERMINAL233_SSH_TIMEOUT_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 3 || seconds > 120 {
			return Config{}, errors.New("TERMINAL233_SSH_TIMEOUT_SECONDS 必须是 3 到 120 的整数")
		}
		cfg.SSHTimeout = time.Duration(seconds) * time.Second
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
	return fmt.Sprintf("address=%s data=%s admin=%s", c.Address, c.DataPath, c.AdminUsername)
}
