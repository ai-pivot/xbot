package channel

// blockCache is a reusable cache for rendered output blocks. It eliminates the
// repeated (content, fingerprint, width) triple pattern that appeared 6+ times
// across the progress rendering pipeline.
//
// Usage:
//
//	result := m.cache.reasoning.getOrBuild(fp, width, func() string {
//	    // expensive render...
//	    return rendered
//	})
//	sb.WriteString(result)
//
// All blockCache fields are accessed through methods — never read/written directly.
type blockCache struct {
	content string
	fp      uint64
	width   int
}

// getOrBuild returns the cached content if fp and width match, otherwise calls
// build to produce new content, caches it, and returns it.
func (c *blockCache) getOrBuild(fp uint64, width int, build func() string) string {
	if c.content != "" && c.fp == fp && c.width == width {
		return c.content
	}
	c.content = build()
	c.fp = fp
	c.width = width
	return c.content
}

// reset clears the cache entry.
func (c *blockCache) reset() {
	c.content = ""
	c.fp = 0
	c.width = 0
}

// blockLinesCache extends blockCache with pre-split lines for incremental assembly.
type blockLinesCache struct {
	blockCache
	lines []string
}

// reset clears the cache entry including lines.
func (c *blockLinesCache) reset() {
	c.blockCache.reset()
	c.lines = nil
}

// progressHistoryCache is a specialized cache for iteration history that supports
// incremental append (only render newly added iterations) in addition to full rebuild.
type progressHistoryCache struct {
	content string
	lines   []string
	count   int    // len(iterationHistory) when cache was built
	width   int    // bubbleWidth when cache was built
	fp      uint64 // fingerprint of content
}

// get returns (lines, ok) if the cache is valid for the given count and width.
func (c *progressHistoryCache) get(count, width int) ([]string, bool) {
	if c.count == count && c.width == width && c.content != "" {
		return c.lines, true
	}
	return nil, false
}

// appendIncremental appends newly rendered content to the existing cache.
func (c *progressHistoryCache) appendIncremental(newContent string, newLines []string, totalCount int) {
	c.content += newContent
	c.count = totalCount
	c.fp = fnvHash64(c.content)
	c.lines = append(c.lines, newLines...)
}

// rebuild replaces the entire cache with fresh content.
func (c *progressHistoryCache) rebuild(content string, count, width int) {
	c.content = content
	c.count = count
	c.width = width
	c.fp = fnvHash64(content)
	c.lines = padLinesFromContent(content)
}

// reset clears the history cache.
func (c *progressHistoryCache) reset() {
	c.content = ""
	c.lines = nil
	c.count = 0
	c.width = 0
	c.fp = 0
}

// renderCache aggregates all render caching state for cliModel.
// All fields are accessed through methods to prevent direct field manipulation.
type renderCache struct {
	valid       bool   // global cache validity (set false on resize)
	history     string // cached rendered history messages (excludes streaming msg)
	msgCount    int    // len(messages) when cache was built
	vpContent   string // last setViewportContent raw content (dedup)
	vpWidth     int    // last setViewportContent width (dedup)
	wrapHistory string // hard-wrapped history (already split/wrapped at width)
	wrapRaw     string // raw history that was wrapped (for invalidation)
	wrapWidth   int    // width at which wrapHistory was built
	histMaxW    int    // actual max display width of cached wrapped lines
	histLines   []string

	// Progress sub-blocks — use blockCache.getOrBuild() for FP-gated caching.
	progressHistory progressHistoryCache // iteration history (incremental + full rebuild)
	currentStatic   blockCache           // completed tools + content (also keyed on iteration)
	currentIter     int                  // progress.Iteration when currentStatic was built
	reasoning       blockCache           // reasoning lines with guides and cursor
	stream          blockCache           // stream (assistant text) lines with guides and cursor
	thinking        blockCache           // thinking lines with guides
	progressBlock   blockLinesCache      // full progress panel output (content + lines)

	// Tick-level dirty detection for updateViewportContent fast path.
	lastTickHistLen int    // len(rc.history) at last tick
	lastTickProgFP  uint64 // progressBlock.fp at last tick
	lastTickRewFP   uint64 // fnvHash64(rewindBlock) at last tick

	// Reused slice for viewport line assembly across ticks.
	allLines        []string
	allLinesHistLen int

	// Dynamic viewport suffix (progress + rewind).
	dynamicRaw   string
	dynamicLines []string
	dynamicWidth int
}

// resetAll clears all render caches. Called on resize, session switch, etc.
func (rc *renderCache) resetAll() {
	rc.valid = false
	rc.history = ""
	rc.msgCount = 0
	rc.vpContent = ""
	rc.vpWidth = 0
	rc.wrapHistory = ""
	rc.wrapRaw = ""
	rc.wrapWidth = 0
	rc.histMaxW = 0
	rc.histLines = nil
	rc.progressHistory.reset()
	rc.currentStatic.reset()
	rc.currentIter = 0
	rc.reasoning.reset()
	rc.stream.reset()
	rc.thinking.reset()
	rc.progressBlock.reset()
	rc.lastTickHistLen = 0
	rc.lastTickProgFP = 0
	rc.lastTickRewFP = 0
	rc.allLines = nil
	rc.allLinesHistLen = 0
	rc.dynamicRaw = ""
	rc.dynamicLines = nil
	rc.dynamicWidth = 0
}

// invalidateProgress resets all progress-related caches (called on iteration change).
func (rc *renderCache) invalidateProgress() {
	rc.progressHistory.reset()
	rc.currentStatic.reset()
	rc.currentIter = 0
	rc.reasoning.reset()
	rc.stream.reset()
	rc.thinking.reset()
	rc.progressBlock.reset()
}
