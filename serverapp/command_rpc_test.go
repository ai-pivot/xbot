package serverapp

import (
	"encoding/json"
	"testing"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
)

func TestListCommandsRPCIncludesUsageAndDescription(t *testing.T) {
	ag := newTestAgentForExport(t)
	table := BuildRPCTable(config.Load(), ag, &channel.Dispatcher{}, bus.NewMessageBus(), nil)

	raw := callRPC(t, table, agent.MethodListCommands, nil)
	var commands []struct {
		Name        string `json:"name"`
		Usage       string `json:"usage"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &commands); err != nil {
		t.Fatal(err)
	}

	for _, command := range commands {
		if command.Name == "/model" {
			t.Fatal("list_commands advertises removed /model command")
		}
		if command.Name != "/set-model" {
			continue
		}
		if command.Usage == "" || command.Description == "" {
			t.Fatalf("/set-model metadata incomplete: %+v", command)
		}
		return
	}
	t.Fatal("list_commands response does not contain /set-model")
}

func TestLegacyListCommandNamesRPCRemainsAvailable(t *testing.T) {
	ag := newTestAgentForExport(t)
	table := BuildRPCTable(config.Load(), ag, &channel.Dispatcher{}, bus.NewMessageBus(), nil)

	metadataRaw := callRPC(t, table, agent.MethodListCommands, nil)
	var metadata []agent.CommandInfo
	if err := json.Unmarshal(metadataRaw, &metadata); err != nil {
		t.Fatal(err)
	}

	raw := callRPC(t, table, agent.MethodListCommandNames, nil)
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		t.Fatal(err)
	}
	if len(names) != len(metadata) {
		t.Fatalf("legacy names count = %d, metadata count = %d", len(names), len(metadata))
	}
	for i, name := range names {
		if name != metadata[i].Name {
			t.Fatalf("legacy name %d = %q, metadata name = %q", i, name, metadata[i].Name)
		}
	}
}
