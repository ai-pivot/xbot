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
	if m.progress == nil {
		m.twVisible = 0
		m.rwVisible = 0
		return
	}

	// Advance reasoning writer
	if m.progress.ReasoningStreamContent != "" {
		target := len([]rune(m.progress.ReasoningStreamContent))
		m.advanceWriterCJK(&m.rwVisible, target, m.progress.ReasoningStreamContent, &m.rwCjkSkipTick)
	}

	// Advance stream writer
	if m.progress.StreamContent != "" {
		target := len([]rune(m.progress.StreamContent))
		m.advanceWriterCJK(&m.twVisible, target, m.progress.StreamContent, &m.twCjkSkipTick)
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
	// Max advance per 50ms tick is capped to avoid teleporting when
	// network coalesces multiple stream updates into one big gap.
	advance := 1
	switch {
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

// cycleModel switches to the next model across all subscriptions.
// Uses ListAllModels() so models from ALL subscriptions are visible (not just the
// current default LLM). Cycles through the model names displayed in the status bar.
// Note: this only changes the cached model name — the actual subscription switch
// happens when a new LLM call is made (or via quick switch panel).
// typewriterTickMsg 独立的打字机刷新（50ms 间隔，逐 rune 输出）
type typewriterTickMsg struct{}

// cliTempStatusClearMsg 临时状态提示自动清除
