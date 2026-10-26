package agent

import (
	"strings"
	"testing"

	"xbot/prompt"
)

// memoryToolNames is the union of every provider-specific memory tool. A "none"
// provider must never surface ANY of them in its prompt.
var memoryToolNames = []string{
	"memory_search", "memory_add", "memory_manage",
	"memory_write", "memory_list", "memory_read",
	"core_memory_append", "core_memory_replace", "rethink",
	"archival_memory_insert", "archival_memory_search",
	"recall_memory_search", "consolidate_memory",
}

func assertNoMemoryPrompt(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "## Memory") {
		t.Errorf("%s: injected a memory prompt section:\n%s", where, text)
	}
	for _, name := range memoryToolNames {
		if strings.Contains(text, name) {
			t.Errorf("%s: injected memory tool %q", where, name)
		}
	}
}

// none 模式 = 不注入任何记忆相关提示词（但仍保留 provider-neutral 工具引导）。
func TestEnrichPromptData_NoneProviderHasNoMemoryPrompt(t *testing.T) {
	got := enrichPromptData(PromptData{MemoryProvider: "none"})
	if got.Memory != "" {
		t.Errorf("none: Memory prompt must be empty, got %q", got.Memory)
	}
	if got.Tools != prompt.ToolsFlat {
		t.Errorf("none: expected the provider-neutral tool guide, got %q", got.Tools)
	}
	assertNoMemoryPrompt(t, "none", got.Tools+got.Memory)
}

// none 模式渲染出的完整 system prompt 里不能出现任何记忆工具/记忆章节。
func TestRender_NoneProviderHasNoMemorySection(t *testing.T) {
	loader := NewPromptLoader("")
	data := PromptData{Channel: "cli", WorkDir: "/tmp", CWD: "/tmp"}

	data.MemoryProvider = "none"
	out := loader.Render(data)
	assertNoMemoryPrompt(t, "none", out)

	// Sanity: an actual memory provider DOES inject its section/tools, so the
	// assertion above has discriminating power (it is not passing vacuously).
	data.MemoryProvider = "xbot"
	xbotOut := loader.Render(data)
	if !strings.Contains(xbotOut, "## Memory") || !strings.Contains(xbotOut, "memory_search") {
		t.Fatalf("sanity check failed: xbot prompt should contain its memory section/tools")
	}
}
