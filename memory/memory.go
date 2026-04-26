package memory

import (
	"context"

	"xbot/llm"
)

// MemoryProvider is the core interface for pluggable memory systems.
// All memory implementations (flat/tiered/agentic) must satisfy this interface.
type MemoryProvider interface {
	// Recall retrieves relevant memories for the current conversation, returning text to inject into the system prompt.
	// query is the user's current message, used for on-demand retrieval (flat implementations ignore this).
	Recall(ctx context.Context, query string) (string, error)

	// Memorize processes memories after conversation ends (compression, storage, evolution, etc.).
	Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error)

	// Close releases resources.
	Close() error
}

// ToolIndexer provides tool semantic search capability
type ToolIndexer interface {
	// IndexTools indexes tools into vector storage (called at startup)
	IndexTools(ctx context.Context, tools []ToolIndexEntry) error

	// SearchTools performs semantic tool search
	SearchTools(ctx context.Context, query string, topK int) ([]ToolIndexEntry, error)
}

// ToolIndexEntry is a single tool index entry
type ToolIndexEntry struct {
	Name        string   // Tool name (e.g. mcp_server_tool)
	ServerName  string   // MCP server name (e.g. feishu, global)
	Source      string   // Source: "global" or "personal"
	Description string   // Tool description
	Channels    []string // Supported channels (empty = all channels)
}

// MemorizeInput holds the input parameters for memory writing.
type MemorizeInput struct {
	Messages         []llm.ChatMessage // Conversation messages to process
	LastConsolidated int               // Last consolidation offset
	LLMClient        llm.LLM           // LLM client for compression/analysis
	Model            string            // Model name
	ArchiveAll       bool              // true = archive all messages (/new command)
}

// MemorizeResult holds the result of memory writing.
type MemorizeResult struct {
	NewLastConsolidated int  // New consolidation offset
	OK                  bool // Whether the operation succeeded
}
