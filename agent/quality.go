package agent

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"xbot/llm"
)

// ActiveFile recent N rounds of active file records
type ActiveFile struct {
	Path         string   // File path
	LastSeenIter int      // Last seen round (0=most recent round)
	Functions    []string // Function signatures involved (extracted from tool result)
}

// semanticMatchThreshold is the minimum word overlap ratio for a semantic match.
const semanticMatchThreshold = 0.6

// containsSemanticMatch semantic fuzzy matching: normalized substring + keyword overlap.
func containsSemanticMatch(text, target string) bool {
	if text == "" || target == "" {
		return false
	}

	// Exact substring match (case-insensitive)
	if strings.Contains(strings.ToLower(text), strings.ToLower(target)) {
		return true
	}

	// Reverse check: target contains key parts of text
	if len(target) > 50 && len(target) > len(text) {
		if strings.Contains(strings.ToLower(target), strings.ToLower(text)) {
			return true
		}
	}

	// Keyword overlap: at least 60% of target's words appear in text
	targetWords := splitToWords(target)
	if len(targetWords) == 0 {
		return false
	}
	matchedWords := 0
	textLower := strings.ToLower(text)
	for _, w := range targetWords {
		if strings.Contains(textLower, strings.ToLower(w)) {
			matchedWords++
		}
	}
	return float64(matchedWords)/float64(len(targetWords)) >= semanticMatchThreshold
}

// ----------------------------------------------------------------
// Helper functions
// ----------------------------------------------------------------

// extractFilePaths 正则提取File path。
// Matches: /absolute/path, ./relative/path, ../parent/path, filename.ext (must contain at least one / or .ext)
var filePathRe = regexp.MustCompile(`(?:[A-Za-z]:[\\\/]|[./~][\w./~-]*|/\S+?\.\w{1,10})(?:\s|[),;:}"'\n]|$)`)

var funcSigRe = regexp.MustCompile(`func\s+(\w+)`)

func extractFilePaths(text string) []string {
	matches := filePathRe.FindAllString(text, -1)
	var result []string
	seen := make(map[string]bool)
	for _, m := range matches {
		// Remove trailing non-path characters
		m = strings.TrimRight(m, " \t\n\r,);:}\"'")
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		result = append(result, m)
	}
	return result
}

// extractFunctionSignatures extracts Go function signatures from text (func FuncName pattern).
func extractFunctionSignatures(text string) []string {
	matches := funcSigRe.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		if len(m) > 1 {
			sig := "func " + m[1]
			if !seen[sig] {
				seen[sig] = true
				result = append(result, sig)
			}
		}
	}
	return result
}

// stopWords stop word set.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "shall": true, "should": true,
	"may": true, "might": true, "must": true, "can": true, "could": true,
	"to": true, "of": true, "in": true, "for": true, "on": true,
	"at": true, "by": true, "with": true, "from": true, "as": true,
	"into": true, "about": true, "like": true, "through": true, "after": true,
	"over": true, "between": true, "out": true, "against": true, "during": true,
	"without": true, "before": true, "under": true, "around": true, "among": true,
	"and": true, "but": true, "or": true, "nor": true, "not": true,
	"so": true, "yet": true, "both": true, "either": true, "neither": true,
	"each": true, "every": true, "all": true, "any": true, "few": true,
	"more": true, "most": true, "other": true, "some": true, "such": true,
	"no": true, "only": true, "own": true, "same": true, "than": true,
	"too": true, "very": true, "just": true, "because": true, "if": true,
	"when": true, "where": true, "how": true, "what": true, "which": true,
	"who": true, "whom": true, "this": true, "that": true, "these": true,
	"those": true, "then": true, "there": true, "here": true, "also": true,
	"it": true, "its": true, "i": true, "me": true, "my": true,
	"we": true, "us": true, "our": true, "you": true, "your": true,
	"he": true, "him": true, "his": true, "she": true, "her": true,
	"they": true, "them": true, "their": true, "up": true, "down": true,
}

// splitToWords tokenizes text (removes stop words).
func splitToWords(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	var result []string
	for _, f := range fields {
		lower := strings.ToLower(f)
		if !stopWords[lower] && len(f) > 1 {
			result = append(result, f)
		}
	}
	return result
}

// Internal helpers

// ExtractActiveFiles extracts active files from the last N rounds of tool calls.
// One round = one set of assistant(tool_calls) + corresponding tool result messages.
// 扫描 messages 尾部，按工具类型提取File path：
//   - Read/Edit/Write → "path" or "file_path" in Arguments JSON
//   - Glob → "pattern" in Arguments JSON
//   - Grep → "path" in Arguments JSON
//   - Shell → 从 tool result Content 中正则提取File path
//   - SubAgent → don't extract
//
// Also extract function signatures from tool result Content (func \w+ pattern).
// Deduplicate and sort by LastSeenIter descending (most recent first).
func ExtractActiveFiles(messages []llm.ChatMessage, lastN int) []ActiveFile {
	if len(messages) == 0 || lastN <= 0 {
		return nil
	}

	// Find tool groups from the tail
	type toolRound struct {
		iter  int
		paths []string
		funcs []string
	}
	var rounds []toolRound

	// Identify tool groups: assistant(tool_calls) + tool results
	// Scan from tail forward
	var currentPaths []string
	var currentFuncs []string

	roundCount := 0
	i := len(messages) - 1

	for i >= 0 && roundCount < lastN {
		msg := messages[i]

		if msg.Role == "tool" {
			// Extract paths from tool result (Shell special handling)
			if msg.ToolName == "Shell" {
				// 从 Shell 输出中正则提取File path
				currentPaths = append(currentPaths, extractFilePaths(msg.Content)...)
			}
			// Extract function signatures from tool result
			funcSigs := extractFunctionSignatures(msg.Content)
			currentFuncs = append(currentFuncs, funcSigs...)
		} else if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Extract paths from tool call Arguments JSON
			for _, tc := range msg.ToolCalls {
				paths := extractPathsFromToolArgs(tc.Name, tc.Arguments)
				currentPaths = append(currentPaths, paths...)
			}

			// This is the start of a new round
			if len(currentPaths) > 0 || len(currentFuncs) > 0 {
				rounds = append(rounds, toolRound{
					iter:  roundCount,
					paths: currentPaths,
					funcs: currentFuncs,
				})
				roundCount++
				currentPaths = nil
				currentFuncs = nil
			} else {
				// Count as a round even if no paths extracted (avoid undercounting)
				roundCount++
			}
		} else if roundCount > 0 {
			// Encountered non-tool/assistant message and collection has started, stop
			break
		}
		i--
	}

	// Reverse rounds so most recent is first
	for l, r := 0, len(rounds)-1; l < r; l, r = l+1, r-1 {
		rounds[l], rounds[r] = rounds[r], rounds[l]
	}

	// Merge and deduplicate
	pathInfo := make(map[string]*ActiveFile) // path -> ActiveFile
	for _, rd := range rounds {
		seen := make(map[string]bool)
		for _, p := range rd.paths {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			if existing, ok := pathInfo[p]; ok {
				// Update to a more recent round
				if rd.iter < existing.LastSeenIter {
					existing.LastSeenIter = rd.iter
				}
			} else {
				pathInfo[p] = &ActiveFile{
					Path:         p,
					LastSeenIter: rd.iter,
				}
			}
		}
		// Function signatures assigned to corresponding files (by nearest path)
		for _, f := range rd.funcs {
			if f == "" {
				continue
			}
			// Find the most relevant path in this round
			if len(rd.paths) > 0 {
				p := rd.paths[0]
				if af, ok := pathInfo[p]; ok {
					af.Functions = append(af.Functions, f)
				}
			}
		}
	}

	// Deduplicate function signatures
	result := make([]ActiveFile, 0, len(pathInfo))
	for _, af := range pathInfo {
		af.Functions = dedupStrings(af.Functions)
		result = append(result, *af)
	}

	// Sort by LastSeenIter descending (most recent first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastSeenIter < result[j].LastSeenIter
	})

	return result
}

// extractPathsFromToolArgs 从工具调用的 Arguments JSON 中提取File path。
func extractPathsFromToolArgs(toolName, argsJSON string) []string {
	if argsJSON == "" {
		return nil
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}

	var paths []string
	switch toolName {
	case "Read", "FileCreate", "FileReplace", "Write":
		if p, ok := args["path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
		if p, ok := args["file_path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	case "Glob":
		if p, ok := args["pattern"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	case "Grep":
		if p, ok := args["path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// dedupStrings deduplicates string slice, preserving order
func dedupStrings(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
