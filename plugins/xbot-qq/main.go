// Command qq-plugin implements the xbot QQ channel plugin.
//
// 形态：xbot 的 stdio 渠道插件（plugin.json 里 runtime="stdio"）—— 由 host
// 用 entry 命令拉起一个独立进程，双方用 JSON lines over stdin/stdout 通信
// （协议与 WebSocket 同一套，见 plugin/protocol/protocol.go）。
//
// 协议要点（host → plugin）：
//
//	{"method":"activate","params":{"pluginId":"xbot.qq"}}
//	{"type":"channel_config","metadata":{"config":"{\"enabled\":\"true\",...}"}}
//	{"id":"srv-1","method":"channel_send","params":{"chat_id":"...","content":"..."}}
//	{"method":"deactivate"}
//
// 协议要点（plugin → host）：
//
//	{"result":"ok","channel_provider":{"name":"qq","config_schema":[...]}}   // 应答 activate
//	{"id":"plugin-1","method":"send_inbound","params":{...}}                 // 阶段 2
//	{"id":"srv-1","result":"..."} | {"id":"srv-1","error":"..."}             // 应答 RPC
//
// 阶段划分（见 skeleton.go 头部）：
//
//	阶段 1（本文件已实现）：渠道声明 + 配置解析 + 访问策略 + 协议骨架
//	阶段 2：OneBot 连接、身份注入、渠道提示词落盘
//	阶段 3：slash 门禁、断线重连
//
// 因此本进程现在**不发起任何网络连接**：收到 channel_config 只解析并记录，
// channel_send 一律回 error（诚实失败，不假装发出去）。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

func main() {
	a := newApp(os.Stderr)
	if err := a.run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "qq-plugin: fatal: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Wire types（只镜像本插件用到的子集，保持零依赖）
// ---------------------------------------------------------------------------

// frame 是 host → plugin 的一帧。RPC 请求有 ID+Method，事件推送有 Type。
type frame struct {
	ID       string            `json:"id,omitempty"`
	Method   string            `json:"method,omitempty"`
	Type     string            `json:"type,omitempty"`
	Params   json.RawMessage   `json:"params,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// activateResponse 应答 activate：声明本插件提供名为 qq 的渠道。
type activateResponse struct {
	Result          string             `json:"result"`
	ChannelProvider ChannelDeclaration `json:"channel_provider"`
}

// errorResponse 应答一个失败/未实现的 RPC。必须带 ID，否则 host 侧调用方
// 会一直等到超时 —— 未知 method 也要回，不能静默丢弃。
type errorResponse struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// okResponse 应答一个成功但无返回值的 RPC。
type okResponse struct {
	ID     string `json:"id"`
	Result string `json:"result"`
}

// ---------------------------------------------------------------------------
// app — 协议循环与状态
// ---------------------------------------------------------------------------

type app struct {
	mu  sync.RWMutex
	cfg Config

	// logw 是诊断输出（stderr）。host 会把它重定向到每插件日志文件，
	// 所以这里可以自由打；绝不能写 stdout（stdout 只走协议帧）。
	logw io.Writer
}

func newApp(logw io.Writer) *app {
	return &app{logw: logw}
}

func (a *app) logf(format string, args ...any) {
	if a.logw == nil {
		return
	}
	fmt.Fprintf(a.logw, "qq-plugin: "+format+"\n", args...)
}

func (a *app) config() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func (a *app) setConfig(c Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg = c
}

// run 是协议主循环。拆出来是为了单测能喂内存流（与 plugin/protocol 的
// run(h, stdin, stdout) 同构）。
func (a *app) run(stdin io.Reader, stdout io.Writer) error {
	enc := json.NewEncoder(stdout)
	// bufio.Reader, NOT bufio.Scanner: Scanner 的行长上限会让一条大消息
	// 直接掐断整个协议循环；Reader 没有该限制。
	br := bufio.NewReader(stdin)

	for {
		raw, rerr := br.ReadBytes('\n')
		line := strings.TrimSpace(string(raw))
		if line != "" {
			if stop := a.handleLine(enc, line); stop {
				a.logf("deactivate received, exiting")
				return nil
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil // host 关掉了管道
			}
			return fmt.Errorf("read stdin: %w", rerr)
		}
	}
}

// handleLine 分发一帧。返回 true 表示应当退出进程（deactivate）。
func (a *app) handleLine(enc *json.Encoder, line string) (stop bool) {
	var f frame
	if err := json.Unmarshal([]byte(line), &f); err != nil {
		// 坏帧不回 ID（不知道是谁），只记录。协议循环必须活下去。
		a.logf("drop malformed frame: %v", err)
		return false
	}

	switch {
	case f.Method == "activate":
		a.handleActivate(enc)
	case f.Method == "deactivate":
		writeJSON(enc, okResponse{ID: f.ID, Result: "ok"})
		return true
	case f.Method == "channel_send":
		a.handleChannelSend(enc, f)
	case f.Type == "channel_config":
		a.handleChannelConfig(f)
	case f.Method != "":
		// 未知 RPC：必须应答，否则 host 侧等到超时。
		writeJSON(enc, errorResponse{ID: f.ID, Error: "unsupported method: " + f.Method})
	default:
		// 未识别的异步事件（stream_content / text / session / progress_structured …）
		// 阶段 2 接线后才会关心，现在安全忽略。
	}
	return false
}

// handleActivate 应答渠道声明。host 在插件激活时只问一次。
func (a *app) handleActivate(enc *json.Encoder) {
	decl := qqChannelDeclaration()
	a.logf("activate: declaring channel %q with %d config keys", decl.Name, len(decl.ConfigSchema))
	writeJSON(enc, activateResponse{Result: "ok", ChannelProvider: decl})
}

// handleChannelConfig 处理 host 推来的渠道配置（首帧，热更新时会再推）。
//
// 两段式：宽松解析（永不失败）→ 严格校验（失败只记日志，留着上一次可用配置
// 继续跑）。插件进程绝不因为配置写错而退出 —— 那会让 host 陷入重启循环。
func (a *app) handleChannelConfig(f frame) {
	var raw map[string]string
	if s := strings.TrimSpace(f.Metadata["config"]); s != "" {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			a.logf("channel_config: unparsable config JSON, keeping previous config: %v", err)
			return
		}
	}

	cfg := ParseConfig(raw)
	a.setConfig(cfg)

	if err := cfg.Validate(); err != nil {
		a.logf("channel_config: %v", err)
		a.logf("channel_config: channel stays disconnected until config is fixed")
		return
	}
	a.logf("channel_config: %s", cfg.Summary())
	if !cfg.Enabled {
		a.logf("channel_config: disabled — no OneBot connection will be made")
		return
	}
	if cfg.Allow.Empty() {
		a.logf("channel_config: %s is empty — every inbound message will be rejected "+
			"(set it to \"*\" to process everything)", KeyAllow)
	}
}

// handleChannelSend 应答 host 的下行发送请求。
//
// 阶段 1 没有 OneBot 连接，所以这里**必须诚实地失败**：回 error 让 host 记一条
// 发送失败，而不是回 "ok" 假装发出去（假成功会让上层永远查不出消息丢了）。
func (a *app) handleChannelSend(enc *json.Encoder, f frame) {
	var p struct {
		ChatID  string `json:"chat_id"`
		Content string `json:"content"`
	}
	if len(f.Params) > 0 {
		_ = json.Unmarshal(f.Params, &p)
	}
	a.logf("channel_send to %q dropped: channel not wired yet (phase 2)", p.ChatID)
	writeJSON(enc, errorResponse{ID: f.ID, Error: "qq channel is not wired yet (phase 2)"})
}

// writeJSON 写一帧到 stdout。编码失败无法补救（管道已断），忽略返回值。
func writeJSON(enc *json.Encoder, v any) {
	_ = enc.Encode(v)
}
