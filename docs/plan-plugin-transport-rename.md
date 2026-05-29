# 计划：Plugin Transport 重构 — 消除 "Grpc" 命名 + 提供官方 SDK 包

> 生成时间：2026-05-29 22:00 CST
> 状态：待确认

## 背景与目标

### 核心问题
`GrpcPluginTransport`、`GrpcPluginProcess`、`grpcRuntimeFactory` 等命名具有严重误导性——它们全部使用 **JSON-RPC over stdin/stdout** 协议，与 gRPC / Protocol Buffers 完全无关。这些命名会让新贡献者和插件开发者产生错误预期。

### 期望的最终状态
1. **所有代码中的 "Grpc" 命名替换为准确反映实际协议的名称**
2. **提供官方 Python SDK 包**，让插件开发者 5 分钟内上手，不再需要手动实现 NDJSON 协议循环
3. **Runtime 名称 "grpc" 保持向后兼容**（manifest 中 `"runtime": "grpc"` 继续可用，同时推荐新名称 `"stdio"`）

## 现状分析

### 关键文件
| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `agent/transport_grpc.go` | Channel 插件的 JSON-RPC transport (Layer 2) | 重命名 |
| `agent/transport_grpc_test.go` | Transport 测试 | 重命名 |
| `plugin/runtime.go` | 插件进程管理 + gRPC runtime factory (Layer 1) | 重命名 |
| `plugin/channel_provider.go` | ChannelProvider 工厂，引用 `GrpcPluginProcess` | 重命名 |
| `plugin/runtime_factory.go` | Runtime 注册表 | 重命名 |
| `plugin/plugin.go` | `RuntimeGRPC` 常量 | 修改 |
| `serverapp/channel_plugin.go` | `grpcPluginChannelProvider` + `NewGrpcPluginChannelProvider` | 重命名 |
| `serverapp/server.go` | 引用 `agent.GrpcPluginTransport` | 重命名 |
| `cmd/xbot-cli/main.go` | 引用 `agent.GrpcPluginTransport` 和 `plugin.GrpcPluginProcess` | 重命名 |
| `plugin/PROTOCOL.md` | 协议文档，标注 "historical naming" | 更新 |
| `plugin/sdk/` (新建) | Python SDK 包目录 | 新增 |

### 命名映射表

| 旧名称 | 新名称 | 文件 |
|--------|--------|------|
| `GrpcPluginTransport` | `ChannelPluginTransport` | `agent/transport_grpc.go` → `agent/transport_channel_plugin.go` |
| `GrpcPluginTransportConfig` | `ChannelPluginTransportConfig` | 同上 |
| `NewGrpcPluginTransport` | `NewChannelPluginTransport` | 同上 |
| `NewGrpcPluginTransportWithIO` | `NewChannelPluginTransportWithIO` | 同上 |
| `GrpcPluginProcess` | `StdioPluginProcess` | `plugin/runtime.go` |
| `grpcPlugin` | `stdioPlugin` | `plugin/runtime.go` |
| `grpcRuntimeFactory` | `stdioRuntimeFactory` | `plugin/runtime.go` |
| `NewGRPCRuntime` | `NewStdioRuntime` | `plugin/runtime.go` |
| `grpcPluginChannelProvider` | `stdioChannelPluginProvider` | `serverapp/channel_plugin.go` |
| `NewGrpcPluginChannelProvider` | `NewStdioChannelPluginProvider` | `serverapp/channel_plugin.go` |
| `RuntimeGRPC` | `RuntimeStdio`（保留 `RuntimeGRPC` 作为 alias） | `plugin/plugin.go` |

### 文件重命名

| 旧文件名 | 新文件名 |
|----------|----------|
| `agent/transport_grpc.go` | `agent/transport_channel_plugin.go` |
| `agent/transport_grpc_test.go` | `agent/transport_channel_plugin_test.go` |

### 依赖关系
```
serverapp/server.go
  └→ serverapp/channel_plugin.go (NewGrpcPluginChannelProvider)
       └→ agent/transport_grpc.go (GrpcPluginTransport)
       └→ plugin/channel_provider.go (ChannelProviderFactory → GrpcPluginProcess)

plugin/runtime_factory.go
  └→ plugin/runtime.go (NewGRPCRuntime → grpcRuntimeFactory → grpcPlugin)
       └→ GrpcPluginProcess (stdin/stdout 进程管理)

plugin/examples/grpc-python/main.py  — 示例插件
plugin/examples/echo-channel/main.py — 示例 channel 插件
```

### 风险点
- **Runtime 名称向后兼容**：现有 `plugin.json` 中 `"runtime": "grpc"` 必须继续工作
- **131 处代码引用**：重命名范围大，遗漏任何一处都会导致编译失败（go build 可捕获）
- **AGENTS.md 文档**：多处引用 `GrpcPluginTransport`，需同步更新

## 详细计划

### 阶段一：核心重命名（Host 侧 Go 代码）

- [ ] 步骤 1.1：重命名 `plugin/runtime.go` 中的类型 — 涉及文件：`plugin/runtime.go`
  - `GrpcPluginProcess` → `StdioPluginProcess`
  - `grpcPlugin` → `stdioPlugin`（私有结构体）
  - `grpcRuntimeFactory` → `stdioRuntimeFactory`
  - `NewGRPCRuntime()` → `NewStdioRuntime()`
  - 所有方法接收者保持一致（虽然 Go 方法接收者是值类型不需要改，但函数签名中的参数类型需改）

- [ ] 步骤 1.2：添加 Runtime 名称兼容 — 涉及文件：`plugin/plugin.go`, `plugin/runtime_factory.go`
  - 添加 `RuntimeStdio RuntimeType = "stdio"` 作为推荐新名称
  - 保留 `RuntimeGRPC = "grpc"` 作为向后兼容 alias
  - `runtime_factory.go` 中 `compositeRuntimeFactory`：
    - 字段 `grpc RuntimeFactory` 重命名为 `stdio RuntimeFactory`
    - `NewCompositeRuntimeFactory` 中调用 `NewStdioRuntime()`
    - `Create()` 中 `case RuntimeGRPC` 改为 `case RuntimeGRPC, RuntimeStdio`（两个值都路由到 stdio factory）
    - 错误信息改为 `"unsupported runtime: %q (supported: native, stdio/grpc, script)"`

- [ ] 步骤 1.3：重命名 `plugin/channel_provider.go` — 涉及文件：`plugin/channel_provider.go`
  - `ChannelProviderFactory` 参数 `*GrpcPluginProcess` → `*StdioPluginProcess`
  - `CreateChannelProvider` 参数同步修改

- [ ] 步骤 1.4：重命名 `agent/transport_grpc.go` → `agent/transport_channel_plugin.go` — 涉及文件：文件重命名 + 全文替换
  - `GrpcPluginTransport` → `ChannelPluginTransport`
  - `GrpcPluginTransportConfig` → `ChannelPluginTransportConfig`
  - `NewGrpcPluginTransport` → `NewChannelPluginTransport`
  - `NewGrpcPluginTransportWithIO` → `NewChannelPluginTransportWithIO`
  - 所有错误信息中的 `"grpc transport:"` → `"channel plugin transport:"`

- [ ] 步骤 1.5：重命名测试文件 — 涉及文件：`agent/transport_grpc_test.go` → `agent/transport_channel_plugin_test.go`
  - `TestGrpcPluginTransport_*` → `TestChannelPluginTransport_*`
  - 所有函数体内的类型引用同步修改

- [ ] 步骤 1.6：更新引用方 — 涉及文件：`serverapp/channel_plugin.go`, `serverapp/server.go`, `cmd/xbot-cli/main.go`
  - `grpcPluginChannelProvider` → `stdioChannelPluginProvider`
  - `NewGrpcPluginChannelProvider` → `NewStdioChannelPluginProvider`
  - `*agent.GrpcPluginTransport` → `*agent.ChannelPluginTransport`
  - `*plugin.GrpcPluginProcess` → `*plugin.StdioPluginProcess`

- [ ] 步骤 1.7：更新注释和文档 — 涉及文件：`plugin/PROTOCOL.md`, `docs/agent/plugin.md`, `AGENTS.md`
  - 移除 "historical naming" 免责声明
  - 统一使用新名称
  - 在 PROTOCOL.md 中注明 `"runtime": "stdio"` (推荐) 和 `"grpc"` (兼容) 都可用

- [ ] 步骤 1.8：验证编译和测试
  - `go build ./...` — 确保零编译错误
  - `go test ./...` — 确保所有测试通过
  - `golangci-lint run ./...` — 确保无 lint 警告

### 阶段二：Python SDK 包

- [ ] 步骤 2.1：创建 `plugin/sdk/python/` 目录结构
  ```
  plugin/sdk/python/
  ├── xbot_plugin_sdk/
  │   ├── __init__.py          # 版本号 + 便捷导出
  │   ├── plugin.py            # XbotPlugin 基类 + 装饰器 API
  │   ├── protocol.py          # NDJSON 协议读写（内部使用，也可独立使用）
  │   ├── types.py             # ToolDef, HookResult 等数据类
  │   ├── channel.py           # ChannelPlugin 基类（JSON-RPC channel 协议）
  │   └── _logging.py          # stderr 日志辅助
  ├── README.md                # 快速上手文档
  ├── pyproject.toml           # 打包配置
  └── examples/
      ├── simple_tool.py       # 最简工具插件示例
      └── echo_channel.py     # Channel 插件示例
  ```

- [ ] 步骤 2.2：实现 `protocol.py` — 底层 NDJSON 读写
  - `StdioTransport` 类：封装 stdin/stdout 的 JSON 读写
  - `read_message()` / `write_message()` — 行级 NDJSON
  - 线程安全的 `send_request(method, params)` → 等待 response
  - `on_request(callback)` — 注册请求处理器
  - 自动 flush，UTF-8 编码

- [ ] 步骤 2.3：实现 `types.py` — 数据类定义
  - `ToolDef(name, description, parameters, input_schema)`
  - `HookResult(decision, message, data)`
  - `ToolParam(name, type, description, required, items)`
  - `HookReg(event, matcher)`

- [ ] 步骤 2.4：实现 `plugin.py` — 工具/钩子插件基类
  - `XbotPlugin` 基类：
    - `@xbot.tool(name, description)` 装饰器注册工具
    - `@xbot.hook(event, matcher="")` 装饰器注册钩子
    - `@xbot.enricher(name)` 装饰器注册富化器
    - `run()` — 启动协议循环（阻塞）
  - 内部处理 activate/deactivate/execute_tool/hook/enrich 消息分发
  - 插件开发者只需继承并写业务逻辑

  使用示例：
  ```python
  from xbot_plugin_sdk import XbotPlugin

  class MyPlugin(XbotPlugin):
      @XbotPlugin.tool("greet", "Greet someone by name")
      def greet(self, name: str) -> str:
          return f"Hello, {name}!"

      @XbotPlugin.hook("PostToolUse", matcher="greet")
      def on_greet_done(self, event, tool_name, tool_input, **kwargs):
          return HookResult.allow(message=f"{tool_name} completed")

  if __name__ == "__main__":
      MyPlugin().run()
  ```

- [ ] 步骤 2.5：实现 `channel.py` — Channel 插件基类
  - `ChannelPlugin` 基类：
    - 继承 `XbotPlugin` 的 activate 能力
    - 额外实现 JSON-RPC channel 协议（event push、RPC 请求/响应）
    - `on_event(type, handler)` — 注册事件处理器
    - `call_rpc(method, params)` → 向 xbot 发送 RPC 请求
    - `send_inbound(chat_id, content)` — 发送入站消息
    - 自动处理 xbot RPC 请求（如 `channel_send`）

  使用示例：
  ```python
  from xbot_plugin_sdk import ChannelPlugin

  class EchoChannel(ChannelPlugin):
      channel_name = "echo"

      def on_text(self, chat_id, content, metadata):
          if metadata.get("is_final"):
              print(f"Agent replied: {content}")

      def on_config(self, config):
          port = config.get("port", "9876")
          self.start_http_server(port)

  if __name__ == "__main__":
      EchoChannel().run()
  ```

- [ ] 步骤 2.6：编写 README.md — 快速上手文档
  - 安装方式：`pip install xbot-plugin-sdk` 或直接复制 `xbot_plugin_sdk/` 目录
  - 3 个示例：最简工具、带钩子的工具、Channel 插件
  - plugin.json 配置示例
  - 调试技巧

- [ ] 步骤 2.7：更新现有示例插件使用 SDK
  - `plugin/examples/grpc-python/main.py` → 用 SDK 重写
  - `plugin/examples/echo-channel/main.py` → 用 SDK 重写
  - 保留原始版本作为 `*_raw.py` 参考（或移除，因为 PROTOCOL.md 仍是完整参考）

### 阶段三：收尾

- [ ] 步骤 3.1：验证全量编译 + 测试
  - `go build ./...`
  - `go test ./...`
  - 手动运行 Python 示例验证协议正确性

- [ ] 步骤 3.2：更新 AGENTS.md 和 Knowledge Files
  - `docs/agent/plugin.md` — 更新所有 "Grpc" 引用
  - `AGENTS.md` — 更新 Plugin System 章节中的命名
  - `docs/agent/tools.md` — 更新 transport 相关描述

## 验证方案

- `go build ./...` — 零编译错误（重命名遗漏的任何引用都会导致编译失败）
- `go test ./agent/ -run TestChannelPlugin` — 所有 transport 测试通过
- `go test ./plugin/ -run TestGrpcPlugin` — 所有 runtime 测试通过（测试函数名也需更新）
- Python 示例手动测试：`echo '{"method":"activate","params":{"pluginId":"test"}}' | python3 plugin/sdk/python/examples/simple_tool.py` 验证协议输出

## 回滚策略

所有重命名都是纯机械操作（文件重命名 + 标识符替换），不涉及逻辑变更。如果出现问题：
1. `git revert` 整个 commit 即可完全回滚
2. Python SDK 是新增文件，删除 `plugin/sdk/python/` 不影响任何现有功能

## 注意事项

- `"runtime": "grpc"` 的向后兼容性必须保留 — 现有插件不需要修改 plugin.json
- `runtime_factory.go` 中的注册表需要同时映射 `"grpc"` 和 `"stdio"` 到同一个 factory
- Python SDK 的 `channel.py` 需要正确处理三种消息类型的路由（event push、RPC request、RPC response），这与 Layer 1 的简单协议不同
- `transport_grpc.go` 中的 `rpcResponse` 结构体（`agent/transport_remote.go`）无需改名——它是通用的 RPC 响应类型，不携带 "Grpc" 语义
- `plugin/runtime.go` 中的 `PluginRequest`、`PluginResponse`、`PluginInbound` 等类型名称不需要改——它们是协议消息类型，命名准确
