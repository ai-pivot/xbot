package agent

import "encoding/json"

// ExtractJSONString extracts a string value for the given key from a JSON object.
// Returns empty string if not found or parsing fails.
func ExtractJSONString(jsonStr, key string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return ""
	}
	val, ok := obj[key].(string)
	if !ok {
		return ""
	}
	return val
}
