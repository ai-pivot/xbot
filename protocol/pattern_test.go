package protocol

import "testing"

func TestEventPatternMatches(t *testing.T) {
	t.Run("empty pattern matches everything", func(t *testing.T) {
		p := EventPattern{}
		if !p.Matches("anything", 0) {
			t.Error("empty pattern should match anything")
		}
		if !p.Matches("progress", 5) {
			t.Error("empty pattern should match progress v5")
		}
	})

	t.Run("type filter", func(t *testing.T) {
		p := EventPattern{Type: "progress"}
		if !p.Matches("progress", 1) {
			t.Error("should match progress")
		}
		if p.Matches("outbound", 1) {
			t.Error("should not match outbound")
		}
	})

	t.Run("type filter empty string matches all", func(t *testing.T) {
		p := EventPattern{Type: ""}
		if !p.Matches("progress", 1) {
			t.Error("empty type should match progress")
		}
		if !p.Matches("whatever", 99) {
			t.Error("empty type should match whatever")
		}
	})

	t.Run("MinVersion boundary", func(t *testing.T) {
		p := EventPattern{MinVersion: 2}
		if !p.Matches("progress", 2) {
			t.Error("should match version == MinVersion")
		}
		if !p.Matches("progress", 3) {
			t.Error("should match version > MinVersion")
		}
		if p.Matches("progress", 1) {
			t.Error("should not match version < MinVersion")
		}
		if p.Matches("progress", 0) {
			t.Error("should not match version 0 when MinVersion is 2")
		}
	})

	t.Run("MinVersion zero means no minimum", func(t *testing.T) {
		p := EventPattern{MinVersion: 0}
		if !p.Matches("progress", 0) {
			t.Error("MinVersion=0 should match v0")
		}
		if !p.Matches("progress", 1) {
			t.Error("MinVersion=0 should match v1")
		}
		if !p.Matches("progress", 99) {
			t.Error("MinVersion=0 should match v99")
		}
	})

	t.Run("MaxVersion boundary", func(t *testing.T) {
		p := EventPattern{MaxVersion: 3}
		if !p.Matches("progress", 3) {
			t.Error("should match version == MaxVersion")
		}
		if !p.Matches("progress", 2) {
			t.Error("should match version < MaxVersion")
		}
		if !p.Matches("progress", 1) {
			t.Error("should match version < MaxVersion")
		}
		if p.Matches("progress", 4) {
			t.Error("should not match version > MaxVersion")
		}
	})

	t.Run("MaxVersion zero means no maximum", func(t *testing.T) {
		p := EventPattern{MaxVersion: 0}
		if !p.Matches("progress", 0) {
			t.Error("MaxVersion=0 should match v0")
		}
		if !p.Matches("progress", 1) {
			t.Error("MaxVersion=0 should match v1")
		}
		if !p.Matches("progress", 9999) {
			t.Error("MaxVersion=0 should match v9999")
		}
	})

	t.Run("combined type and version range", func(t *testing.T) {
		p := EventPattern{Type: "progress", MinVersion: 1, MaxVersion: 3}
		if !p.Matches("progress", 2) {
			t.Error("should match progress v2")
		}
		if p.Matches("outbound", 2) {
			t.Error("should not match outbound v2")
		}
		if p.Matches("progress", 0) {
			t.Error("should not match progress v0")
		}
		if p.Matches("progress", 4) {
			t.Error("should not match progress v4")
		}
	})

	t.Run("exact version match", func(t *testing.T) {
		p := EventPattern{MinVersion: 1, MaxVersion: 1}
		if !p.Matches("x", 1) {
			t.Error("should match v1")
		}
		if p.Matches("x", 2) {
			t.Error("should not match v2")
		}
		if p.Matches("x", 0) {
			t.Error("should not match v0")
		}
	})

	t.Run("MinVersion equals MaxVersion non-one", func(t *testing.T) {
		p := EventPattern{MinVersion: 5, MaxVersion: 5}
		if !p.Matches("x", 5) {
			t.Error("should match v5")
		}
		if p.Matches("x", 4) {
			t.Error("should not match v4")
		}
		if p.Matches("x", 6) {
			t.Error("should not match v6")
		}
	})

	t.Run("type filter with version 0", func(t *testing.T) {
		p := EventPattern{Type: "progress"}
		if !p.Matches("progress", 0) {
			t.Error("should match progress v0 when no version constraints")
		}
	})

	t.Run("all constraints together", func(t *testing.T) {
		p := EventPattern{Type: "progress", MinVersion: 1, MaxVersion: 5}
		tests := []struct {
			typ  string
			ver  int
			want bool
			desc string
		}{
			{"progress", 1, true, "lower bound"},
			{"progress", 3, true, "mid range"},
			{"progress", 5, true, "upper bound"},
			{"progress", 0, false, "below min"},
			{"progress", 6, false, "above max"},
			{"outbound", 3, false, "wrong type"},
		}
		for _, tc := range tests {
			got := p.Matches(tc.typ, tc.ver)
			if got != tc.want {
				t.Errorf("%s: Matches(%q, %d) = %v, want %v", tc.desc, tc.typ, tc.ver, got, tc.want)
			}
		}
	})
}
