package storage

import (
	"encoding/json"
	"strings"
)

func normalizeAPIKeyMethod(method string) string {
	if strings.EqualFold(strings.TrimSpace(method), "round_robin") {
		return "round_robin"
	}
	return "off"
}

func encodeAPIKeys(keys []string) string {
	clean := make([]string, 0, len(keys))
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			clean = append(clean, key)
		}
	}
	raw, _ := json.Marshal(clean)
	return string(raw)
}

func decodeAPIKeys(raw string) []string {
	var keys []string
	if json.Unmarshal([]byte(raw), &keys) != nil {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			out = append(out, key)
		}
	}
	return out
}
