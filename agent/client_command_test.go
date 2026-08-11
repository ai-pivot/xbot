package agent

import (
	"encoding/json"
	"fmt"
	"testing"
)

type legacyCommandTransport struct {
	methods []string
}

func (t *legacyCommandTransport) Call(method string, _ json.RawMessage) (json.RawMessage, error) {
	t.methods = append(t.methods, method)
	switch method {
	case MethodListCommands:
		return nil, fmt.Errorf("unknown RPC method: %s", method)
	case MethodListCommandNames:
		return json.Marshal([]string{"/new", "/set-model"})
	default:
		return nil, fmt.Errorf("unexpected RPC method: %s", method)
	}
}

func (t *legacyCommandTransport) Close() error { return nil }

type modernCommandTransport struct {
	methods []string
}

func (t *modernCommandTransport) Call(method string, _ json.RawMessage) (json.RawMessage, error) {
	t.methods = append(t.methods, method)
	if method != MethodListCommands {
		return nil, fmt.Errorf("unexpected RPC method: %s", method)
	}
	return json.Marshal([]CommandInfo{{Name: "/deploy", Usage: "/deploy <env>", Description: "deploy"}})
}

func (t *modernCommandTransport) Close() error { return nil }

func TestListCommandsUsesModernMetadataWithoutLegacyCall(t *testing.T) {
	transport := &modernCommandTransport{}
	client := NewClient(transport, nil)
	defer client.Close()

	commands, err := client.ListCommands()
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Usage != "/deploy <env>" || commands[0].Description != "deploy" {
		t.Fatalf("commands = %#v, want full modern metadata", commands)
	}
	if len(transport.methods) != 1 || transport.methods[0] != MethodListCommands {
		t.Fatalf("RPC methods = %v, want only %s", transport.methods, MethodListCommands)
	}
}

func TestListCommandsFallsBackToLegacyNames(t *testing.T) {
	transport := &legacyCommandTransport{}
	client := NewClient(transport, nil)
	defer client.Close()

	commands, err := client.ListCommands()
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0].Name != "/new" || commands[0].Usage != "/new" || commands[1].Name != "/set-model" {
		t.Fatalf("commands = %#v, want legacy names as minimal metadata", commands)
	}
	if len(transport.methods) != 2 || transport.methods[0] != MethodListCommands || transport.methods[1] != MethodListCommandNames {
		t.Fatalf("RPC methods = %v, want [%s %s]", transport.methods, MethodListCommands, MethodListCommandNames)
	}
}
