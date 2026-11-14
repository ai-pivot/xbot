// Command qq-channel-verify 用 **host 自己的代码路径** 验证 QQ 渠道插件真的接进了
// xbot 的渠道体系，而不是"plugin.json 里写了就算"。
//
// 它跑的是生产链路的同一串调用（见 serverapp/server.go InitServer）：
//
//	plugin.NewPluginManager → Discover → ActivateAll
//	  → plugin.WireChannelProviders → ChannelProviderRegistrar
//	  → serverapp.ChannelProviderRegistry.Register
//	  → registry.Get("qq").CreateChannel(...)   ← 真的 spawn channel 进程
//	  → 插件发 channel_prompt → agent.ChannelPluginTransport.handleChannelPrompt
//	  → channelPluginPromptProvider.ChannelSystemParts
//	  → agent.ChannelPromptMiddleware.Process 把片段注入 system prompt
//
// 只读、不常驻：用临时 XBOT_HOME（/tmp），不碰 ~/.xbot、不装任何东西、不启停
// 任何服务。跑完自动清理。
//
// 用法（仓库根目录）：
//
//	make -C plugins/xbot-qq build
//	go run ./scripts/qq-channel-verify
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/plugin"
	"xbot/serverapp"
)

// stubRPC 是 RPCTableDispatcher 的最小实现。本验证不涉及工具/设置类 RPC，
// 插件此刻也不会发 RPC（阶段 2 没有消息源），所以回一个固定 result 即可。
type stubRPC struct{}

func (stubRPC) Dispatch(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"result":"ok"}`), nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ 验证失败：%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pluginsDir, err := filepath.Abs("plugins")
	if err != nil {
		return fmt.Errorf("resolve plugins dir: %w", err)
	}
	binPath := filepath.Join(pluginsDir, "xbot-qq", "bin", "qq-plugin")
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("找不到插件二进制 %s —— 先跑 `make -C plugins/xbot-qq build`", binPath)
	}

	home, err := os.MkdirTemp("", "xbot-qq-verify-")
	if err != nil {
		return fmt.Errorf("create temp xbot home: %w", err)
	}
	defer os.RemoveAll(home)
	fmt.Printf("临时 XBOT_HOME: %s（跑完删除，不触碰 ~/.xbot）\n", home)

	// 按**生产安装布局**摆一份插件：<home>/plugins/xbot.qq/{plugin.json,bin/qq-plugin}。
	// 必须用 ID 作目录名 —— plugin.Manager.findPluginDir 按 <dir>/<pluginID> 定位，
	// 源码树里的 plugins/xbot-qq（dash 命名）只是构建目录，不是运行时布局。
	installedDir := filepath.Join(home, "plugins", "xbot.qq")
	if err := installPluginLayout(installedDir, filepath.Join(pluginsDir, "xbot-qq")); err != nil {
		return err
	}
	fmt.Printf("插件安装布局: %s（plugin.json + bin/qq-plugin）\n", installedDir)

	// ── 1. host 侧接线：注册表 + registrar + factory（照抄 server.go） ──
	fmt.Println("\n=== 1. host 侧渠道注册表接线 ===")
	reg := serverapp.NewChannelProviderRegistry()
	serverapp.SetChannelProviderRegistry(reg)
	plugin.SetChannelProviderRegistrar(func(provider any) error {
		cp, ok := provider.(channel.ChannelProvider)
		if !ok {
			return fmt.Errorf("provider does not implement channel.ChannelProvider (got %T)", provider)
		}
		return reg.Register(cp)
	})
	plugin.SetChannelProviderFactory(func(decl *plugin.ChannelProviderDecl, _ *plugin.StdioPluginProcess) (any, error) {
		return serverapp.NewStdioChannelPluginProvider(decl, stubRPC{}, nil, nil), nil
	})
	fmt.Println("registrar / factory 已注入（同 serverapp.InitServer）")

	// ── 2. 插件发现与激活 ──
	fmt.Println("\n=== 2. 插件发现与激活 ===")
	pm := plugin.NewPluginManager(home)
	pm.SetRuntimeFactory(plugin.NewCompositeRuntimeFactory())
	// 不需要 AddSearchDirs：DefaultPluginDirs 已经包含 <xbotHome>/plugins。
	// 只关心 qq：其余内置插件禁用以避免无关的 spawn/error 噪音。
	pm.DisablePlugins([]string{"xbot.genui", "xbot.git-fancy", "xbot.ssh-runner", "xbot.ambience"})

	n, err := pm.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discover plugins: %w", err)
	}
	fmt.Printf("discovered=%d\n", n)
	if err := pm.ActivateAll(ctx); err != nil {
		return fmt.Errorf("activate plugins: %w", err)
	}

	// ── 3. 注册表里能查到 qq ──
	fmt.Println("\n=== 3. provider 注册进 host 注册表 ===")
	plugin.WireChannelProviders(pm)

	names := make([]string, 0, len(reg.List()))
	for _, p := range reg.List() {
		names = append(names, p.Name())
	}
	fmt.Printf("registry.List() → %v\n", names)

	prov, ok := reg.Get("qq")
	if !ok {
		return fmt.Errorf(`registry.Get("qq") 不存在 —— 渠道注册没接线`)
	}
	fmt.Printf(`registry.Get("qq") → name=%q enabled(cfg)=%t config_keys=%d`+"\n",
		prov.Name(), prov.IsEnabled(map[string]string{"enabled": "true"}), len(prov.ConfigSchema()))
	for _, s := range prov.ConfigSchema() {
		fmt.Printf("    - %-12s type=%-8s label=%s\n", s.Key, s.Type, s.Label)
	}

	// ── 4. 创建渠道实例：真的 spawn channel 进程并推 channel_config ──
	fmt.Println("\n=== 4. CreateChannel（spawn channel 进程 + 推 channel_config）===")
	// 用 enabled=false 是有意的：本脚本只验证"注册 + 提示词接线"，不碰网络。
	// 一旦 enabled=true，插件会真的去 bots.qq.com 换 token —— 本地验证不该
	// 依赖外部凭据，也不该在 CI/开发机上发起外网请求。
	cfg := map[string]string{
		"enabled":    "false",
		"app_id":     "102030405",
		"app_secret": "not-a-real-secret",
		"allow":      "*",
		"admins":     "openid-admin",
	}
	ch, err := prov.CreateChannel(cfg, bus.NewMessageBus())
	if err != nil {
		return fmt.Errorf("create channel: %w", err)
	}
	tr, ok := ch.(*agent.ChannelPluginTransport)
	if !ok {
		return fmt.Errorf("create channel returned %T, want *agent.ChannelPluginTransport", ch)
	}
	defer func() {
		tr.Stop()
		_ = tr.Close()
		pm.DeactivateAll(ctx)
	}()

	if err := tr.Start(); err != nil {
		return fmt.Errorf("start transport: %w", err)
	}
	go tr.Run(ctx)
	fmt.Printf("channel 实例已创建并启动读取循环: name=%q\n", tr.Name())

	// ── 5. 等插件发 channel_prompt，并由 host 侧解析 ──
	fmt.Println("\n=== 5. 渠道提示词被 host 取用（transport → ChannelPromptProvider）===")
	parts := waitForChannelPrompt(ctx, tr, 10*time.Second)
	if parts == nil {
		return fmt.Errorf("10s 内没收到 channel_prompt —— 提示词落盘没接线")
	}

	var (
		key string
		val string
	)
	for k, v := range parts {
		key, val = k, v
	}
	sum := sha256.Sum256([]byte(val))
	fmt.Printf("ChannelSystemParts → key=%q bytes=%d sha256=%s\n", key, len(val), hex.EncodeToString(sum[:]))
	if !strings.HasPrefix(key, "05_channel_") {
		return fmt.Errorf("提示词 key = %q, 期望 05_channel_ 前缀（否则排序会被技能目录冲淡）", key)
	}
	printFirstLines(val, 8)

	// ── 6. 中间件注入：mc.Channel == "qq" 时片段真的进了 SystemParts ──
	fmt.Println("\n=== 6. ChannelPromptMiddleware 注入（system prompt parts）===")
	mw := agent.NewChannelPromptMiddleware(tr.ChannelPromptProvider())
	mc := &agent.MessageContext{
		Ctx:         ctx,
		Channel:     "qq",
		ChatID:      "group:GROUP-OPENID",
		SenderID:    "MEMBER-OPENID",
		SystemParts: map[string]string{},
	}
	if err := mw.Process(mc); err != nil {
		return fmt.Errorf("run channel prompt middleware: %w", err)
	}
	if mc.SystemParts[key] == "" {
		return fmt.Errorf("middleware 没有把 %q 注入 SystemParts（keys=%v）", key, keysOf(mc.SystemParts))
	}
	fmt.Printf("注入后的 SystemParts keys=%v（其中 %q 长度 %d）\n",
		keysOf(mc.SystemParts), key, len(mc.SystemParts[key]))

	// 反证：别的渠道不该拿到 qq 的片段。
	other := &agent.MessageContext{Ctx: ctx, Channel: "feishu", SystemParts: map[string]string{}}
	if err := mw.Process(other); err != nil {
		return fmt.Errorf("run channel prompt middleware for feishu: %w", err)
	}
	if _, leaked := other.SystemParts[key]; leaked {
		return fmt.Errorf("qq 的提示词泄漏进了 feishu 渠道")
	}
	fmt.Println(`反证：channel="feishu" 的 SystemParts 里没有 05_channel_qq ✓`)

	// ── 7. 插件进程侧日志：本程序用未设置 xbotHome 的 provider，插件 stderr
	// 直通到这里（上面那些 "qq-plugin: ..." 行），真实部署时会被 host 重定向到
	// $XBOT_HOME/plugins/qq/logs/stderr.log。
	fmt.Println("\n=== 7. 插件进程日志（stderr 直通）===")
	fmt.Println("  见上方 \"qq-plugin: channel_prompt declared: …\" 与 \"channel_config: …\" 两行")

	fmt.Println("\n✅ 验证通过：qq provider 已注册、channel 实例可创建、渠道提示词被 host 取用并注入。")
	return nil
}

// waitForChannelPrompt 轮询 provider，直到插件发来的 channel_prompt 被 host 收下。
func waitForChannelPrompt(ctx context.Context, tr *agent.ChannelPluginTransport, timeout time.Duration) map[string]string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if parts := tr.ChannelPromptProvider().ChannelSystemParts(ctx, "group:GROUP-OPENID", "MEMBER-OPENID"); len(parts) > 0 {
			return parts
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func printFirstLines(text string, n int) {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for _, l := range lines {
		fmt.Println("  │ " + l)
	}
	fmt.Println("  │ …（全文见 plugins/xbot-qq/prompt/channel_prompt.md）")
}

// installPluginLayout 把构建目录摆成 host 认的安装布局（目录名 = plugin ID）。
//
// 生产态由 `make -C plugins/xbot-qq install` 完成同样的两件事：plugin.json +
// bin/qq-plugin 落到 $XBOT_HOME/plugins/xbot.qq/ 下。这里落到临时目录，等价且
// 不触碰 ~/.xbot。
func installPluginLayout(dst, src string) error {
	if err := os.MkdirAll(filepath.Join(dst, "bin"), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	if err := copyFile(filepath.Join(src, "plugin.json"), filepath.Join(dst, "plugin.json"), 0o644); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(src, "bin", "qq-plugin"), filepath.Join(dst, "bin", "qq-plugin"), 0o755); err != nil {
		return err
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return os.Chmod(dst, mode)
}
