package channel

// AskQItem represents a question item for AskUser functionality.
// Shared by CLI and Feishu channels.
type AskQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}
