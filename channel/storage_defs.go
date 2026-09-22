package channel

// StorageSchema is the SINGLE definition of the file-storage (cloud OSS / local
// static) settings, shared by the Web settings panel (get_storage_config →
// `_schema`) and any other consumer (CLI / docs generation). Adding a storage
// backend field must only touch this function — never duplicate the list in the
// frontend or in serverapp (historical lesson: the CLI carried a hard-coded
// copy of the channel schema and silently drifted).
//
// Semantics (see serverapp/storage_config.go):
//   - provider "" / "local" → local static storage (default: works with zero
//     configuration; uploads land in <xbotHome>/uploads/, served by the
//     same-origin /api/files/download).
//   - provider "qiniu" / "s3" → cloud object storage; the bucket becomes the
//     source of truth and a local spill copy is kept so the model can be handed
//     a real path (see the multimodal image resolver).
//
// Conditional visibility: backend-specific fields declare DependsOnKey
// "provider" so the panel only shows the credentials that matter.
func StorageSchema() []SettingDefinition {
	return []SettingDefinition{
		{
			Key:          "provider",
			Label:        "Storage backend",
			Description:  "local = keep uploads on this server (default, no config needed). aliyun (阿里云 OSS) / cos (腾讯云 COS) / qiniu / s3 = cloud object storage — the bucket becomes the source of truth and images never expire unless you set a lifecycle rule.",
			Type:         SettingTypeSelect,
			Category:     "File storage",
			DefaultValue: "local",
			Options: []SettingOption{
				{Label: "Local static (this server)", Value: "local", Description: "Served by /api/files/download; newest 500 uploads are kept (older ones are pruned)"},
				{Label: "Alibaba Cloud OSS (阿里云 OSS)", Value: "aliyun", Description: "S3-compatible; endpoint derived from region (e.g. cn-hangzhou ⇒ oss-cn-hangzhou.aliyuncs.com)"},
				{Label: "Tencent Cloud COS (腾讯云 COS)", Value: "cos", Description: "S3-compatible; endpoint derived from region (e.g. ap-guangzhou ⇒ cos.ap-guangzhou.myqcloud.com)"},
				{Label: "Qiniu Kodo (七牛)", Value: "qiniu"},
				{Label: "S3 compatible", Value: "s3", Description: "AWS S3 / MinIO / R2 / SeaweedFS ..."},
			},
		},

		// ── Qiniu ────────────────────────────────────────────────────────────
		{Key: "qiniu_access_key", Label: "Access key", Type: SettingTypeText, Category: "File storage",
			Description: "Qiniu access key", DefaultValue: "", DependsOnKey: "provider", DependsOnValues: "qiniu"},
		{Key: "qiniu_secret_key", Label: "Secret key", Type: SettingTypePassword, Category: "File storage",
			Description: "Qiniu secret key (stored in config.json, masked on read)", DefaultValue: "",
			DependsOnKey: "provider", DependsOnValues: "qiniu"},
		{Key: "qiniu_bucket", Label: "Bucket", Type: SettingTypeText, Category: "File storage",
			Description: "Qiniu bucket name", DefaultValue: "", DependsOnKey: "provider", DependsOnValues: "qiniu"},
		{Key: "qiniu_domain", Label: "CDN domain", Type: SettingTypeText, Category: "File storage",
			Description: "Bound CDN domain for the bucket (e.g. https://cdn.example.com)", DefaultValue: "",
			DependsOnKey: "provider", DependsOnValues: "qiniu"},
		{Key: "qiniu_region", Label: "Region", Type: SettingTypeText, Category: "File storage",
			Description: "Bucket region (e.g. z0, z1, z2, na0, as0)", DefaultValue: "",
			DependsOnKey: "provider", DependsOnValues: "qiniu"},

		// ── S3 compatible ────────────────────────────────────────────────────
		{Key: "s3_access_key", Label: "Access key", Type: SettingTypeText, Category: "File storage",
			Description: "S3 access key ID", DefaultValue: "", DependsOnKey: "provider", DependsOnValues: "s3,aliyun,cos"},
		{Key: "s3_secret_key", Label: "Secret key", Type: SettingTypePassword, Category: "File storage",
			Description: "S3 secret access key (stored in config.json, masked on read)", DefaultValue: "",
			DependsOnKey: "provider", DependsOnValues: "s3,aliyun,cos"},
		{Key: "s3_bucket", Label: "Bucket", Type: SettingTypeText, Category: "File storage",
			Description: "S3 bucket name", DefaultValue: "", DependsOnKey: "provider", DependsOnValues: "s3,aliyun,cos"},
		{Key: "s3_region", Label: "Region", Type: SettingTypeText, Category: "File storage",
			Description: "Region — AWS: us-east-1 · Aliyun OSS: cn-hangzhou · Tencent COS: ap-guangzhou · MinIO: us-east-1", DefaultValue: "",
			DependsOnKey: "provider", DependsOnValues: "s3,aliyun,cos"},
		{Key: "s3_endpoint", Label: "Endpoint", Type: SettingTypeText, Category: "File storage",
			Description: "Optional endpoint override. Empty = derived from the provider + region (AWS default / oss-<region>.aliyuncs.com / cos.<region>.myqcloud.com); set it for MinIO / R2 / self-hosted gateways", DefaultValue: "",
			DependsOnKey: "provider", DependsOnValues: "s3,aliyun,cos"},
		{Key: "s3_domain", Label: "CDN domain", Type: SettingTypeText, Category: "File storage",
			Description: "Public domain for inline rendering (empty = presigned URLs)", DefaultValue: "",
			DependsOnKey: "provider", DependsOnValues: "s3,aliyun,cos"},
		{Key: "s3_use_path_style", Label: "Path-style addressing", Type: SettingTypeToggle, Category: "File storage",
			Description: "Enable for MinIO and most S3-compatible gateways", DefaultValue: "false",
			DependsOnKey: "provider", DependsOnValues: "s3,aliyun,cos"},
	}
}

// StorageSecretKeys lists the schema keys whose values must never be returned
// verbatim by get_storage_config (masked) nor overwritten from a masked round
// trip (see serverapp/storage_config.go). Single source — the panel and the
// RPC agree by construction.
func StorageSecretKeys() []string {
	return []string{"qiniu_secret_key", "s3_secret_key", "qiniu_access_key", "s3_access_key"}
}
