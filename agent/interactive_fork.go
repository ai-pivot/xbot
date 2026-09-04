package agent

import (
	"context"
	"fmt"
	"strings"

	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
)

// splitRoleInstance splits a "role:instance" / "role/instance" reference into
// its two parts. Returns ok=false when the value does not have the required
// two non-empty parts.
func splitRoleInstance(s string) (role, instance string, ok bool) {
	// "role:instance" — colon form (interactive key suffix style).
	if r, i, found := strings.Cut(s, ":"); found && r != "" && i != "" {
		return r, i, true
	}
	// "role/instance" — slash form (SendMessage address style).
	if r, i, found := strings.Cut(s, "/"); found && r != "" && i != "" {
		return r, i, true
	}
	return "", "", false
}

// resolveForkSourceSession resolves a SubAgent fork reference to the source
// session. Supported forms (first match wins):
//
//  1. "me"                                  — the calling agent's session:
//     sub-agent caller → its own interactive session;
//     main-agent caller → the origin session.
//  2. "agent:<full interactive key>"         — exact interactive key (channel:chatID/role:instance).
//  3. "agent:role/instance"                  — SendMessage address style.
//  4. "<full interactive key>"              — e.g. "cli:/path/explore:mem-1".
//  5. "role:instance" / "role/instance"      — interactive sub-agent of the current session
//     (falls back to a globally-unique suffix match).
//  6. "channel:chatID"                       — any session (e.g. "web:chat_abc").
//  7. bare chatID                            — resolved against the origin channel.
//
// The returned label identifies the source in logs and the sub-agent's
// inherited-context system-prompt note.
func (a *Agent) resolveForkSourceSession(ctx context.Context, originChannel, originChatID, fork string) (*session.TenantSession, string, error) {
	fork = strings.TrimSpace(fork)
	if fork == "" {
		return nil, "", fmt.Errorf("fork reference is empty")
	}

	// 1. "me" — the calling agent's own session.
	if fork == "me" {
		// Sub-agent caller: its own interactive session key rides on the context
		// (bgParentKey marker set by both the foreground and background Run paths).
		if parentKey, ok := ctx.Value(bgParentKey{}).(string); ok && parentKey != "" {
			if sess, found := a.multiSession.GetSession("agent", parentKey); found {
				return sess, "agent:" + parentKey, nil
			}
			return nil, "", fmt.Errorf("fork \"me\": parent session %q not found", parentKey)
		}
		// Main-agent caller: the origin session (channel, chatID).
		if sess, found := a.multiSession.GetSession(originChannel, originChatID); found {
			return sess, qualifyChatID(originChannel, originChatID), nil
		}
		return nil, "", fmt.Errorf("fork \"me\": current session %s not found", qualifyChatID(originChannel, originChatID))
	}

	// 2. "agent:" prefix — full interactive key, or SendMessage address style.
	if rest, ok := strings.CutPrefix(fork, "agent:"); ok {
		// 2a. Full interactive key: interactive sessions live under channel
		// "agent" with the interactiveKey as chatID.
		if sess, found := a.multiSession.GetSession("agent", rest); found {
			return sess, "agent:" + rest, nil
		}
		// 2b. "role/instance" address — resolve via the sub-agent lookup.
		if role, instance, ok2 := splitRoleInstance(rest); ok2 {
			return a.lookupSubAgentForkSource(originChannel, originChatID, role, instance)
		}
		return nil, "", fmt.Errorf("fork %q: sub-agent session not found", fork)
	}

	// 3/4. Full interactive key without the "agent:" prefix (contains "/").
	if strings.Contains(fork, "/") {
		if sess, found := a.multiSession.GetSession("agent", fork); found {
			return sess, fork, nil
		}
		// "role/instance" address — resolve via the sub-agent lookup.
		if role, instance, ok2 := splitRoleInstance(fork); ok2 {
			return a.lookupSubAgentForkSource(originChannel, originChatID, role, instance)
		}
		// Fall through: might be a "channel:chatID" whose chatID contains "/"
		// (e.g. "cli:/home/user/project") — rule 6 handles it below.
	}

	// 5. "role:instance" — interactive sub-agent of the current session.
	// Fall through to rule 6 if not found: a "channel:chatID" value like
	// "web:chat_abc" also matches splitRoleInstance (role="web", instance=
	// "chat_abc"), so the sub-agent lookup must miss before we try the
	// channel:chatID interpretation.
	if role, instance, ok2 := splitRoleInstance(fork); ok2 {
		if sess, label, err := a.lookupSubAgentForkSource(originChannel, originChatID, role, instance); err == nil {
			return sess, label, nil
		}
	}

	// 6. "channel:chatID" — any session.
	if ch, id, found := strings.Cut(fork, ":"); found && ch != "" && id != "" {
		if sess, ok2 := a.multiSession.GetSession(ch, id); ok2 {
			return sess, qualifyChatID(ch, id), nil
		}
		return nil, "", fmt.Errorf("fork %q: session not found (it must exist and be active)", fork)
	}

	// 7. Bare chatID — resolve against the origin channel.
	if sess, found := a.multiSession.GetSession(originChannel, fork); found {
		return sess, qualifyChatID(originChannel, fork), nil
	}
	return nil, "", fmt.Errorf("fork %q: session not found — use \"me\", \"role:instance\", an \"agent:role/instance\" address, or a full \"channel:chatID\" session key", fork)
}

// lookupSubAgentForkSource resolves a "role:instance" reference to an interactive
// sub-agent session: first under the current origin session, then a globally
// unique suffix match (ambiguous matches are rejected with the candidate list).
func (a *Agent) lookupSubAgentForkSource(originChannel, originChatID, role, instance string) (*session.TenantSession, string, error) {
	// Exact key under the current origin session.
	key := interactiveKey(originChannel, originChatID, role, instance)
	if sess, found := a.multiSession.GetSession("agent", key); found {
		return sess, "agent:" + key, nil
	}
	// Globally unique "/role:instance" suffix match (cross-session fork).
	var matches []string
	a.interactiveSubAgents.Range(func(k, v any) bool {
		keyStr, ok := k.(string)
		if ok && strings.HasSuffix(keyStr, "/"+role+":"+instance) {
			matches = append(matches, keyStr)
		}
		return true
	})
	if len(matches) == 1 {
		if sess, found := a.multiSession.GetSession("agent", matches[0]); found {
			return sess, "agent:" + matches[0], nil
		}
	}
	if len(matches) > 1 {
		return nil, "", fmt.Errorf("fork reference %q:%s is ambiguous — it matches %d sessions (%s); use the full session key", role, instance, len(matches), strings.Join(matches, ", "))
	}
	return nil, "", fmt.Errorf("fork reference %q:%s: no such sub-agent session under the current session; spawn it first or use a full session key", role, instance)
}

// copyForkMessages copies the active conversation state (post-Replay:
// compression/mask applied, display-only excluded) from src to dst, plus the
// per-iteration history rows so forked turns render with full detail.
// Message DB IDs are re-assigned by the destination; turn IDs are preserved so
// the forked turns render as their own turns and the next turn stays monotonic.
// Returns the copied messages (for the RunConfig context).
func copyForkMessages(src, dst *session.TenantSession) ([]llm.ChatMessage, error) {
	msgs, err := src.GetMessages()
	if err != nil {
		return nil, fmt.Errorf("read source messages: %w", err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	// Sanitize: strips dangling tool_calls (e.g. the in-flight SubAgent call that
	// triggered this fork), empty assistant shells, and orphaned tool results —
	// guarantees the forked context is a valid message sequence for the LLM.
	msgs = llm.SanitizeMessages(msgs)
	if len(msgs) == 0 {
		return nil, nil
	}
	// Copy: reset DB IDs (destination re-assigns), keep turn IDs (render grouping).
	// Deep-copy the ToolCalls slice — a shallow copy would share the backing
	// array with the source, a hidden bug if either side later mutates a slice
	// element in place (post-Replay messages are read-only today, but this
	// guards against future regressions).
	copied := make([]llm.ChatMessage, len(msgs))
	for i, m := range msgs {
		m.ID = 0
		if len(m.ToolCalls) > 0 {
			m.ToolCalls = append([]llm.ToolCall(nil), m.ToolCalls...)
		}
		copied[i] = m
	}
	if _, err := dst.AppendMessages(copied); err != nil {
		return nil, fmt.Errorf("append forked messages: %w", err)
	}
	// Copy iteration_history rows (turn_id preserved — the forked messages keep
	// their source turn IDs, so the read side's (tenant, turn) join still works).
	// message_id=0 per the write-side convention (read side queries by turn_id).
	// Non-fatal on failure: iteration detail is a rendering nicety, not context
	// data — forked messages are still valid. Log for observability.
	if recs, err := src.GetAllIterationHistory(); err != nil {
		log.WithError(err).Warn("fork: read source iteration history failed (skipped)")
	} else {
		for _, r := range recs {
			r.MessageID = 0
			if err := dst.AppendIterationHistory(0, r.TurnID, r); err != nil {
				log.WithError(err).Warn("fork: copy iteration history record failed (skipped)")
				break
			}
		}
	}
	return copied, nil
}

// ForkSessionMessages copies the active conversation state (post-Replay messages
// + iteration_history) from the source session to the destination session. Used
// by the user-level fork REST API (POST /api/chats/fork) to clone a session's
// context into a freshly created session. Both sessions must be resolvable via
// GetOrCreateSession (the destination is always a freshly created tenant).
//
// Message IDs are re-assigned by the destination; turn IDs are preserved so the
// forked turns keep their grouping. SanitizeMessages strips dangling tool_calls
// so the forked context is a valid prefix for the LLM.
func (a *Agent) ForkSessionMessages(srcChannel, srcChatID, dstChannel, dstChatID string) error {
	srcSess, err := a.multiSession.GetOrCreateSession(srcChannel, srcChatID)
	if err != nil {
		return fmt.Errorf("open source session %s:%s: %w", srcChannel, srcChatID, err)
	}
	dstSess, err := a.multiSession.GetOrCreateSession(dstChannel, dstChatID)
	if err != nil {
		return fmt.Errorf("open destination session %s:%s: %w", dstChannel, dstChatID, err)
	}
	_, err = copyForkMessages(srcSess, dstSess)
	return err
}

// forkContextNote returns the system-prompt section explaining the inherited
// context to the forked sub-agent. This is the anti-confusion contract: the
// sub-agent knows the history below is inherited, treats it as its own prior
// conversation, but keeps its OWN role identity.
func forkContextNote(sourceLabel string, msgCount int) string {
	return fmt.Sprintf(`
## Inherited Context (Fork)

This session was forked from session "%s" — the next %d messages below (before
your task message) are that conversation's verbatim history, copied at fork time
and already compressed to its current context state. Treat them as your own
prior conversation: do not re-ask for information already present there, and
continue from where it left off. The final user message after the inherited
history is your current task. The inherited conversation may involve a different
agent role or a human user — use it as background knowledge, NOT as instructions
about who you are: your identity and capabilities are defined by THIS system
prompt and your role, not by the forked history.
`, sourceLabel, msgCount)
}
