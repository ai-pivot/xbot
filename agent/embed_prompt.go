package agent

import "xbot/prompt"

// EmbeddedPrompt returns the compile-time embedded default system prompt.
// Used by PromptLoader when file doesn't exist.
func EmbeddedPrompt() string { return prompt.Default }

// EmbeddedFallbackPrompt returns the compile-time embedded minimal fallback system prompt.
func EmbeddedFallbackPrompt() string { return prompt.Fallback }

// EmbeddedCronPrompt returns the compile-time embedded Cron system prompt template.
func EmbeddedCronPrompt() string { return prompt.CronSystem }
