package cli

// blockLinesCache caches rendered output with pre-split lines for incremental assembly.
type blockLinesCache struct {
	content string
	fp      uint64
	width   int
	lines   []string
}

// reset clears the cache entry.
func (c *blockLinesCache) reset() {
	c.content = ""
	c.fp = 0
	c.width = 0
	c.lines = nil
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

	// Progress block cache (now minimal — renderProgressBlock is a no-op).
	progressBlock blockLinesCache

	// Tick-level dirty detection for updateViewportContent fast path.
	lastTickHistLen int    // len(rc.history) at last tick
	lastTickProgFP  uint64 // progressBlock.fp at last tick
	lastTickRewFP   uint64 // fnvHash64(rewindBlock) at last tick

	// Reused slice for viewport line assembly across ticks.
	allLines        []string
	allLinesHistLen int

	// Generation counter for histLines → allLines consistency.
	// histGen is incremented every time histLines content changes
	// (fullRebuild, appendNewMessagesToCache, setViewportContent slow path).
	// allLinesGen records the generation when allLines was last built.
	// The tick fast path only reuses allLines when histGen == allLinesGen.
	// This is an algorithmic guarantee against stale cache reuse —
	// no string comparison needed.
	histGen     uint64
	allLinesGen uint64

	// Dynamic viewport suffix (progress + rewind).
	dynamicRaw   string
	dynamicLines []string
	dynamicWidth int

	// Streaming message incremental cache — avoids O(N) re-render of all
	// completed iterations on every 100ms tick during streaming.
	// Only the live (in-progress) iteration is re-rendered per tick.
	streamCompletedLines []string // guide-prefixed lines for completed iterations
	streamCompletedCount int      // len(iterations) when streamCompletedLines was built
	streamCompletedWidth int      // contentWidth when cache was built
	streamHeaderLine     string   // cached header line (guide + "Assistant ..." label)
	streamHeaderWidth    int      // contentWidth for the header line cache
	streamMaxW           int      // max visual width across all cached streaming lines
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
	rc.progressBlock.reset()
	rc.lastTickHistLen = 0
	rc.lastTickProgFP = 0
	rc.lastTickRewFP = 0
	rc.allLines = nil
	rc.allLinesHistLen = 0
	rc.histGen = 0
	rc.allLinesGen = 0
	rc.dynamicRaw = ""
	rc.dynamicLines = nil
	rc.dynamicWidth = 0
	rc.streamCompletedLines = nil
	rc.streamCompletedCount = 0
	rc.streamCompletedWidth = 0
	rc.streamHeaderLine = ""
	rc.streamHeaderWidth = 0
	rc.streamMaxW = 0
}

// invalidateProgress resets all progress-related caches (called on iteration change).
func (rc *renderCache) invalidateProgress() {
	rc.progressBlock.reset()
	// Completed iteration lines depend on iterationHistory which changed.
	rc.streamCompletedLines = nil
	rc.streamCompletedCount = 0
}

// bumpHistGen increments the histLines generation counter.
// Must be called at every site that modifies histLines content.
// This ensures the tick fast path detects stale allLines via generation
// mismatch (histGen != allLinesGen) instead of length comparison.
func (rc *renderCache) bumpHistGen() {
	rc.histGen++
}
