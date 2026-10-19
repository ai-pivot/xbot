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
	if utf8.ValidString(s) {
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
