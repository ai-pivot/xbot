package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"xbot/protocol"

	"github.com/joho/godotenv"
)

func init() {
	if err := godotenv.Load(".env"); err != nil {
		slog.Debug("failed to load .env file, using environment variables only", "error", err)
	}
}

// Duration is like time.Duration but serializes to human-readable strings in JSON
// (e.g. "30m0s" instead of 1800000000000). It accepts both string and number
// formats when deserializing for backward compatibility with old config files.
type Duration time.Duration

// DefaultMaxContextTokens is the default context window size (200k tokens)
// used when no per-model or subscription override is configured.
const DefaultMaxContextTokens = 200_000

// DefaultMaxOutputTokens is the default max output tokens (32k) for LLM responses.
const DefaultMaxOutputTokens = 32_768

// Duration constants for use in config defaults and comparisons.
const (
	Nanosecond  Duration = 1
	Microsecond          = 1000 * Nanosecond
	Millisecond          = 1000 * Microsecond
	Second               = 1000 * Millisecond
	Minute               = 60 * Second
	Hour                 = 60 * Minute
)

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler. Accepts both human-readable strings
// ("30m", "1h30m") and legacy nanosecond numbers (1800000000000).
func (d *Duration) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		dur, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(dur)
		return nil
	}
	var ns int64
	if err := json.Unmarshal(b, &ns); err != nil {
		return fmt.Errorf("duration must be a string like \"30m\" or a number (nanoseconds)")
	}
	*d = Duration(ns)
	return nil
}

// OAuthConfig OAuth 配置
type OAuthConfig struct {
	Enable  bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	BaseURL string `json:"base_url"`
}

// SandboxConfig 沙箱配置
type SandboxConfig struct {
	Mode        string   `json:"mode"`
	RemoteMode  string   `json:"remote_mode"`
	DockerImage string   `json:"docker_image"`
	HostWorkDir string   `json:"host_work_dir"`
	IdleTimeout Duration `json:"idle_timeout"`
	WSPort      int      `json:"ws_port"`
	AuthToken   string   `json:"auth_token"`
	PublicURL   string   `json:"public_url"`
}

// QQConfig QQ 机器人渠道配置
type QQConfig struct {
	Enabled      bool     `json:"enabled"`
	AppID        string   `json:"app_id"`
	ClientSecret string   `json:"client_secret"`
	AllowFrom    []string `json:"allow_from"`
}

// NapCatConfig NapCat (OneBot 11) 渠道配置
type NapCatConfig struct {
	Enabled   bool     `json:"enabled"`
	WSUrl     string   `json:"ws_url"`
	Token     string   `json:"token"`
	AllowFrom []string `json:"allow_from"`
}

// EmbeddingConfig Embedding 配置
type EmbeddingConfig struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

// StartupNotifyConfig 启动通知配置
type StartupNotifyConfig struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// AdminConfig 管理员配置
type AdminConfig struct {
	ChatID string `json:"chat_id"`
	Token  string `json:"token"`
}

// OSSConfig 对象存储配置
type OSSConfig struct {
	Provider       string `json:"provider"`
	QiniuAccessKey string `json:"qiniu_access_key"`
	QiniuSecretKey string `json:"qiniu_secret_key"`
	QiniuBucket    string `json:"qiniu_bucket"`
	QiniuDomain    string `json:"qiniu_domain"`
	QiniuRegion    string `json:"qiniu_region"`
}

// EventWebhookConfig 事件 Webhook 配置
type EventWebhookConfig struct {
	Enable      bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	BaseURL     string `json:"base_url"`
	MaxBodySize int64  `json:"max_body_size"`
	RateLimit   int    `json:"rate_limit"` // max requests per minute per trigger
}

// WebConfig Web 渠道配置
type WebConfig struct {
	Enable           bool   `json:"enabled"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	StaticDir        string `json:"static_dir"`
	UploadDir        string `json:"upload_dir"`
	PersonaIsolation bool   `json:"persona_isolation"`
	InviteOnly       bool   `json:"invite_only"`
}

// Config 应用配置
// CLIConfig CLI 客户端配置（存储在 config.json，供 xbot-cli 读取）。
type CLIConfig struct {
	// ServerURL 指定远端 agent server 的 WebSocket 地址（如 ws://localhost:8080）。
	// 若非空，xbot-cli 默认以 RemoteBackend 连接该 server，而非本地运行 agent。
	// 可通过 --server flag 在命令行覆盖此值。
	ServerURL string `json:"server_url,omitempty"`
	// Token 连接 server 时使用的认证 token（对应 server 端的 admin.token）。
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
	Plugins       PluginConfig         `json:"plugins,omitempty"`

	// Channels 存储插件 channel 的配置。key 是 channel name（如 "telegram"）。
	// 内置 channel（feishu/qq/napcat/web）使用各自的结构体字段，
	// 插件 channel 使用此 map 存储任意 key-value 配置。
	Channels map[string]map[string]string `json:"channels,omitempty"`

	// CLISetupCompleted is set to true after the first-run setup wizard
	// completes successfully. Used by isFirstRun() to avoid showing the
	// setup panel on every startup when credentials are stored in DB
	// (user_llm_subscriptions) rather than config.json.
	CLISetupCompleted bool `json:"cli_setup_completed,omitempty"`
}

// ExperimentalConfig holds experimental features that may change or be removed.
type ExperimentalConfig struct {
	// AutoWorktree enables automatic git worktree creation when multiple agents
	// work on the same repo. Default: false (opt-in experimental).
	AutoWorktree bool `json:"auto_worktree,omitempty"`
}

// PluginConfig configures the plugin system.
type PluginConfig struct {
	// Enabled controls whether the plugin system is active.
	// Defaults to true when not explicitly set.
	Enabled *bool `json:"enabled,omitempty"`

	// Dirs is a list of additional directories to scan for plugins.
	// Defaults to ~/.xbot/plugins/ if empty.
	Dirs []string `json:"dirs,omitempty"`

	// DisabledPlugins is a list of plugin IDs to skip during discovery.
	DisabledPlugins []string `json:"disabled_plugins,omitempty"`

	// AllowUnverified allows loading plugins without verified manifests.
	AllowUnverified bool `json:"allow_unverified,omitempty"`
}

// IsEnabled returns whether the plugin system is enabled.
// Defaults to true when Enabled is nil (not explicitly configured).
func (p PluginConfig) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

// FeishuConfig 飞书渠道配置
type FeishuConfig struct {
	Enabled           bool     `json:"enabled"`
	AppID             string   `json:"app_id"`
	AppSecret         string   `json:"app_secret"`
	EncryptKey        string   `json:"encrypt_key"`
	VerificationToken string   `json:"verification_token"`
	AllowFrom         []string `json:"allow_from"`
	Domain            string   `json:"domain"`
}

// AgentConfig Agent 配置
type AgentConfig struct {
	MaxIterations  int    `json:"max_iterations"`
	MaxConcurrency int    `json:"max_concurrency"`
	MemoryProvider string `json:"memory_provider"`
	WorkDir        string `json:"work_dir"`
	PromptFile     string `json:"prompt_file"`

	MCPInactivityTimeout Duration `json:"mcp_inactivity_timeout"`
	MCPCleanupInterval   Duration `json:"mcp_cleanup_interval"`
	SessionCacheTimeout  Duration `json:"session_cache_timeout"`

	ContextMode string `json:"context_mode"`
	// EnableAutoCompress 为 nil 表示 JSON 未写该字段，Load 后与未设置 AGENT_ENABLE_AUTO_COMPRESS 一致，默认启用压缩。
	EnableAutoCompress   *bool          `json:"enable_auto_compress,omitempty"`
	MaxContextTokens     int            `json:"max_context_tokens"`
	ModelContexts        map[string]int `json:"model_contexts,omitempty"` // model -> max context tokens, overrides MaxContextTokens
	CompressionThreshold float64        `json:"compression_threshold"`
	DynamicMaxTokens     *bool          `json:"dynamic_max_tokens,omitempty"` // DEPRECATED: no longer used, kept for config.json compat

	PurgeOldMessages bool `json:"purge_old_messages"`

	MaxSubAgentDepth int `json:"max_sub_agent_depth"`

	// Experimental features
	Experimental ExperimentalConfig `json:"experimental,omitempty"`

	LLMRetryAttempts int      `json:"llm_retry_attempts"`
	LLMRetryDelay    Duration `json:"llm_retry_delay"`
	LLMRetryMaxDelay Duration `json:"llm_retry_max_delay"`
	LLMRetryTimeout  Duration `json:"llm_retry_timeout"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	ReadTimeout  Duration `json:"read_timeout"`
	WriteTimeout Duration `json:"write_timeout"`
}

// LLMConfig LLM 配置
type LLMConfig struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"` // 0 = use default (DefaultMaxOutputTokens)
	ThinkingMode    string `json:"thinking_mode,omitempty"`
}

// SubscriptionConfig CLI 订阅配置（存储在 config.json，不存数据库）。
// Alias to protocol.Subscription — the canonical definition shared across all packages.
type SubscriptionConfig = protocol.Subscription

// PerModelConfig stores per-model token overrides within a subscription.
// Alias to protocol.PerModelConfig — the canonical definition used across all packages.
type PerModelConfig = protocol.PerModelConfig

// LogConfig 日志配置
type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// PProfConfig pprof 配置
type PProfConfig struct {
	Enable bool   `json:"enabled"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

// XbotHome 返回 xbot 全局目录路径（$XBOT_HOME 或 ~/.xbot）。
// 目录如果不存在会自动创建。
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

// ConfigFilePath 返回全局配置文件路径。
func ConfigFilePath() string {
	return filepath.Join(XbotHome(), "config.json")
}

// DBFilePath 返回全局数据库文件路径。
func DBFilePath() string {
	return filepath.Join(XbotHome(), "xbot.db")
}

// fieldType represents the expected JSON type for a config field.
// Used by normalizeConfigTypes to coerce string values to the correct type.
type fieldType int

const (
	ftInt   fieldType = iota // int fields: port, max_iterations, etc.
	ftInt64                  // int64 fields: max_body_size
	ftFloat                  // float64 fields: compression_threshold
	ftBool                   // bool fields: enabled, enable, active
)

// configTypeSchema defines the expected JSON types for config fields.
// Top-level keys map to section names; each section maps field names to expected types.
// This schema covers all int/bool/float64 fields across all config structs.
// When adding new numeric/bool fields to any config struct, add them here too.
var configTypeSchema = map[string]map[string]fieldType{
	"server": {
		"port": ftInt,
	},
	"web": {
		"enabled":           ftBool,
		"port":              ftInt,
		"persona_isolation": ftBool,
		"invite_only":       ftBool,
	},
	"oauth": {
		"enabled": ftBool,
		"port":    ftInt,
	},
	"pprof": {
		"enabled": ftBool,
		"port":    ftInt,
	},
	"feishu": {
		"enabled": ftBool,
	},
	"qq": {
		"enabled": ftBool,
	},
	"napcat": {
		"enabled": ftBool,
	},
	"agent": {
		"max_iterations":        ftInt,
		"max_concurrency":       ftInt,
		"max_context_tokens":    ftInt,
		"enable_auto_compress":  ftBool,
		"compression_threshold": ftFloat,
		"purge_old_messages":    ftBool,
		"max_sub_agent_depth":   ftInt,
		"llm_retry_attempts":    ftInt,
	},
	"embedding": {
		"max_tokens": ftInt,
	},
	"sandbox": {
		"ws_port": ftInt,
	},
	"event_webhook": {
		"enabled":       ftBool,
		"port":          ftInt,
		"max_body_size": ftInt64,
		"rate_limit":    ftInt,
	},
	"plugins": {
		"enabled":          ftBool,
		"allow_unverified": ftBool,
	},
	"llm": {
		"max_output_tokens": ftInt,
	},
}

// subscriptionTypeSchema defines expected types for subscription array items.
var subscriptionTypeSchema = map[string]fieldType{
	"max_output_tokens": ftInt,
	"max_context":       ftInt,
	"active":            ftBool,
}

// coerceRawValue checks if a JSON RawMessage value is a string that should be
// int/bool/float and returns the corrected JSON bytes. Returns the original
// value unchanged if no coercion is needed or possible.
func coerceRawValue(raw json.RawMessage, ft fieldType) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if len(s) == 0 || s[0] != '"' {
		return raw // not a string, leave untouched
	}
	// Extract the string content
	var strVal string
	if json.Unmarshal(raw, &strVal) != nil {
		return raw
	}
	switch ft {
	case ftBool:
		switch strings.ToLower(strings.TrimSpace(strVal)) {
		case "true", "1", "yes", "on":
			return json.RawMessage(`true`)
		case "false", "0", "no", "off", "":
			return json.RawMessage(`false`)
		}
	case ftInt, ftInt64:
		if n, err := strconv.ParseInt(strings.TrimSpace(strVal), 10, 64); err == nil {
			return json.RawMessage(strconv.FormatInt(n, 10))
		}
	case ftFloat:
		if f, err := strconv.ParseFloat(strings.TrimSpace(strVal), 64); err == nil {
			b, _ := json.Marshal(f)
			return json.RawMessage(b)
		}
	}
	return raw // can't coerce, leave as-is
}

// normalizeObjectFields fixes string→type mismatches in a JSON object.
// Takes raw JSON object bytes and a field schema, returns fixed bytes and whether changes were made.
func normalizeObjectFields(objRaw json.RawMessage, schema map[string]fieldType) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(objRaw, &obj) != nil {
		return objRaw, false
	}
	changed := false
	for key, ft := range schema {
		val, ok := obj[key]
		if !ok {
			continue
		}
		fixed := coerceRawValue(val, ft)
		if string(fixed) != string(val) {
			obj[key] = fixed
			changed = true
		}
	}
	if !changed {
		return objRaw, false
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return objRaw, false
	}
	return out, true
}

// normalizeLegacyEnableKeys renames legacy "enable" keys to "enabled"
// at the top level of each known config section. This ensures backward
// compatibility with config files that use the old "enable" spelling.
func normalizeLegacyEnableKeys(data []byte) []byte {
	sections := []string{"web", "oauth", "pprof", "event_webhook"}
	for _, section := range sections {
		// Pattern: "section": { ... "enable": ... }
		// We need to replace "enable" with "enabled" within that section only
		// Simple approach: find "section" and then replace "enable" within its block
		oldKey := fmt.Sprintf(`"%s": {`, section)
		idx := bytes.Index(data, []byte(oldKey))
		if idx < 0 {
			continue
		}
		// Find the matching closing brace
		depth := 0
		start := idx + len(oldKey) - 1 // point to '{'
		end := start
		for i := start; i < len(data); i++ {
			if data[i] == '{' {
				depth++
			} else if data[i] == '}' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end == start {
			continue
		}
		// Within this block, replace "enable" with "enabled"
		block := data[start : end+1]
		block = bytes.Replace(block, []byte(`"enable"`), []byte(`"enabled"`), 1)
		data = append(data[:start], append(block, data[end+1:]...)...)
	}
	return data
}

// normalizeConfigTypes preprocesses raw JSON bytes to coerce string values
// into the types expected by Go config structs. This handles config files
// written by install scripts (e.g., jq --arg always writes strings) or
// manually edited by users who may write "8082" instead of 8082.
//
// Uses json.RawMessage for surgical fixes — only modifies fields that need
// coercion, everything else (formatting, unknown keys, comments-safe structure)
// is preserved through the RawMessage layer.
//
// Returns the (possibly modified) JSON bytes. If preprocessing fails,
// returns the original data unchanged — the subsequent json.Unmarshal
// will then produce its own descriptive error.
func normalizeConfigTypes(data []byte) []byte {
	// Fast path: try direct unmarshal into Config struct first.
	// If Go can handle the JSON as-is, no preprocessing needed.
	var probe Config
	if json.Unmarshal(data, &probe) == nil {
		return data
	}

	// Slow path: direct parse failed (likely string values where int/bool expected).
	// Parse into raw map and surgically fix type mismatches.
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return data // can't even parse as JSON object
	}

	changed := false

	// Fix top-level sections
	for section, fields := range configTypeSchema {
		sectionRaw, ok := raw[section]
		if !ok {
			continue
		}
		fixed, wasChanged := normalizeObjectFields(sectionRaw, fields)
		if wasChanged {
			raw[section] = fixed
			changed = true
		}
	}

	// Fix subscriptions array items (including nested per_model_configs)
	if subsRaw, ok := raw["subscriptions"]; ok {
		var subs []json.RawMessage
		if json.Unmarshal(subsRaw, &subs) == nil {
			fixed := make([]json.RawMessage, len(subs))
			subChanged := false
			for i, sub := range subs {
				fixed[i], subChanged = normalizeObjectFields(sub, subscriptionTypeSchema)
				// Also fix nested per_model_configs
				var subObj map[string]json.RawMessage
				if json.Unmarshal(fixed[i], &subObj) == nil {
					if pmcRaw, ok := subObj["per_model_configs"]; ok {
						var pmcMap map[string]json.RawMessage
						if json.Unmarshal(pmcRaw, &pmcMap) == nil {
							pmcChanged := false
							for model, modelRaw := range pmcMap {
								fixedPMC, c := normalizeObjectFields(modelRaw, subscriptionTypeSchema)
								if c {
									pmcMap[model] = fixedPMC
									pmcChanged = true
								}
							}
							if pmcChanged {
								out, _ := json.Marshal(pmcMap)
								subObj["per_model_configs"] = out
								out2, _ := json.Marshal(subObj)
								fixed[i] = out2
								subChanged = true
							}
						}
					}
				}
			}
			if subChanged {
				out, _ := json.Marshal(fixed)
				raw["subscriptions"] = out
				changed = true
			}
		}
	}

	// Top-level bool field
	if v, ok := raw["cli_setup_completed"]; ok {
		fixed := coerceRawValue(v, ftBool)
		if string(fixed) != string(v) {
			raw["cli_setup_completed"] = fixed
			changed = true
		}
	}

	if !changed {
		return data
	}

	result, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	return result
}

// LoadFromFile 从 JSON 文件加载配置。只覆盖文件中存在的非零值字段。
// 对 int/bool/float64 字段自动做字符串兼容（如 "8082" → 8082, "true" → true），
// 防止安装脚本（jq --arg）或手动编辑写入的字符串值导致反序列化失败。
func LoadFromFile(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// 预处理：将字符串值转换为 struct 期望的类型
	data = normalizeLegacyEnableKeys(data)
	data = normalizeConfigTypes(data)
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse config file, ignoring", "path", path, "error", err)
		return nil
	}
	return &cfg
}

// SaveToFile 将配置保存到 JSON 文件（原子写入：先写临时文件再 rename）。
// 它会先读取磁盘上已有的文件，将 Go struct 序列化后的顶层 key 覆盖到原始 JSON 上，
// 同时保留磁盘文件中存在但 Go struct 未定义的字段（未知 key）。
// 这样用户手动添加的自定义字段或未来新增的 struct 字段不会被静默丢弃。
func SaveToFile(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// 序列化 Go struct
	structData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// 尝试读取磁盘上已有的文件，做 JSON 级合并以保留未知字段
	finalData := structData
	if existing, readErr := os.ReadFile(path); readErr == nil && len(existing) > 0 {
		// Normalize existing data so dirty string values don't break the merge
		existing = normalizeConfigTypes(existing)
		if merged, mergeErr := mergeJSONPreserveUnknown(existing, structData); mergeErr == nil {
			finalData = merged
		}
		// 合并失败时回退到纯 struct 序列化（安全降级）
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

// mergeJSONPreserveUnknown 将 structData 的顶层 key 深度合并到 existing 上。
// 对于两边都是 JSON object 的嵌套值，递归合并以保留 unknown 字段。
// structData 中的 key 始终覆盖 existing 中的同名 key（非 object 时直接替换）。
func mergeJSONPreserveUnknown(existing, structData []byte) ([]byte, error) {
	var existingMap map[string]json.RawMessage
	if err := json.Unmarshal(existing, &existingMap); err != nil {
		return nil, err
	}
	var structMap map[string]json.RawMessage
	if err := json.Unmarshal(structData, &structMap); err != nil {
		return nil, err
	}
	// 递归合并：两边都是 object 时深度合并，否则 struct 覆盖
	for k, structVal := range structMap {
		if existingVal, ok := existingMap[k]; ok {
			merged, err := deepMergeJSON(existingVal, structVal)
			if err != nil {
				// 降级为直接覆盖
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

// deepMergeJSON 对两个 JSON 值做深度合并。
// 如果两者都是 JSON object，递归合并（structVal 的 key 覆盖 existingVal）。
// 如果 structVal 是 JSON null（Go nil 指针/接口零值），保留 existing 值（防止覆盖）。
// 否则返回 structVal（直接替换）。
func deepMergeJSON(existing, structVal json.RawMessage) (json.RawMessage, error) {
	// structVal 为 null 时保留现有值，防止 Go 零值覆盖磁盘数据
	if len(structVal) == 0 || string(structVal) == "null" {
		return existing, nil
	}
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

func setDurationEnv(key string, dst *Duration) {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*dst = Duration(d)
		}
	}
}

func setSecondsEnv(key string, dst *Duration) {
	if v := os.Getenv(key); v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			*dst = Duration(sec) * Second
		}
	}
}

// applyEnvOverrides 用环境变量覆盖配置（与 README / .env.example 中的变量名一致，优先级高于 config.json）。
//
// 策略：环境变量始终覆盖 config.json 的值（不做零值检测）。
// 这保证了可预测的行为：用户设置环境变量就意味着覆盖，无需关心 config.json 里写了什么。
// 默认值填充在 Load() 函数中，只对 config.json 和环境变量都未设置的字段生效。
func applyEnvOverrides(cfg *Config) {
	// Server
	setStringEnv("SERVER_HOST", &cfg.Server.Host)
	setIntEnv("SERVER_PORT", &cfg.Server.Port)
	setSecondsEnv("SERVER_READ_TIMEOUT", &cfg.Server.ReadTimeout)
	setSecondsEnv("SERVER_WRITE_TIMEOUT", &cfg.Server.WriteTimeout)

	// LLM
	setStringEnv("LLM_PROVIDER", &cfg.LLM.Provider)
	setStringEnv("LLM_BASE_URL", &cfg.LLM.BaseURL)
	setStringEnv("LLM_API_KEY", &cfg.LLM.APIKey)
	setStringEnv("LLM_MODEL", &cfg.LLM.Model)
	setIntEnv("LLM_RETRY_ATTEMPTS", &cfg.Agent.LLMRetryAttempts)
	setDurationEnv("LLM_RETRY_DELAY", &cfg.Agent.LLMRetryDelay)
	setDurationEnv("LLM_RETRY_MAX_DELAY", &cfg.Agent.LLMRetryMaxDelay)
	setDurationEnv("LLM_RETRY_TIMEOUT", &cfg.Agent.LLMRetryTimeout)

	// Log
	setStringEnv("LOG_LEVEL", &cfg.Log.Level)
	setStringEnv("LOG_FORMAT", &cfg.Log.Format)

	// Embedding
	setStringEnv("LLM_EMBEDDING_PROVIDER", &cfg.Embedding.Provider)
	setStringEnv("LLM_EMBEDDING_BASE_URL", &cfg.Embedding.BaseURL)
	setStringEnv("LLM_EMBEDDING_API_KEY", &cfg.Embedding.APIKey)
	setStringEnv("LLM_EMBEDDING_MODEL", &cfg.Embedding.Model)
	setIntEnv("LLM_EMBEDDING_MAX_TOKENS", &cfg.Embedding.MaxTokens)

	// Agent
	setStringEnv("WORK_DIR", &cfg.Agent.WorkDir)
	setStringEnv("PROMPT_FILE", &cfg.Agent.PromptFile)
	// SINGLE_USER env var removed — singleUser normalization is no longer used
	setStringEnv("MEMORY_PROVIDER", &cfg.Agent.MemoryProvider)
	setIntEnv("AGENT_MAX_ITERATIONS", &cfg.Agent.MaxIterations)
	setIntEnv("AGENT_MAX_CONCURRENCY", &cfg.Agent.MaxConcurrency)
	setDurationEnv("MCP_INACTIVITY_TIMEOUT", &cfg.Agent.MCPInactivityTimeout)
	setDurationEnv("MCP_CLEANUP_INTERVAL", &cfg.Agent.MCPCleanupInterval)
	setDurationEnv("SESSION_CACHE_TIMEOUT", &cfg.Agent.SessionCacheTimeout)
	setStringEnv("AGENT_CONTEXT_MODE", &cfg.Agent.ContextMode)
	// AGENT_ENABLE_AUTO_COMPRESS: *bool — keep inline
	if v := os.Getenv("AGENT_ENABLE_AUTO_COMPRESS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Agent.EnableAutoCompress = &b
		}
	}
	setIntEnv("AGENT_MAX_CONTEXT_TOKENS", &cfg.Agent.MaxContextTokens)
	setFloatEnv("AGENT_COMPRESSION_THRESHOLD", &cfg.Agent.CompressionThreshold)
	setBoolEnv("AGENT_PURGE_OLD_MESSAGES", &cfg.Agent.PurgeOldMessages)
	setIntEnv("MAX_SUBAGENT_DEPTH", &cfg.Agent.MaxSubAgentDepth)

	// Sandbox
	setStringEnv("SANDBOX_MODE", &cfg.Sandbox.Mode)
	setStringEnv("SANDBOX_REMOTE_MODE", &cfg.Sandbox.RemoteMode)
	setStringEnv("SANDBOX_DOCKER_IMAGE", &cfg.Sandbox.DockerImage)
	setStringEnv("HOST_WORK_DIR", &cfg.Sandbox.HostWorkDir)
	// SANDBOX_IDLE_TIMEOUT_MINUTES: minutes → Duration — keep inline
	if v := os.Getenv("SANDBOX_IDLE_TIMEOUT_MINUTES"); v != "" {
		if min, err := strconv.Atoi(v); err == nil {
			cfg.Sandbox.IdleTimeout = Duration(min) * Minute
		}
	}
	setIntEnv("SANDBOX_WS_PORT", &cfg.Sandbox.WSPort)
	setStringEnv("SANDBOX_AUTH_TOKEN", &cfg.Sandbox.AuthToken)
	setStringEnv("SANDBOX_PUBLIC_URL", &cfg.Sandbox.PublicURL)

	// Feishu
	setBoolEnv("FEISHU_ENABLED", &cfg.Feishu.Enabled)
	setStringEnv("FEISHU_APP_ID", &cfg.Feishu.AppID)
	setStringEnv("FEISHU_APP_SECRET", &cfg.Feishu.AppSecret)
	setStringEnv("FEISHU_ENCRYPT_KEY", &cfg.Feishu.EncryptKey)
	setStringEnv("FEISHU_VERIFICATION_TOKEN", &cfg.Feishu.VerificationToken)
	if v, ok := os.LookupEnv("FEISHU_ALLOW_FROM"); ok {
		cfg.Feishu.AllowFrom = splitCommaTrim(v)
	}
	setStringEnv("FEISHU_DOMAIN", &cfg.Feishu.Domain)

	// QQ
	setBoolEnv("QQ_ENABLED", &cfg.QQ.Enabled)
	setStringEnv("QQ_APP_ID", &cfg.QQ.AppID)
	setStringEnv("QQ_CLIENT_SECRET", &cfg.QQ.ClientSecret)
	if v, ok := os.LookupEnv("QQ_ALLOW_FROM"); ok {
		cfg.QQ.AllowFrom = splitCommaTrim(v)
	}

	// NapCat
	setBoolEnv("NAPCAT_ENABLED", &cfg.NapCat.Enabled)
	setStringEnv("NAPCAT_WS_URL", &cfg.NapCat.WSUrl)
	setStringEnv("NAPCAT_TOKEN", &cfg.NapCat.Token)
	if v, ok := os.LookupEnv("NAPCAT_ALLOW_FROM"); ok {
		cfg.NapCat.AllowFrom = splitCommaTrim(v)
	}

	// Web
	setBoolEnv("WEB_ENABLED", &cfg.Web.Enable)
	setStringEnv("WEB_HOST", &cfg.Web.Host)
	setIntEnv("WEB_PORT", &cfg.Web.Port)
	setStringEnv("WEB_STATIC_DIR", &cfg.Web.StaticDir)
	setStringEnv("WEB_UPLOAD_DIR", &cfg.Web.UploadDir)
	setBoolEnv("WEB_PERSONA_ISOLATION", &cfg.Web.PersonaIsolation)
	setBoolEnv("WEB_INVITE_ONLY", &cfg.Web.InviteOnly)

	// Event Webhook
	setBoolEnv("EVENT_WEBHOOK_ENABLE", &cfg.EventWebhook.Enable)
	setStringEnv("EVENT_WEBHOOK_HOST", &cfg.EventWebhook.Host)
	setIntEnv("EVENT_WEBHOOK_PORT", &cfg.EventWebhook.Port)
	setStringEnv("EVENT_WEBHOOK_BASE_URL", &cfg.EventWebhook.BaseURL)
	// EVENT_WEBHOOK_MAX_BODY_SIZE: int64 — keep inline
	if v := os.Getenv("EVENT_WEBHOOK_MAX_BODY_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EventWebhook.MaxBodySize = int64(i)
		}
	}
	setIntEnv("EVENT_WEBHOOK_RATE_LIMIT", &cfg.EventWebhook.RateLimit)

	// OAuth
	setBoolEnv("OAUTH_ENABLE", &cfg.OAuth.Enable)
	setStringEnv("OAUTH_HOST", &cfg.OAuth.Host)
	setIntEnv("OAUTH_PORT", &cfg.OAuth.Port)
	setStringEnv("OAUTH_BASE_URL", &cfg.OAuth.BaseURL)

	// PProf
	setBoolEnv("PPROF_ENABLE", &cfg.PProf.Enable)
	setStringEnv("PPROF_HOST", &cfg.PProf.Host)
	setIntEnv("PPROF_PORT", &cfg.PProf.Port)

	// Startup / Admin
	setStringEnv("STARTUP_NOTIFY_CHANNEL", &cfg.StartupNotify.Channel)
	setStringEnv("STARTUP_NOTIFY_CHAT_ID", &cfg.StartupNotify.ChatID)
	setStringEnv("ADMIN_CHAT_ID", &cfg.Admin.ChatID)
	setStringEnv("ADMIN_TOKEN", &cfg.Admin.Token)

	// Misc
	setStringEnv("TAVILY_API_KEY", &cfg.TavilyAPIKey)
}

// EffectiveEnableAutoCompress 返回是否启用自动压缩；config.json 省略该字段时与文档默认一致，为 true。
func (a AgentConfig) EffectiveEnableAutoCompress() bool {
	if a.EnableAutoCompress == nil {
		return true
	}
	return *a.EnableAutoCompress
}

// Load 加载配置：先从全局 config.json 读取基础值，再用环境变量覆盖。
// 这保证了：config.json 提供持久化配置，环境变量用于临时覆盖（如 CI/Docker）。
func Load() *Config {
	cfg := LoadFromFile(ConfigFilePath())
	if cfg == nil {
		cfg = &Config{}
	}
	applyEnvOverrides(cfg)

	// 填充 CLI 常用的默认值（仅在配置和环境变量都未设置时生效）
	// 注意: LLM Provider/BaseURL 不设默认值。
	// 首次启动时用户未配置任何 LLM，这里保持空值，避免创建指向 api.openai.com 的无意义 client。
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
		cfg.Agent.MaxConcurrency = 100
	}
	if cfg.Agent.MCPInactivityTimeout == 0 {
		cfg.Agent.MCPInactivityTimeout = 30 * Minute
	}
	if cfg.Agent.MCPCleanupInterval == 0 {
		cfg.Agent.MCPCleanupInterval = 5 * Minute
	}
	if cfg.Agent.SessionCacheTimeout == 0 {
		cfg.Agent.SessionCacheTimeout = 24 * Hour
	}
	if cfg.Agent.LLMRetryAttempts == 0 {
		cfg.Agent.LLMRetryAttempts = 5
	}
	if cfg.Agent.LLMRetryDelay == 0 {
		cfg.Agent.LLMRetryDelay = 1 * Second
	}
	if cfg.Agent.LLMRetryMaxDelay == 0 {
		cfg.Agent.LLMRetryMaxDelay = 30 * Second
	}
	if cfg.Agent.LLMRetryTimeout == 0 {
		cfg.Agent.LLMRetryTimeout = 120 * Second
	}
	if cfg.Sandbox.Mode == "" {
		cfg.Sandbox.Mode = "none"
	}
	if cfg.Sandbox.IdleTimeout == 0 {
		cfg.Sandbox.IdleTimeout = 30 * Minute
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
		cfg.Agent.MaxContextTokens = DefaultMaxContextTokens
	}
	// Plugin system defaults to enabled (IsEnabled() returns true when Enabled is nil).
	// Explicitly set "enabled": false to disable.
	if cfg.Agent.CompressionThreshold == 0 {
		cfg.Agent.CompressionThreshold = 0.9
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
		cfg.Server.ReadTimeout = 30 * Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 120 * Second
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

// getAdminChatID 获取管理员会话 ID，实现回退逻辑
// 优先读取 ADMIN_CHAT_ID，如果为空则回退到 STARTUP_NOTIFY_CHAT_ID
func getAdminChatID() string {
	if adminChatID := os.Getenv("ADMIN_CHAT_ID"); adminChatID != "" {
		return adminChatID
	}
	return os.Getenv("STARTUP_NOTIFY_CHAT_ID")
}
