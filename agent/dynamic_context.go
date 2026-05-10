package agent

import (
	"fmt"

	"xbot/llm"
	"xbot/tools"
)

// DynamicContextInjector 在 Run() 循环中检测动态信息变化并注入。
// 首轮不注入（system prompt 已包含最新值），后续 iteration 检测到 CWD 变化或 peer 变化时注入。
// 注入位置：tool message content 末尾（与 sys_reminder 相同方式，在 sys_reminder 之前）。
type DynamicContextInjector struct {
	lastCWD   string
	getCWD    func() string
	lastPeers string
	getPeers  func() string
}

// NewDynamicContextInjector 创建动态上下文注入器。
func NewDynamicContextInjector(getCWD func() string) *DynamicContextInjector {
	return &DynamicContextInjector{getCWD: getCWD}
}

// NewDynamicContextInjectorWithPeers 创建带 peer 感知的动态上下文注入器。
func NewDynamicContextInjectorWithPeers(getCWD func() string, getPeers func() string) *DynamicContextInjector {
	return &DynamicContextInjector{getCWD: getCWD, getPeers: getPeers}
}

// InjectIfNeeded 检测 CWD 和 peer 变化，如有变化则将 <dynamic-context> 追加到最新 tool message 末尾。
func (d *DynamicContextInjector) InjectIfNeeded(messages []llm.ChatMessage) bool {
	currentCWD := d.getCWD()

	needInject := false
	cwdChanged := false
	peersChanged := false

	if d.lastCWD == "" {
		d.lastCWD = currentCWD
	} else if currentCWD != d.lastCWD {
		cwdChanged = true
		needInject = true
		d.lastCWD = currentCWD
	}

	var currentPeers string
	if d.getPeers != nil {
		currentPeers = d.getPeers()
	}
	if d.lastPeers == "" {
		d.lastPeers = currentPeers
	} else if currentPeers != d.lastPeers {
		peersChanged = true
		needInject = true
		d.lastPeers = currentPeers
	}

	if !needInject {
		return false
	}

	injection := "<dynamic-context>"

	if cwdChanged {
		injection += "\n环境变化:\n" +
			fmt.Sprintf("- 当前目录已切换为：%s，切换后所有 Shell 命令在新目录执行", currentCWD)
	}

	if peersChanged && currentPeers != "" {
		injection += "\n\n协作状态:\n" + currentPeers +
			"\n\n你正在与其他 agent 协作修改同一仓库。注意：\n" +
			"- 你的改动在独立 worktree 中，不会影响同伴\n" +
			"- 完成后需要与同伴协调合并，使用 SendMessage 主动沟通\n" +
			"- 合并冲突时与相关同伴直接协商，必要时请用户仲裁"
	}

	injection += "\n</dynamic-context>"

	if len(messages) > 0 {
		lastIdx := len(messages) - 1
		messages[lastIdx].Content += "\n\n" + injection
	}

	return true
}

// buildPeerContextXML builds the <peers> XML section for the dynamic context injector.
func buildPeerContextXML(workspaceRoot, sessionKey string) string {
	repoPath, err := tools.GitRepoRoot(workspaceRoot)
	if err != nil {
		return ""
	}

	peers := tools.GlobalWorktreeRegistry.GetPeers(repoPath, sessionKey)
	if len(peers) == 0 {
		return ""
	}

	result := "<peers"
	if repoPath != "" {
		result += fmt.Sprintf(` repo="%s"`, repoPath)
	}
	result += ">\n"

	for _, p := range peers {
		worktreeInfo := "main project"
		if p.WorktreeDir != "" {
			worktreeInfo = p.WorktreeDir
		}
		result += fmt.Sprintf(
			`  <peer session="%s" role="%s" branch="%s" worktree="%s" status="%s"/>`,
			p.SessionKey, p.Role, p.Branch, worktreeInfo, p.Status,
		) + "\n"
	}
	result += "</peers>"
	return result
}
