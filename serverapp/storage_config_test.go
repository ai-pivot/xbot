package serverapp

import (
	"encoding/json"
	"strings"
	"testing"

	"xbot/channel"
	"xbot/config"
)

// isolateStorageConfig points XBOT_HOME at a temp dir so config.json writes in
// these tests never touch the developer's real ~/.xbot.
func isolateStorageConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XBOT_HOME", t.TempDir())
}

// TestStorageSchema_SingleSource — the panel is schema-driven: the provider
// selector must offer exactly local/qiniu/s3 and every masked secret key must
// exist in the schema (a secret missing from the schema would be un-maskable
// and, worse, un-editable from the Web UI).
func TestStorageSchema_SingleSource(t *testing.T) {
	var provider *channel.SettingDefinition
	byKey := map[string]channel.SettingDefinition{}
	for _, f := range channel.StorageSchema() {
		f := f
		byKey[f.Key] = f
		if f.Key == "provider" {
			provider = &f
		}
	}
	if provider == nil {
		t.Fatal("schema must declare `provider`")
	}
	if provider.Type != channel.SettingTypeSelect {
		t.Errorf("provider type = %q, want select", provider.Type)
	}
	var opts []string
	for _, o := range provider.Options {
		opts = append(opts, o.Value)
	}
	for _, want := range []string{"local", "qiniu", "s3"} {
		found := false
		for _, o := range opts {
			if o == want {
				found = true
			}
		}
		if !found {
			t.Errorf("provider options %v missing %q", opts, want)
		}
	}
	for _, k := range channel.StorageSecretKeys() {
		if _, ok := byKey[k]; !ok {
			t.Errorf("secret key %q is not in the schema (cannot be edited in the Web UI)", k)
		}
	}
	// Backend-specific credentials must be conditional (depends_on provider).
	for _, k := range []string{"qiniu_bucket", "s3_bucket", "s3_endpoint"} {
		f, ok := byKey[k]
		if !ok {
			t.Fatalf("schema missing %q", k)
		}
		if f.DependsOnKey != "provider" || f.DependsOnValues == "" {
			t.Errorf("%q must declare DependsOnKey/Values (conditional visibility), got %q/%q", k, f.DependsOnKey, f.DependsOnValues)
		}
	}
}

// TestGetStorageConfig_MasksSecrets — a browser must never receive a usable
// credential. Read path returns first-4 + "****" plus the schema and the
// provider actually running.
func TestGetStorageConfig_MasksSecrets(t *testing.T) {
	isolateStorageConfig(t)
	cfg := &config.Config{}
	cfg.OSS.QiniuAccessKey = "AKIDEXAMPLEKEY"
	cfg.OSS.QiniuSecretKey = "SUPERSECRETVALUE"
	cfg.OSS.S3SecretKey = "anothersecret"
	if err := config.SaveToFile(config.ConfigFilePath(), cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	got, err := getStorageConfig()
	if err != nil {
		t.Fatalf("getStorageConfig: %v", err)
	}
	for _, k := range []string{"qiniu_access_key", "qiniu_secret_key", "s3_secret_key"} {
		v := got[k]
		if v == "" {
			continue
		}
		if !strings.Contains(v, "****") {
			t.Errorf("%s = %q, want masked", k, v)
		}
		if strings.Contains(v, "SECRET") || strings.Contains(v, "another") {
			t.Errorf("%s leaked the secret: %q", k, v)
		}
	}
	if got["_schema"] == "" {
		t.Error("_schema missing")
	}
	var schema []channel.SettingDefinition
	if err := json.Unmarshal([]byte(got["_schema"]), &schema); err != nil {
		t.Errorf("_schema is not valid JSON: %v", err)
	}
	if got["_active"] != "local" {
		t.Errorf("_active = %q, want local (empty provider ⇒ local)", got["_active"])
	}
}

// TestSetStorageConfig_SkipsMaskedEchoes — the panel round-trips masked values;
// writing them back must NOT destroy the real credential.
func TestSetStorageConfig_SkipsMaskedEchoes(t *testing.T) {
	isolateStorageConfig(t)
	cfg := &config.Config{}
	cfg.OSS.QiniuAccessKey = "AKIDREALKEY"
	cfg.OSS.QiniuSecretKey = "REALSECRET"
	cfg.OSS.QiniuBucket = "mybucket"
	if err := config.SaveToFile(config.ConfigFilePath(), cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
	// Simulate the panel: it submits what it read (masked) plus a changed bucket.
	if _, err := setStorageConfig(map[string]string{
		"provider":         "qiniu",
		"qiniu_access_key": "AKID****",
		"qiniu_secret_key": "REAL****",
		"qiniu_bucket":     "mybucket2",
	}); err != nil {
		t.Fatalf("setStorageConfig: %v", err)
	}
	onDisk := config.LoadFromFile(config.ConfigFilePath())
	if onDisk.OSS.QiniuSecretKey != "REALSECRET" {
		t.Errorf("secret overwritten by a masked echo: %q", onDisk.OSS.QiniuSecretKey)
	}
	if onDisk.OSS.QiniuAccessKey != "AKIDREALKEY" {
		t.Errorf("access key overwritten by a masked echo: %q", onDisk.OSS.QiniuAccessKey)
	}
	if onDisk.OSS.QiniuBucket != "mybucket2" {
		t.Errorf("bucket = %q, want mybucket2", onDisk.OSS.QiniuBucket)
	}
	if onDisk.OSS.Provider != "qiniu" {
		t.Errorf("provider = %q, want qiniu", onDisk.OSS.Provider)
	}
}

// TestSetStorageConfig_RejectsIncomplete — a half-configured cloud backend must
// be rejected BEFORE anything is written (otherwise every upload breaks).
func TestSetStorageConfig_RejectsIncomplete(t *testing.T) {
	isolateStorageConfig(t)
	if err := config.SaveToFile(config.ConfigFilePath(), &config.Config{}); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
	if _, err := setStorageConfig(map[string]string{"provider": "qiniu", "qiniu_bucket": "b"}); err == nil {
		t.Error("qiniu without credentials must be rejected")
	}
	if _, err := setStorageConfig(map[string]string{"provider": "nope"}); err == nil {
		t.Error("unknown provider must be rejected")
	}
	// Nothing was written: provider still empty (local default).
	if got := config.LoadFromFile(config.ConfigFilePath()).OSS.Provider; got != "" {
		t.Errorf("rejected write still persisted provider=%q", got)
	}
}

// TestSetStorageConfig_HotApplies — saving must switch the running provider
// without a restart, and report what is live via _active.
func TestSetStorageConfig_HotApplies(t *testing.T) {
	isolateStorageConfig(t)
	if err := config.SaveToFile(config.ConfigFilePath(), &config.Config{}); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
	var applied string
	prevApply := storageApplier
	prevName := storageProviderName
	t.Cleanup(func() { storageApplier = prevApply; storageProviderName = prevName })
	SetStorageApplier(func(c *config.Config) (string, error) {
		applied = c.OSS.Provider
		return "qiniu", nil
	})

	got, err := setStorageConfig(map[string]string{
		"provider":         "qiniu",
		"qiniu_access_key": "AK",
		"qiniu_secret_key": "SK",
		"qiniu_bucket":     "bucket",
	})
	if err != nil {
		t.Fatalf("setStorageConfig: %v", err)
	}
	if applied != "qiniu" {
		t.Errorf("applier received provider %q, want qiniu", applied)
	}
	if got["_active"] != "qiniu" {
		t.Errorf("_active = %q, want qiniu", got["_active"])
	}
	if n := storageProviderName; n != "qiniu" {
		t.Errorf("storageProviderName = %q, want qiniu", n)
	}
}

// TestBuildStorageProvider_LocalDefault — the zero-config path must always
// resolve to local static (uploads must never 503 "file storage not configured").
func TestBuildStorageProvider_LocalDefault(t *testing.T) {
	isolateStorageConfig(t)
	for _, prov := range []string{"", "local"} {
		p, name, err := buildStorageProvider(&config.Config{OSS: config.OSSConfig{Provider: prov}})
		if err != nil {
			t.Fatalf("provider %q: %v", prov, err)
		}
		if name != "local" || p == nil {
			t.Errorf("provider %q → (%v, %q), want (provider, local)", prov, p, name)
		}
	}
	if _, _, err := buildStorageProvider(&config.Config{OSS: config.OSSConfig{Provider: "weird"}}); err == nil {
		t.Error("unknown provider must return an error")
	}
}

// TestStorageSchema_CloudAliases — the panel offers Alibaba Cloud OSS and
// Tencent Cloud COS next to local/qiniu/s3, and the shared S3 credential group
// is visible for all three S3-compatible providers (DependsOnValues is a
// comma-separated list — a single "s3" would hide the fields for aliyun/cos).
func TestStorageSchema_CloudAliases(t *testing.T) {
	var opts []string
	for _, f := range channel.StorageSchema() {
		if f.Key == "provider" {
			for _, o := range f.Options {
				opts = append(opts, o.Value)
			}
		}
	}
	for _, want := range []string{"local", "aliyun", "cos", "qiniu", "s3"} {
		found := false
		for _, o := range opts {
			if o == want {
				found = true
			}
		}
		if !found {
			t.Errorf("provider options %v missing %q", opts, want)
		}
	}
	for _, f := range channel.StorageSchema() {
		switch f.Key {
		case "s3_access_key", "s3_secret_key", "s3_bucket", "s3_region", "s3_endpoint", "s3_domain", "s3_use_path_style":
			for _, want := range []string{"aliyun", "cos"} {
				if !strings.Contains(f.DependsOnValues, want) {
					t.Errorf("%s DependsOnValues=%q must include %q (shared S3-compatible credential group)", f.Key, f.DependsOnValues, want)
				}
			}
		}
	}
}

// TestBuildStorageProvider_AliyunCosEndpoints — Aliyun OSS / Tencent COS are
// S3-compatible: their endpoint is DERIVED from the region (visible in the
// presigned URL host), an explicit override wins, and provider aliases are
// normalized to the canonical name.
func TestBuildStorageProvider_AliyunCosEndpoints(t *testing.T) {
	isolateStorageConfig(t)
	cases := []struct {
		provider, region, endpoint, wantHost, wantName string
	}{
		{"aliyun", "cn-hangzhou", "", "oss-cn-hangzhou.aliyuncs.com", "aliyun"},
		{"aliyun-oss", "cn-beijing", "", "oss-cn-beijing.aliyuncs.com", "aliyun"},
		{"cos", "ap-guangzhou", "", "cos.ap-guangzhou.myqcloud.com", "cos"},
		{"tencent", "ap-shanghai", "", "cos.ap-shanghai.myqcloud.com", "cos"},
		{"aliyun", "cn-hangzhou", "https://minio.internal:9000", "minio.internal:9000", "aliyun"},
	}
	for _, tc := range cases {
		cfg := &config.Config{OSS: config.OSSConfig{
			Provider:     tc.provider,
			S3AccessKey:  "AK",
			S3SecretKey:  "SK",
			S3Bucket:     "mybucket",
			S3Region:     tc.region,
			S3Endpoint:   tc.endpoint,
		}}
		p, name, err := buildStorageProvider(cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if name != tc.wantName {
			t.Errorf("%s → name %q, want %q", tc.provider, name, tc.wantName)
		}
		u, err := p.GetViewURL("uploads/1/x.png")
		if err != nil {
			t.Fatalf("%s: GetViewURL: %v", tc.provider, err)
		}
		if !strings.Contains(u, tc.wantHost) {
			t.Errorf("%s: url %q must contain host %q", tc.provider, u, tc.wantHost)
		}
	}
}

// TestBuildStorageProvider_AliyunRequiresRegion — without a region (and no
// endpoint override) the endpoint cannot be derived: fail loudly instead of
// building a provider that points at nowhere.
func TestBuildStorageProvider_AliyunRequiresRegion(t *testing.T) {
	isolateStorageConfig(t)
	for _, prov := range []string{"aliyun", "cos"} {
		cfg := &config.Config{OSS: config.OSSConfig{Provider: prov, S3AccessKey: "AK", S3SecretKey: "SK", S3Bucket: "b"}}
		if _, _, err := buildStorageProvider(cfg); err == nil {
			t.Errorf("%s without region and without endpoint must fail", prov)
		}
	}
}

// TestSetStorageConfig_AliyunValidation — the write path rejects a半配置 aliyun
// (no region) and persists the canonical provider name for aliases.
func TestSetStorageConfig_AliyunValidation(t *testing.T) {
	isolateStorageConfig(t)
	if err := config.SaveToFile(config.ConfigFilePath(), &config.Config{}); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
	prevApply := storageApplier
	t.Cleanup(func() { storageApplier = prevApply })
	SetStorageApplier(func(c *config.Config) (string, error) { return "cos", nil })

	// Missing region ⇒ rejected, nothing written.
	if _, err := setStorageConfig(map[string]string{
		"provider": "aliyun", "s3_access_key": "AK", "s3_secret_key": "SK", "s3_bucket": "b",
	}); err == nil {
		t.Error("aliyun without region must be rejected")
	}
	if got := config.LoadFromFile(config.ConfigFilePath()).OSS.Provider; got != "" {
		t.Errorf("rejected write persisted provider=%q", got)
	}

	// With region ⇒ accepted, canonical name persisted (tencent → cos).
	if _, err := setStorageConfig(map[string]string{
		"provider": "tencent", "s3_access_key": "AK", "s3_secret_key": "SK",
		"s3_bucket": "b", "s3_region": "ap-guangzhou",
	}); err != nil {
		t.Fatalf("tencent COS with region must be accepted: %v", err)
	}
	if got := config.LoadFromFile(config.ConfigFilePath()).OSS.Provider; got != "cos" {
		t.Errorf("provider = %q, want canonical cos", got)
	}
}
