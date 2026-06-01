package store

import "encoding/json"

// encodeJSON marshals a header map to a JSON string for storage, defaulting to
// an empty object on error.
func encodeJSON(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// decodeJSON parses a stored JSON object back into a header map.
func decodeJSON(s string) map[string]string {
	m := map[string]string{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}
