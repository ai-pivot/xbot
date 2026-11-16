package tools

import (
	"strings"
	"testing"
)

// to="parent" must be derived from the caller's own session key — never guessed,
// and never silently falling back to some other address when there is no parent.
func TestResolveParentAddress(t *testing.T) {
	cases := []struct {
		name string
		ctx  *ToolContext
		want string
		err  bool
	}{
		{
			name: "top-level subagent -> user session",
			ctx:  &ToolContext{CallerSessionKey: "web:web-4/court.ducha:main"},
			want: "session:web:web-4",
		},
		{
			name: "nested subagent -> parent agent",
			ctx:  &ToolContext{CallerSessionKey: "web:web-4/court.shoufu:main/court.gongbu:build-1"},
			want: "agent:court.shoufu/main", // 父是 shoufu，不是调用者自己
		},
		{
			name: "agent-qualified caller key is stripped first",
			ctx:  &ToolContext{CallerSessionKey: "agent:web:chat_1/taizi:dpsk-perf-1"},
			want: "session:web:chat_1",
		},
		{
			name: "falls back to SessionKey when CallerSessionKey is empty",
			ctx:  &ToolContext{SessionKey: "agent:web:chat_1/taizi:x"},
			want: "session:web:chat_1",
		},
		{
			name: "deep chain keeps the immediate parent",
			ctx:  &ToolContext{CallerSessionKey: "web:a/p:1/c:2/d:3"},
			want: "agent:c/2",
		},
		{name: "no session key", ctx: &ToolContext{}, err: true},
		{name: "top-level session has no parent", ctx: &ToolContext{CallerSessionKey: "web:web-4"}, err: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveParentAddress(tc.ctx)
			if tc.err {
				if err == nil {
					t.Fatalf("resolveParentAddress() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveParentAddress() error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveParentAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The tool description must keep telling agents that to="parent" exists — this is
// the discoverability contract (models only know what the schema says).
func TestSendMessageDescriptionDocumentsParentRoute(t *testing.T) {
	d := (&SendMessageTool{}).Description()
	for _, want := range []string{`to="parent"`, `agent:<parent-role>/<parent-instance>`, `session:<channel>:<chatID>`} {
		if !strings.Contains(d, want) {
			t.Fatalf("description lost %q", want)
		}
	}
}
