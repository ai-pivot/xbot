package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

func TestValidateInvariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		state       *runState
		wantErr     bool
		errContains string
	}{
		{
			name: "valid state",
			state: &runState{
				messages: []llm.ChatMessage{
					{Role: "system", Content: "sys"},
					{Role: "user", Content: "hi"},
					{Role: "assistant", Content: "hello"},
					{Role: "user", Content: "how"},
					{Role: "assistant", Content: "fine"},
				},
				persistence: NewPersistenceBridge(nil, 3),
				tokenTracker: func() *TokenTracker {
					tt := &TokenTracker{}
					tt.RecordLLMCall(100, 50)
					return tt
				}(),
			},
			wantErr: false,
		},
		{
			name: "violation: LastPersistedCount > len(messages)",
			state: &runState{
				messages: []llm.ChatMessage{
					{Role: "user", Content: "hi"},
					{Role: "assistant", Content: "hello"},
					{Role: "user", Content: "msg"},
				},
				persistence:  NewPersistenceBridge(nil, 5),
				tokenTracker: &TokenTracker{},
			},
			wantErr:     true,
			errContains: "LastPersistedCount(5) > len(messages)(3)",
		},
		{
			name: "violation: promptTokens > 0 but no source",
			state: &runState{
				messages:    []llm.ChatMessage{{Role: "user", Content: "hi"}},
				persistence: NewPersistenceBridge(nil, 0),
				tokenTracker: &TokenTracker{
					promptTokens: 100,
				},
			},
			wantErr:     true,
			errContains: "promptTokens=100 but hadLLMCall=false restoredFromDB=false",
		},
		{
			name: "valid: hasLLMCall but promptTokens=0 (API cache hit)",
			state: &runState{
				messages:    []llm.ChatMessage{{Role: "user", Content: "hi"}},
				persistence: NewPersistenceBridge(nil, 0),
				tokenTracker: &TokenTracker{
					hadLLMCall: true,
				},
			},
			wantErr: false, // API may return 0 prompt tokens for fully cached responses
		},
		{
			name: "valid after compress",
			state: func() *runState {
				msgs := []llm.ChatMessage{
					{Role: "system", Content: "sys"},
					{Role: "user", Content: "compressed"},
					{Role: "assistant", Content: "summary"},
				}
				tt := &TokenTracker{}
				tt.RecordLLMCall(500, 100)
				tt.ResetAfterCompress()
				return &runState{
					messages:     msgs,
					persistence:  NewPersistenceBridge(nil, 0),
					tokenTracker: tt,
				}
			}(),
			wantErr: false,
		},
		{
			name: "valid after compress with restoredFromDB",
			state: func() *runState {
				msgs := []llm.ChatMessage{
					{Role: "system", Content: "sys"},
					{Role: "user", Content: "compressed"},
					{Role: "assistant", Content: "summary"},
				}
				// Simulate: session restored from DB (promptTokens=500) → LLM call → compress
				tt := NewTokenTracker(500, 200)
				tt.RecordLLMCall(800, 150)
				tt.ResetAfterCompress()
				return &runState{
					messages:     msgs,
					persistence:  NewPersistenceBridge(nil, 0),
					tokenTracker: tt,
				}
			}(),
			wantErr: false,
		},
		{
			name: "valid empty state",
			state: &runState{
				messages:     nil,
				persistence:  NewPersistenceBridge(nil, 0),
				tokenTracker: &TokenTracker{},
			},
			wantErr: false,
		},
		{
			name: "valid restored from DB",
			state: &runState{
				messages: []llm.ChatMessage{
					{Role: "system", Content: "sys"},
					{Role: "user", Content: "hi"},
					{Role: "assistant", Content: "hello"},
				},
				persistence:  NewPersistenceBridge(nil, 3),
				tokenTracker: NewTokenTracker(500, 200),
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.state.ValidateInvariants()
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateInvariants() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && tc.errContains != "" {
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tc.errContains)
				}
			}
		})
	}
}
