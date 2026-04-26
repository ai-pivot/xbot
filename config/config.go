package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func init() {
	if err := godotenv.Load(".env"); err != nil {
		slog.Debug("failed to load .env file, using environment variables only", "error", err)
	}
}

// OAuthConfig holds OAuth configuration
type OAuthConfig struct {
	Enable  bool   `json:"enable"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	BaseURL string `json:"base_url"`
}

// SandboxConfig holds sandbox configuration
type SandboxConfig struct {
	Mode        string        `json:"mode"`
	RemoteMode  string        `json:"remote_mode"`
	DockerImage string        `json:"docker_image"`
	HostWorkDir string        `json:"host_work_dir"`
	IdleTimeout time.Duration `json:"idle_timeout"`
	WSPort      int           `json:"ws_port"`
	AuthToken   string        `json:"auth_token"`
	PublicURL   string        `json:"public_url"`
}

// QQConfig holds QQ bot channel configuration
type QQConfig struct {
	Enabled      bool     `json:"enabled"`
	AppID        string   `json:"app_id"`
	ClientSecret string   `json:"client_secret"`
	AllowFrom    []string `json:"allow_from"`
}

// NapCatConfig holds NapCat (OneBot 11) channel configuration
type NapCatConfig struct {
	Enabled   bool     `json:"enabled"`
	WSUrl     string   `json:"ws_url"`
	Token     string   `json:"token"`
	AllowFrom []string `json:"allow_from"`
}

// EmbeddingConfig holds embedding configuration
type EmbeddingConfig struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

// StartupNotifyConfig holds startup notification configuration
type StartupNotifyConfig struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// AdminConfig holds admin configuration
type AdminConfig struct {
	ChatID string `json:"chat_id"`
	Token  string `json:"token"`
}

// OSSConfig holds object storage configuration
type OSSConfig struct {
	Provider       string `json:"provider"`
	QiniuAccessKey string `json:"qiniu_access_key"`
	QiniuSecretKey string `json:"qiniu_secret_key"`
	QiniuBucket    string `json:"qiniu_bucket"`
	QiniuDomain    string `json:"qiniu_domain"`
	QiniuRegion    string `json:"qiniu_region"`
}

// EventWebhookConfig holds event webhook configuration
type EventWebhookConfig struct {
	Enable      bool   `json:"enable"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	BaseURL     string `json:"base_url"`
	MaxBodySize int64  `json:"max_body_size"`
	RateLimit   int    `json:"rate_limit"` // max requests per minute per trigger
}

// WebConfig holds Web channel configuration
type WebConfig struct {
	Enable           bool   `json:"enable"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	StaticDir        string `json:"static_dir"`
	UploadDir        string `json:"upload_dir"`
	PersonaIsolation bool   `json:"persona_isolation"`
	InviteOnly       bool   `json:"invite_only"`
}

// Config holds the application configuration
// CLIConfig holds CLI client configuration (stored in config.json, read by xbot-cli).
type CLIConfig struct {
	// ServerURL specifies the remote agent server WebSocket address (e.g. ws://localhost:8080).
	// If non-empty, xbot-cli connects to the server via RemoteBackend instead of running locally.
	// Can be overridden via --server flag on the command line.
	ServerURL string `json:"server_url,omitempty"`
	// Token is the authentication token for server connection (maps to server-side admin.token).
	Token string `json:"token,omitempty"`
}

type Config struct {
	Server        ServerConfig         `json:"server"`
	LLM           LLMConfig            `json:"llm"`
	Embedding     EmbeddingConfig      `json:"embedding"`
	Log           LogConfig            `json:"log"`
	PProf         PProfConfig          `json:"pprof"`
	Feishu        FeishuConfig         `json:"feishu"`
	QQ            QQConfig             `json:"qq"`
	NapCat        NapCatConfig         `json:"napcat"`
	Agent         AgentConfig          `json:"agent"`
	OAuth         OAuthConfig          `json:"oauth"`
	Sandbox       SandboxConfig        `json:"sandbox"`
	StartupNotify StartupNotifyConfig  `json:"startup_notify"`
	Admin         AdminConfig          `json:"admin"`
	Web           WebConfig            `json:"web"`
	EventWebhook  EventWebhookConfig   `json:"event_webhook"`
	OSS           OSSConfig            `json:"oss"`
	TavilyAPIKey  string               `json:"tavily_api_key"`
	Subscriptions []SubscriptionConfig `json:"subscriptions,omitempty"`
	CLI           CLIConfig            `json:"cli,omitempty"`
}

// FeishuConfig holds Feishu channel configuration
type FeishuConfig struct {
	Enabled           bool     `json:"enabled"`
	AppID             string   `json:"app_id"`
	AppSecret         string   `json:"app_secret"`
	EncryptKey        string   `json:"encrypt_key"`
	VerificationToken string   `json:"verification_token"`
	AllowFrom         []string `json:"allow_from"`
	Domain            string   `json:"domain"`
}

// AgentConfig holds agent configuration
type AgentConfig struct {
	MaxIterations  int    `json:"max_iterations"`
	MaxConcurrency int    `json:"max_concurrency"`
	MemoryProvider string `json:"memory_provider"`
	WorkDir        string `json:"work_dir"`
	PromptFile     string `json:"prompt_file"`
	SingleUser     bool   `json:"single_user"` // Deprecated: no longer used, kept for config file compatibility

	MCPInactivityTimeout time.Duration `json:"mcp_inactivity_timeout"`
	MCPCleanupInterval   time.Duration `json:"mcp_cleanup_interval"`
	SessionCacheTimeout  time.Duration `json:"session_cache_timeout"`

	ContextMode string `json:"context_mode"`
	// EnableAutoCompress: nil means the field was absent in JSON; after Load, behaves the same as unset AGENT_ENABLE_AUTO_COMPRESS, defaulting to compression enabled.
	EnableAutoCompress   *bool   `json:"enable_auto_compress,omitempty"`
	MaxContextTokens     int     `json:"max_context_tokens"`
	CompressionThreshold float64 `json:"compression_threshold"`
	DynamicMaxTokens     *bool   `json:"dynamic_max_tokens,omitempty"` // DEPRECATED: no longer used, kept for config.json compat

	PurgeOldMessages bool `json:"purge_old_messages"`

	MaxSubAgentDepth int `json:"max_sub_agent_depth"`

	LLMRetryAttempts int           `json:"llm_retry_attempts"`
	LLMRetryDelay    time.Duration `json:"llm_retry_delay"`
	LLMRetryMaxDelay time.Duration `json:"llm_retry_max_delay"`
	LLMRetryTimeout  time.Duration `json:"llm_retry_timeout"`
}

// ServerConfig holds server configuration
type ServerConfig struct {
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
}

// LLMConfig holds LLM configuration
type LLMConfig struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	VanguardModel   string `json:"vanguard_model,omitempty"`
	BalanceModel    string `json:"balance_model,omitempty"`
	SwiftModel      string `json:"swift_model,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"` // 0 = use default (8192)
	ThinkingMode    string `json:"thinking_mode,omitempty"`
}

// SubscriptionConfig holds CLI subscription configuration (stored in config.json, not in database).
type SubscriptionConfig struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"` // 0 = use default (8192)
	ThinkingMode    string `json:"thinking_mode,omitempty"`     // "" = auto, "enabled", "disabled"
	Active          bool   `json:"active"`
}

// LogConfig holds log configuration
type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// PProfConfig holds pprof configuration
type PProfConfig struct {
	Enable bool   `json:"enable"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

// XbotHome returns the xbot global directory path ($XBOT_HOME or ~/.xbot).
// The directory is auto-created if it does not exist.
func XbotHome() string {
	dir := os.Getenv("XBOT_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			dir = ".xbot"
		} else {
			dir = filepath.Join(home, ".xbot")
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("failed to create xbot home directory", "path", dir, "error", err)
	}
	return dir
}

// ConfigFilePath returns the global config file path.
func ConfigFilePath() string {
	return filepath.Join(XbotHome(), "config.json")
}

// DBFilePath returns the global database file path.
func DBFilePath() string {
	return filepath.Join(XbotHome(), "xbot.db")
}

// LoadFromFile loads configuration from a JSON file. Only overwrites non-zero fields present in the file.
func LoadFromFile(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse config file, ignoring", "path", path, "error", err)
		return nil
	}
	return &cfg
}

// SaveToFile saves configuration to a JSON file (atomic: write temp file then rename)。
// It reads the existing file from disk first, then overlays the Go struct's top-level keys onto the original JSON,
// preserving keys that exist on disk but are not defined in the Go struct (unknown keys).
// This prevents silently dropping user-added custom fields or future struct fields.
func SaveToFile(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Serialize Go struct
	structData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Try to read existing file from disk and merge at JSON level to preserve unknown fields
	finalData := structData
	if existing, readErr := os.ReadFile(path); readErr == nil && len(existing) > 0 {
		if merged, mergeErr := mergeJSONPreserveUnknown(existing, structData); mergeErr == nil {
			finalData = merged
		}
		// On merge failure, fall back to pure struct serialization (safe degradation)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, finalData, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// mergeJSONPreserveUnknown deep-merges top-level keys from structData onto existing.
// For nested values that are both JSON objects, it recursively merges to preserve unknown fields.
// Keys in structData always override same-named keys in existing (non-objects are replaced directly).
func mergeJSONPreserveUnknown(existing, structData []byte) ([]byte, error) {
	var existingMap map[string]json.RawMessage
	if err := json.Unmarshal(existing, &existingMap); err != nil {
		return nil, err
	}
	var structMap map[string]json.RawMessage
	if err := json.Unmarshal(structData, &structMap); err != nil {
		return nil, err
	}
	// Recursive merge: deep merge when both are objects, otherwise struct overrides
	for k, structVal := range structMap {
		if existingVal, ok := existingMap[k]; ok {
			merged, err := deepMergeJSON(existingVal, structVal)
			if err != nil {
				// Fall back to direct override
				existingMap[k] = structVal
				continue
			}
			existingMap[k] = merged
		} else {
			existingMap[k] = structVal
		}
	}
	return json.MarshalIndent(existingMap, "", "  ")
}

// deepMergeJSON performs a deep merge of two JSON values.
// If both are JSON objects, it recursively merges (structVal keys override existingVal).
// Otherwise returns structVal (direct replacement).
func deepMergeJSON(existing, structVal json.RawMessage) (json.RawMessage, error) {
	var existingObj, structObj map[string]json.RawMessage
	existingIsObj := json.Unmarshal(existing, &existingObj) == nil
	structIsObj := json.Unmarshal(structVal, &structObj) == nil

	if existingIsObj && structIsObj {
		for k, v := range structObj {
			if ev, ok := existingObj[k]; ok {
				merged, err := deepMergeJSON(ev, v)
				if err != nil {
					existingObj[k] = v
					continue
				}
				existingObj[k] = merged
			} else {
				existingObj[k] = v
			}
		}
		return json.Marshal(existingObj)
	}
	return structVal, nil
}

func splitCommaTrim(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func setDurationEnv(key string, dst *time.Duration) {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}
}

func setSecondsEnv(key string, dst *time.Duration) {
	if v := os.Getenv(key); v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			*dst = time.Duration(sec) * time.Second
		}
	}
}

// applyEnvOverrides applies environment variable overrides to config (variable names match README/.env.example, higher priority than config.json).
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = i
		}
	}
	setSecondsEnv("SERVER_READ_TIMEOUT", &cfg.Server.ReadTimeout)
	setSecondsEnv("SERVER_WRITE_TIMEOUT", &cfg.Server.WriteTimeout)

	if v := os.Getenv("LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := os.Getenv("LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := os.Getenv("LLM_RETRY_ATTEMPTS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Agent.LLMRetryAttempts = i
		}
	}
	setDurationEnv("LLM_RETRY_DELAY", &cfg.Agent.LLMRetryDelay)
	setDurationEnv("LLM_RETRY_MAX_DELAY", &cfg.Agent.LLMRetryMaxDelay)
	setDurationEnv("LLM_RETRY_TIMEOUT", &cfg.Agent.LLMRetryTimeout)

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}

	if v := os.Getenv("LLM_EMBEDDING_PROVIDER"); v != "" {
		cfg.Embedding.Provider = v
	}
	if v := os.Getenv("LLM_EMBEDDING_BASE_URL"); v != "" {
		cfg.Embedding.BaseURL = v
	}
	if v := os.Getenv("LLM_EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.APIKey = v
	}
	if v := os.Getenv("LLM_EMBEDDING_MODEL"); v != "" {
		cfg.Embedding.Model = v
	}
	if v := os.Getenv("LLM_EMBEDDING_MAX_TOKENS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Embedding.MaxTokens = i
		}
	}

	if v := os.Getenv("WORK_DIR"); v != "" {
		cfg.Agent.WorkDir = v
	}
	if v := os.Getenv("PROMPT_FILE"); v != "" {
		cfg.Agent.PromptFile = v
	}
	// SINGLE_USER env var removed — singleUser normalization is no longer used
	if v := os.Getenv("MEMORY_PROVIDER"); v != "" {
		cfg.Agent.MemoryProvider = v
	}
	if v := os.Getenv("AGENT_MAX_ITERATIONS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxIterations == 0 {
			cfg.Agent.MaxIterations = i
		}
	}
	if v := os.Getenv("AGENT_MAX_CONCURRENCY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxConcurrency == 0 {
			cfg.Agent.MaxConcurrency = i
		}
	}
	setDurationEnv("MCP_INACTIVITY_TIMEOUT", &cfg.Agent.MCPInactivityTimeout)
	setDurationEnv("MCP_CLEANUP_INTERVAL", &cfg.Agent.MCPCleanupInterval)
	setDurationEnv("SESSION_CACHE_TIMEOUT", &cfg.Agent.SessionCacheTimeout)

	if v := os.Getenv("AGENT_CONTEXT_MODE"); v != "" {
		cfg.Agent.ContextMode = v
	}
	if v := os.Getenv("AGENT_ENABLE_AUTO_COMPRESS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Agent.EnableAutoCompress = &b
		}
	}
	if v := os.Getenv("AGENT_MAX_CONTEXT_TOKENS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxContextTokens == 0 {
			cfg.Agent.MaxContextTokens = i
		}
	}
	if v := os.Getenv("AGENT_COMPRESSION_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Agent.CompressionThreshold = f
		}
	}
	if v := os.Getenv("AGENT_PURGE_OLD_MESSAGES"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Agent.PurgeOldMessages = b
		}
	}
	if v := os.Getenv("MAX_SUBAGENT_DEPTH"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxSubAgentDepth == 0 {
			cfg.Agent.MaxSubAgentDepth = i
		}
	}

	if v := os.Getenv("SANDBOX_MODE"); v != "" {
		cfg.Sandbox.Mode = v
	}
	if v := os.Getenv("SANDBOX_REMOTE_MODE"); v != "" {
		cfg.Sandbox.RemoteMode = v
	}
	if v := os.Getenv("SANDBOX_DOCKER_IMAGE"); v != "" {
		cfg.Sandbox.DockerImage = v
	}
	if v := os.Getenv("HOST_WORK_DIR"); v != "" {
		cfg.Sandbox.HostWorkDir = v
	}
	if v := os.Getenv("SANDBOX_IDLE_TIMEOUT_MINUTES"); v != "" {
		if min, err := strconv.Atoi(v); err == nil && cfg.Sandbox.IdleTimeout == 0 {
			cfg.Sandbox.IdleTimeout = time.Duration(min) * time.Minute
		}
	}
	if v := os.Getenv("SANDBOX_WS_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Sandbox.WSPort == 0 {
			cfg.Sandbox.WSPort = i
		}
	}
	if v := os.Getenv("SANDBOX_AUTH_TOKEN"); v != "" {
		cfg.Sandbox.AuthToken = v
	}
	if v := os.Getenv("SANDBOX_PUBLIC_URL"); v != "" {
		cfg.Sandbox.PublicURL = v
	}

	if v := os.Getenv("FEISHU_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Feishu.Enabled = b
		}
	}
	if v := os.Getenv("FEISHU_APP_ID"); v != "" {
		cfg.Feishu.AppID = v
	}
	if v := os.Getenv("FEISHU_APP_SECRET"); v != "" {
		cfg.Feishu.AppSecret = v
	}
	if v := os.Getenv("FEISHU_ENCRYPT_KEY"); v != "" {
		cfg.Feishu.EncryptKey = v
	}
	if v := os.Getenv("FEISHU_VERIFICATION_TOKEN"); v != "" {
		cfg.Feishu.VerificationToken = v
	}
	if v, ok := os.LookupEnv("FEISHU_ALLOW_FROM"); ok {
		cfg.Feishu.AllowFrom = splitCommaTrim(v)
	}
	if v := os.Getenv("FEISHU_DOMAIN"); v != "" {
		cfg.Feishu.Domain = v
	}

	if v := os.Getenv("QQ_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.QQ.Enabled = b
		}
	}
	if v := os.Getenv("QQ_APP_ID"); v != "" {
		cfg.QQ.AppID = v
	}
	if v := os.Getenv("QQ_CLIENT_SECRET"); v != "" {
		cfg.QQ.ClientSecret = v
	}
	if v, ok := os.LookupEnv("QQ_ALLOW_FROM"); ok {
		cfg.QQ.AllowFrom = splitCommaTrim(v)
	}

	if v := os.Getenv("NAPCAT_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NapCat.Enabled = b
		}
	}
	if v := os.Getenv("NAPCAT_WS_URL"); v != "" {
		cfg.NapCat.WSUrl = v
	}
	if v := os.Getenv("NAPCAT_TOKEN"); v != "" {
		cfg.NapCat.Token = v
	}
	if v, ok := os.LookupEnv("NAPCAT_ALLOW_FROM"); ok {
		cfg.NapCat.AllowFrom = splitCommaTrim(v)
	}

	if v := os.Getenv("WEB_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Web.Enable = b
		}
	}
	if v := os.Getenv("WEB_HOST"); v != "" {
		cfg.Web.Host = v
	}
	if v := os.Getenv("WEB_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Web.Port = i
		}
	}
	if v := os.Getenv("WEB_STATIC_DIR"); v != "" {
		cfg.Web.StaticDir = v
	}
	if v := os.Getenv("WEB_UPLOAD_DIR"); v != "" {
		cfg.Web.UploadDir = v
	}
	if v := os.Getenv("WEB_PERSONA_ISOLATION"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Web.PersonaIsolation = b
		}
	}
	if v := os.Getenv("WEB_INVITE_ONLY"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Web.InviteOnly = b
		}
	}

	if v := os.Getenv("EVENT_WEBHOOK_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.EventWebhook.Enable = b
		}
	}
	if v := os.Getenv("EVENT_WEBHOOK_HOST"); v != "" {
		cfg.EventWebhook.Host = v
	}
	if v := os.Getenv("EVENT_WEBHOOK_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EventWebhook.Port = i
		}
	}
	if v := os.Getenv("EVENT_WEBHOOK_BASE_URL"); v != "" {
		cfg.EventWebhook.BaseURL = v
	}
	if v := os.Getenv("EVENT_WEBHOOK_MAX_BODY_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EventWebhook.MaxBodySize = int64(i)
		}
	}
	if v := os.Getenv("EVENT_WEBHOOK_RATE_LIMIT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EventWebhook.RateLimit = i
		}
	}

	if v := os.Getenv("OAUTH_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.OAuth.Enable = b
		}
	}
	if v := os.Getenv("OAUTH_HOST"); v != "" {
		cfg.OAuth.Host = v
	}
	if v := os.Getenv("OAUTH_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.OAuth.Port = i
		}
	}
	if v := os.Getenv("OAUTH_BASE_URL"); v != "" {
		cfg.OAuth.BaseURL = v
	}

	if v := os.Getenv("PPROF_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.PProf.Enable = b
		}
	}
	if v := os.Getenv("PPROF_HOST"); v != "" {
		cfg.PProf.Host = v
	}
	if v := os.Getenv("PPROF_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.PProf.Port = i
		}
	}

	if v := os.Getenv("STARTUP_NOTIFY_CHANNEL"); v != "" {
		cfg.StartupNotify.Channel = v
	}
	if v := os.Getenv("STARTUP_NOTIFY_CHAT_ID"); v != "" {
		cfg.StartupNotify.ChatID = v
	}
	if v := os.Getenv("ADMIN_CHAT_ID"); v != "" {
		cfg.Admin.ChatID = v
	}
	if v := os.Getenv("ADMIN_TOKEN"); v != "" {
		cfg.Admin.Token = v
	}

	if v := os.Getenv("TAVILY_API_KEY"); v != "" {
		cfg.TavilyAPIKey = v
	}
}

// EffectiveEnableAutoCompress returns whether auto-compress is enabled; when config.json omits the field, defaults to true per documentation.
func (a AgentConfig) EffectiveEnableAutoCompress() bool {
	if a.EnableAutoCompress == nil {
		return true
	}
	return *a.EnableAutoCompress
}

// Load loads configuration: reads base values from global config.json first, then applies environment variable overrides.
// This ensures: config.json provides persistent config, env vars for temporary overrides (e.g. CI/Docker).
func Load() *Config {
	cfg := LoadFromFile(ConfigFilePath())
	if cfg == nil {
		cfg = &Config{}
	}
	applyEnvOverrides(cfg)

	// Fill CLI-common default values (only effective when both config and env vars are unset)
	if cfg.LLM.Provider == "" {
		cfg.LLM.Provider = "openai"
	}
	if cfg.LLM.BaseURL == "" {
		cfg.LLM.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.LLM.Model == "" {
		cfg.LLM.Model = "gpt-4o"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "json"
	}
	if cfg.Agent.WorkDir == "" {
		cfg.Agent.WorkDir = "."
	}
	if cfg.Agent.PromptFile == "" {
		cfg.Agent.PromptFile = "prompt.md"
	}
	if cfg.Agent.MaxIterations == 0 {
		cfg.Agent.MaxIterations = 2000
	}
	if cfg.Agent.MaxConcurrency == 0 {
		cfg.Agent.MaxConcurrency = 3
	}
	if cfg.Agent.MCPInactivityTimeout == 0 {
		cfg.Agent.MCPInactivityTimeout = 30 * time.Minute
	}
	if cfg.Agent.MCPCleanupInterval == 0 {
		cfg.Agent.MCPCleanupInterval = 5 * time.Minute
	}
	if cfg.Agent.SessionCacheTimeout == 0 {
		cfg.Agent.SessionCacheTimeout = 24 * time.Hour
	}
	if cfg.Agent.LLMRetryAttempts == 0 {
		cfg.Agent.LLMRetryAttempts = 5
	}
	if cfg.Agent.LLMRetryDelay == 0 {
		cfg.Agent.LLMRetryDelay = 1 * time.Second
	}
	if cfg.Agent.LLMRetryMaxDelay == 0 {
		cfg.Agent.LLMRetryMaxDelay = 30 * time.Second
	}
	if cfg.Agent.LLMRetryTimeout == 0 {
		cfg.Agent.LLMRetryTimeout = 120 * time.Second
	}
	if cfg.Sandbox.Mode == "" {
		cfg.Sandbox.Mode = "docker"
	}
	if cfg.Sandbox.IdleTimeout == 0 {
		cfg.Sandbox.IdleTimeout = 30 * time.Minute
	}
	if cfg.Sandbox.DockerImage == "" {
		cfg.Sandbox.DockerImage = "ubuntu:22.04"
	}
	if cfg.Sandbox.WSPort == 0 {
		cfg.Sandbox.WSPort = 8080
	}
	if cfg.Agent.MemoryProvider == "" {
		cfg.Agent.MemoryProvider = "flat"
	}
	if cfg.OAuth.Host == "" {
		cfg.OAuth.Host = "127.0.0.1"
	}
	if cfg.OAuth.Port == 0 {
		cfg.OAuth.Port = 8081
	}
	if cfg.Web.Host == "" {
		cfg.Web.Host = "0.0.0.0"
	}
	if cfg.Web.Port == 0 {
		cfg.Web.Port = 8082
	}
	if cfg.EventWebhook.Host == "" {
		cfg.EventWebhook.Host = "0.0.0.0"
	}
	if cfg.EventWebhook.Port == 0 {
		cfg.EventWebhook.Port = 8090
	}
	if cfg.EventWebhook.MaxBodySize == 0 {
		cfg.EventWebhook.MaxBodySize = 1 << 20 // 1 MB
	}
	if cfg.EventWebhook.RateLimit == 0 {
		cfg.EventWebhook.RateLimit = 60
	}
	if cfg.NapCat.WSUrl == "" {
		cfg.NapCat.WSUrl = "ws://localhost:3001"
	}
	if cfg.PProf.Host == "" {
		cfg.PProf.Host = "localhost"
	}
	if cfg.PProf.Port == 0 {
		cfg.PProf.Port = 6060
	}
	if cfg.Embedding.MaxTokens == 0 {
		cfg.Embedding.MaxTokens = 2048
	}
	if cfg.Agent.MaxContextTokens == 0 {
		cfg.Agent.MaxContextTokens = 200000
	}
	if cfg.Agent.CompressionThreshold == 0 {
		cfg.Agent.CompressionThreshold = 0.7
	}
	if cfg.Agent.MaxSubAgentDepth == 0 {
		cfg.Agent.MaxSubAgentDepth = 6
	}
	// Server.Host/Port defaults follow Web.Host/Port since all traffic
	// (HTTP API, WebSocket, runner WS) goes through the same port.
	// Keeping them in sync avoids confusion.
	if cfg.Server.Host == "" {
		cfg.Server.Host = cfg.Web.Host // "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = cfg.Web.Port // 8082
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 120 * time.Second
	}
	if cfg.Admin.ChatID == "" {
		cfg.Admin.ChatID = getAdminChatID()
	}

	return cfg
}

// PublicWSAddr returns the WebSocket address runners should connect to.
// Uses Sandbox.PublicURL if set, otherwise falls back to the unified
// web server address (Server.Host:Server.Port, which defaults to Web.Host:Web.Port).
func (c *Config) PublicWSAddr() string {
	if c.Sandbox.PublicURL != "" {
		return c.Sandbox.PublicURL
	}
	return fmt.Sprintf("ws://%s:%d", c.Server.Host, c.Server.Port)
}

// getAdminChatID returns the admin chat ID with fallback logic
// Reads ADMIN_CHAT_ID first; if empty, falls back to STARTUP_NOTIFY_CHAT_ID
func getAdminChatID() string {
	if adminChatID := os.Getenv("ADMIN_CHAT_ID"); adminChatID != "" {
		return adminChatID
	}
	return os.Getenv("STARTUP_NOTIFY_CHAT_ID")
}
