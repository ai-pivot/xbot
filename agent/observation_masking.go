package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// MaskedObservation stores the complete information of a masked tool result.
type MaskedObservation struct {
	ID         string    `json:"id"`
	ToolName   string    `json:"tool_name"`
	Arguments  string    `json:"arguments"`
	Content    string    `json:"content"` // Complete original tool result
	MaskedAt   time.Time `json:"masked_at"`
	MessageIdx int       `json:"message_idx"` // Original position in messages slice
}

const (
	defaultMaxEntries = 200       // Default max entries
	defaultMaxChars   = 2_000_000 // Default max stored characters (~2MB)

	maskedEntryPrefix  = "📂 [masked:"  // Prefix identifying masked tool result entries
	offloadEntryPrefix = "📂 [offload:" // Prefix identifying offloaded tool result entries
)

// ObservationMaskStore manages observation masking storage and recall.
// Zero-cost compression strategy: mask old tool results, don't send to LLM, but fully retain for tool-based recall.
// Dual capacity limit: maxSize (entry count) + maxChars (total characters), evicts oldest entries when either is exceeded.
//
// Disk persistence: each mask entry stored as {storeDir}/{id}.json, recoverable after restart.
// Recall checks memory first, reads disk on miss. CleanOldEntries synchronously deletes disk files.
type ObservationMaskStore struct {
	mu         sync.RWMutex
	entries    []MaskedObservation // Stored in mask order
	maxSize    int                 // Max stored entries
	maxChars   int                 // Max total stored characters
	totalChars int                 // Current total characters
	baseDir    string              // Disk storage base directory (baseDir/{tenantID})
	storeDir   string              // Current tenant's disk storage directory (empty = memory-only mode)
	tenantID   int64               // Current tenant ID
	loaded     bool                // Whether loaded from disk
}

// NewObservationMaskStore creates ObservationMaskStore.
// Enables disk persistence when storeDir is non-empty.
func NewObservationMaskStore(maxSize int, storeDir ...string) *ObservationMaskStore {
	if maxSize <= 0 {
		maxSize = defaultMaxEntries
	}
	s := &ObservationMaskStore{
		maxSize:  maxSize,
		maxChars: defaultMaxChars,
	}
	if len(storeDir) > 0 && storeDir[0] != "" {
		s.storeDir = storeDir[0]
	}
	return s
}

// SetStoreDir sets a fixed disk storage directory (backward compatible with old calls/tests).
func (s *ObservationMaskStore) SetStoreDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseDir = ""
	s.storeDir = dir
	s.tenantID = 0
	s.entries = nil
	s.totalChars = 0
	s.loaded = false
}

// SetBaseDir sets the tenant-sharded base directory: baseDir/{tenantID}/.
func (s *ObservationMaskStore) SetBaseDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseDir = dir
	if s.tenantID != 0 {
		s.storeDir = filepath.Join(dir, strconv.FormatInt(int64(s.tenantID), 10))
	} else {
		s.storeDir = ""
	}
	s.entries = nil
	s.totalChars = 0
	s.loaded = false
}

// SetTenantID switches the current tenant directory to {baseDir}/{tenantID}/.
func (s *ObservationMaskStore) SetTenantID(tenantID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseDir == "" {
		s.tenantID = tenantID
		return
	}
	if s.tenantID == tenantID && s.loaded {
		return
	}
	s.tenantID = tenantID
	if tenantID == 0 {
		s.storeDir = ""
	} else {
		s.storeDir = filepath.Join(s.baseDir, strconv.FormatInt(int64(tenantID), 10))
	}
	s.entries = nil
	s.totalChars = 0
	s.loaded = false
}

// ensureLoaded loads all mask entries from disk directory on first access.
func (s *ObservationMaskStore) ensureLoaded() {
	if s.loaded || s.storeDir == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true

	dir := s.storeDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).Warn("ObservationMaskStore: failed to list store directory")
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			log.WithError(err).WithField("file", entry.Name()).Warn("ObservationMaskStore: failed to read entry file")
			continue
		}
		var obs MaskedObservation
		if err := json.Unmarshal(data, &obs); err != nil {
			log.WithError(err).WithField("file", entry.Name()).Warn("ObservationMaskStore: failed to unmarshal entry")
			continue
		}
		s.entries = append(s.entries, obs)
		s.totalChars += len([]rune(obs.Content))
	}

	// Sort by time (ensure correct eviction order)
	sort.Slice(s.entries, func(i, j int) bool {
		return s.entries[i].MaskedAt.Before(s.entries[j].MaskedAt)
	})

	if len(s.entries) > 0 {
		log.WithField("count", len(s.entries)).Info("ObservationMaskStore: loaded entries from disk")
	}
}

// persistEntry writes a single entry to disk.
func (s *ObservationMaskStore) persistEntry(entry MaskedObservation) {
	if s.storeDir == "" {
		return
	}
	if err := os.MkdirAll(s.storeDir, 0o755); err != nil {
		log.WithError(err).Warn("ObservationMaskStore: failed to create store directory")
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		log.WithError(err).Warn("ObservationMaskStore: failed to marshal entry")
		return
	}
	fp := filepath.Join(s.storeDir, entry.ID+".json")
	if err := os.WriteFile(fp, data, 0o644); err != nil {
		log.WithError(err).Warn("ObservationMaskStore: failed to persist entry")
	}
}

// deleteEntryFile deletes the entry file on disk.
func (s *ObservationMaskStore) deleteEntryFile(id string) {
	if s.storeDir == "" {
		return
	}
	fp := filepath.Join(s.storeDir, id+".json")
	os.Remove(fp)
}

// generateMaskID generates mask ID: "mk_" + 8 random hex chars.
func generateMaskID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails (should never happen)
		log.WithError(err).Warn("crypto/rand.Read failed in generateMaskID, using fallback")
		now := time.Now().UnixNano()
		return fmt.Sprintf("mk_%08x", now&0xffffffff)
	}
	return "mk_" + hex.EncodeToString(b)
}

// Mask masks a tool result, stores complete content and returns placeholder text.
// Placeholder format: 📂 [masked:mk_xxxx] ToolName(args_preview) — N chars — Result masked, use recall_masked to view full content
func (s *ObservationMaskStore) Mask(toolName, arguments, content string, messageIdx int) (MaskedObservation, string) {
	s.ensureLoaded()
	id := generateMaskID()

	entry := MaskedObservation{
		ID:         id,
		ToolName:   toolName,
		Arguments:  arguments,
		Content:    content,
		MaskedAt:   time.Now(),
		MessageIdx: messageIdx,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Dual capacity limit: evict oldest entries when entry count or character count is exceeded
	contentLen := len([]rune(content))
	evictedCount := 0
	for len(s.entries) >= s.maxSize || (s.totalChars+contentLen > s.maxChars && len(s.entries) > 0) {
		evicted := s.entries[0]
		s.totalChars -= len([]rune(evicted.Content))
		s.entries = s.entries[1:]
		evictedCount++
		// Async delete disk files (no need to wait, eviction is a low-frequency operation)
		go s.deleteEntryFile(evicted.ID)
	}
	// Reallocate slice, release underlying array memory of evicted entries
	if evictedCount > 0 {
		newEntries := make([]MaskedObservation, len(s.entries))
		copy(newEntries, s.entries)
		s.entries = newEntries
	}
	s.entries = append(s.entries, entry)
	s.totalChars += contentLen

	// Persist to disk
	s.persistEntry(entry)

	// Generate placeholder
	argsPreview := arguments
	if len([]rune(argsPreview)) > 80 {
		argsPreview = string([]rune(argsPreview)[:80]) + "..."
	}
	charCount := len([]rune(content))
	placeholder := fmt.Sprintf("📂 [masked:%s] %s(%s) — %d chars — 结果已遮蔽，使用 recall_masked 可查看完整内容", id, toolName, argsPreview, charCount)

	return entry, placeholder
}

// Recall recalls a masked complete tool result by ID.
// Memory lookup first, load from disk on miss.
func (s *ObservationMaskStore) Recall(id string) (MaskedObservation, error) {
	s.ensureLoaded()

	s.mu.RLock()
	for _, e := range s.entries {
		if e.ID == id {
			s.mu.RUnlock()
			return e, nil
		}
	}
	s.mu.RUnlock()

	// Not found in memory, try reading from disk
	if s.storeDir != "" {
		fp := filepath.Join(s.storeDir, id+".json")
		data, err := os.ReadFile(fp)
		if err == nil {
			var obs MaskedObservation
			if jsonErr := json.Unmarshal(data, &obs); jsonErr == nil {
				// Restore to memory
				s.mu.Lock()
				s.entries = append(s.entries, obs)
				s.totalChars += len([]rune(obs.Content))
				s.mu.Unlock()
				return obs, nil
			}
		}
	}

	return MaskedObservation{}, fmt.Errorf("masked observation %s not found", id)
}

// List lists all masked observations (in reverse mask time order).
func (s *ObservationMaskStore) List() []MaskedObservation {
	s.ensureLoaded()

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]MaskedObservation, len(s.entries))
	copy(result, s.entries)
	return result
}

// Size returns the current number of stored observations.
func (s *ObservationMaskStore) Size() int {
	s.ensureLoaded()

	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Clear clears all masked observations (memory + disk).
func (s *ObservationMaskStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.totalChars = 0
	// Delete disk files
	if s.storeDir != "" {
		os.RemoveAll(s.storeDir)
		os.MkdirAll(s.storeDir, 0o755)
	}
}

// CleanOldEntries deletes records with MaskedAt before cutoff.
// For post-compression cleanup: masked observations before the compression point have been replaced by summaries, no longer need recall.
func (s *ObservationMaskStore) CleanOldEntries(cutoff time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []MaskedObservation
	removedCount := 0
	for _, e := range s.entries {
		if e.MaskedAt.Before(cutoff) {
			s.totalChars -= len([]rune(e.Content))
			removedCount++
			// Delete disk files
			go s.deleteEntryFile(e.ID)
		} else {
			kept = append(kept, e)
		}
	}
	s.entries = kept
	if removedCount > 0 {
		log.WithFields(log.Fields{
			"removed": removedCount,
			"kept":    len(kept),
			"cutoff":  cutoff.Format(time.RFC3339),
		}).Info("ObservationMaskStore: cleaned old entries after compression")
	}
	return removedCount
}

// CleanStale cleans up stale mask data older than specified days (disk files).
// For periodic cleanup.
func (s *ObservationMaskStore) CleanStale(maxAgeDays int) {
	if maxAgeDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)

	s.mu.RLock()
	baseDir := s.baseDir
	storeDir := s.storeDir
	s.mu.RUnlock()

	var dirs []string
	if baseDir != "" {
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			log.WithError(err).Warn("ObservationMaskStore: failed to list base directory for stale cleanup")
			return
		}
		for _, entry := range entries {
			if entry.IsDir() {
				dirs = append(dirs, filepath.Join(baseDir, entry.Name()))
			}
		}
	} else if storeDir != "" {
		dirs = append(dirs, storeDir)
	} else {
		return
	}

	removed := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.WithError(err).WithField("dir", dir).Warn("ObservationMaskStore: failed to list store directory for stale cleanup")
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(dir, entry.Name()))
				removed++
			}
		}
	}
	if removed > 0 {
		log.WithField("removed", removed).Info("ObservationMaskStore: cleaned stale entries from disk")
	}
}

// --- tools.MaskedRecallStore interface implementation ---
// These methods make ObservationMaskStore satisfy the tools package's MaskedRecallStore interface.
// No need to import tools package (Go duck typing), just need matching method signatures.

// RecallMasked recalls masked content by ID.
func (s *ObservationMaskStore) RecallMasked(id string) (string, string, error) {
	obs, err := s.Recall(id)
	if err != nil {
		return "", "", err
	}
	argsPreview := obs.Arguments
	if len([]rune(argsPreview)) > 80 {
		argsPreview = string([]rune(argsPreview)[:80]) + "..."
	}
	return fmt.Sprintf("%s(%s)", obs.ToolName, argsPreview), obs.Content, nil
}

// ListMasked lists all masked observations (summary info).
func (s *ObservationMaskStore) ListMasked() []map[string]interface{} {
	entries := s.List()
	result := make([]map[string]interface{}, len(entries))
	for i, e := range entries {
		argsPreview := e.Arguments
		if len([]rune(argsPreview)) > 60 {
			argsPreview = string([]rune(argsPreview)[:60]) + "..."
		}
		result[i] = map[string]interface{}{
			"id":           e.ID,
			"tool_name":    e.ToolName,
			"args_preview": argsPreview,
			"char_count":   len([]rune(e.Content)),
		}
	}
	return result
}

// calculateKeepGroups dynamically calculates the number of tool groups to keep based on token usage.
// More context available → keep more; reduce only when context is tight.
func calculateKeepGroups(totalTokens, maxTokens int) int {
	ratio := float64(totalTokens) / float64(maxTokens)
	switch {
	case ratio <= 0.70:
		return 12
	case ratio <= 0.80:
		return 8
	case ratio <= 0.90:
		return 5
	default:
		return 3
	}
}

// MaskedEntry records the position and new content of a masked message, for persisting back to Session.
type MaskedEntry struct {
	MessageIndex int    // Position in messages slice
	Content      string // Replaced content (placeholder or empty string)
}

// MaskOldToolResults masks older tool results in messages, returns modified messages slice.
//
// Strategy:
//   - Keep the most recent keepGroups complete tool groups
//   - Tool groups related to active files are not masked (even if exceeding keepGroups)
//   - Short content (<300 chars) is not masked
//   - Consecutive pure tool groups (assistant has no thinking text) are folded into a pair of messages
//   - Sort masking by token benefit (longest content first)
//   - Assistant message thinking content is preserved (don't strip think blocks)
//
// Returns: modified messages (new slice), actual masked count, modified message entries (for persistence).
func MaskOldToolResults(messages []llm.ChatMessage, store *ObservationMaskStore, keepGroups int) ([]llm.ChatMessage, int, []MaskedEntry) {
	if keepGroups <= 0 {
		keepGroups = 3
	}

	type toolGroup struct{ start, end int }

	var groups []toolGroup
	for i := range messages {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			g := toolGroup{start: i, end: i}
			for j := i + 1; j < len(messages) && messages[j].Role == "tool"; j++ {
				g.end = j
			}
			groups = append(groups, g)
		}
	}

	maskCount := len(groups) - keepGroups
	if maskCount <= 0 {
		return messages, 0, nil
	}

	// Extract active files (file paths involved in the last 3 rounds of tool calls)
	activeFiles := ExtractActiveFiles(messages, 3)
	activePaths := make(map[string]bool)
	for _, af := range activeFiles {
		activePaths[af.Path] = true
	}

	// Collect maskable candidate groups, excluding active file groups
	type maskCandidate struct {
		groupIdx int
		grp      toolGroup
		chars    int // Total character count of all tool results in group
	}
	var candidates []maskCandidate

	for g := range maskCount {
		grp := groups[g]

		// Check if involves active files
		if isGroupActiveFile(messages, grp, activePaths) {
			continue
		}

		// Calculate total maskable tool result characters in this group
		chars := 0
		allShort := true
		for j := grp.start; j <= grp.end; j++ {
			if messages[j].Role == "tool" {
				content := messages[j].Content
				// Skip already masked
				if content == "" || content == "null" || strings.HasPrefix(content, maskedEntryPrefix) {
					continue
				}
				runeLen := len([]rune(content))
				if runeLen >= 300 {
					allShort = false
					chars += runeLen
				}
			}
		}
		// All tool results too short, don't mask
		if allShort {
			continue
		}

		candidates = append(candidates, maskCandidate{groupIdx: g, grp: grp, chars: chars})
	}

	if len(candidates) == 0 {
		return messages, 0, nil
	}

	// Sort by token benefit: most characters masked first
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].chars > candidates[j].chars
	})

	result := make([]llm.ChatMessage, len(messages))
	copy(result, messages)

	maskedTotal := 0
	var maskedEntries []MaskedEntry

	for _, cand := range candidates {
		grp := cand.grp

		// Check if this group is a "pure tool group" (assistant has no thinking text, only tool_calls)
		assistantMsg := messages[grp.start]
		isPureToolGroup := strings.TrimSpace(llm.StripThinkBlocks(assistantMsg.Content)) == ""

		if isPureToolGroup {
			// Consecutive pure tool group folding: collect all tool results in the group, fold into a pair of messages
			n, entries := foldPureToolGroup(result, grp, store)
			maskedTotal += n
			maskedEntries = append(maskedEntries, entries...)
		} else {
			// Assistant group with thinking content: mask tool results independently, preserve complete assistant content
			for j := grp.start; j <= grp.end; j++ {
				msg := result[j]
				if msg.Role == "tool" {
					content := msg.Content
					if content != "" && content != "null" && !strings.HasPrefix(content, maskedEntryPrefix) {
						runeLen := len([]rune(content))
						if runeLen < 300 {
							continue // Short content not masked
						}
						_, placeholder := store.Mask(msg.ToolName, msg.ToolArguments, msg.Content, j)
						msg.Content = placeholder
						maskedTotal++
						maskedEntries = append(maskedEntries, MaskedEntry{MessageIndex: j, Content: placeholder})
					}
				}
				// Assistant message: preserve complete content (don't strip think blocks)
				result[j] = msg
			}
		}
	}

	log.WithFields(map[string]interface{}{
		"masked_count":  maskedTotal,
		"kept_groups":   keepGroups,
		"total_groups":  len(groups),
		"candidates":    len(candidates),
		"active_groups": maskCount - len(candidates),
	}).Info("Observation masking: masked old tool results")

	return result, maskedTotal, maskedEntries
}

// isGroupActiveFile checks if a tool group involves active files.
func isGroupActiveFile(messages []llm.ChatMessage, grp struct{ start, end int }, activePaths map[string]bool) bool {
	for j := grp.start; j <= grp.end; j++ {
		msg := messages[j]
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				paths := extractPathsFromToolArgs(tc.Name, tc.Arguments)
				for _, p := range paths {
					if activePaths[p] {
						return true
					}
				}
			}
		}
	}
	return false
}

// foldPureToolGroup folds a pure tool group into a pair of assistant+tool messages.
// All tool results stored in MaskStore, assistant and first tool replaced with fold summary.
// Returns actual masked tool result count and modified message entries.
func foldPureToolGroup(result []llm.ChatMessage, grp struct{ start, end int }, store *ObservationMaskStore) (int, []MaskedEntry) {
	// Collect all tool call names and arguments
	var callSummaries []string
	maskedCount := 0
	var batchIDs []string
	var entries []MaskedEntry

	for j := grp.start; j <= grp.end; j++ {
		msg := result[j]
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				argsPreview := tc.Arguments
				if len([]rune(argsPreview)) > 60 {
					argsPreview = string([]rune(argsPreview)[:60]) + "..."
				}
				callSummaries = append(callSummaries, fmt.Sprintf("%s(%s)", tc.Name, argsPreview))
			}
		} else if msg.Role == "tool" {
			content := msg.Content
			if content == "" || content == "null" || strings.HasPrefix(content, maskedEntryPrefix) {
				continue
			}
			// Short content not masked
			if len([]rune(content)) < 300 {
				continue
			}
			entry, _ := store.Mask(msg.ToolName, msg.ToolArguments, msg.Content, j)
			batchIDs = append(batchIDs, entry.ID)
			maskedCount++
		}
	}

	if maskedCount == 0 {
		return 0, nil
	}

	// Fold assistant: preserve ToolCalls to maintain tool_use/tool_result pairing, only replace Content
	summary := fmt.Sprintf("📂 [batch: %d tool calls folded] %s", maskedCount, strings.Join(callSummaries, ", "))
	assistantMsg := result[grp.start]
	assistantMsg.Content = summary
	result[grp.start] = assistantMsg
	entries = append(entries, MaskedEntry{MessageIndex: grp.start, Content: summary})

	// Fold tool results: replace Content with placeholder, preserve ToolCallID to maintain pairing
	batchPlaceholder := fmt.Sprintf("📂 [batch-masked: %d results] IDs: %s — recall_masked <id> to view", maskedCount, strings.Join(batchIDs, ", "))
	firstTool := true
	for j := grp.start + 1; j <= grp.end; j++ {
		msg := result[j]
		if msg.Role == "tool" {
			content := msg.Content
			if content == "" || content == "null" || strings.HasPrefix(content, maskedEntryPrefix) {
				continue
			}
			if len([]rune(content)) < 300 {
				continue
			}
			if firstTool {
				msg.Content = batchPlaceholder
				result[j] = msg
				entries = append(entries, MaskedEntry{MessageIndex: j, Content: batchPlaceholder})
				firstTool = false
			} else {
				msg.Content = "" // Clear subsequent tool result
				result[j] = msg
				entries = append(entries, MaskedEntry{MessageIndex: j, Content: ""})
			}
		}
	}

	return maskedCount, entries
}
