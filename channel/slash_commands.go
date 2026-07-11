package channel

// TUISlashCommands is the local TUI slash-command completion source. Agent-level
// commands are provided by CommandRegistry metadata and merged by consumers.
var TUISlashCommands = []string{
	"/cancel", "/channel", "/chat", "/clear", "/commands", "/copy", "/exit",
	"/help", "/list-sessions", "/palette", "/plugin", "/quit", "/rename", "/rewind",
	"/search", "/sessions", "/settings", "/setup", "/ss", "/su", "/tasks", "/update", "/user",
}
