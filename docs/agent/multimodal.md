# 多模态视觉输入（Multimodal Vision）

> 状态：v64 起。设计目标：用户在 Web 上传图片 / Feishu 发图 / 模型用 `view_image` 工具看自己环境里的图 → vision 模型真实"看到"图片内容；非 vision 模型诚实降级为文本占位。

## 核心原则

1. **引用协议（reference protocol）**：消息 content 中永远只存**稳定引用**（markdown `![name](/api/files/viewimg/<uuid>.<ext>)` 等相对 URL，~100B），**绝不存 base64**。图片 → `data:` URL 的转换发生在**每次构建 LLM 请求时**（幂等、可缓存、可降级）。DB / 压缩 / SSE / 前端渲染全管道零改动。
2. **纯手动 vision 开关（无内置白名单）**：`PerModelConfig.Vision`（`subscription_models.vision` 列，v64）+ `VisionDetail`（low/high/auto）。**没有任何模型名 pattern 白名单**——用户在模型编辑面板（Web 设置→LLM→模型行 `E`；CLI Ctrl+N `E`）手动开启。未开启 = 图片引用降级为 `[图片: name — 当前模型未开启视觉输入]` 文本占位，模型知道有图但看不到（可以引导用户开启）。
3. **user role 是唯一多模态载体**：OpenAI tool message 只能是纯文本——`view_image` 的图不能塞 tool result，必须注入 follow-up user 消息（`injectViewImages`）。

## 数据流

```
用户上传（Web 粘贴/拖拽）
  → /api/files/upload (OSS) → upload_key
  → expandUploadKeys: content += ![name](/api/files/download?key=<enc>&inline=1)   ← 单份 markdown、相对 URL（不过期）
  → DB (session_messages, ~100B 引用)
  → 构建请求: llm.parseMultimodalContent(content, mc)
      ├ vision off → 占位 [图片: name — 当前模型未开启视觉输入]（折叠为单 text part）
      └ vision on  → ImageResolver.ResolveImage(ref) → data: URL（LRU 缓存）
        → OpenAI image_url parts / Anthropic base64 image blocks
```

```
view_image 工具（模型主动看图）
  → tools/view_image.go: path(白名单 workspace/view_images) 或 url → 存 ~/.xbot/view_images/<uuid>.<ext>
  → ToolResult.Images []ImageInjection{Ref: "/api/files/viewimg/<uuid>.<ext>"}（json:"-"）
  → agent engine processToolResults 收集 → injectViewImages: follow-up USER 消息
     content = "📷 以下图片已通过 view_image 工具加载…\n\n![name](/api/files/viewimg/…)"
     （TurnID stamped + AppendMessages 持久化 + MarkAllPersisted）
  → 下一迭代 LLM 构建时同一 parseMultimodalContent 管道
  → 前端渲染：user 消息里的 markdown img → GET /api/files/viewimg/<uuid>（cookie auth）
```

```
Feishu 图片入站
  → feishu.go case "image" / post 的 img 元素
  → downloadAndStoreFeishuImage (lark MessageResource API) → ~/.xbot/view_images/
  → content = ![图片](/api/files/viewimg/<uuid>.<ext>)（失败降级 <image image_key=…> 原标签）
```

## 关键组件

| 组件 | 位置 | 职责 |
|---|---|---|
| `llm.ImageResolver` 接口 | `llm/multimodal.go` | ref → data: URL（接口在 llm 包，实现在 serverapp——llm 不依赖 channel） |
| `llm.MultimodalConfig` | `llm/multimodal.go` | `{ImageResolver, VisionEnabled, VisionDetail, MaxImages=8}`——createClient 时从 per-model config 构造 |
| `parseMultimodalContent` | `llm/multimodal.go` | markdown `![]()` + 旧 `<image url=…>` 标签 → text/image parts；vision off 全降级（折叠单 text part）；预算：最近 8 张保留，旧图占位 `[图片: name — 已省略…]`；resolver 失败 → `[图片: name — 加载失败]`（**绝不 fail 请求**） |
| `webImageResolver` | `serverapp/image_resolver.go` | 四类 ref：`viewimg://`、`/api/files/viewimg/`、`/api/files/download?key=`（OSS GetViewURL→GET）、`http(s)://`、`file://`（workspace 白名单）；**预处理**（CatmullRom 缩到 ≤2048px 长边 → >4MB 再 jpeg q85→q70；bmp/tiff→png；gif/未知格式透传）；LRU（32 entries / 128MB） |
| `expandUploadKeys` | `channel/web/web_inbound.go` | 图片 → **单份** markdown 相对 URL（`appendUploadRef` 共享 helper，REST+WS 同一格式）；附件 → `<file url=签名URL>`（DownloadFile 工具用） |
| viewimg 端点 | `channel/web/web_viewimg.go` | `GET /api/files/viewimg/<uuid>.<ext>`（cookie auth + magic bytes Content-Type + view_images 目录边界）|
| `view_image` 工具 | `tools/view_image.go` | path（workspace/view_images/ReadOnlyRoots 白名单 + 沙箱感知 ReadFile）或 url；magic bytes 校验（拒绝非图片）；`ToolResult.Images` 注入 |
| `injectViewImages` | `agent/engine_run.go` | processToolResults 尾部收集 `result.Images` → follow-up user 消息（**user role 是 OpenAI 多模态的唯一载体**） |
| vision 开关存储 | `storage/sqlite` v64 | `subscription_models.vision`/`vision_detail` 列；`SetModelVisionConfig` 是唯一写路径（UpsertModel 的 ON CONFLICT 不碰 vision 列——token 配置写入不重置开关）；`update_per_model_config` RPC 透传 |

## 前端交互

- **EditModelModal**（`llm-console.tsx`）：`视觉输入（多模态）` Switch + `低分辨率/高分辨率` detail 档（vision on 时显示）。保存走 `update_per_model_config`（vision + vision_detail 字段）。
- **模型列表 👁 徽标**：SettingsLLM 模型行 + ModelSelector 下拉（`ModelEntry.Vision`，`list_all_model_entries` RPC）。
- **上传提示条**（`MessageInput.tsx`）：附件含图片 + 当前模型 vision off → 琥珀色非阻断提示 `vision-off-hint`（`modelVision={false}` 时显示）；vision on → 发送 toast `👁 N 张图片将作为视觉输入发送给模型`（i18n `agent.visionImagesAttached`）。`modelVision` 由 AgentPanel 从 `sessionContext.model + llmSettings.data.subscriptions` 的 per_model_configs 查得，`undefined` = 未知（不显示）。
- **MarkdownImage**（`MarkdownRenderer.tsx`）：max-h 400px + 点击灯箱（`Lightbox.tsx` 模块级单例，App 挂 `<ImageLightboxHost />`——**不进 MarkdownRenderer state**，memo 树不破坏）+ onError 占位（`img-load-failed`，历史过期 URL）。view_image 注入卡渲染 = user 消息 markdown img（viewimg 相对 URL cookie auth 直渲）。

## 陷阱（改动前必读）

- **`UpsertModel` 的 ON CONFLICT 不 SET vision 列**——vision 只经 `SetModelVisionConfig` 写（单列 UPDATE，同 SetModelEnabled 模式）。改 upsert 的 SET 子句会让 token 配置写入重置 vision 开关（回归 `TestSetModelVisionConfig_RoundTrip`）。
- **`parseMultimodalContent` vision off 时必须折叠为单 text part**（纯文本多 part → join）——否则 toOpenAIMessages 走 content-parts 数组路径，OpenAI 部分后端拒收纯文本 parts 数组。
- **`splitDataURL` 空 media type 拒绝**（`data:;base64,`）——Anthropic 需要 concrete media_type。
- **图片预算 8 张是"最近的赢"**（`MaxImages`，旧图降级）——同 turn 多迭代重发全部历史消息，LRU 缓存（32/128MB）吸收重复解析。
- **`viewimgIDRe`/`viewimgIDPattern` 限定 id 字符集**（`[a-zA-Z0-9_-]{1,128}\.(png|...)`）——view_images 目录 join 前的路径穿越防线（resolver + web 端点双侧）。
- **`resolveImageRef` 对 `data:` 前缀直通不缓存**（ResolveImage 开头）——已 inline 的引用不走 LRU。
- **Feishu 下载失败降级原 `<image image_key>` 标签**——消息不丢，模型看到标签可提示重发。
- **`file://` 引用只在 workspace roots + view_images 内解析**（`NewImageResolver(provider, xbotHome, workDir...)` variadic roots）——resolver 绝不读任意本地路径。
- **migration v64**：`subscription_models` 加 `vision`/`vision_detail` 列（columnExists 幂等）；`schemaVersion=64`。
- **schema.go 的 CREATE TABLE 和 migration 必须同步**（新库直接建 v64；老库 ALTER——`migrateV63ToV64`）。

## 本地路径透出（image caption + LocalPath）

模型只拿到像素时**无法对文件做任何操作**：让它 `ps` 一张粘贴的图片，它只能在全盘
find（用户报告）。因此成功解析的 image part 旁会附一个**结构化 caption**：

```
[image alt="…" local="…" ref="…"]
```

- `local` 仅在 `ImageResolver` 实现了**可选**扩展 `LocalPath(ref) (string, bool)`
  时给出（type-assert，不破坏既有实现）。serverapp 的 resolver 映射：
  `viewimg://` 与 `/api/files/viewimg/<id>` → `<xbotHome>/view_images/<id>`；
  `/api/files/download?key=uploads/...` → `<xbotHome>/uploads/uploads/...`；
  workspace 白名单内的 `file://`（与 `ResolveImage` 共用 `resolveFileRef`，两者
  永不漂移）。
- **落盘缓存**：web 上传除了进 OSS，还会在 `<xbotHome>/uploads/<key>` 留一份本地
  副本（0600），以便模型有真实路径可用。**OSS 仍是唯一权威存储**，副本是缓存：
  每次落盘后 `pruneLocalUploads` 只保留最新 500 份（best-effort，不影响上传结果）。
- ⚠️ **安全边界（知情项）**：`local` 会把**服务器绝对路径**（含用户名/目录布局）
  带进第三方 LLM 上下文。自托管/内网模型影响很小；公共 API 部署需知悉这一元数据面。
- ⚠️ **抗伪造**：`alt` 完全由用户控制（`![alt](url)`）。caption 因此采用 `字段=值`
  形式而非散文，且 alt 会被截断（≤120 runes）并清除控制字符/换行 —— user 文本
  不可能伪装成系统注入的 `local=`/`ref=`。
- 降级路径（vision off / 解析失败 / 超预算）走 `imagePlaceholder`，同样保留引用。

## 测试

- `llm/multimodal_test.go`——四类引用/vision off 降级/预算(最近优先)/Anthropic blocks/splitDataURL/折叠行为/**caption 携带引用与本地路径 + alt 不可伪造(结构化/截断/清洗)**
- `serverapp/image_resolver_test.go`——四类 ref 解析/LRU 驱赶（entries+bytes 双预算）/2048px 缩放/file:// 白名单/bmp 透传/**LocalPath 校验矩阵（`..`/分隔符/非 uploads key/白名单越界全部拒绝）**
- `tools/view_image_test.go`——白名单拒绝/magic bytes 拒绝非图片/参数互斥/ImageInjection 引用格式
- `channel/web/web_inbound_expand_test.go`——单份 markdown 相对 URL（无 `<image>` 标签、无绝对签名 URL）
- `agent/llm_factory_vision_test.go`——buildMultimodalConfig（off→nil）/resolveModelConfig 读取/UpsertModel 不重置 vision
- `storage/sqlite/subscription_vision_test.go`——vision 列 round-trip/SetModelVisionConfig 唯一写路径/v64 幂等
- `web/e2e/vision-input.spec.ts`——👁 徽标 / vision-off 提示条（route mock）
- `plugin/protocol/protocol_test.go`——**协议超长单行（>2MB）不再以 token-too-long 中止循环**（Scanner→bufio.Reader 回归）
