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
