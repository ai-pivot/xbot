package protocol

import (
	"testing"
)

func TestNegotiate(t *testing.T) {
	t.Run("both sides match perfectly", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2, 3}},
			{EventType: "outbound", Versions: []int{1}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2, 3}},
			{EventType: "outbound", Versions: []int{1}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 results, got %d", len(result))
		}
		// Should pick highest common version
		checkResult(t, result, "progress", 3)
		checkResult(t, result, "outbound", 1)
	})

	t.Run("client old, server new", func(t *testing.T) {
		// Client supports v1-2, server supports v1-4 → pick v2
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2, 3, 4}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}
		checkResult(t, result, "progress", 2)
	})

	t.Run("server old, client new", func(t *testing.T) {
		// Client supports v1-3, server supports v1-2 → pick v2
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2, 3}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}
		checkResult(t, result, "progress", 2)
	})

	t.Run("no version intersection skipped silently without required", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{4, 5}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("no intersection is not an error without required: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("expected 0 results for non-intersecting versions, got %d", len(result))
		}
	})

	t.Run("required event missing from remote entirely", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
		}
		_, err := Negotiate(local, remote, []string{"outbound"})
		if err == nil {
			t.Fatal("expected error for missing required event")
			return
		}
		if err.Error() != `required event "outbound" not supported by remote` {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("required event with no common version returns error", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{2}},
		}
		// progress appears on both but no common version → not in result
		// it's required → error
		_, err := Negotiate(local, remote, []string{"progress"})
		if err == nil {
			t.Fatal("expected error for required event with no common version")
			return
		}
	})

	t.Run("required event not in local at all", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
		}
		remote := []EventCapability{
			{EventType: "other", Versions: []int{1}},
		}
		_, err := Negotiate(local, remote, []string{"progress"})
		if err == nil {
			t.Fatal("expected error for required event not in local")
			return
		}
	})

	t.Run("multiple events with mixed intersection", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2, 3}},
			{EventType: "outbound", Versions: []int{1}},
			{EventType: "inject_user", Versions: []int{1, 2}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2}},
			{EventType: "inject_user", Versions: []int{1}},
			{EventType: "conn_state", Versions: []int{1}},
		}
		result, err := Negotiate(local, remote, []string{"progress"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should have: progress v2, inject_user v1
		// outbound not in remote, conn_state not in local
		if len(result) != 2 {
			t.Fatalf("expected 2 results, got %d", len(result))
		}
		checkResult(t, result, "progress", 2)
		checkResult(t, result, "inject_user", 1)
	})

	t.Run("required events all satisfied", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
			{EventType: "outbound", Versions: []int{1}},
			{EventType: "inject_user", Versions: []int{1}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
			{EventType: "outbound", Versions: []int{1}},
			{EventType: "inject_user", Versions: []int{1}},
		}
		result, err := Negotiate(local, remote, []string{"progress", "outbound"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("expected 3 results, got %d", len(result))
		}
	})

	t.Run("empty local capabilities", func(t *testing.T) {
		result, err := Negotiate(nil, []EventCapability{{EventType: "progress", Versions: []int{1}}}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("expected 0 results, got %d", len(result))
		}
	})

	t.Run("empty remote capabilities", func(t *testing.T) {
		result, err := Negotiate([]EventCapability{{EventType: "progress", Versions: []int{1}}}, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("expected 0 results, got %d", len(result))
		}
	})

	t.Run("unordered version lists", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{3, 1, 2}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{2, 1}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		checkResult(t, result, "progress", 2)
	})

	t.Run("duplicate versions in list", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1, 1, 2, 2}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1, 2}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		checkResult(t, result, "progress", 2)
	})

	t.Run("event only in remote is ignored", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
			{EventType: "outbound", Versions: []int{1}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}
		checkResult(t, result, "progress", 1)
	})

	t.Run("event only in local is ignored", func(t *testing.T) {
		local := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
			{EventType: "outbound", Versions: []int{1}},
		}
		remote := []EventCapability{
			{EventType: "progress", Versions: []int{1}},
		}
		result, err := Negotiate(local, remote, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}
		checkResult(t, result, "progress", 1)
	})
}

func TestHighestCommon(t *testing.T) {
	t.Run("returns highest common version", func(t *testing.T) {
		if got := highestCommon([]int{1, 2, 3}, []int{2, 3, 4}); got != 3 {
			t.Errorf("highestCommon() = %d, want 3", got)
		}
	})

	t.Run("returns 0 when no common", func(t *testing.T) {
		if got := highestCommon([]int{1, 2}, []int{3, 4}); got != 0 {
			t.Errorf("highestCommon() = %d, want 0", got)
		}
	})

	t.Run("single element match", func(t *testing.T) {
		if got := highestCommon([]int{5}, []int{5, 6, 7}); got != 5 {
			t.Errorf("highestCommon() = %d, want 5", got)
		}
	})

	t.Run("single element no match", func(t *testing.T) {
		if got := highestCommon([]int{5}, []int{6, 7}); got != 0 {
			t.Errorf("highestCommon() = %d, want 0", got)
		}
	})

	t.Run("empty first slice", func(t *testing.T) {
		if got := highestCommon(nil, []int{1, 2}); got != 0 {
			t.Errorf("highestCommon() = %d, want 0", got)
		}
	})

	t.Run("empty second slice", func(t *testing.T) {
		if got := highestCommon([]int{1, 2}, nil); got != 0 {
			t.Errorf("highestCommon() = %d, want 0", got)
		}
	})

	t.Run("both empty", func(t *testing.T) {
		if got := highestCommon(nil, nil); got != 0 {
			t.Errorf("highestCommon() = %d, want 0", got)
		}
	})

	t.Run("with duplicate values", func(t *testing.T) {
		if got := highestCommon([]int{1, 1, 2, 2}, []int{2, 2, 3, 3}); got != 2 {
			t.Errorf("highestCommon() = %d, want 2", got)
		}
	})
}

func checkResult(t *testing.T, result []EventCapability, eventType string, expectedVersion int) {
	t.Helper()
	for _, c := range result {
		if c.EventType == eventType {
			if len(c.Versions) != 1 {
				t.Errorf("for event %q: expected 1 version, got %v", eventType, c.Versions)
				return
			}
			if c.Versions[0] != expectedVersion {
				t.Errorf("for event %q: expected version %d, got %d", eventType, expectedVersion, c.Versions[0])
			}
			return
		}
	}
	t.Errorf("event %q not found in result", eventType)
}
