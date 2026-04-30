package plugin

import (
	"errors"
	"sort"
	"testing"
)

// makeManifest creates a test PluginManifest with the given ID and dependencies.
func makeManifest(id string, deps ...string) *PluginManifest {
	ds := make([]PluginDependency, len(deps))
	for i, d := range deps {
		ds[i] = PluginDependency{ID: d}
	}
	return &PluginManifest{
		ID:               id,
		Name:             id,
		Version:          "1.0.0",
		Runtime:          RuntimeNative,
		ActivationEvents: []string{"onStart"},
		Dependencies:     ds,
	}
}

func TestDependencyResolver_Simple(t *testing.T) {
	// Chain: C → B → A (C depends on B, B depends on A)
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("a"))
	dr.AddManifest(makeManifest("b", "a"))
	dr.AddManifest(makeManifest("c", "b"))

	order, err := dr.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	// Must contain all 3
	if len(order) != 3 {
		t.Fatalf("expected 3 plugins, got %d: %v", len(order), order)
	}

	// A must come before B, B must come before C
	idxA := indexOf(t, order, "a")
	idxB := indexOf(t, order, "b")
	idxC := indexOf(t, order, "c")
	if idxA >= idxB {
		t.Errorf("a (index %d) should come before b (index %d)", idxA, idxB)
	}
	if idxB >= idxC {
		t.Errorf("b (index %d) should come before c (index %d)", idxB, idxC)
	}
}

func TestDependencyResolver_Circular(t *testing.T) {
	// A → B → A (mutual dependency)
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("a", "b"))
	dr.AddManifest(makeManifest("b", "a"))

	_, err := dr.Resolve()
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}

	var ce *ErrCircularDependency
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ErrCircularDependency, got %T: %v", err, err)
	}

	if len(ce.Cycle) != 2 {
		t.Errorf("expected 2 plugins in cycle, got %d: %v", len(ce.Cycle), ce.Cycle)
	}

	// Cycle should contain both a and b
	sorted := make([]string, len(ce.Cycle))
	copy(sorted, ce.Cycle)
	sort.Strings(sorted)
	if sorted[0] != "a" || sorted[1] != "b" {
		t.Errorf("cycle should contain [a b], got %v", sorted)
	}
}

func TestDependencyResolver_Missing(t *testing.T) {
	// A depends on B and C, but C is not installed
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("a", "b", "c"))
	dr.AddManifest(makeManifest("b"))

	_, err := dr.Resolve()
	if err == nil {
		t.Fatal("expected error for missing dependency")
	}

	var me *ErrMissingDependency
	if !errors.As(err, &me) {
		t.Fatalf("expected *ErrMissingDependency, got %T: %v", err, err)
	}

	if me.PluginID != "a" {
		t.Errorf("expected PluginID 'a', got %q", me.PluginID)
	}
	if me.Missing != "c" {
		t.Errorf("expected Missing 'c', got %q", me.Missing)
	}
}

func TestDependencyResolver_Complex(t *testing.T) {
	// Diamond: A → B, A → C, B → D, C → D, plus standalone E
	//
	//      A
	//     / \
	//    B   C
	//     \ /
	//      D
	//  E (standalone)
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("a", "b", "c"))
	dr.AddManifest(makeManifest("b", "d"))
	dr.AddManifest(makeManifest("c", "d"))
	dr.AddManifest(makeManifest("d"))
	dr.AddManifest(makeManifest("e"))

	order, err := dr.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	if len(order) != 5 {
		t.Fatalf("expected 5 plugins, got %d: %v", len(order), order)
	}

	// Verify ordering constraints
	idxD := indexOf(t, order, "d")
	idxB := indexOf(t, order, "b")
	idxC := indexOf(t, order, "c")
	idxA := indexOf(t, order, "a")

	if idxD >= idxB {
		t.Errorf("d (index %d) should come before b (index %d)", idxD, idxB)
	}
	if idxD >= idxC {
		t.Errorf("d (index %d) should come before c (index %d)", idxD, idxC)
	}
	if idxB >= idxA {
		t.Errorf("b (index %d) should come before a (index %d)", idxB, idxA)
	}
	if idxC >= idxA {
		t.Errorf("c (index %d) should come before a (index %d)", idxC, idxA)
	}

	// E must be present somewhere
	indexOf(t, order, "e")
}

// --- Edge cases ---

func TestDependencyResolver_Empty(t *testing.T) {
	dr := NewDependencyResolver()
	order, err := dr.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 0 {
		t.Fatalf("expected empty order, got %v", order)
	}
}

func TestDependencyResolver_SinglePlugin(t *testing.T) {
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("solo"))

	order, err := dr.Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "solo" {
		t.Fatalf("expected [solo], got %v", order)
	}
}

func TestDependencyResolver_SelfDependency(t *testing.T) {
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("loopy", "loopy"))

	_, err := dr.Resolve()
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}

	var ce *ErrCircularDependency
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ErrCircularDependency, got %T: %v", err, err)
	}
}

func TestDependencyResolver_Validate_MissingDep(t *testing.T) {
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("a", "nonexistent"))

	err := dr.Validate()
	if err == nil {
		t.Fatal("expected error from Validate")
	}

	var me *ErrMissingDependency
	if !errors.As(err, &me) {
		t.Fatalf("expected *ErrMissingDependency, got %T: %v", err, err)
	}
}

func TestDependencyResolver_Validate_AllPresent(t *testing.T) {
	dr := NewDependencyResolver()
	dr.AddManifest(makeManifest("a", "b"))
	dr.AddManifest(makeManifest("b"))

	if err := dr.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func indexOf(t *testing.T, slice []string, target string) int {
	t.Helper()
	for i, s := range slice {
		if s == target {
			return i
		}
	}
	t.Fatalf("%q not found in %v", target, slice)
	return -1
}
