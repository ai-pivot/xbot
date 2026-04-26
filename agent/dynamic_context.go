package agent

import (
	"fmt"

	"xbot/llm"
)

// DynamicContextInjector detects dynamic info changes and injects them during Run() loop.
// First iteration doesn't inject (system prompt already has latest value); subsequent iterations inject when CWD change is detected.
// Injection position: end of tool message content (same as sys_reminder, before sys_reminder).
type DynamicContextInjector struct {
	lastCWD string
	getCWD  func() string // Function to get current CWD (main Agent uses session.GetCurrentDir(), SubAgent uses cfg.InitialCWD)
}

// NewDynamicContextInjector creates a dynamic context injector.
func NewDynamicContextInjector(getCWD func() string) *DynamicContextInjector {
	return &DynamicContextInjector{getCWD: getCWD}
}

// InjectIfNeeded detects CWD change, appends <dynamic-context> to latest tool message end if changed.
// Returns true if injection occurred.
//
// Injection order: before sys_reminder (dynamic-context describes factual environment changes, sys_reminder describes behavioral guidance).
func (d *DynamicContextInjector) InjectIfNeeded(messages []llm.ChatMessage) bool {
	currentCWD := d.getCWD()
	if d.lastCWD == "" {
		// First iteration: record but don't inject (CWD in system prompt is already latest)
		d.lastCWD = currentCWD
		return false
	}

	if currentCWD == d.lastCWD {
		return false // No change, don't inject
	}

	// CWD changed, build injection content
	injection := "<dynamic-context>\n" +
		"Environment change:\n" +
		fmt.Sprintf("- Current directory:已切换为：%s，All Shell commands execute in new directory after switch", currentCWD) +
		"\n</dynamic-context>"

	// Append to end of last message (tool message)
	if len(messages) > 0 {
		lastIdx := len(messages) - 1
		messages[lastIdx].Content += "\n\n" + injection
	}

	d.lastCWD = currentCWD
	return true
}
