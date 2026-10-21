package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// Turn-level frame transport (`done` / `final`) — STATE-MODEL §3.3
// ---------------------------------------------------------------------------
//
// These are ADDITIVE frames: the legacy progress_structured payload keeps its
// full field set (active_tools / completed_tools / streaming_tools /
// iteration_history / ...), so existing web and mobile clients are unaffected.
// The new frames carry the same facts in the "one complete object" shape that
// the new client model consumes directly.

// frameRoute splits a channel-qualified chat id ("web:chat_X") into the SSE
// route (channel, chatID). Raw chat ids default to the web route, matching
// SendProgress's derivation.
func frameRoute(qualified string) (channel, chatID string) {
	if ch, id, ok := strings.Cut(qualified, ":"); ok && ch != "" && id != "" {
		return ch, id
	}
	return "web", qualified
}

// SendIterationDone implements channel.TurnFrameSender: one `done` frame per
// started iteration, carrying the iteration's COMPLETE block (contract C-6).
func (wc *WebChannel) SendIterationDone(chatID string, block *protocol.IterationBlock) {
	if block == nil {
		return
	}
	channel, bare := frameRoute(chatID)
	wc.hub.sendToSession(channel, bare, protocol.WSMessage{
		Type:    protocol.MsgTypeDone,
		TS:      time.Now().Unix(),
		Channel: channel,
		ChatID:  bare,
		TurnID:  block.TurnID,
		Done:    block,
	})
}

// SendTurnFinal implements channel.TurnFrameSender: exactly one `final` frame
// per turn, including empty and cancelled turns (contract C-7).
func (wc *WebChannel) SendTurnFinal(chatID string, final *protocol.TurnFinalFrame) {
	if final == nil {
		return
	}
	channel, bare := frameRoute(chatID)
	wc.hub.sendToSession(channel, bare, protocol.WSMessage{
		Type:    protocol.MsgTypeFinal,
		TS:      time.Now().Unix(),
		Channel: channel,
		ChatID:  bare,
		TurnID:  final.TurnID,
		Final:   final,
	})
}

// ---------------------------------------------------------------------------
// Authoritative pull endpoints (STATE-MODEL §2.4 / §3.5 / §14.3)
//
//   GET /api/iteration?chat=&turn=&iter=  → {ok, block} | 404 {ok:false,absent:true}
//   GET /api/turn?chat=&turn=             → {ok, blocks, final?}
//   GET /api/session-state?chat=          → {ok, epoch, seq, turns, live, session}
//
// These are the ONLY recovery paths a client needs (no frame-log replay):
// a missing iteration is pulled by object, a missing closing record is pulled
// by turn, and "everything is untrustworthy" (epoch change / reconnect) is a
// session-state snapshot.
// ---------------------------------------------------------------------------

// handleStateIteration serves GET /api/iteration — the authoritative content of
// ONE iteration (pullIter).
func (wc *WebChannel) handleStateIteration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	turnID, ok := parseUintQuery(r, "turn")
	if !ok || turnID == 0 {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid turn")
		return
	}
	iter64, ok := parseUintQuery(r, "iter")
	if !ok || iter64 == 0 {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid iter")
		return
	}
	sel, ok := wc.resolveAPISession(w, r, senderID, r.URL.Query().Get("channel"), r.URL.Query().Get("chat"))
	if !ok {
		return
	}
	if wc.callbacks.IterationBlock == nil {
		writeAbsentIteration(w)
		return
	}
	block, err := wc.callbacks.IterationBlock(sel.Channel, sel.ChatID, turnID, int(iter64))
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if block == nil {
		// Absent is an authoritative answer (the client uses it to confirm a
		// legal iteration jump — U-2), never an error and never a guess.
		writeAbsentIteration(w)
		return
	}
	writeStateJSON(w, http.StatusOK, map[string]any{"ok": true, "block": block})
}

// writeAbsentIteration writes the 404 "authoritatively absent" body.
func writeAbsentIteration(w http.ResponseWriter) {
	writeStateJSON(w, http.StatusNotFound, map[string]any{"ok": false, "absent": true})
}

// handleStateTurn serves GET /api/turn — one turn's blocks + closing record
// (pullTurn).
func (wc *WebChannel) handleStateTurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	turnID, ok := parseUintQuery(r, "turn")
	if !ok || turnID == 0 {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid turn")
		return
	}
	sel, ok := wc.resolveAPISession(w, r, senderID, r.URL.Query().Get("channel"), r.URL.Query().Get("chat"))
	if !ok {
		return
	}
	if wc.callbacks.TurnSnapshot == nil {
		writeStateJSON(w, http.StatusOK, map[string]any{"ok": true, "blocks": []any{}})
		return
	}
	snap, err := wc.callbacks.TurnSnapshot(sel.Channel, sel.ChatID, turnID)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if snap == nil {
		writeStateJSON(w, http.StatusNotFound, map[string]any{"ok": false, "absent": true})
		return
	}
	body := map[string]any{"ok": true, "blocks": snap.Blocks}
	if body["blocks"] == nil {
		body["blocks"] = []protocol.IterationBlock{}
	}
	if snap.Final != nil {
		body["final"] = snap.Final
	}
	writeStateJSON(w, http.StatusOK, body)
}

// stateSnapshotReadTries bounds the two-phase read retries. A busy turn
// advances seq on every tick, so "retry until stable" would spin; the fallback
// (stamp the pre-read watermark) is safe because frames are position states —
// re-applying one the snapshot already contains is a no-op (C-12).
const stateSnapshotReadTries = 4

// handleSessionState serves GET /api/session-state — the authoritative whole-
// session snapshot stamped with (epoch, seq).
//
// Two-phase read (C-12): the (slow) state assembly runs OUTSIDE every frame
// lock so a DB read can never block the server's frame publishing; then the
// per-chat watermark is read under the per-chat counter lock and compared with
// the pre-read watermark. If a frame was published in between, the state may be
// stale, so we retry; on exhaustion we stamp the pre-read watermark — a lower
// bound, which can only cause the client to re-apply idempotent frames.
func (wc *WebChannel) handleSessionState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sel, ok := wc.resolveAPISession(w, r, senderID, r.URL.Query().Get("channel"), r.URL.Query().Get("chat"))
	if !ok {
		return
	}
	if wc.callbacks.SessionState == nil {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "session state not available")
		return
	}
	routeKey := sessionRouteKey(sel.Channel, sel.ChatID)
	var (
		snap *protocol.SessionStateSnapshot
		seq  uint64
	)
	for i := 0; i < stateSnapshotReadTries; i++ {
		_, before := wc.currentCursor(routeKey)
		s, err := wc.callbacks.SessionState(sel.Channel, sel.ChatID)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		_, after := wc.currentCursor(routeKey)
		snap, seq = s, before
		if after == before {
			break
		}
	}
	if snap == nil {
		snap = &protocol.SessionStateSnapshot{}
	}
	snap.Epoch = ServerEpoch()
	snap.Seq = seq
	turns := snap.Turns
	if turns == nil {
		turns = []protocol.SessionStateTurn{}
	}
	writeStateJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"epoch":   snap.Epoch,
		"seq":     snap.Seq,
		"turns":   turns,
		"live":    snap.Live, // nil serializes as null — explicit "no live iteration"
		"session": snap.Sess,
	})
}

// writeStateJSON writes a RAW JSON body (no {ok,data,error} envelope).
//
// These three endpoints are part of the new wire contract, which fixes their
// exact shapes (STATE-MODEL §2.4): `{ok, block}` / `404 {ok:false, absent:true}`
// / `{ok, epoch, seq, turns, live, session}`. The generic writeJSON helper
// flattens the body into an envelope and DROPS `ok`/`absent`, which would make
// "authoritatively absent" indistinguishable from any other 404 — so the
// documented shape is written verbatim.
func writeStateJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// parseUintQuery parses a required non-negative integer query parameter.
func parseUintQuery(r *http.Request, key string) (uint64, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// jsonString is a tiny helper used by tests to keep assertions readable.
func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
