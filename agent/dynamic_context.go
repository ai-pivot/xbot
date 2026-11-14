package agent

import (
	"fmt"

	"xbot/llm"
)

// DynamicContextInjector 在 Run() 循环中检测动态信息变化并注入。
// 首轮不注入（system prompt 已包含最新值），后续 iteration 检测到 CWD 变化时注入。
// 注入位置：tool message content 末尾（与 sys_reminder 相同方式，在 sys_reminder 之前）。
type DynamicContextInjector struct {
	lastCWD string
	getCWD  func() string
}

// NewDynamicContextInjector 创建动态上下文注入器。
func NewDynamicContextInjector(getCWD func() string) *DynamicContextInjector {
	return &DynamicContextInjector{getCWD: getCWD}
}

// InjectIfNeeded 检测 CWD 变化，如有变化则将 <dynamic-context> 追加到最新 tool message 末尾。
func (d *DynamicContextInjector) InjectIfNeeded(messages []llm.ChatMessage) bool {
	currentCWD := d.getCWD()

	needInject := false
	cwdChanged := false

	if d.lastCWD == "" {
		d.lastCWD = currentCWD
	} else if currentCWD != d.lastCWD {
		cwdChanged = true
		needInject = true
		d.lastCWD = currentCWD
	}

	if !needInject {
		return false
	}

	injection := "<dynamic-context>"

	if cwdChanged {
		injection += "\n环境变化:\n" +
			fmt.Sprintf("- 当前目录已切换为：%s，切换后所有 Shell 命令在新目录执行", currentCWD)
	}

	injection += "\n</dynamic-context>"

	if len(messages) > 0 {
		lastIdx := len(messages) - 1
		messages[lastIdx].Content += "\n\n" + injection
	}

	return true
}
