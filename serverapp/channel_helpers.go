package serverapp

import "fmt"

// ---------------------------------------------------------------------------
// Shared helpers for channel config parsing
// ---------------------------------------------------------------------------

func strVal(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func boolVal(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
