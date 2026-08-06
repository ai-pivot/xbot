package tools

import (
	"strings"
	"testing"
)

// TestAskUserToolSupportsWeb guards the channel capability: the AskUser tool
// MUST be injected into web sessions too. Previously SupportedChannels was
// ["cli","feishu"] — web agents never got the tool, so they could not initiate
// a question and the (otherwise complete) web ask_user pipeline never fired.
func TestAskUserToolSupportsWeb(t *testing.T) {
	ch := (&AskUserTool{}).SupportedChannels()
	joined := strings.Join(ch, ",")
	for _, want := range []string{"cli", "feishu", "web"} {
		if !strings.Contains(joined, want) {
			t.Errorf("AskUserTool.SupportedChannels missing %q; got %v", want, ch)
		}
	}
}
