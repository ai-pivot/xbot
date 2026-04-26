package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// ChatHistoryStore stores recent message history for each group/session
type ChatHistoryStore struct {
	mu      sync.RWMutex
	history map[string]*ChatHistory // key: channel:chatID
	maxSize int                     // 每个群组保留的最大消息数
}

// ChatHistory is the message history for a single group
type ChatHistory struct {
	messages   []ChatMessage
	maxSize    int
	lastUpdate time.Time
}

// ChatMessage is a single message record
type ChatMessage struct {
	Content   string    `json:"content"`
	SenderID  string    `json:"sender_id"`
	Timestamp time.Time `json:"timestamp"`
}

// NewChatHistoryStore creates a new chat history store
// maxSize max messages per group (default 200 when <=0)
func NewChatHistoryStore(maxSize int) *ChatHistoryStore {
	if maxSize <= 0 {
		maxSize = 200 // 默认保留最近 200 条，防止长期运行 OOM
	}
	return &ChatHistoryStore{
		history: make(map[string]*ChatHistory),
		maxSize: maxSize,
	}
}

// defaultMaxChannels global max channel count, preventing unbounded history map growth
const defaultMaxChannels = 10000

// Add adds a message to the history
func (s *ChatHistoryStore) Add(channel, chatID, senderID, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(channel, chatID)
	hist, exists := s.history[key]
	if !exists {
		// Prevent unbounded map growth: clean up the oldest channel when the limit is exceeded
		if len(s.history) >= defaultMaxChannels {
			s.evictOldestLocked()
		}
		hist = &ChatHistory{
			messages:   make([]ChatMessage, 0, s.maxSize),
			maxSize:    s.maxSize,
			lastUpdate: time.Now(),
		}
		s.history[key] = hist
	}

	// Add new message
	msg := ChatMessage{
		Content:   content,
		SenderID:  senderID,
		Timestamp: time.Now(),
	}
	hist.messages = append(hist.messages, msg)
	hist.lastUpdate = time.Now()

	// Limit size
	if len(hist.messages) > hist.maxSize {
		// keep the latest maxSize messages
		hist.messages = hist.messages[len(hist.messages)-hist.maxSize:]
	}
}

// evictOldestLocked evicts the oldest channel (caller must hold write lock)
func (s *ChatHistoryStore) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	for k, h := range s.history {
		if oldestKey == "" || h.lastUpdate.Before(oldestTime) {
			oldestKey = k
			oldestTime = h.lastUpdate
		}
	}
	if oldestKey != "" {
		delete(s.history, oldestKey)
	}
}

// Get returns recent message history for the specified group
func (s *ChatHistoryStore) Get(channel, chatID string, limit int) []ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := s.makeKey(channel, chatID)
	hist, exists := s.history[key]
	if !exists {
		return nil
	}

	messages := hist.messages
	if limit > 0 && limit < len(messages) {
		// Return the most recent limit messages
		messages = messages[len(messages)-limit:]
	}

	// Return a copy to prevent external modification
	result := make([]ChatMessage, len(messages))
	copy(result, messages)
	return result
}

// makeKey generate storage key
func (s *ChatHistoryStore) makeKey(channel, chatID string) string {
	return fmt.Sprintf("%s:%s", channel, chatID)
}

// ---- ChatHistoryTool: allows the LLM to query chat history ----

// ChatHistoryTool chat history query tool
type ChatHistoryTool struct {
	store *ChatHistoryStore
}

// NewChatHistoryTool creates a new chat history tool
func NewChatHistoryTool(store *ChatHistoryStore) *ChatHistoryTool {
	return &ChatHistoryTool{store: store}
}

func (t *ChatHistoryTool) Name() string {
	return "ChatHistory"
}

func (t *ChatHistoryTool) Description() string {
	return `Query recent chat message history in the current group/conversation.
IMPORTANT: Only use this tool when you need to understand recent context or conversation flow that is not in your immediate memory.
Parameters (JSON):
  - limit: integer, optional, number of recent messages to retrieve (defaults to 10, max 50)
Example: {"limit": 10}`
}

func (t *ChatHistoryTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "limit", Type: "integer", Description: "Number of recent messages to retrieve (defaults to 10, max 50)", Required: false},
	}
}

type chatHistoryParams struct {
	Limit int `json:"limit"`
}

func (t *ChatHistoryTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	log.WithField("ctx", fmt.Sprintf("%+v", ctx)).Debug("ChatHistory tool called")

	var params chatHistoryParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	// get current channel and chatID from ToolContext
	channel := ctx.Channel
	chatID := ctx.ChatID

	log.WithFields(log.Fields{
		"channel": channel,
		"chat_id": chatID,
		"limit":   limit,
	}).Debug("ChatHistory retrieving messages")

	if channel == "" || chatID == "" {
		return NewResult("No active conversation context."), nil
	}

	messages := t.store.Get(channel, chatID, limit)
	if len(messages) == 0 {
		return NewResult("No recent message history found."), nil
	}

	// Format output
	var sb strings.Builder
	fmt.Fprintf(&sb, "Recent %d messages in this conversation:\n\n", len(messages))

	for i, msg := range messages {
		// Format time
		timeStr := msg.Timestamp.Format("15:04")
		if time.Since(msg.Timestamp) > 24*time.Hour {
			timeStr = msg.Timestamp.Format("01/02 15:04")
		}

		fmt.Fprintf(&sb, "[%d] %s <%s>: %s\n", i+1, timeStr, msg.SenderID, msg.Content)
	}

	return NewResult(sb.String()), nil
}
