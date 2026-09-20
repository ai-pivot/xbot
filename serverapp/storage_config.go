package serverapp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"xbot/channel"
	"xbot/config"
)

// storageApplier hot-applies a storage configuration change: rebuild the OSS
// provider and re-register the multimodal image resolver. Wired by server.go
// (where the web channel / agent / workDir exist); nil in unit tests and in
// deployments without a web channel.
var storageApplier func(cfg *config.Config) (string, error)

// storageProviderName is the provider the RUNNING instance actually uses
// ("local" / "qiniu" / "s3"). Set at startup and on every successful apply —
// surfaced as `_active` so the panel can show what is live, not just what is
// written in config.json.
var storageProviderName string

// SetStorageApplier registers the hot-apply hook.
func SetStorageApplier(fn func(cfg *config.Config) (string, error)) { storageApplier = fn }

// SetActiveStorageProvider records the provider in use (startup + apply).
func SetActiveStorageProvider(name string) { storageProviderName = name }

// maskStorageSecret shows the first 4 chars so an operator can tell which key is
// configured, never the secret itself. Mirrors maskAPIKey (serverapp/server.go).
func maskStorageSecret(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "****"
	}
	return v[:4] + "****"
}

// isMaskedStorageValue reports whether the client echoed back a masked secret
// (never write those to disk — the real credential stays untouched).
func isMaskedStorageValue(v string) bool { return strings.Contains(v, "****") }

// storageValuesFromConfig projects config.OSS into the schema-key space.
func storageValuesFromConfig(cfg *config.Config) map[string]string {
	return map[string]string{
		"provider":          cfg.OSS.Provider,
		"qiniu_access_key":  cfg.OSS.QiniuAccessKey,
		"qiniu_secret_key":  cfg.OSS.QiniuSecretKey,
		"qiniu_bucket":      cfg.OSS.QiniuBucket,
		"qiniu_domain":      cfg.OSS.QiniuDomain,
		"qiniu_region":      cfg.OSS.QiniuRegion,
		"s3_access_key":     cfg.OSS.S3AccessKey,
		"s3_secret_key":     cfg.OSS.S3SecretKey,
		"s3_bucket":         cfg.OSS.S3Bucket,
		"s3_region":         cfg.OSS.S3Region,
		"s3_endpoint":       cfg.OSS.S3Endpoint,
		"s3_domain":         cfg.OSS.S3Domain,
		"s3_use_path_style": strconv.FormatBool(cfg.OSS.S3UsePathStyle),
	}
}

// getStorageConfig returns the current file-storage values (secrets masked) plus
// the schema (`_schema`) and the provider actually running (`_active`).
func getStorageConfig() (map[string]string, error) {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		cfg = &config.Config{}
	}
	values := storageValuesFromConfig(cfg)
	// Mask secrets on read (a browser must never receive a usable credential).
	for _, k := range channel.StorageSecretKeys() {
		if v, ok := values[k]; ok {
			values[k] = maskStorageSecret(v)
		}
	}
	schemaJSON, err := json.Marshal(channel.StorageSchema())
	if err != nil {
		return nil, fmt.Errorf("marshal storage schema: %w", err)
	}
	values["_schema"] = string(schemaJSON)
	active := storageProviderName
	if active == "" {
		active = "local" // startup default before the hook records it
	}
	if cfg.OSS.Provider == "" {
		active = "local"
	}
	values["_active"] = active
	return values, nil
}

// setStorageConfig persists file-storage values to config.json and hot-applies
// them (no restart). Unknown providers and incomplete credential sets are
// rejected BEFORE anything is written — a half-configured cloud backend would
// break every upload.
func setStorageConfig(values map[string]string) (map[string]string, error) {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		cfg = &config.Config{}
	}

	// Overlay only what the caller actually submitted; masked echoes are skipped
	// (the real secret stays in place — same rule as the subscription RPC).
	get := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	if v, ok := get("provider"); ok {
		cfg.OSS.Provider = strings.TrimSpace(v)
	}
	if v, ok := get("qiniu_access_key"); ok && !isMaskedStorageValue(v) {
		cfg.OSS.QiniuAccessKey = strings.TrimSpace(v)
	}
	if v, ok := get("qiniu_secret_key"); ok && !isMaskedStorageValue(v) {
		cfg.OSS.QiniuSecretKey = strings.TrimSpace(v)
	}
	if v, ok := get("qiniu_bucket"); ok {
		cfg.OSS.QiniuBucket = strings.TrimSpace(v)
	}
	if v, ok := get("qiniu_domain"); ok {
		cfg.OSS.QiniuDomain = strings.TrimSpace(v)
	}
	if v, ok := get("qiniu_region"); ok {
		cfg.OSS.QiniuRegion = strings.TrimSpace(v)
	}
	if v, ok := get("s3_access_key"); ok && !isMaskedStorageValue(v) {
		cfg.OSS.S3AccessKey = strings.TrimSpace(v)
	}
	if v, ok := get("s3_secret_key"); ok && !isMaskedStorageValue(v) {
		cfg.OSS.S3SecretKey = strings.TrimSpace(v)
	}
	if v, ok := get("s3_bucket"); ok {
		cfg.OSS.S3Bucket = strings.TrimSpace(v)
	}
	if v, ok := get("s3_region"); ok {
		cfg.OSS.S3Region = strings.TrimSpace(v)
	}
	if v, ok := get("s3_endpoint"); ok {
		cfg.OSS.S3Endpoint = strings.TrimSpace(v)
	}
	if v, ok := get("s3_domain"); ok {
		cfg.OSS.S3Domain = strings.TrimSpace(v)
	}
	if v, ok := get("s3_use_path_style"); ok {
		cfg.OSS.S3UsePathStyle, _ = strconv.ParseBool(v)
	}

	// Validate the resulting provider before persisting anything.
	switch cfg.OSS.Provider {
	case "", "local":
		cfg.OSS.Provider = "" // "" is the canonical "local default" (see server.go)
	case "qiniu":
		var missing []string
		if cfg.OSS.QiniuAccessKey == "" {
			missing = append(missing, "qiniu_access_key")
		}
		if cfg.OSS.QiniuSecretKey == "" {
			missing = append(missing, "qiniu_secret_key")
		}
		if cfg.OSS.QiniuBucket == "" {
			missing = append(missing, "qiniu_bucket")
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("qiniu storage requires: %s", strings.Join(missing, ", "))
		}
	case "s3", "aliyun", "aliyun-oss", "cos", "tencent", "tencent-cos":
		// S3-compatible clouds share the s3_* credential fields. Aliyun OSS /
		// Tencent COS additionally need a region (endpoint is derived from it)
		// unless an explicit endpoint override is provided — so a half-configured
		// backend is rejected here instead of breaking every upload later.
		canonical := cfg.OSS.Provider
		switch canonical {
		case "aliyun-oss":
			canonical = "aliyun"
		case "tencent", "tencent-cos":
			canonical = "cos"
		}
		var missing []string
		if cfg.OSS.S3AccessKey == "" {
			missing = append(missing, "s3_access_key")
		}
		if cfg.OSS.S3SecretKey == "" {
			missing = append(missing, "s3_secret_key")
		}
		if cfg.OSS.S3Bucket == "" {
			missing = append(missing, "s3_bucket")
		}
		if canonical != "s3" && cfg.OSS.S3Region == "" && cfg.OSS.S3Endpoint == "" {
			missing = append(missing, "s3_region (e.g. cn-hangzhou / ap-guangzhou) or s3_endpoint")
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("%s storage requires: %s", canonical, strings.Join(missing, ", "))
		}
		cfg.OSS.Provider = canonical // persist the canonical name (tencent → cos)
	default:
		return nil, fmt.Errorf("unknown storage provider %q (want local/qiniu/s3)", cfg.OSS.Provider)
	}

	if err := config.SaveToFile(config.ConfigFilePath(), cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}

	// Hot-apply: rebuild the provider + re-register the image resolver. A failed
	// apply leaves the config on disk (retryable) and reports the error.
	if storageApplier != nil {
		name, err := storageApplier(cfg)
		if err != nil {
			return nil, fmt.Errorf("apply storage config: %w", err)
		}
		SetActiveStorageProvider(name)
	}

	return getStorageConfig()
}
