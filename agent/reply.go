package agent

import "strings"

// ExtractFinalReply extracts the final reply from LLM's complete output.
// Short content (<500 chars) returned as-is.
// Long content split by paragraphs, take last 2-3 paragraphs (max 2000 chars) to avoid losing conclusion context.
func ExtractFinalReply(content string) string {
	if len(content) < 500 {
		return content
	}
	paragraphs := strings.Split(strings.TrimSpace(content), "\n\n")
	if len(paragraphs) <= 1 {
		return content
	}

	// Take last few paragraphs: prefer 3, reduce to 2 if exceeding 2000 chars
	takeLast := 3
	if len(paragraphs) < takeLast {
		takeLast = len(paragraphs)
	}

	candidate := strings.TrimSpace(strings.Join(paragraphs[len(paragraphs)-takeLast:], "\n\n"))
	if len(candidate) > 2000 && takeLast > 2 {
		candidate = strings.TrimSpace(strings.Join(paragraphs[len(paragraphs)-2:], "\n\n"))
	}
	return candidate
}
