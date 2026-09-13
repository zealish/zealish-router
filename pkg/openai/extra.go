package openai

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

// extras holds wire fields this gateway does not model explicitly — tools,
// tool_calls, response_format, logprobs, provider extensions. A gateway that
// drops them corrupts the conversation, so they are carried verbatim.
type extras = map[string]json.RawMessage

// knownCache memoises the declared JSON field names of a struct type.
var knownCache sync.Map // reflect.Type -> map[string]struct{}

func knownFields(t reflect.Type) map[string]struct{} {
	if cached, ok := knownCache.Load(t); ok {
		return cached.(map[string]struct{})
	}
	names := make(map[string]struct{}, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		names[name] = struct{}{}
	}
	knownCache.Store(t, names)
	return names
}

// decodeExtras unmarshals data into alias — a copy of T without its methods,
// which is what stops the decoder recursing — and returns every field that T
// does not declare.
func decodeExtras[T any](data []byte, alias any) (extras, error) {
	if err := json.Unmarshal(data, alias); err != nil {
		return nil, err
	}
	var all extras
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	for name := range knownFields(reflect.TypeFor[T]()) {
		delete(all, name)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// encodeExtras marshals alias and folds the unmodelled fields back in. A
// declared field always wins, so a value the router rewrote cannot be shadowed.
func encodeExtras(alias any, ex extras) ([]byte, error) {
	base, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	if len(ex) == 0 {
		return base, nil
	}

	var merged extras
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for name, value := range ex {
		if _, declared := merged[name]; !declared {
			merged[name] = value
		}
	}
	return json.Marshal(merged)
}
