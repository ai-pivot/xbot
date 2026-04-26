package prompt

import _ "embed"

// Default is the compile-time embedded default system prompt template.
// Used when the user has not configured a prompt file (Agent.PromptFile / PROMPT_FILE).
// Channel-agnostic: contains no channel-specific hints; channel customization is injected by ChannelPromptProvider.
//
//go:embed prompt.md
var Default string

// Base parts.
//
//go:embed base/identity.md
var Identity string

//go:embed base/behavior.md
var Behavior string

//go:embed base/environment.md
var Environment string

//go:embed base/code_rules.md
var CodeRules string

// Mode-specific parts.
//
//go:embed modes/tools_flat.md
var ToolsFlat string

//go:embed modes/tools_letta.md
var ToolsLetta string

//go:embed modes/memory_letta.md
var MemoryLetta string

// User message guide parts.
//
//go:embed guides/user_message_flat.md
var UserMessageGuideFlat string

//go:embed guides/user_message_letta.md
var UserMessageGuideLetta string

// Fallback is a minimal fallback system prompt template, used only when the default prompt cannot be parsed.
//
//go:embed fallback.md
var Fallback string

// CLIChannel is the CLI channel-specific prompt.
//
//go:embed channels/cli.md
var CLIChannel string

// FeishuChannel is the Feishu channel-specific prompt.
//
//go:embed channels/feishu.md
var FeishuChannel string

// CronSystem is the Cron-specific system prompt template.
// Rendered with fmt.Sprintf(workDir, now).
//
//go:embed cron/system.md
var CronSystem string
