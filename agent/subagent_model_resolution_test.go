package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// subagent_model_resolution_test.go —— 子代理模型解析的守护测试。
//
// 用户要求（2026-09-30「在我们本分支彻底修好，注意子代理的模型解析逻辑」）：
//   1. 角色设定了等级（vanguard/balance/swift）⇒ 先按等级去表里查（user_settings
//      的 tier_<等级> = "subID|model"，LLMFactory.resolveTierModel）；
//   2. 查不到时 ⇒ fall back 到**主会话的模型**（UserContext.Model），不是部署默认。
//
// 生产 panic（cli-panic.log 50/50，2026-09-17 起）：交互式会话的 Run ctx 派生
// 丢失 UserContext ⇒ 嵌套 spawn 的 buildSubAgentRunConfig 拿到 nil *UserContext
// 接收者 ⇒ ResolveLLMForModel 空指针 panic（被 clipanic 恢复，该次工具调用失败）。

// stubLLMClient —— 最小 llm.LLM 桩（身份可区分即可）。
type stubLLMClient struct{ name string }

func (c stubLLMClient) Generate(_ context.Context, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	return nil, nil
}
func (c stubLLMClient) ListModels() []string { return nil }

// newResolutionTestUserContext 构造带 factory 桩的 UserContext：
// 主会话模型 = ("main-model", stubLLMClient{"main-client"}, subID="sub-main")。
func newResolutionTestUserContext(factory *llmFactoryRef) *UserContext {
	return &UserContext{
		LLMClient:        stubLLMClient{"main-client"},
		Model:            "main-model",
		MaxContextTokens: 1000,
		ThinkingMode:     "enabled",
		MaxOutputTokens:  100,
		SubID:            "sub-main",
		SenderID:         "cli_user",
		factory:          factory,
	}
}

// 守护 ①：等级**查表命中** ⇒ 用等级配置的模型（“先查表”的要求不得回归）。
func TestResolveLLMForModel_TierFromTableWins(t *testing.T) {
	tierClient := stubLLMClient{"tier-client"}
	uc := newResolutionTestUserContext(&llmFactoryRef{
		getLLMForModel: func(senderID, model string) (llm.LLM, string, string, int, string, int, bool) {
			return tierClient, "sub-tier", "tier-model", 2000, "disabled", 200, true
		},
	})
	client, resolvedModel, _, _, _, subID := uc.ResolveLLMForModelWithFallback("vanguard")
	if client != llm.LLM(tierClient) {
		t.Fatalf("tier configured: client must be the tier client, got %v", client)
	}
	if resolvedModel != "tier-model" || subID != "sub-tier" {
		t.Fatalf("tier configured: got model=%q subID=%q, want (tier-model, sub-tier)", resolvedModel, subID)
	}
}

// 守护 ②（用户要求的核心语义）：等级**查不到**（GetLLMForModel 回落部署默认，ok=false）
// ⇒ 必须回落**主会话的模型**（uc.LLMClient/uc.Model）。
// 旧实现在这里返回了部署默认的 client —— 日志写“falling back to main model”，
// 代码却把 GetLLMForModel 的部署默认原样透传（说一套做一套）。
func TestResolveLLMForModel_TierNotFoundFallsBackToMainSessionModel(t *testing.T) {
	deployClient := stubLLMClient{"deploy-default"}
	uc := newResolutionTestUserContext(&llmFactoryRef{
		getLLMForModel: func(senderID, model string) (llm.LLM, string, string, int, string, int, bool) {
			// 复刻 GetLLMForModel 的真实形态：等级未配置 ⇒ 部署默认 + ok=false。
			return deployClient, "", "deploy-model", 500, "", 50, false
		},
	})
	client, resolvedModel, _, _, _, subID := uc.ResolveLLMForModelWithFallback("vanguard")
	if client != uc.LLMClient || resolvedModel != uc.Model {
		t.Fatalf("tier not found: got (%v, %q), want MAIN SESSION model (%v, %q) — per-user rule: 查不到等级 ⇒ 回落主会话的模型，不是部署默认",
			client, resolvedModel, uc.LLMClient, uc.Model)
	}
	if subID != uc.SubID {
		t.Fatalf("tier not found: subID=%q, want %q（主会话的订阅）", subID, uc.SubID)
	}
}

// 守护 ③（panic 根治）：buildSubAgentRunConfig 在**没有 UserContext 的 ctx**下必须
// 显式报错，而不是 nil 接收者 panic。这正是生产 50/50 panic 的现场形态——
// 交互式会话的 Run ctx 派生曾丢失 UserContext，嵌套 spawn 以 nil 调用
// ResolveLLMForModelWithFallback（user_context.go:129 空指针）。
func TestSubAgentRunConfig_NilUserContextReturnsErrorNotPanic(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{
		multiSession: mt,
		tools:        tools.NewRegistry(),
		skills:       NewSkillStore(t.TempDir(), nil, nil),
	}
	a.SetBgTaskManager(tools.NewBackgroundTaskManager())
	parentCtx := &tools.ToolContext{
		AgentID:  "main",
		Channel:  "web",
		ChatID:   "chat-nil-uc",
		SenderID: "u1",
	}

	// bare ctx（无 UserContext）：交互式会话 Run ctx 派生丢失 UserContext 的产物。
	_, err := a.buildSubAgentRunConfig(context.Background(), parentCtx, "task", "", nil, tools.SubAgentCapabilities{}, "reviewer", false, "inst-1", "vanguard")
	if err == nil {
		t.Fatal("nil UserContext must be an explicit error（子代理的模型解析需要用户上下文：等级查表 + 主会话回落都以它为前提）")
	}
	if !strings.Contains(err.Error(), "UserContext") && !strings.Contains(err.Error(), "user context") {
		t.Fatalf("error must say what's missing, got: %v", err)
	}
}
