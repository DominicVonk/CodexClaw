package config

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	SourcePath string          `yaml:"-"`
	Service    ServiceConfig   `yaml:"service"`
	Codex      CodexConfig     `yaml:"codex"`
	Telegram   TelegramConfig  `yaml:"telegram"`
	WhatsApp   WhatsAppConfig  `yaml:"whatsapp"`
	Router     RouterConfig    `yaml:"router"`
	Sessions   SessionsConfig  `yaml:"sessions"`
	Media      MediaConfig     `yaml:"media"`
	Allowlist  AllowlistConfig `yaml:"allowlist"`
}

type ServiceConfig struct {
	Mode string `yaml:"mode"`
}

type CodexConfig struct {
	Command           string   `yaml:"command"`
	Args              []string `yaml:"args"`
	CWD               string   `yaml:"cwd"`
	ClientName        string   `yaml:"client_name"`
	ClientTitle       string   `yaml:"client_title"`
	ClientVersion     string   `yaml:"client_version"`
	Model             string   `yaml:"model"`
	Effort            string   `yaml:"effort"`
	ApprovalPolicy    string   `yaml:"approval_policy"`
	PermissionProfile string   `yaml:"permission_profile"`
}

type TelegramConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Token          string `yaml:"token"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

func (c TelegramConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

type WhatsAppConfig struct {
	Enabled    bool   `yaml:"enabled"`
	SQLitePath string `yaml:"sqlite_path"`
	QR         bool   `yaml:"qr"`
}

type RouterConfig struct {
	ProgressMessage string `yaml:"progress_message"`
	MaxReplyChars   int    `yaml:"max_reply_chars"`
	ShowToolUsage   bool   `yaml:"show_tool_usage"`
}

type MediaConfig struct {
	Dir string `yaml:"dir"`
}

type SessionsConfig struct {
	SQLitePath             string `yaml:"sqlite_path"`
	ContextMode            string `yaml:"context_mode"`
	AutoCompact            bool   `yaml:"auto_compact"`
	AutoCompactAfterTokens int64  `yaml:"auto_compact_after_tokens"`
}

type AllowlistConfig struct {
	Enabled bool     `yaml:"enabled"`
	Entries []string `yaml:"entries"`
}

func Load(path string) (Config, error) {
	if err := loadDotEnv(filepath.Join(defaultStorageRoot(), ".env")); err != nil {
		return Config{}, err
	}
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}
	resolved, err := ResolvePath(path)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return Config{}, err
	}

	expanded := os.ExpandEnv(string(data))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return Config{}, err
	}
	cfg.SourcePath = resolved
	cfg.setDefaults()
	cfg.applyEnvOverrides()
	return cfg, cfg.Validate()
}

func ResolvePath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return path, nil
	}
	for _, candidate := range configCandidates() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("config file not found: expected config.yml or config.yaml in current directory or " + defaultStorageRoot())
}

func configCandidates() []string {
	return []string{
		"config.yml",
		"config.yaml",
		filepath.Join(defaultStorageRoot(), "config.yml"),
		filepath.Join(defaultStorageRoot(), "config.yaml"),
	}
}

func (c *Config) setDefaults() {
	if c.Service.Mode == "" {
		c.Service.Mode = "both"
	}
	if c.Codex.Command == "" {
		c.Codex.Command = "codex"
	}
	if c.Codex.CWD == "" {
		c.Codex.CWD = "."
	}
	if c.Codex.ClientName == "" {
		c.Codex.ClientName = "codexclaw"
	}
	if c.Codex.ClientTitle == "" {
		c.Codex.ClientTitle = "CodexClaw"
	}
	if c.Codex.ClientVersion == "" {
		c.Codex.ClientVersion = "0.1.0"
	}
	if c.Codex.PermissionProfile == "" {
		c.Codex.PermissionProfile = ":workspace"
	}
	if c.Router.ProgressMessage == "" {
		c.Router.ProgressMessage = "Working on it..."
	}
	if c.Router.MaxReplyChars <= 0 {
		c.Router.MaxReplyChars = 3500
	}
	if c.WhatsApp.SQLitePath == "" {
		c.WhatsApp.SQLitePath = filepath.Join(".", "whatsapp-session", "whatsapp.db")
	}
	storageRoot := defaultStorageRoot()
	if c.Sessions.SQLitePath == "" {
		c.Sessions.SQLitePath = filepath.Join(storageRoot, "sessions.db")
	}
	if c.Sessions.ContextMode == "" {
		c.Sessions.ContextMode = "persistent"
	}
	c.Sessions.ContextMode = strings.ToLower(strings.TrimSpace(c.Sessions.ContextMode))
	if c.Media.Dir == "" {
		c.Media.Dir = filepath.Join(storageRoot, "media")
	}
	if c.Sessions.AutoCompactAfterTokens <= 0 {
		c.Sessions.AutoCompactAfterTokens = 120000
	}
}

func (c *Config) applyEnvOverrides() {
	setString(&c.Service.Mode, "CODEXCLAW_SERVICE_MODE")
	setString(&c.Codex.Command, "CODEXCLAW_CODEX_COMMAND")
	setString(&c.Codex.CWD, "CODEXCLAW_CODEX_CWD")
	setString(&c.Codex.Model, "CODEXCLAW_CODEX_MODEL")
	setString(&c.Codex.Effort, "CODEXCLAW_CODEX_EFFORT")
	setString(&c.Codex.ApprovalPolicy, "CODEXCLAW_CODEX_APPROVAL_POLICY")
	setString(&c.Codex.PermissionProfile, "CODEXCLAW_CODEX_PERMISSION_PROFILE")
	setBool(&c.Telegram.Enabled, "CODEXCLAW_TELEGRAM_ENABLED")
	setString(&c.Telegram.Token, "CODEXCLAW_TELEGRAM_TOKEN", "TELEGRAM_BOT_TOKEN")
	setInt(&c.Telegram.TimeoutSeconds, "CODEXCLAW_TELEGRAM_TIMEOUT_SECONDS")
	setBool(&c.Router.ShowToolUsage, "CODEXCLAW_ROUTER_SHOW_TOOL_USAGE")
	setBool(&c.WhatsApp.Enabled, "CODEXCLAW_WHATSAPP_ENABLED")
	setString(&c.WhatsApp.SQLitePath, "CODEXCLAW_WHATSAPP_SQLITE_PATH")
	setBool(&c.WhatsApp.QR, "CODEXCLAW_WHATSAPP_QR")
	setString(&c.Sessions.SQLitePath, "CODEXCLAW_SESSIONS_SQLITE_PATH")
	setString(&c.Sessions.ContextMode, "CODEXCLAW_SESSIONS_CONTEXT_MODE")
	setBool(&c.Sessions.AutoCompact, "CODEXCLAW_SESSIONS_AUTO_COMPACT")
	setInt64(&c.Sessions.AutoCompactAfterTokens, "CODEXCLAW_SESSIONS_AUTO_COMPACT_AFTER_TOKENS")
	setString(&c.Media.Dir, "CODEXCLAW_MEDIA_DIR")
	setBool(&c.Allowlist.Enabled, "CODEXCLAW_ALLOWLIST_ENABLED")
}

func defaultStorageRoot() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codex-claw")
	}
	return filepath.Join(".", ".codex-claw")
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "'\"")
		if key == "" {
			continue
		}
		_ = os.Setenv(key, os.ExpandEnv(value))
	}
	return scanner.Err()
}

func setString(target *string, keys ...string) {
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			*target = value
			return
		}
	}
}

func setBool(target *bool, key string) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	parsed, err := strconv.ParseBool(value)
	if err == nil {
		*target = parsed
	}
}

func setInt(target *int, key string) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	parsed, err := strconv.Atoi(value)
	if err == nil {
		*target = parsed
	}
}

func setInt64(target *int64, key string) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		*target = parsed
	}
}

func (c Config) TelegramActive() bool {
	switch strings.ToLower(strings.TrimSpace(c.Service.Mode)) {
	case "telegram", "both":
		return c.Telegram.Enabled
	default:
		return false
	}
}

func (c Config) WhatsAppActive() bool {
	switch strings.ToLower(strings.TrimSpace(c.Service.Mode)) {
	case "whatsapp", "both":
		return c.WhatsApp.Enabled
	default:
		return false
	}
}

func validServiceMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "telegram", "whatsapp", "both":
		return true
	default:
		return false
	}
}

func ValidSessionContextMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "minimal", "persistent":
		return true
	default:
		return false
	}
}

func (s SessionsConfig) MinimalContext() bool {
	return strings.EqualFold(strings.TrimSpace(s.ContextMode), "minimal")
}

func (c Config) Validate() error {
	if !validServiceMode(c.Service.Mode) {
		return errors.New("service.mode must be one of: telegram, whatsapp, both")
	}
	if !ValidSessionContextMode(c.Sessions.ContextMode) {
		return errors.New("sessions.context_mode must be one of: minimal, persistent")
	}
	if c.Codex.Command == "" {
		return errors.New("codex.command is required")
	}
	if c.TelegramActive() && c.Telegram.Token == "" {
		return errors.New("telegram.token is required when telegram is enabled")
	}
	if c.WhatsAppActive() && c.WhatsApp.SQLitePath == "" {
		return errors.New("whatsapp.sqlite_path is required when whatsapp is enabled")
	}
	if c.Sessions.SQLitePath == "" {
		return errors.New("sessions.sqlite_path is required")
	}
	return nil
}
