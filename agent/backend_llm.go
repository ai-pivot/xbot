package agent

import "xbot/config"

// LLMManagement groups methods for LLM model and parameter management.
type LLMManagement interface {
	ListModels() []string
	ListAllModels() []string
	GetDefaultModel() string
	SetUserModel(senderID, model string) error
	SwitchModel(senderID, model string) error
	GetContextMode() string
	SetContextMode(mode string) error
	SetModelTiers(cfg config.LLMConfig) error
	GetUserMaxContext(senderID string) int
	SetUserMaxContext(senderID string, maxContext int) error
	GetUserMaxOutputTokens(senderID string) int
	SetUserMaxOutputTokens(senderID string, maxTokens int) error
	GetUserThinkingMode(senderID string) string
	SetUserThinkingMode(senderID string, mode string) error
	GetLLMConcurrency(senderID string) int
	SetLLMConcurrency(senderID string, personal int) error
	SetDefaultThinkingMode(mode string) error
}
