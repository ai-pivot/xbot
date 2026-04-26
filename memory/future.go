package memory

// Phase 2+ interfaces — predefined for future use, not yet implemented.

// Manageable represents memory entries that can be pinned/unpinned and deleted.
// EXPERIMENTAL: not yet implemented.
type Manageable interface {
	Pin() error
	Unpin() error
	Delete() error
}

// Evolvable represents memory entries that can evolve/transform over time.
// EXPERIMENTAL: not yet implemented.
type Evolvable interface {
	Evolve() error
}
