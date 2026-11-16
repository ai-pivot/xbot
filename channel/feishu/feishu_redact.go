package feishu

// feishu_redact.go — 内容级工具脱敏已于 2026-11-07 整体删除。
//
// 历史：2026-09-17 曾按「飞书加一个工具脱敏功能，自动去掉所有敏感内容」实现
// 四层正则（key=value / --flag / Bearer / 高置信 token 格式）。实测全是误杀：
//   pwd=<路径>、signature: <函数名>、session_id=<业务标识>、
//   num_tokens / max_tokens / total_tokens 这类字段名都在射程内；
//   收窄清单 + 改判据后仍被判定「匹配逻辑本身蠢、高置信格式是扯淡」。
//
// 裁定：**不做内容级扫描**。任意文本的正则脱敏无法区分「凭据」与「正常内容」，
// 只会污染展示。真正的凭据防泄露应在结构化字段处做：
//   - 订阅配置 API Key 展示：agent/llm_config_handler.go / serverapp/server.go 的 maskAPIKey
//   - 环境变量不入库/不入日志：源头治理
//
// 本文件保留为空壳以便 grep 到裁定记录；调用点（feishu_cot_renderer.go /
// feishu_stream_card.go）已改为直出原文。
