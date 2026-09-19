package channel

import (
	"xbot/protocol"
	"xbot/tools"
)

// RunnerCallbacks groups runner management closures shared between Web and Feishu channels.
type RunnerCallbacks struct {
	// RunnerList lists every managed machine (no credentials included).
	RunnerList func() ([]tools.RunnerInfo, error)
	// RunnerCreate registers (or re-keys) a runner and returns the connect command
	// to run on that machine.
	RunnerCreate func(name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error)
	// RunnerDelete removes a runner (and drops its live connection).
	RunnerDelete func(name string) error
	// RunnerRename renames a runner.
	RunnerRename func(oldName, newName string) error
	// RunnerConnectCmd returns the connect command for an existing runner.
	RunnerConnectCmd func(name string) (string, error)
	// RunnerSessionGet reports the runner bound to a session and whether it is online.
	RunnerSessionGet func(channelName, chatID string) (string, bool)
	// RunnerSessionSet binds a session to a runner ("" = back to the local host).
	RunnerSessionSet func(channelName, chatID, name string) error
}

// LLMCallbacks groups LLM management closures shared between Web and Feishu channels.
type LLMCallbacks struct {
	LLMList func(senderID string) ([]protocol.ModelEntry, protocol.ModelEntry)
	LLMSet  func(senderID, subID, model string) error
	// MaxContext / MaxOutputTokens callbacks take an explicit (subID, model)
	// pair so channel UIs that already know the selected model (e.g. feishu
	// model tab) can write per-model config directly. When subID/model are
	// empty (legacy/web callers without a model selector), the implementation
	// falls back to session resolution.
	LLMGetMaxContext      func(senderID, subID, model string) int
	LLMSetMaxContext      func(senderID, subID, model string, maxContext int) error
	LLMGetMaxOutputTokens func(senderID, subID, model string) int
	LLMSetMaxOutputTokens func(senderID, subID, model string, maxTokens int) error
	LLMGetThinkingMode    func(senderID string) string
	LLMSetThinkingMode    func(senderID string, mode string) error
}
