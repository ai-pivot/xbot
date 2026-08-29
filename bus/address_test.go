package bus

import (
	"testing"
)

func TestAddressString(t *testing.T) {
	tests := []struct {
		addr Address
		want string
	}{
		{Address{Scheme: "im", Domain: "feishu", ID: "ou_xxx"}, "im://feishu/ou_xxx"},
		{Address{Scheme: "agent", Domain: "main"}, "agent://main"},
		{Address{Scheme: "agent", Domain: "main", ID: "code-reviewer"}, "agent://main/code-reviewer"},
		{Address{Scheme: "system", Domain: "cron"}, "system://cron"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.addr.String()
			if got != tt.want {
				t.Errorf("Address.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAddressPredicates(t *testing.T) {
	im := NewIMAddress("feishu", "ou_xxx")
	if im.IsAgent() {
		t.Error("IM address should not be agent")
	}

	ag := NewAgentAddress("main/code-reviewer")
	if !ag.IsAgent() {
		t.Error("expected IsAgent() = true")
	}
	if ag.Domain != "main" || ag.ID != "code-reviewer" {
		t.Errorf("NewAgentAddress(\"main/code-reviewer\") = %v", ag)
	}
}
