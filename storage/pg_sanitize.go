package storage

import "xbot/internal/textutil"

// pgText makes a string safe to send to PostgreSQL.
//
// PostgreSQL validates encoding on write and rejects the ENTIRE statement when
// it meets an invalid byte sequence (SQLSTATE 22021), whereas SQLite stores
// TEXT as raw bytes and never looks. That asymmetry means a single torn byte
// anywhere upstream — a `s[:n]` that cut a CJK character in half, a third-party
// API that returned a truncated body, an OCR/PDF extractor — turns into a lost
// conversation turn instead of one unreadable character. NUL bytes are the same
// failure with a twist: 0x00 IS valid UTF-8 (U+0000), so a UTF-8 validator
// passes it, but PG text cannot store it at all. Binary tool output (Read on an
// ELF, Shell `cat` on a tarball) is full of them.
//
// The individual producers are fixed at their source (see internal/textutil for
// the truncation helpers, and tools.Read which refuses binary files outright),
// but a storage backend should not depend on every caller being correct: this is
// the last line of defence, applied at the point where data crosses into PG. It
// mirrors the repair `cmd/xbot-migrate` applies to historical rows, so runtime
// writes and migrated rows stay consistent.
//
// Valid input without NUL is returned unchanged and without allocating.
func pgText(s string) string {
	return textutil.SanitizeText(s)
}
