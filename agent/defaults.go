package agent

import "time"

// Default constants for agent configuration. Centralised here to avoid
// magic numbers scattered across the codebase.

const (
	// LLM defaults
	DefaultMaxContextTokens      = 100_000
	DefaultMaxOutputTokens       = 4_096
	MaxOutputTokensHardLimit     = 131_072
	DefaultOffloadThresholdBytes = 10_240

	// Compression defaults
	DefaultCompressionThreshold = 0.9
	CompactTailFraction         = 0.15
	CompactTokensPerMessage     = 200
	CompactMaxTailMessages      = 300
	CompactMinTailMessages      = 50

	// Session defaults
	ChatHistoryCapacity    = 200
	StreamIdleTimeout      = 120 * time.Second
	DefaultTimestampFormat = "2006-01-02 15:04:05 MST"
)
