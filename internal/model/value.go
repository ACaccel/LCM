package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Normalize converts v into the canonical JSON value space: nil, bool,
// string, int64, float64, []any and map[string]any. Integral numbers become
// int64 so that values read back from storage have the same Go types as
// values produced in memory, which keeps expression results identical
// before and after a restart. It fails for values that cannot be encoded as
// JSON, such as maps with non-string keys.
func Normalize(v any) (any, error) {
	n, _, err := NormalizeJSON(v)
	return n, err
}

// NormalizeJSON is Normalize that also returns the JSON encoding.
func NormalizeJSON(v any) (any, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("value is not JSON-compatible (maps need string keys): %w", err)
	}
	n, err := DecodeJSONValue(b)
	if err != nil {
		return nil, nil, err
	}
	return n, b, nil
}

// DecodeJSONValue decodes JSON into the canonical value space.
func DecodeJSONValue(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode JSON value: %w", err)
	}
	return FixNumbers(v), nil
}

// FixNumbers replaces json.Number values (from a decoder with UseNumber)
// inside v with int64 or float64, in place where possible.
func FixNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return i
		}
		f, err := x.Float64()
		if err != nil {
			// Unreachable for numbers produced by encoding/json, which only
			// emits finite values.
			return string(x)
		}
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = FixNumbers(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = FixNumbers(e)
		}
		return x
	}
	return v
}
