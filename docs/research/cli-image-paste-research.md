# xbot CLI 图片粘贴方案调研报告

## 1. 现状分析

### 1.1 当前架构全景

```
用户按键/粘贴
    ↓
cliModel.Update(msg)                    ← channel/cli_update.go
    ├── tea.PasteMsg → textarea.Update() ← 只处理纯文本
    ├── Ctrl+V        → Paste()          ← internal/textarea/textarea.go:1956
    │                    └── clipboard.ReadAll() (atotto/clipboard，仅文本)
    └── Enter → sendMessage(content)     ← channel/cli_message.go:264
                   ├── parseFileReferences(content) → []string (文件路径)
                   ├── msg.Media = media             ← 设置了但被丢弃！
                   └── sendInbound(msg)
                        └── Client.SendInbound(ch,chatID,content,sender,sender,chatType,meta)
                              └── ⚠️ 无 Media 参数 → Media 丢失
```

### 1.2 已有能力

| 层 | 能力 | 状态 |
|---|---|---|
| InboundMsg.Media | `[]string` 字段 | ✅ 存在但未传递 |
| bus.MessagePayload.Media | `[]string` | ✅ Agent 会处理但收到空值 |
| Agent Media 处理 | 追加 `[Attached files]` 文本 | ✅ 但永远收不到 |
| parseEmbeddedImages | `data:image/...` URL 解析 | ✅ OpenAI 路径已支持 |
| WSClientMessage | FileIDs/UploadKeys 字段 | ✅ CLI 未使用 |
| Web 文件上传 | OSS 上传完整流程 | ✅ 参考 |

### 1.3 关键缺陷

1. **Media 全链路断裂**：`Client.SendInbound()` 签名无 Media 参数（`agent/client.go:256`）
2. **剪贴板只支持文本**：`atotto/clipboard.ReadAll()` → `string`
3. **BubbleTea PasteMsg 纯文本**：ultraviolet 解析器将粘贴内容作为 UTF-8 文本，二进制数据被丢弃
4. **Anthropic 无图片支持**：`toAnthropicMessages`（`llm/anthropic.go:207`）不解析 `data:` URL
5. **无模型能力检测**：`ListModels()` 只返回 `[]string`，无 vision/multimodal 信息

---

## 2. 剪贴板图片提取方案

### 2.1 核心问题

**Bracketed Paste Mode 无法传输图片。** 终端粘贴协议（`\x1b[200~...\x1b[201~`）只传文本字符流。不能通过监听 `tea.PasteMsg` 获取图片。

**必须使用外部工具/系统 API 直接读取剪贴板二进制数据。**

### 2.2 跨平台剪贴板图片提取

#### 方案 A：使用 `golang.design/x/clipboard` 库（推荐）

- 支持 macOS / Linux (X11 + Wayland) / Windows
- 原生支持 `Read(clipboard.FmtImage)` → `[]byte` (PNG)
- macOS: 使用 CGo + Objective-C (`NSPasteboard`)
- Linux: 使用 CGo + GTK
- Windows: 使用 CGo + Win32 API
- **缺点**: 需要 CGo，交叉编译困难

#### 方案 B：使用 `phoenix-tui/phoenix/clipboard`（新库）

- `ReadImage() ([]byte, string, error)` — 返回图片字节 + MIME 类型
- 支持 macOS / Linux (X11 + Wayland) / Windows
- 支持 SSH/远程模式（OSC 52 fallback）
- **缺点**: 较新，生态不成熟，纯文本 clipboard 不支持图片

#### 方案 C：调用平台外部工具（最可靠，推荐为主方案）

| 平台 | 工具 | 命令 | 输出 |
|------|------|------|------|
| **macOS** | `osascript` | `osascript -e 'clipboard info'` 检测 → `osascript -e 'the clipboard as «class PNGf»'` | PNG 二进制 |
| **macOS** | `pngpaste` (brew) | `pngpaste /tmp/clip-$TIME.png` | PNG 文件 |
| **X11** | `xclip` | `xclip -selection clipboard -t TARGETS -o` 检测 → `xclip -selection clipboard -t image/png -o` | PNG 二进制 stdout |
| **Wayland** | `wl-paste` | `wl-paste -l` 检测 → `wl-paste -t image/png` | PNG 二进制 stdout |
| **Windows** | PowerShell | `[Windows.Forms.Clipboard]::ContainsImage()` 检测 → `GetImage().Save(path)` | PNG 文件 |

**推荐: 方案 C (外部工具) + 方案 A (golang.design/x/clipboard) 作为增强备选**

理由：
1. 外部工具无需 CGo，交叉编译零障碍
2. `xclip`/`wl-paste` 几乎所有 Linux 桌面都有预装
3. macOS `osascript` 是系统自带
4. 可以通过 `exec.LookPath` 优雅降级

### 2.3 剪贴板图片检测流程

```
用户按 Ctrl+V / Ctrl+Shift+V (图片粘贴快捷键)
    ↓
1. 检测平台 (runtime.GOOS)
    ↓
2. 检测剪贴板是否有图片
   ├── macOS:  osascript 'clipboard info' contains "PNGf" or "TIFF"
   ├── X11:    xclip -t TARGETS -o | grep -q "image/png"
   ├── Wayland: wl-paste -l | grep -q "image/png"
   └── Windows: powershell [Clipboard]::ContainsImage()
    ↓
3a. 有图片 → 提取 PNG 数据 → 保存到临时文件 / base64 编码
3b. 无图片 → 回退到正常文本粘贴 (clipboard.ReadAll())
```

---

## 3. 图片传输架构设计

### 3.1 两种传输策略

```
                    ┌─────────────────────────────────────────┐
                    │         图片粘贴后需要传输给后端           │
                    └────────────┬────────────────────────────┘
                                 ↓
                    ┌────────────────────────┐
                    │   CLI 是否本地模式？     │
                    │ (eventCh != nil)        │
                    └───────┬────────┬────────┘
                     本地   │        │  远程
                            ↓        ↓
                 ┌──────────────┐  ┌──────────────────┐
                 │ 策略 A:      │  │ 策略 B:          │
                 │ 直接传文件路径 │  │ Base64 内嵌      │
                 │              │  │                  │
                 │ 1. 保存图片   │  │ 1. 读取图片       │
                 │    到 /tmp    │  │ 2. base64 编码    │
                 │ 2. 传路径给   │  │ 3. data:image/... │
                 │    Agent     │  │    嵌入 content   │
                 │ 3. Agent 直  │  │ 4. JSON RPC 传输  │
                 │    接读文件   │  │                  │
                 └──────────────┘  └──────────────────┘
```

### 3.2 策略 A：本地模式直接路径（高效）

**核心优势：** 本地模式下 CLI 和 Agent 在同一进程，Agent 的 Read/Shell 工具可直接访问本地文件系统。无需 base64 编码，无需 OSS 上传。

```
流程：
1. 检测剪贴板图片 → 保存到 ~/.xbot/tmp/clipboard-{timestamp}.png
2. 构造消息：content 中附加 ![clipboard](/path/to/image.png)
3. msg.Media = ["/path/to/image.png"]
4. Agent 收到后：
   a. Media 路径直接可读（本地文件系统）
   b. Agent 将图片读取 → base64 → 嵌入 data:image URL → LLM multimodal
   c. 无需通过 WebSocket 传输大体积 base64
```

**数据流对比：**
| | 传统方式 | 本地优化 |
|--|---------|---------|
| 传输 | 13.3MB base64 字符串通过 RPC | ~100 字节文件路径字符串 |
| 编码 | 客户端 base64 编码 100ms+ | 无编码 |
| Agent 端 | 直接使用 | 直接读文件，本地编码 |
| 总耗时 | ~500ms (编码+序列化+传输) | ~5ms (路径传递) |

### 3.3 策略 B：远程模式 Base64 内嵌

远程模式下 CLI 和 Agent 不在同一机器，必须传输图片数据。

```
流程：
1. 检测剪贴板图片 → 读取为 []byte
2. Base64 编码 → data:image/png;base64,{data}
3. 注入 content: "用户粘贴了一张图片：\n![clipboard](data:image/png;base64,...)"
4. 通过 WebSocket JSON RPC 发送
5. Agent 端 parseEmbeddedImages 解析 → OpenAI image_url content part
```

**优化点：**
- 图片压缩/缩放后再 base64（建议最大 2048px 边长，JPEG 质量 85）
- 分块传输：超大方片可用多条消息分片（但增加复杂度，暂不建议）
- 懒加载：先传缩略图，Agent 需要时再传原图

### 3.4 统一消息格式设计

```go
// channel/cli_types.go — 增强 InboundMsg
type InboundMsg struct {
    Channel    string            `json:"channel"`
    ChatID     string            `json:"chat_id"`
    Content    string            `json:"content"`
    SenderID   string            `json:"sender_id"`
    SenderName string            `json:"sender_name"`
    ChatType   string            `json:"chat_type"`
    RequestID  string            `json:"request_id"`
    Media      []string          `json:"media,omitempty"`      // 文件路径
    Images     []ClipboardImage  `json:"images,omitempty"`     // 新增：内联图片
    Metadata   map[string]string `json:"metadata,omitempty"`
}

// ClipboardImage 表示粘贴的图片数据
type ClipboardImage struct {
    Path     string `json:"path,omitempty"`     // 本地模式：文件路径
    DataURL  string `json:"data_url,omitempty"` // 远程模式：data:image/png;base64,...
    MimeType string `json:"mime_type"`          // image/png, image/jpeg 等
    Size     int64  `json:"size"`               // 原始字节大小
    Width    int    `json:"width,omitempty"`     // 像素宽
    Height   int    `json:"height,omitempty"`    // 像素高
}
```

---

## 4. 模型多模态能力检测

### 4.1 问题

当前 `ListModels()` 只返回 `[]string`，无法知道模型是否支持图片。向非 vision 模型发送图片会导致 API 400 错误。

### 4.2 检测策略

**三级检测（从精确到模糊）：**

#### Level 1: 模型名称规则推断（立即可用）

```go
// llm/vision.go — 新文件
var visionModelPatterns = []string{
    // OpenAI
    "gpt-4o", "gpt-4-turbo", "gpt-4-vision", "gpt-4.*-vision",
    // Anthropic  
    "claude-3", "claude-sonnet", "claude-opus", "claude-3.5", "claude-3-5",
    // Google
    "gemini",
    // DeepSeek
    "deepseek-vl",
    // Qwen
    "qwen-vl", "qwen2-vl",
    // 通配
    "vision", "vl-", "-vl", "mm-", "multimodal",
}

func ModelSupportsVision(modelName string) bool {
    lower := strings.ToLower(modelName)
    for _, pattern := range visionModelPatterns {
        if strings.Contains(lower, pattern) {
            return true
        }
    }
    return false
}
```

#### Level 2: 订阅元数据（需扩展）

```go
// 在 PerModelConfig 或 LLMSubscription 中添加：
type PerModelConfig struct {
    MaxOutputTokens int  `json:"max_output_tokens"`
    MaxContext      int  `json:"max_context"`
    SupportsVision  *bool `json:"supports_vision,omitempty"` // 新增，nil=未知
}
```

用户可在 `/settings` 中手动设置。`nil` 表示未配置，回退到 Level 1 推断。

#### Level 3: API 响应检测（长期）

- OpenAI: 某些 provider 返回模型能力信息
- 发送测试请求检测（成本高，不推荐）

### 4.3 降级行为

```
图片粘贴 → 检测模型能力
    ├── 支持 vision → 正常发送图片
    ├── 不支持 vision → 提示用户："当前模型不支持图片理解，图片将以文件路径形式附加"
    │                    → 降级为 Media 文件路径方式（Agent 可用工具读取）
    └── 未知 → 尝试发送，API 报错则降级 + 缓存结果
```

---

## 5. 本地模式特殊优化

### 5.1 核心洞察

本地模式下 CLI 和 Agent 在同一进程内运行。`sandboxMode == "none"` 时 Agent 直接访问本地文件系统。

**优化策略：文件路径传递，零拷贝**

```
┌─────────────────────────────────────────────────────────────┐
│                    本地模式 (sandbox=none)                    │
│                                                             │
│  CLI 剪贴板                                                  │
│    ↓ 保存文件                                                │
│  ~/.xbot/tmp/clip-{ts}.png (本地磁盘)                        │
│    ↓ 传路径                                                  │
│  Client.SendInbound(..., media=["/home/.../clip-ts.png"])   │
│    ↓ ChannelTransport (进程内直接调用)                        │
│  RPCTable.send_inbound                                      │
│    ↓                                                        │
│  Agent.processMessage                                       │
│    ↓ Media 处理增强：                                         │
│  1. 检测文件后缀是图片                                        │
│  2. 读取文件 → base64 → data:image URL                       │
│  3. 嵌入 content: ![image](data:image/png;base64,...)       │
│    ↓                                                        │
│  LLM (parseEmbeddedImages 解析)                              │
│                                                             │
│  ⚡ 路径传递 ~100 bytes vs base64 ~13MB                      │
│  ⚡ Agent 端读取是本地 IO，不走网络                             │
└─────────────────────────────────────────────────────────────┘
```

### 5.2 与远程模式的对比

```
┌─────────────────────────────────────────────────────────────┐
│                    远程模式 (WebSocket)                       │
│                                                             │
│  CLI 剪贴板                                                  │
│    ↓ 读取为 []byte                                          │
│  Base64 编码 (膨胀 4/3x)                                    │
│    ↓ 嵌入 content                                           │
│  data:image/png;base64,{huge_string}                        │
│    ↓ JSON 序列化                                             │
│  WebSocket 发送 (可能 10MB+ payload)                         │
│    ↓ 网络传输                                                │
│  服务端 Agent                                                │
│    ↓ parseEmbeddedImages 解析                               │
│  LLM                                                        │
│                                                             │
│  ⚠️ 大 payload，WS 帧可能需要分片                              │
│  ⚠️ 建议压缩图片后再传输                                      │
└─────────────────────────────────────────────────────────────┘
```

### 5.3 临时文件管理

```go
// 本地模式图片存储
const tmpImageDir = "~/.xbot/tmp/clipboard"

// 清理策略：
// 1. 图片创建时设置 mtime
// 2. Agent 处理完消息后异步清理 >1h 的临时图片
// 3. CLI 退出时清理所有临时文件
func cleanupOldClipboardImages() {
    // os.ReadDir(tmpImageDir), RemoveAll if mtime > 1h
}
```

---

## 6. 终端图片预览（可选增强）

### 6.1 终端能力检测

```go
// 检测终端支持的图片协议
type TerminalImageSupport int

const (
    TermImageNone   TerminalImageSupport = iota
    TermImageSixel  // X11: xterm, urxvt, mintty
    TermImageKitty  // Kitty, Ghostty, WezTerm
    TermImageIterm2 // iTerm2 only
)

func DetectTerminalImageSupport() TerminalImageSupport {
    termProgram := os.Getenv("TERM_PROGRAM")
    
    switch termProgram {
    case "iTerm.app":
        return TermImageIterm2
    case "kitty":
        return TermImageKitty
    case "ghostty":
        return TermImageKitty
    case "WezTerm":
        return TermImageKitty
    }
    
    // Sixel: 发送 DA1 查询并检查响应
    // \x1b[c → 终端回复包含 "4" 表示支持 Sixel
    if checkSixelSupport() {
        return TermImageSixel
    }
    
    return TermImageNone
}
```

### 6.2 已有依赖可直接使用

xbot 的 `go.mod` 已间接包含 `charmbracelet/x/ansi/sixel` 和 `charmbracelet/x/ansi/kitty`，无需新增依赖：

- **Sixel**: `ansi/sixel/encoder.go` → `Encoder.Encode(w, img)`
- **Kitty**: `ansi/kitty/graphics.go` → `EncodeGraphics(w, img, opts)`

### 6.3 预览策略

```
图片粘贴后 → 在输入框下方显示缩略预览
    ↓
检测终端能力
    ├── Sixel/Kitty/iTerm2 → 渲染终端内联图片预览 (缩放到 40x20 字符大小)
    └── 不支持 → 显示 "[📷 image.png 2.1MB 1920x1080]" 文本占位符
```

---

## 7. 完整实现计划

### Phase 1: 基础设施（Media 链路修复 + 剪贴板图片提取）

#### 7.1 修复 Media 全链路

| # | 文件 | 修改内容 |
|---|------|---------|
| 1 | `agent/client.go:256` | `SendInbound` 签名添加 `media []string` 参数 |
| 2 | `cmd/xbot-cli/main.go:1524` | `SetSendInboundFn` 中传递 `msg.Media` |
| 3 | `serverapp/rpc_table.go:121` | `send_inbound` RPC 结构体添加 `Media` 字段 |
| 4 | `protocol/ws.go:106` | `WSClientMessage` 添加 `Media []string` (已有但 CLI 不用) |
| 5 | `agent/client.go:258-275` | Remote 模式 `InboundMessage.Media` 传递 |

#### 7.2 新增剪贴板图片模块

```
internal/clipboard/
    ├── clipboard.go       — 统一接口
    ├── clipboard_darwin.go — macOS: osascript + pngpaste
    ├── clipboard_linux.go  — Linux: xclip (X11) + wl-paste (Wayland)
    └── clipboard_windows.go — Windows: PowerShell
```

核心接口：

```go
// internal/clipboard/clipboard.go
type ClipboardImage struct {
    Data     []byte // PNG bytes
    MimeType string // "image/png"
}

// HasImage 检测剪贴板是否有图片（快速，不读取数据）
func HasImage() bool

// ReadImage 读取剪贴板图片数据
func ReadImage() (*ClipboardImage, error)

// IsAvailable 检测剪贴板工具是否可用
func IsAvailable() bool
```

### Phase 2: TUI 集成

| # | 文件 | 修改内容 |
|---|------|---------|
| 6 | `internal/textarea/textarea.go:1956` | 改造 `Paste()` 函数：先检测图片再回退文本 |
| 7 | `channel/cli_update.go:164` | PasteMsg 拦截中添加图片处理路径 |
| 8 | `channel/cli_types.go` | 新增 `pendingImages []ClipboardImage` 字段 |
| 9 | `channel/cli_message.go:264` | `sendMessage` 中处理 pendingImages |

### Phase 3: Agent 端图片处理增强

| # | 文件 | 修改内容 |
|---|------|---------|
| 10 | `agent/agent.go:2068` | 增强 Media 处理：检测图片文件 → 读文件 → base64 → data URL |
| 11 | `llm/anthropic.go:207` | 添加 `parseEmbeddedImages` 支持（类似 OpenAI 路径） |
| 12 | `llm/vision.go` (新) | `ModelSupportsVision()` 推断函数 |

### Phase 4: 本地模式优化

| # | 文件 | 修改内容 |
|---|------|---------|
| 13 | `channel/cli_message.go` | 本地模式：图片保存到 `~/.xbot/tmp/` + 传路径 |
| 14 | `agent/agent.go` | Agent 处理 Media 时检测本地文件 → 高效嵌入 |
| 15 | 临时文件清理逻辑 | 异步清理 >1h 的 clipboard 临时文件 |

### Phase 5: 可选增强

| # | 内容 |
|---|------|
| 16 | 终端图片预览（Sixel/Kitty/iTerm2） |
| 17 | 图片压缩/缩放（添加 `disintegration/imaging` 依赖） |
| 18 | 拖拽文件支持（终端 CSI drop 协议） |
| 19 | 多图粘贴支持 |

---

## 8. 关键设计决策

### Q1: 快捷键选择

| 快捷键 | 行为 |
|--------|------|
| `Ctrl+V` | **智能粘贴**: 检测剪贴板 → 有图则粘贴图片，无图则粘贴文本 |
| `Ctrl+Shift+V` | 强制纯文本粘贴（与浏览器行为一致） |

理由：`Ctrl+V` 智能检测最符合用户直觉。用户不想粘贴图片时可用 `Ctrl+Shift+V`。

### Q2: 图片大小限制

| 场景 | 限制 | 理由 |
|------|------|------|
| 单张图片 | **10MB** 原始数据 | 与 Web 上传一致 |
| Base64 编码后 | ~13.3MB | 膨胀 4/3 倍 |
| 建议压缩后 | ≤2MB | JPEG quality 85, max 2048px |
| 单次粘贴图片数 | 5 张 | 避免上下文爆炸 |

### Q3: 图片传输格式

| 模式 | 格式 | 原因 |
|------|------|------|
| 本地 | 文件路径 (`/tmp/xxx.png`) | 零拷贝，Agent 直接读 |
| 远程 | `data:image/jpeg;base64,...` | 唯一可通过 JSON 传输的方式 |
| LLM 输入 | `data:image/...` (统一) | `parseEmbeddedImages` 已支持 |

### Q4: 是否需要新增 Go 依赖？

| 依赖 | 用途 | 是否必要 |
|------|------|---------|
| `golang.design/x/clipboard` | 跨平台剪贴板图片 | 可选增强（需 CGo） |
| `disintegration/imaging` | 图片缩放/压缩 | Phase 5 可选 |
| `phoenix-tui/phoenix/clipboard` | 备选剪贴板库 | 不必要 |
| `charmbracelet/x/ansi/sixel` | 终端图片预览 | 已在间接依赖中 ✅ |
| `charmbracelet/x/ansi/kitty` | 终端图片预览 | 已在间接依赖中 ✅ |

**最小依赖方案：** Phase 1-3 不需要任何新依赖。图片提取通过外部工具（xclip/wl-paste/osascript），图片编码通过 `encoding/base64`（标准库），终端预览用已有的 `charmbracelet/x/ansi` 间接依赖。

---

## 9. 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| 外部工具未安装 | 无法提取图片 | 优雅降级到纯文本粘贴，提示用户安装 |
| CGo 交叉编译 | `golang.design/x/clipboard` 不可用 | 主方案不依赖 CGo |
| 大图片 WS 传输慢 | 远程模式体验差 | 压缩/缩放后传输，提示大小 |
| 非视觉模型报错 | API 400 | `ModelSupportsVision()` 预检 + 降级 |
| Base64 膨胀 | Context 消耗过大 | 本地模式用路径，远程模式压缩 |
| 终端图片乱码 | 预览失败 | 检测终端能力，不支持则只显示文本占位 |

---

## 10. 总结

本方案的核心设计原则：

1. **本地优先**：本地模式下传文件路径，零拷贝，零网络开销
2. **渐进增强**：从基础功能（图片提取+传路径）到高级功能（终端预览+压缩+多模态检测）
3. **优雅降级**：每一步都有 fallback（无工具→文本粘贴，非视觉模型→文件路径模式，无终端支持→文本占位）
4. **最小依赖**：Phase 1-3 不引入任何新依赖
5. **平台覆盖**：macOS (osascript/pngpaste) + Linux X11 (xclip) + Linux Wayland (wl-paste) + Windows (PowerShell)
