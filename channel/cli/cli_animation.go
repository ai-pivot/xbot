package cli

import (
	"time"

	"charm.land/lipgloss/v2"
)

func newAnimTicker(frames []string, color string) *animTicker {
	altColor := currentTheme.AccentAlt
	return &animTicker{
		frames:   frames,
		style:    lipgloss.NewStyle().Foreground(lipgloss.Color(color)),
		styleAlt: lipgloss.NewStyle().Foreground(lipgloss.Color(altColor)),
		color:    color,
		colorAlt: altColor,
	}
}

func (t *animTicker) tick() {
	t.ticks++
	// Advance frame only every `speed` ticks (speed=1 → every tick, speed=3 → every 3rd)
	if t.speed <= 1 || t.ticks%int64(t.speed) == 0 {
		t.frame = (t.frame + 1) % len(t.frames)
	}
}

// viewFrames renders a frame from a given set using the ticker's current frame index.
// speedOverride controls per-call animation speed (0 = use ticker's default speed).
// 同样带呼吸效果。
// viewFrames renders a frame from a given set using the ticker's current frame index.
// speedOverride controls per-call animation speed (0 = use ticker's default speed).
// 同样带呼吸效果。
func (t *animTicker) viewFrames(frames []string, speedOverride ...int) string {
	speed := t.speed
	if len(speedOverride) > 0 && speedOverride[0] > 0 {
		speed = speedOverride[0]
	}
	// Calculate effective frame based on speed
	effectiveFrame := t.frame
	if speed > 1 {
		// Use a separate counter for this frame set, keyed by speed
		effectiveFrame = int(t.ticks/int64(speed)) % len(frames)
	}
	idx := effectiveFrame % len(frames)
	if t.ticks%20 < 10 {
		return t.style.Render(frames[idx])
	}
	return t.styleAlt.Render(frames[idx])
}

// isCJK reports whether r is a CJK character (ideographs, kana, hangul, etc.).
// isCJK reports whether r is a CJK character (ideographs, kana, hangul, etc.).
func isCJK(r rune) bool {
	return r >= 0x2E80
}

// advanceTypewriter advances both typewriters (stream + reasoning) on each tick.
// Called every typewriterTickMsg (50ms) during streaming.
// advanceTypewriter advances both typewriters (stream + reasoning) on each tick.
// Called every typewriterTickMsg (50ms) during streaming.
func (m *cliModel) advanceTypewriter() {
	if m.progressState.current == nil {
		m.progressState.twVisible = 0
		m.progressState.rwVisible = 0
		return
	}

	// Advance reasoning writer
	if m.progressState.current.ReasoningStreamContent != "" {
		target := len([]rune(m.progressState.current.ReasoningStreamContent))
		m.advanceWriterCJK(&m.progressState.rwVisible, target, m.progressState.current.ReasoningStreamContent, &m.progressState.rwCjkSkip)
	}

	// Advance stream writer
	if m.progressState.current.StreamContent != "" {
		target := len([]rune(m.progressState.current.StreamContent))
		m.advanceWriterCJK(&m.progressState.twVisible, target, m.progressState.current.StreamContent, &m.progressState.twCjkSkip)
	}
}

// advanceWriterCJK is like advanceWriter but CJK-aware: when the next rune to reveal
// is CJK, it only advances every other tick (effectively half speed).
// skipFlip tracks alternating ticks within a single call chain.
// advanceWriterCJK is like advanceWriter but CJK-aware: when the next rune to reveal
// is CJK, it only advances every other tick (effectively half speed).
// skipFlip tracks alternating ticks within a single call chain.
func (m *cliModel) advanceWriterCJK(visible *int, target int, content string, skipFlip *bool) {
	if target == 0 {
		*visible = 0
		return
	}
	gap := target - *visible
	if gap <= 0 {
		return
	}

	// Check if the next rune to reveal is CJK
	runes := []rune(content)
	nextIsCJK := *visible < len(runes) && isCJK(runes[*visible])

	// Gap-based acceleration — smooth catch-up without visible jumps.
	// For large gaps, advance proportionally so catch-up time stays
	// roughly constant (~500ms) regardless of how far behind we are.
	// Old fixed cap of 20 meant gap=500 took 1.25s to catch up, making
	// the typewriter look stuck when stream content arrives in bursts.
	advance := 1
	switch {
	case gap > 200:
		advance = gap / 5 // 200→40, 500→100, 1000→200 — catches up in ~8-10 ticks
	case gap > 80:
		advance = 20
	case gap > 40:
		advance = 10
	case gap > 20:
		advance = 3
	}

	// CJK penalty: if next rune is CJK and we're at normal speed, skip every other tick
	if nextIsCJK && advance <= 3 && gap <= 20 {
		*skipFlip = !*skipFlip
		if *skipFlip {
			return // skip this tick, revealing nothing
		}
	}

	*visible += advance
	if *visible > target {
		*visible = target
	}
}

// Ticker frame presets
// pickVerb returns a deterministic verb based on tick count (changes every ~2s at 10 FPS).
func (m *cliModel) pickVerb(ticks int64) string {
	verbs := m.locale.ThinkingVerbs
	if len(verbs) == 0 {
		return "Thinking"
	}
	idx := (ticks / 20) % int64(len(verbs))
	return verbs[idx]
}

// pickIdlePlaceholder 根据时间返回轮换的 placeholder（每 5 秒切换）
// pickIdlePlaceholder 根据时间返回轮换的 placeholder（每 5 秒切换）
func (m *cliModel) pickIdlePlaceholder() string {
	placeholders := m.locale.IdlePlaceholders
	if len(placeholders) == 0 {
		return ""
	}
	idx := int(time.Now().Unix()/5) % len(placeholders)
	return placeholders[idx]
}

// updatePlaceholder refreshes the placeholder text based on typing state.
// We store it in m.placeholderText instead of m.textarea.Placeholder to avoid
// CJK rendering bugs caused by textarea's internal placeholder↔normal view switch.
// updatePlaceholder refreshes the placeholder text based on typing state.
// We store it in m.placeholderText instead of m.textarea.Placeholder to avoid
// CJK rendering bugs caused by textarea's internal placeholder↔normal view switch.
func (m *cliModel) updatePlaceholder() {
	if m.typing {
		m.placeholderText = m.locale.ProcessingPlaceholder
	} else {
		m.placeholderText = m.pickIdlePlaceholder()
	}
}

// typewriterTickMsg 独立的打字机刷新（50ms 间隔，逐 rune 输出）
type typewriterTickMsg struct{}

// cliTempStatusClearMsg 临时状态提示自动清除
