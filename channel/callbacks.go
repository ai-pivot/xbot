package channel

import (
	"xbot/protocol"
	"xbot/tools"
)

// RunnerCallbacks groups runner management closures shared between Web and Feishu channels.
type RunnerCallbacks struct {
	RunnerTokenGet      func(senderID string) string
	RunnerTokenGenerate func(senderID, mode, dockerImage, workspace string) (string, error)
	RunnerTokenRevoke   func(senderID string) error
	RunnerList          func(senderID string) ([]tools.RunnerInfo, error)
	RunnerCreate        func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error)
	RunnerDelete        func(senderID, name string) error
	RunnerGetActive     func(senderID string) (string, error)
	RunnerSetActive     func(senderID, name string) error
}

// PackItemSpec specifies a local item to include when building an app.
type PackItemSpec struct {
	Type string
	Name string
}

// PackInstallResult records what was installed from an app.
type PackInstallResult struct {
	Name      string
	Version   string
	Installed []string
}

// RegistryCallbacks groups registry management closures shared between Web and Feishu channels.
type RegistryCallbacks struct {
	RegistryUninstall   func(entryType, name, senderID string) error
	RegistryPack        func(name string, items []PackItemSpec, outputPath, author string) error
	RegistryInstallFile func(zipPath, senderID string) (*PackInstallResult, error)
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
