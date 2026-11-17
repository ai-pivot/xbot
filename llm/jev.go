package llm

import (
	"context"
	"fmt"
)

// JEVLLM 是 JEV 协议的**占位客户端**。
//
// 为什么要有它：用户要求模型级 API 类型里必须能选 `jev`（2026-11-17），
// 但 JEV 不是对话协议、且当前仓库内没有任何协议规范（请求路径/鉴权/请求体/
// 响应体/流式格式均未知）。此时正确做法是**显式声明未实现**，而不是：
//   - 静默回落到 OpenAI 兼容层（会发出错误请求，用户看到的是"莫名其妙的 400"）；
//   - 或者拿一个猜测的协议去跑（污染数据、误导排障）。
//
// 一旦提供规范，只需在这里实现 Generate/GenerateStream，其余调用方无需改动。
type JEVLLM struct {
	model string
}

func NewJEVLLM(model string) *JEVLLM { return &JEVLLM{model: model} }

func (j *JEVLLM) ListModels() []string {
	if j.model == "" {
		return nil
	}
	return []string{j.model}
}

func (j *JEVLLM) GetDefaultModel() string { return j.model }

func (j *JEVLLM) Generate(_ context.Context, _ string, _ []ChatMessage, _ []ToolDefinition, _ string) (*LLMResponse, error) {
	return nil, fmt.Errorf("jev protocol not implemented: 需要协议规范（请求路径/鉴权头/请求体/响应体/是否流式）")
}
