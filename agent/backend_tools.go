package agent

import (
	"xbot/event"
	"xbot/tools"
)

// ToolManagement groups methods for tool registration and sandbox management.
type ToolManagement interface {
	RegisterCoreTool(tool tools.Tool)
	RegisterTool(tool tools.Tool)
	IndexGlobalTools()
	SetSandbox(sb tools.Sandbox, mode string)
	GetCardBuilder() *tools.CardBuilder
	SetEventRouter(router *event.Router)
	RegistryManager() *RegistryManager
}
