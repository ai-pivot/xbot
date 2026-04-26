package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	"xbot/tools"

	log "xbot/logger"
)

// sessionDirReplacer cleans dangerous path characters from sessionKey, declared as package-level variable to avoid repeated creation.
var sessionDirReplacer = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "\x00", "_")

// Regex used by extractGoStructure, declared as package-level variable to avoid recompilation on each call.
var (
	goImportRe     = regexp.MustCompile(`"([^"]+)"`)
	goTypeRe       = regexp.MustCompile(`type\s+(\w+)\s+(struct|interface|func)\b`)
	goConstVarRe   = regexp.MustCompile(`^(?:const|var)\s+\(?(\w+)\s*$`)
	goFuncRe       = regexp.MustCompile(`func\s+(?:\([^)]+\)\s+)?(\w+)\s*\(([^)]*)\)`)
	goMethodFuncRe = regexp.MustCompile(`func\s+\([^)]+\)\s+(\w+)\s*\(|func\s+(\w+)\s*\(`)
	pyFuncRe       = regexp.MustCompile(`def\s+(\w+)\s*\(`)
	jsFuncRe       = regexp.MustCompile(`function\s+(\w+)\s*\(`)
)

// OffloadConfig configures offload behavior for large tool results.
type OffloadConfig struct {
	MaxResultTokens int    // Token threshold for triggering offload (default 2000)
	MaxResultBytes  int    // Byte threshold for triggering offload (default 10240)
	StoreDir        string // Offload file storage root directory
	CleanupAgeDays  int    // Expiration cleanup days (default 7)
	Model           string // Model used by tokenizer (default "gpt-4o")
}

// OffloadedResult represents metadata of an offloaded tool result.
type OffloadedResult struct {
	ID          string    `json:"id"`
	ToolName    string    `json:"tool_name"`
	Args        string    `json:"args"`
	FilePath    string    `json:"file_path"`
	TokenSize   int       `json:"token_size"`
	Timestamp   time.Time `json:"timestamp"`
	Summary     string    `json:"summary"`
	ContentHash string    `json:"content_hash"` // SHA256 of content at offload time (Read only)
	ReadPath    string    `json:"read_path"`    // Resolved file path from Read tool args
	Stale       bool      `json:"stale"`        // Whether this offload is stale
}

// offloadIndex is the offload index for a single session.
type offloadIndex struct {
	mu      sync.RWMutex
	entries []OffloadedResult
}

// offloadFile is the disk storage format for complete tool results.
type offloadFile struct {
	ID        string    `json:"id"`
	ToolName  string    `json:"tool_name"`
	Args      string    `json:"args"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// OffloadStore manages offload and recall of large tool results.
type OffloadStore struct {
	config   OffloadConfig
	sessions sync.Map      // map[sessionKey]*offloadIndex
	sandbox  tools.Sandbox // optional sandbox for file hash computation (remote mode)
}

// NewOffloadStore creates an OffloadStore instance, filling zero-value fields with defaults.
func NewOffloadStore(config OffloadConfig) *OffloadStore {
	if config.MaxResultTokens <= 0 {
		config.MaxResultTokens = 2000
	}
	if config.MaxResultBytes <= 0 {
		config.MaxResultBytes = 10240
	}
	if config.StoreDir == "" {
		config.StoreDir = "offload_store"
	}
	if config.CleanupAgeDays <= 0 {
		config.CleanupAgeDays = 7
	}
	if config.Model == "" {
		config.Model = "gpt-4o"
	}
	return &OffloadStore{config: config}
}

// SetSandbox sets the sandbox for file hash computation (used in remote mode
// where os.ReadFile cannot access user's machine files).
func (s *OffloadStore) SetSandbox(sb tools.Sandbox) {
	s.sandbox = sb
}

// generateID generates offload short ID: "ol_" + 8 random hex chars.
func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails (should never happen)
		return fmt.Sprintf("ol_%08x", time.Now().UnixNano()&0xffffffff)
	}
	return "ol_" + hex.EncodeToString(b)
}

// getSessionDir gets the storage directory for a specified session.
func (s *OffloadStore) getSessionDir(sessionKey string) string {
	// Clean path separators from sessionKey to prevent directory traversal
	safe := sessionDirReplacer.Replace(sessionKey)
	return filepath.Join(s.config.StoreDir, safe)
}

// getOrCreateIndex gets or creates the index for a specified session.
func (s *OffloadStore) getOrCreateIndex(sessionKey string) *offloadIndex {
	if v, ok := s.sessions.Load(sessionKey); ok {
		return v.(*offloadIndex)
	}
	idx := &offloadIndex{}
	actual, _ := s.sessions.LoadOrStore(sessionKey, idx)
	return actual.(*offloadIndex)
}

// indexFilePath returns the index file path.
func (s *OffloadStore) indexFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, "index.json")
}

// offloadFilePath returns a single offload result file path.
func (s *OffloadStore) offloadFilePath(sessionDir, id string) string {
	return filepath.Join(sessionDir, id+".json")
}

// estimateTokenSize estimates token count using llm.CountTokens, falls back to len(text)*2/5 on error.
func estimateTokenSize(text string, model string) int {
	n, err := llm.CountTokens(text, model)
	if err != nil {
		return len(text) * 2 / 5
	}
	return n
}

// MaybeOffload checks if tool result exceeds threshold, offloads to disk if exceeded.
// Returns (OffloadedResult, true) meaning offloaded, content should be replaced with result.Summary.
// Returns (zero, false) meaning no offload needed.
// workspaceRoot/sandboxWorkDir for Read tool: after resolving ReadPath to host path
// read original file content to calculate ContentHash, ensuring consistency with InvalidateStaleReads comparison.
// sandbox for reading files across sandbox in remote mode to calculate hash.
func (s *OffloadStore) MaybeOffload(ctx context.Context, sessionKey, toolName, args, result, workspaceRoot, sandboxWorkDir string, userID string) (OffloadedResult, bool) {
	if result == "" {
		return OffloadedResult{}, false
	}

	// Never offload recall-type tools — their results are already retrieved content
	// and offloading them would create infinite recursion (offload → recall → offload → ...)
	switch toolName {
	case toolOffloadRecall, toolRecallMasked:
		return OffloadedResult{}, false
	}

	// Check if exceeds threshold
	tokenSize := estimateTokenSize(result, s.config.Model)
	byteSize := len(result)

	if tokenSize < s.config.MaxResultTokens && byteSize < s.config.MaxResultBytes {
		return OffloadedResult{}, false
	}

	// Execute offload
	id := generateID()
	sessionDir := s.getSessionDir(sessionKey)

	// Create directory
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		log.WithError(err).Warn("OffloadStore: failed to create session directory")
		return OffloadedResult{}, false
	}

	// Write complete result file
	of := offloadFile{
		ID:        id,
		ToolName:  toolName,
		Args:      args,
		Content:   result,
		Timestamp: time.Now(),
	}
	data, err := json.MarshalIndent(of, "", "  ")
	if err != nil {
		log.WithError(err).Warn("OffloadStore: failed to marshal offload file")
		return OffloadedResult{}, false
	}
	if err := os.WriteFile(s.offloadFilePath(sessionDir, id), data, 0o644); err != nil {
		log.WithError(err).Warn("OffloadStore: failed to write offload file")
		return OffloadedResult{}, false
	}

	// Generate summary
	summary := generateRuleSummary(toolName, args, result)
	summaryContent := fmt.Sprintf("📂 [offload:%s] %s(%s)\n%s", id, toolName, truncateOffloadArgs(args), summary)

	// Update memory index
	entry := OffloadedResult{
		ID:        id,
		ToolName:  toolName,
		Args:      args,
		FilePath:  s.offloadFilePath(sessionDir, id),
		TokenSize: tokenSize,
		Timestamp: time.Now(),
		Summary:   summaryContent,
	}

	// For Read tool: resolve path and hash the raw file content.
	// In remote mode, use sandbox to read file; in local mode, resolve to host path.
	// This ensures ContentHash matches what InvalidateStaleReads computes,
	// avoiding false stale when the tool result is truncated by applyLineLimit.
	if toolName == "Read" {
		if readPath := extractJSONStringField(args, "path"); readPath != "" {
			entry.ReadPath = readPath
			if s.sandbox != nil {
				if rawData, err := s.sandbox.ReadFile(ctx, readPath, userID); err == nil {
					entry.ContentHash = fmt.Sprintf("%x", sha256.Sum256(rawData))
				}
			} else {
				hostPath := resolveReadPathToHost(readPath, workspaceRoot, sandboxWorkDir)
				if rawData, err := os.ReadFile(hostPath); err == nil {
					entry.ContentHash = fmt.Sprintf("%x", sha256.Sum256(rawData))
				}
			}
		}
	}

	idx := s.getOrCreateIndex(sessionKey)
	idx.mu.Lock()
	idx.entries = append(idx.entries, entry)
	idx.mu.Unlock()

	// Persist index
	s.persistIndex(sessionDir, idx)

	return entry, true
}

// Recall recalls a complete offloaded tool result by ID.

func (s *OffloadStore) Recall(sessionKey, id string) (string, error) {
	sessionDir := s.getSessionDir(sessionKey)
	fp := s.offloadFilePath(sessionDir, id)
	if _, err := os.Stat(fp); err != nil {
		return "", fmt.Errorf("offload ID %s not found in session %s", id, sessionKey)
	}

	// Read file
	data, err := os.ReadFile(fp)
	if err != nil {
		return "", fmt.Errorf("read offload file: %w", err)
	}

	var of offloadFile
	if err := json.Unmarshal(data, &of); err != nil {
		return "", fmt.Errorf("unmarshal offload file: %w", err)
	}

	return of.Content, nil
}

// CleanSession cleans all offload data for a specified session.
func (s *OffloadStore) CleanSession(sessionKey string) {
	// Delete from memory
	s.sessions.Delete(sessionKey)

	// Delete disk files
	sessionDir := s.getSessionDir(sessionKey)
	if err := os.RemoveAll(sessionDir); err != nil {
		log.WithError(err).WithField("session", sessionKey).Debug("OffloadStore: failed to remove session directory")
	}
}

// CleanOldEntries deletes offload records and corresponding files with timestamp before cutoff in a specified session.
// For post-compression cleanup: offloads before the compression point have been replaced by summaries, no longer need recall.
func (s *OffloadStore) CleanOldEntries(sessionKey string, cutoff time.Time) int {
	idx := s.getOrCreateIndex(sessionKey)
	sessionDir := s.getSessionDir(sessionKey)

	idx.mu.Lock()
	var kept []OffloadedResult
	removedCount := 0
	for _, entry := range idx.entries {
		if entry.Timestamp.Before(cutoff) {
			// Delete disk files
			fp := s.offloadFilePath(sessionDir, entry.ID)
			os.Remove(fp)
			removedCount++
		} else {
			kept = append(kept, entry)
		}
	}
	idx.entries = kept
	idx.mu.Unlock()

	// Persist updated index
	if removedCount > 0 {
		s.persistIndex(sessionDir, idx)
		log.WithFields(log.Fields{
			"session": sessionKey,
			"removed": removedCount,
			"kept":    len(kept),
			"cutoff":  cutoff.Format(time.RFC3339),
		}).Info("OffloadStore: cleaned old entries after compression")
	}
	return removedCount
}

// CleanStale cleans up stale offload data older than CleanupAgeDays.
func (s *OffloadStore) CleanStale() {
	cutoff := time.Now().AddDate(0, 0, -s.config.CleanupAgeDays)

	entries, err := os.ReadDir(s.config.StoreDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).Warn("OffloadStore: failed to list store directory")
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			dir := filepath.Join(s.config.StoreDir, entry.Name())
			if err := os.RemoveAll(dir); err != nil {
				log.WithError(err).WithField("dir", dir).Debug("OffloadStore: failed to remove stale directory")
			} else {
				log.WithField("dir", dir).Info("OffloadStore: cleaned stale session directory")
			}
		}
	}
}

// persistIndex 将 session 索引Persist to disk。
func (s *OffloadStore) persistIndex(sessionDir string, idx *offloadIndex) {
	idx.mu.RLock()
	entries := make([]OffloadedResult, len(idx.entries))
	copy(entries, idx.entries)
	idx.mu.RUnlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.WithError(err).Warn("OffloadStore: failed to marshal index")
		return
	}
	if err := os.WriteFile(s.indexFilePath(sessionDir), data, 0o644); err != nil {
		log.WithError(err).Warn("OffloadStore: failed to persist index")
	}
}

// resolveReadPathToHost converts a ReadPath (from LLM args, either sandbox absolute
// or relative) to a host filesystem path so os.ReadFile can access it.
func resolveReadPathToHost(readPath, workspaceRoot, sandboxWorkDir string) string {
	resolved := readPath
	if sandboxWorkDir != "" && workspaceRoot != "" && strings.HasPrefix(resolved, sandboxWorkDir) {
		resolved = workspaceRoot + resolved[len(sandboxWorkDir):]
	}
	if !filepath.IsAbs(resolved) && workspaceRoot != "" {
		resolved = filepath.Join(workspaceRoot, resolved)
	}
	return resolved
}

// InvalidateStaleReads checks all Read offloads in a session and marks stale ones.
// Returns IDs of newly-staled entries (previously not stale).
// workspaceRoot is the host-side workspace root (e.g. /data/users/ou_xxx/workspace).
// sandboxWorkDir is the sandbox-side workspace root (e.g. /workspace).
// Uses sandbox.ReadFile in remote mode, os.ReadFile in local mode.
func (s *OffloadStore) InvalidateStaleReads(ctx context.Context, sessionKey, workspaceRoot, sandboxWorkDir string, userID string) []string {
	idx := s.getOrCreateIndex(sessionKey)
	idx.mu.Lock()

	var newlyStale []string

	for i := range idx.entries {
		e := &idx.entries[i]
		// Only check Read offloads that are not already stale
		if e.ToolName != "Read" || e.Stale || e.ContentHash == "" || e.ReadPath == "" {
			continue
		}

		var currentData []byte
		var err error

		if s.sandbox != nil {
			// Remote mode: read via sandbox (user's machine)
			currentData, err = s.sandbox.ReadFile(ctx, e.ReadPath, userID)
		} else {
			// Local mode: resolve to host path and read directly
			resolvedPath := resolveReadPathToHost(e.ReadPath, workspaceRoot, sandboxWorkDir)
			currentData, err = os.ReadFile(resolvedPath)
		}
		if err != nil {
			if os.IsNotExist(err) {
				e.Stale = true
				newlyStale = append(newlyStale, e.ID)
			}
			continue
		}

		currentHash := fmt.Sprintf("%x", sha256.Sum256(currentData))
		if currentHash != e.ContentHash {
			e.Stale = true
			newlyStale = append(newlyStale, e.ID)
		}
	}

	// Release lock before persisting (persistIndex acquires RLock internally)
	sessionDir := s.getSessionDir(sessionKey)
	idx.mu.Unlock()

	if len(newlyStale) > 0 {
		s.persistIndex(sessionDir, idx)
	}

	return newlyStale
}

// PurgeStaleMessages removes stale offload references from messages.
// For each stale offload ID, finds the corresponding tool message and replaces
// its content with a stale marker. Returns a new slice (does not modify the original).
func (s *OffloadStore) PurgeStaleMessages(sessionKey string, messages []llm.ChatMessage) []llm.ChatMessage {
	idx := s.getOrCreateIndex(sessionKey)
	idx.mu.RLock()
	staleIDs := make(map[string]bool)
	for _, e := range idx.entries {
		if e.Stale {
			staleIDs[e.ID] = true
		}
	}
	idx.mu.RUnlock()

	if len(staleIDs) == 0 {
		return messages
	}

	result := make([]llm.ChatMessage, len(messages))
	copy(result, messages)

	for i, msg := range result {
		if msg.Role != "tool" {
			continue
		}
		for staleID := range staleIDs {
			marker := fmt.Sprintf("📂 [offload:%s]", staleID)
			if strings.Contains(msg.Content, marker) {
				result[i].Content = fmt.Sprintf("⚠️ [offload:%s] STALE — 该文件已被修改，此内容已过期。请重新 Read 获取最新内容。", staleID)
				break // only replace once per message
			}
		}
	}

	return result
}

// truncateOffloadArgs truncates tool arguments for offload display.
func truncateOffloadArgs(args string) string {
	if len(args) <= 80 {
		return args
	}
	return args[:80] + "..."
}

// generateRuleSummary generates rule summary by tool type (synchronous, no LLM dependency).
func generateRuleSummary(toolName, args, content string) string {
	switch toolName {
	case "Read":
		return summarizeRead(args, content)
	case "Grep":
		return summarizeGrep(content)
	case "Shell":
		return summarizeShell(content)
	case "Glob":
		return summarizeGlob(content)
	default:
		return summarizeDefault(content)
	}
}

// summarizeRead generates summary for Read tool results.
func summarizeRead(args, content string) string {
	// Extract filename
	path := extractJSONStringField(args, "path")
	if path == "" {
		path = "(unknown)"
	}

	lines := strings.Split(content, "\n")
	lineCount := len(lines)

	// Single-line truncation protection: prevent extremely long single lines (e.g. JSON-serialized content) from bloating summary
	// Use []rune for UTF-8 safe truncation
	const maxLineRunes = 500
	const lineTruncSuffix = "...(truncated, %d chars)"
	for i, line := range lines {
		runes := []rune(line)
		if len(runes) > maxLineRunes {
			suffix := fmt.Sprintf(lineTruncSuffix, len(runes))
			lines[i] = string(runes[:maxLineRunes]) + suffix
		}
	}

	// Extract key function names
	funcNames := extractFunctionNames(content)

	var sb strings.Builder
	fmt.Fprintf(&sb, "File: %s, %d lines\n", path, lineCount)

	// First and last 3 lines
	showLines := 3
	if lineCount > showLines*2 {
		fmt.Fprintln(&sb, "--- Head ---")
		for i := 0; i < showLines; i++ {
			fmt.Fprintf(&sb, "%s\n", lines[i])
		}
		fmt.Fprintf(&sb, "  ... (%d lines omitted) ...\n", lineCount-showLines*2)
		fmt.Fprintln(&sb, "--- Tail ---")
		for i := lineCount - showLines; i < lineCount; i++ {
			fmt.Fprintf(&sb, "%s\n", lines[i])
		}
	} else {
		for _, l := range lines {
			fmt.Fprintln(&sb, l)
		}
	}

	if len(funcNames) > 0 {
		sort.Strings(funcNames)
		fmt.Fprintf(&sb, "Key functions: %s\n", strings.Join(funcNames[:min(len(funcNames), 10)], ", "))
	}

	// For Go files, additionally extract struct info to enhance summary
	if goStruct := extractGoStructure(content); goStruct != "" {
		fmt.Fprintln(&sb, "--- Structure ---")
		fmt.Fprintln(&sb, goStruct)
	}

	summary := sb.String()

	// Total size limit protection: prevent summary itself from being too large (e.g. file has many lines each near truncation length)
	// Use []rune for UTF-8 safe truncation
	const maxSummaryRunes = 3000
	summaryRunes := []rune(summary)
	if len(summaryRunes) > maxSummaryRunes {
		summary = string(summaryRunes[:maxSummaryRunes]) + "\n...(summary truncated)"
	}

	return summary
}

// summarizeGrep generates summary for Grep tool results.
func summarizeGrep(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	matchCount := 0
	var matches []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Match format: "file:line: content" or "file(line): content"
		if strings.Contains(line, ":") && !strings.HasPrefix(line, "No matches") {
			matchCount++
			if len(matches) < 3 {
				matches = append(matches, line)
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Grep: %d matches\n", matchCount)
	if len(matches) > 0 {
		fmt.Fprintln(&sb, "Top matches:")
		for _, m := range matches {
			fmt.Fprintf(&sb, "  %s\n", m)
		}
	}
	return sb.String()
}

// summarizeShell generates summary for Shell tool results.
func summarizeShell(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) == 0 {
		return "Shell: (empty output)"
	}

	// Check exit code
	var exitCode string
	if len(lines) > 0 {
		lastLine := lines[len(lines)-1]
		if strings.HasPrefix(lastLine, "exit code:") || strings.HasPrefix(lastLine, "Exit code:") {
			exitCode = lastLine
			lines = lines[:len(lines)-1]
		}
	}

	var sb strings.Builder
	if exitCode != "" {
		fmt.Fprintf(&sb, "Shell exit: %s\n", exitCode)
	}

	// Last 5 lines of output
	showCount := min(len(lines), 5)
	if len(lines) > showCount {
		fmt.Fprintf(&sb, "  ... (%d lines omitted) ...\n", len(lines)-showCount)
	}
	for _, l := range lines[len(lines)-showCount:] {
		fmt.Fprintf(&sb, "  %s\n", l)
	}
	return sb.String()
}

// summarizeGlob generates summary for Glob tool results.
func summarizeGlob(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	count := 0
	var files []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		count++
		if len(files) < 5 {
			files = append(files, line)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Glob: %d files matched\n", count)
	if len(files) > 0 {
		fmt.Fprintln(&sb, "Files:")
		for _, f := range files {
			fmt.Fprintf(&sb, "  %s\n", f)
		}
	}
	if count > 5 {
		fmt.Fprintf(&sb, "  ... and %d more\n", count-5)
	}
	return sb.String()
}

// summarizeDefault generates default summary.
func summarizeDefault(content string) string {
	runes := []rune(content)
	maxPreview := 300
	if len(runes) <= maxPreview {
		return fmt.Sprintf("Content: %s\n(Size: %d bytes, ~%d tokens)", content, len(content), estimateTokenSize(content, "gpt-4o"))
	}

	preview := string(runes[:maxPreview])
	tokens := estimateTokenSize(content, "gpt-4o")
	return fmt.Sprintf("Content (first %d chars): %s...\n(Size: %d bytes, ~%d tokens)", maxPreview, preview, len(content), tokens)
}

// extractJSONStringField extracts the value of a specified string field from a JSON string.
func extractJSONStringField(jsonStr, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// extractFunctionNames extracts function names from code content (Go, Python, JS, etc.).
func extractFunctionNames(content string) []string {
	// Go: func Name( or func (recv) Name(
	// Python: def Name(
	// JS: function Name(
	// Regexes pre-compiled at package level

	seen := make(map[string]bool)
	var names []string
	addName := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	for _, m := range goMethodFuncRe.FindAllStringSubmatch(content, -1) {
		if m[1] != "" {
			addName(m[1])
		}
		if m[2] != "" {
			addName(m[2])
		}
	}
	for _, m := range pyFuncRe.FindAllStringSubmatch(content, -1) {
		addName(m[1])
	}
	for _, m := range jsFuncRe.FindAllStringSubmatch(content, -1) {
		addName(m[1])
	}

	return names
}

// extractGoStructure extracts struct info from Go source code (types, interfaces, constants, variables).
// Used to enhance summarizeRead summary quality, helping LLM understand file skeleton.
// Uses package-level regex variables (goImportRe, goTypeRe, goConstVarRe, goFuncRe),
// completes all extraction in a single strings.Split pass.
func extractGoStructure(content string) string {
	// Fast check: skip non-Go files
	if !strings.Contains(content, "package ") {
		return ""
	}

	lines := strings.Split(content, "\n")
	var parts []string
	inImport := false
	var imports []string
	funcCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip comment lines
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		// package
		if strings.HasPrefix(trimmed, "package ") {
			parts = append(parts, trimmed)
			continue
		}

		// import block
		if trimmed == "import (" {
			inImport = true
			continue
		}
		if inImport {
			if trimmed == ")" {
				inImport = false
				continue
			}
			if m := goImportRe.FindStringSubmatch(line); len(m) > 1 {
				imports = append(imports, m[1])
			}
			continue
		}
		if strings.HasPrefix(trimmed, "import ") {
			if m := goImportRe.FindStringSubmatch(line); len(m) > 1 {
				imports = append(imports, m[1])
			}
			continue
		}

		// type definition
		if m := goTypeRe.FindStringSubmatch(trimmed); len(m) > 0 {
			parts = append(parts, fmt.Sprintf("type %s %s", m[1], m[2]))
			continue
		}

		// const/var group name
		if m := goConstVarRe.FindStringSubmatch(trimmed); len(m) > 0 {
			parts = append(parts, trimmed)
			continue
		}

		// func signatures (truncated to 15)
		if m := goFuncRe.FindStringSubmatch(trimmed); len(m) > 0 {
			params := strings.TrimSpace(m[2])
			if params == "" {
				params = "(no params)"
			}
			parts = append(parts, fmt.Sprintf("  %s(%s)", m[1], params))
			funcCount++
			if funcCount >= 15 {
				parts = append(parts, "  ...(more functions omitted)")
				break
			}
			continue
		}
	}

	// Summarize imports as short name list
	if len(imports) > 0 {
		shortNames := make([]string, len(imports))
		for i, imp := range imports {
			if idx := strings.LastIndex(imp, "/"); idx >= 0 {
				shortNames[i] = imp[idx+1:]
			} else {
				shortNames[i] = imp
			}
		}
		parts = append(parts, "Imports: "+strings.Join(shortNames, ", "))
	}

	if len(parts) <= 1 {
		return "" // Only package name, no other struct info
	}

	return strings.Join(parts, "\n")
}
