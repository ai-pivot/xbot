package common

import "fmt"

// ─── Managed runners (remote machines) ───

// RunnerLLMSettings is a runner's optional local LLM configuration.
type RunnerLLMSettings struct {
	Provider string
	APIKey   string
	Model    string
	BaseURL  string
}

// HasLLM reports whether the runner declares LLM capability.
func (l *RunnerLLMSettings) HasLLM() bool {
	return l != nil && l.Provider != ""
}

// RunnerInfo describes one managed remote machine (token is never included).
type RunnerInfo struct {
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	DockerImage string `json:"docker_image"`
	Workspace   string `json:"workspace"`
	CreatedAt   string `json:"created_at"`
	Online      bool   `json:"online"`
	Version     string `json:"version,omitempty"`

	LLMProvider string `json:"llm_provider,omitempty"`
	LLMAPIKey   string `json:"llm_api_key,omitempty"`
	LLMModel    string `json:"llm_model,omitempty"`
	LLMBaseURL  string `json:"llm_base_url,omitempty"`
}

// LLMSettings returns the runner's LLM configuration.
func (r RunnerInfo) LLMSettings() RunnerLLMSettings {
	return RunnerLLMSettings{
		Provider: r.LLMProvider,
		APIKey:   r.LLMAPIKey,
		Model:    r.LLMModel,
		BaseURL:  r.LLMBaseURL,
	}
}

const (
	RunnerModeNative = "native"
	RunnerModeDocker = "docker"
)

// NormalizeRunnerMode validates and canonicalizes a runner mode.
func NormalizeRunnerMode(mode string) (string, error) {
	switch mode {
	case "":
		return RunnerModeNative, nil
	case RunnerModeNative, RunnerModeDocker:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid runner mode %q (valid: %s, %s)", mode, RunnerModeNative, RunnerModeDocker)
	}
}
