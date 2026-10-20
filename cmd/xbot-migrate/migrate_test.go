package main

import (
	"testing"
	"time"
)

// TestParseSQLiteTimestamp covers every shape the writers actually store.
//
// The Go time.Time.String() form is the one that broke the first migration run:
// user_llm_subscriptions stored "2026-09-27 07:46:30.708676068 +0000 UTC m=+267.07"
// and the monotonic-clock suffix made the value unparseable.
func TestParseSQLiteTimestamp(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"2026-09-27 07:46:30", time.Date(2026, 9, 27, 7, 46, 30, 0, time.UTC), true},
		{"2026-09-27T07:46:30Z", time.Date(2026, 9, 27, 7, 46, 30, 0, time.UTC), true},
		{"2026-09-27T07:46:30.123456789Z", time.Date(2026, 9, 27, 7, 46, 30, 123456789, time.UTC), true},
		// Go's time.Time.String(): zone NAME plus a monotonic suffix.
		{"2026-09-27 07:46:30.708676068 +0000 UTC m=+267.070035338",
			time.Date(2026, 9, 27, 7, 46, 30, 708676068, time.UTC), true},
		{"2026-09-27 07:46:30.708676068 +0000 UTC",
			time.Date(2026, 9, 27, 7, 46, 30, 708676068, time.UTC), true},
		{"2026-09-27 07:46:30 +0800 CST", time.Date(2026, 9, 27, 7, 46, 30, 0, time.FixedZone("CST", 8*3600)), true},
		{"2026-09-27", time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC), true},
		{"1780000000", time.Unix(1780000000, 0).UTC(), true},
		{"", time.Time{}, false},
		{"not a timestamp", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := parseSQLiteTimestamp(c.in)
		if ok != c.ok {
			t.Errorf("parseSQLiteTimestamp(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && !got.Equal(c.want) {
			t.Errorf("parseSQLiteTimestamp(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestSanitizeUTF8 pins the repair of SQLite's unvalidated TEXT.
//
// Measured on a real installation: 36 of 15,341 message rows carried a multi-byte
// character cut mid-sequence (a historical truncation). SQLite reads them fine;
// PostgreSQL rejects the whole INSERT with "invalid byte sequence for encoding
// UTF8". Dropping the rows would lose conversation history, so only the damaged
// bytes are replaced.
func TestSanitizeUTF8(t *testing.T) {
	valid := "正常的中文内容 with ascii"
	if got := sanitizeUTF8(valid); got != valid {
		t.Errorf("valid string changed: %q → %q", valid, got)
	}

	// 0xef 0xbc 0x2e is a truncated three-byte sequence (the byte pattern seen in
	// the failing migration): 0xef 0xbc is the start of a fullwidth form, 0x2e is
	// an ASCII dot that cannot be its continuation.
	broken := "before\xef\xbc.after"
	got := sanitizeUTF8(broken)
	if got == broken {
		t.Fatal("invalid UTF-8 was not repaired")
	}
	if !utf8Valid(got) {
		t.Errorf("result is still invalid UTF-8: %q", got)
	}
	if len(got) < len("before")+len("after") {
		t.Errorf("repair dropped content: %q", got)
	}

	if got := sanitizeUTF8(""); got != "" {
		t.Errorf("empty string changed: %q", got)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return true // replacement char is itself valid
		}
	}
	return true
}

// TestConvertValueUsesTargetType pins that conversion follows the PG column TYPE,
// not the column name.
//
// The name-based approach was wrong: user_settings.updated_at is an INTEGER epoch
// while most tables store a timestamp string under the same name, so name-based
// conversion turned the epoch into the year 58708 and PostgreSQL rejected it
// ("invalid input syntax for type bigint").
func TestConvertValueUsesTargetType(t *testing.T) {
	// Same column name, two target types, two different answers.
	if got := convertValue("bigint", "updated_at", int64(1780000000)); got != int64(1780000000) {
		t.Errorf("bigint updated_at = %v, want the epoch unchanged", got)
	}
	ts, ok := convertValue("timestamp with time zone", "updated_at", "2026-09-27 07:46:30").(time.Time)
	if !ok || ts.IsZero() {
		t.Errorf("timestamptz updated_at = %v, want a parsed time", convertValue("timestamp with time zone", "updated_at", "2026-09-27 07:46:30"))
	}

	// SQLite INTEGER 0/1 → PG BOOLEAN.
	if got := convertValue("boolean", "display_only", int64(1)); got != true {
		t.Errorf("boolean 1 = %v, want true", got)
	}
	if got := convertValue("boolean", "display_only", int64(0)); got != false {
		t.Errorf("boolean 0 = %v, want false", got)
	}

	// NULL stays NULL for every type.
	for _, typ := range []string{"text", "bigint", "boolean", "timestamp with time zone", "bytea"} {
		if got := convertValue(typ, "x", nil); got != nil {
			t.Errorf("convertValue(%s, nil) = %v, want nil", typ, got)
		}
	}

	// An unparseable timestamp becomes NULL rather than aborting the migration.
	if got := convertValue("timestamp with time zone", "created_at", "garbage"); got != nil {
		t.Errorf("unparseable timestamp = %v, want nil", got)
	}
}

// TestShiftRecordDataKeepsReferencesPointingAtTheSameRows pins the id-window
// rewrite: after offsetting a session DB's ids, every embedded reference must move
// by the same amount, or replay silently resolves the wrong row.
func TestShiftRecordDataKeepsReferencesPointingAtTheSameRows(t *testing.T) {
	w := idWindow{msgOffset: 1000, iterOffset: 500}

	// Compression snapshot: HistoryIDs + PendingAskUser.
	in := `{"version":1,"messages":[{"role":"user","content":"a"}],"history_ids":[7,9],"pending_ask_user":{"history_id":9,"tool_history_id":7}}`
	out := shiftRecordData(in, w).(string)
	if !contains(out, `"history_ids":[1007,1009]`) {
		t.Errorf("history_ids not shifted: %s", out)
	}
	if !contains(out, `"history_id":1009`) || !contains(out, `"tool_history_id":1007`) {
		t.Errorf("pending ask user ids not shifted: %s", out)
	}

	// Mask mutations.
	mask := `{"mutations":[{"target_history_id":42,"content":"redacted"}]}`
	if got := shiftRecordData(mask, w).(string); !contains(got, `"target_history_id":1042`) {
		t.Errorf("mask target not shifted: %s", got)
	}

	// AskUser question / answer.
	q := `{"metadata":{"request_id":"r1"},"tool_history_id":11}`
	if got := shiftRecordData(q, w).(string); !contains(got, `"tool_history_id":1011`) {
		t.Errorf("ask question target not shifted: %s", got)
	}

	// A zero target means "none" and must stay zero (not become the offset).
	zero := `{"mutations":[{"target_history_id":0,"content":"x"}]}`
	if got := shiftRecordData(zero, w).(string); contains(got, `"target_history_id":1000`) {
		t.Errorf("zero target was shifted: %s", got)
	}

	// Non-JSON payloads pass through untouched.
	if got := shiftRecordData("plain text", w).(string); got != "plain text" {
		t.Errorf("non-JSON payload changed: %s", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
