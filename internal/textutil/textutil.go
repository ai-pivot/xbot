// Package textutil provides rune-safe truncation and UTF-8 repair for strings
// that are about to be shown to a model or written to a database.
//
// Two rules motivate it:
//
//  1. A bare `s[:n]` slices BYTES. On CJK (the default language of this
//     product's conversations) that can cut a 3-byte character in half and
//     leave a string that is not valid UTF-8. Such a string renders as U+FFFD
//     in the UI and, once persisted, poisons every later read of that row.
//  2. Backends disagree about what they will accept. SQLite stores TEXT as raw
//     bytes and never validates it, so this class of bug is invisible until a
//     message is copied to PostgreSQL, which validates encoding on write and
//     fails the ENTIRE statement with SQLSTATE 22021
//     ("invalid byte sequence for encoding UTF8"). One bad byte therefore costs
//     a whole conversation turn, not one character.
package textutil

import (
	"strings"
	"unicode/utf8"
)

// SanitizeUTF8 replaces every invalid UTF-8 sequence in s with U+FFFD, the
// same repair the SQLite→PostgreSQL migration tool applies to historical rows.
// Valid input is returned unchanged (and without allocating), so this is safe
// to call on every write.
func SanitizeUTF8(s string) string {
	return sanitize(s, false)
}

// SanitizeText normalises s into the text pipeline's invariant: valid UTF-8 and
// free of NUL bytes. Every invalid sequence AND every 0x00 byte becomes U+FFFD.
//
// NUL is not a UTF-8 problem (0x00 is a valid encoding of U+0000), which is why
// SanitizeUTF8 lets it through — but PostgreSQL's text type cannot store it at
// all. The server rejects the whole statement with SQLSTATE 22021
// (`invalid byte sequence for encoding "UTF8": 0x00`), so one NUL in a tool
// result (Read on a binary file, Shell `cat` on an ELF, an MCP server) used to
// cost the entire conversation turn. text is arbitrary bytes at the edges;
// this is the normalisation point before it becomes model context or a DB row.
//
// Valid input without NUL is returned unchanged and without allocating.
func SanitizeText(s string) string {
	return sanitize(s, true)
}

// sanitize is the single scan shared by both entry points. replaceNUL is false
// only for SanitizeUTF8, which is deliberately encoding-only (the migration
// tool's historical contract).
func sanitize(s string, replaceNUL bool) string {
	if utf8.ValidString(s) && (!replaceNUL || !strings.ContainsRune(s, 0)) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// One invalid byte. Consume exactly one so the following bytes get
			// their own chance to decode (a valid rune may start mid-sequence
			// after a torn prefix).
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		if r == 0 && replaceNUL {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// TruncateBytes returns the longest prefix of s that fits in maxBytes without
// splitting a rune. It never returns invalid UTF-8: invalid input is repaired
// first, so a caller cannot reintroduce the failure this package exists to
// prevent.
//
// Unlike TruncateHeadPreview it adds no suffix; callers that append one should
// budget for it (see TruncateBytesWithSuffix).
func TruncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	head := s[:maxBytes]
	// Drop trailing bytes while the boundary lands inside a rune.
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	return head
}

// TruncateBytesWithSuffix is TruncateBytes with a marker appended when (and
// only when) truncation happened. The marker counts against maxBytes, so the
// result never exceeds the budget — important because callers use this to cap
// what reaches the model and the database.
//
// A budget too small to hold the suffix degrades to a marker-less cut rather
// than panicking on a negative index.
func TruncateBytesWithSuffix(s string, maxBytes int, suffix string) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= len(suffix) {
		return TruncateBytes(s, maxBytes)
	}
	return TruncateBytes(s, maxBytes-len(suffix)) + suffix
}

// TruncateRunes returns the longest prefix of s holding at most maxRunes
// characters. Use this when the budget is expressed in characters (a UI column,
// a preview length) rather than bytes.
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	i := 0
	for n := 0; n < maxRunes; n++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			break
		}
		i += size
	}
	return s[:i]
}
