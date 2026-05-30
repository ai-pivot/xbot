package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRun_ActivateAndExecuteTool(t *testing.T) {
	var gotActivate bool
	var gotExecuteTool bool

	h := &Handler{
		Activate: func(params *ActivateParams) (*ActivateResult, error) {
			gotActivate = true
			if params.PluginID != "test-plugin" {
				t.Errorf("expected pluginId=test-plugin, got %q", params.PluginID)
			}
			return &ActivateResult{
				Tools: []ToolDef{
					{Name: "echo", Description: "Echo input", InputSchema: json.RawMessage(`{"type":"object"}`)},
				},
			}, nil
		},
		ExecuteTool: func(params *ExecuteToolParams) (*ExecuteToolResult, error) {
			gotExecuteTool = true
			if params.ToolName != "echo" {
				t.Errorf("expected toolName=echo, got %q", params.ToolName)
			}
			return &ExecuteToolResult{Result: `{"echo": "hello"}`}, nil
		},
	}

	// Simulate xbot sending activate then execute_tool
	stdin := strings.NewReader(
		`{"method":"activate","params":{"pluginId":"test-plugin"}}` + "\n" +
			`{"method":"execute_tool","params":{"toolName":"echo","input":"{\"msg\":\"hello\"}"}}` + "\n",
	)
	var stdout bytes.Buffer
	run(h, stdin, &stdout)

	if !gotActivate {
		t.Error("Activate was not called")
	}
	if !gotExecuteTool {
		t.Error("ExecuteTool was not called")
	}

	// Verify stdout output is valid NDJSON
	lines := bytes.Split(stdout.Bytes(), []byte{'\n'})
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			t.Errorf("line %d: invalid JSON: %s", i, line)
		}
	}
}

func TestRun_ActivateError(t *testing.T) {
	h := &Handler{
		Activate: func(params *ActivateParams) (*ActivateResult, error) {
			return nil, fmt.Errorf("init failed")
		},
	}

	stdin := strings.NewReader(`{"method":"activate","params":{"pluginId":"x"}}` + "\n")
	var stdout bytes.Buffer
	run(h, stdin, &stdout)

	var resp Response
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "init failed" {
		t.Errorf("expected error 'init failed', got %q", resp.Error)
	}
}

func TestRun_DeactivateCalled(t *testing.T) {
	var gotDeactivate bool
	h := &Handler{
		Activate: func(params *ActivateParams) (*ActivateResult, error) {
			return &ActivateResult{}, nil
		},
		Deactivate: func() {
			gotDeactivate = true
		},
	}

	stdin := strings.NewReader(
		`{"method":"activate","params":{"pluginId":"x"}}` + "\n" +
			`{"method":"deactivate"}` + "\n",
	)
	var stdout bytes.Buffer
	run(h, stdin, &stdout)

	if !gotDeactivate {
		t.Error("Deactivate was not called")
	}
}

func TestRun_HookDefaultAllow(t *testing.T) {
	h := &Handler{
		Hook: nil, // no hook handler — should default to allow
	}

	stdin := strings.NewReader(`{"method":"hook","params":{"event":"PostToolUse","toolName":"Shell"}}` + "\n")
	var stdout bytes.Buffer
	run(h, stdin, &stdout)

	var resp Response
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HookResult == nil || resp.HookResult.Decision != "allow" {
		t.Errorf("expected default allow, got %v", resp.HookResult)
	}
}

func TestRun_UnknownMethod(t *testing.T) {
	h := &Handler{}

	stdin := strings.NewReader(`{"method":"bogus"}` + "\n")
	var stdout bytes.Buffer
	run(h, stdin, &stdout)

	var resp Response
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.Error, "unknown method") {
		t.Errorf("expected 'unknown method' error, got %q", resp.Error)
	}
}

func TestRun_InvalidJSON(t *testing.T) {
	h := &Handler{}

	stdin := strings.NewReader(`{invalid json}` + "\n")
	var stdout bytes.Buffer
	run(h, stdin, &stdout)

	var resp Response
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.Error, "invalid JSON") {
		t.Errorf("expected 'invalid JSON' error, got %q", resp.Error)
	}
}

func TestHookResultDeny(t *testing.T) {
	hr := HookResultDeny("blocked by policy")
	if hr.Decision != "deny" {
		t.Errorf("expected deny, got %q", hr.Decision)
	}
	if hr.Message != "blocked by policy" {
		t.Errorf("expected message 'blocked by policy', got %q", hr.Message)
	}
}

func TestRun_Enrich(t *testing.T) {
	h := &Handler{
		Enrich: func(params *EnrichParams) (*EnrichResult, error) {
			if params.EnricherName != "status" {
				t.Errorf("expected enricherName=status, got %q", params.EnricherName)
			}
			return &EnrichResult{Result: `{"uptime": "2h"}`}, nil
		},
	}

	stdin := strings.NewReader(`{"method":"enrich","params":{"enricherName":"status"}}` + "\n")
	var stdout bytes.Buffer
	run(h, stdin, &stdout)

	var resp Response
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result != `{"uptime": "2h"}` {
		t.Errorf("unexpected result: %q", resp.Result)
	}
}
