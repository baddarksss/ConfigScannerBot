package engine

import "encoding/json"

// jsonStrField extracts the first non-empty string field among the given
// keys from a JSON object string.
func jsonStrField(jsonStr string, keys ...string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func jsonIntField(jsonStr string, key string) int {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := atoiOK(v)
		return n
	}
	return 0
}
