package channel

// TUISlashCommands is the CLI slash-command completion source. Web uses the
// same list for command completion, then handles only the commands that make
// sense in the Web UI locally.
var TUISlashCommands = []string{
	"/cancel", "/channel", "/chat", "/clear", "/commands", "/compress", "/context", "/copy", "/exit",
	"/help", "/list-sessions", "/llm", "/models", "/new", "/palette", "/plugin", "/quit", "/rename", "/rewind",
	"/search", "/sessions", "/set-llm", "/set-model", "/settings", "/setup", "/ss", "/su", "/tasks", "/unset-llm", "/update",
	"/usage", "/user",
}
