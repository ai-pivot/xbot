package sqlite

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSetBlockCountsRunesNotBytes (m6) verifies SetBlock enforces char_limit on
// CHARACTER count, not byte count. A CJK-heavy block of 667 runes (2001 UTF-8
// bytes) must be accepted under a 2000 char limit — the old `len(content)`
// check rejected it with 2001 > 2000.
func TestSetBlockCountsRunesNotBytes(t *testing.T) {
	db := openTestDB(t)
	svc := NewCoreMemoryService(db)
	ensureTenants(t, db, 0, 301)

	if err := svc.InitBlocks(301, ""); err != nil {
		t.Fatalf("InitBlocks: %v", err)
	}

	// 667 CJK runes = 2001 bytes > 2000 (the default human block char limit),
	// but 667 runes < 2000 chars — must be ACCEPTED.
	content := strings.Repeat("世", 667)
	if got := utf8.RuneCountInString(content); got != 667 {
		t.Fatalf("test fixture: expected 667 runes, got %d", got)
	}
	if got := len(content); got != 2001 {
		t.Fatalf("test fixture: expected 2001 bytes, got %d", got)
	}
	if err := svc.SetBlock(301, "human", content, "u-runes"); err != nil {
		t.Fatalf("SetBlock rejected a 667-rune (2001-byte) block under a 2000 char limit: %v", err)
	}

	// Boundary: exactly charLimit runes must pass.
	if err := svc.SetBlock(301, "human", strings.Repeat("界", 2000), "u-runes"); err != nil {
		t.Fatalf("SetBlock rejected exactly-limit content: %v", err)
	}

	// Over the limit by runes must still fail.
	if err := svc.SetBlock(301, "human", strings.Repeat("界", 2001), "u-runes"); err == nil {
		t.Fatal("SetBlock accepted 2001 runes over a 2000 char limit")
	}
}
