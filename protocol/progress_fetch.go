package protocol

// ProgressFetch controls how much iteration history GetActiveProgress returns.
// It is a sealed interface — the only valid values are FetchAll() and
// FetchSinceWatermark(). A raw int like 0 or -1 will not compile.
//
// This prevents the class of bug where a caller passes 0 expecting "all
// iterations" but the filter uses > (strict greater-than), silently
// excluding iteration 0.
type ProgressFetch interface {
	isProgressFetch()
	Filter(iteration int) bool
	// ToFromIter converts to the wire protocol int value.
	// -1 = all iterations, >=0 = watermark for incremental pull.
	ToFromIter() int
}

// fetchAll returns every iteration including iteration 0.
type fetchAll struct{}

func (fetchAll) isProgressFetch()          {}
func (fetchAll) Filter(iteration int) bool { return true }
func (fetchAll) ToFromIter() int           { return -1 }

// FetchAll returns ALL iterations. Use for initial restore, reconnect,
// /su switch, and Web history snapshots.
func FetchAll() ProgressFetch { return fetchAll{} }

// fetchSince returns only iterations newer than a local watermark.
type fetchSince struct{ watermark int }

func (f fetchSince) isProgressFetch()          {}
func (f fetchSince) Filter(iteration int) bool { return iteration > f.watermark }
func (f fetchSince) ToFromIter() int           { return f.watermark }

// FetchSinceWatermark returns only iterations newer than the caller's
// local watermark. Used by CLI tick-pull to avoid transferring the full
// history every tick.
func FetchSinceWatermark(watermark int) ProgressFetch {
	return fetchSince{watermark: watermark}
}

// fetchTail returns the LAST tail iterations (the rendering-mirrored window —
// the v71 windowing). Unlike FetchAll (which must stay complete for the CLI
// TUI restore), fetchTail bounds the transfer: the live turn's snapshot for
// the Web initial load carries only the tail + the window bounds
// (ProgressEvent.IterWindow) — the client fetches the rest on demand
// (/api/history/iterations scroll-up). The bounds are what make the tail
// SAFE: without them a truncated snapshot is indistinguishable from a
// complete one (the 2026-09-17 "FetchAll 截 60 个迭代" incident — the client
// window was not adjacent to the authoritative window ⇒ gap ⇒ rendering
// truncated mid-history).
type fetchTail struct{ tail int }

func (f fetchTail) isProgressFetch()          {}
func (f fetchTail) Filter(iteration int) bool { return true } // filtering is positional (the last N), done by the caller
func (f fetchTail) ToFromIter() int           { return -1 }   // wire value: not a watermark pull

// FetchTail returns the last `tail` iterations (the rendering-mirrored window).
// The caller (GetActiveProgress) truncates the in-memory history to the tail
// AND stamps ProgressEvent.IterWindow with the bounds so the client can tell
// a windowed snapshot from a complete one.
func FetchTail(tail int) ProgressFetch {
	if tail <= 0 {
		tail = 50
	}
	return fetchTail{tail: tail}
}

// IsFetchTail reports whether the fetch is the windowed tail variant (the
// caller stamps the window bounds only for this variant).
func IsFetchTail(f ProgressFetch) bool {
	_, ok := f.(fetchTail)
	return ok
}

// FetchTailSize returns the tail size for a fetchTail fetch (0 for the other
// variants — the caller falls back to the complete list).
func FetchTailSize(f ProgressFetch) int {
	if ft, ok := f.(fetchTail); ok {
		return ft.tail
	}
	return 0
}
