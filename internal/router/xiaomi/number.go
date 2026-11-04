package xiaomi

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// flexNum is a number the router may send as a JSON number or as a numeric
// string, depending on the field and the firmware version. Anything else
// (null, empty string, bool, object) leaves it unset rather than failing the
// whole answer.
type flexNum struct {
	s   string
	set bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (n *flexNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*n = flexNum{}
	if len(b) == 0 {
		return nil
	}
	switch c := b[0]; {
	case c == '"':
		var s string
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
		if s = strings.TrimSpace(s); s != "" {
			*n = flexNum{s: s, set: true}
		}
	case c == '-' || (c >= '0' && c <= '9'):
		*n = flexNum{s: string(b), set: true}
	}
	return nil
}

func (n flexNum) float() (float64, bool) {
	if !n.set {
		return 0, false
	}
	f, err := strconv.ParseFloat(n.s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func (n flexNum) int() (int64, bool) {
	if !n.set {
		return 0, false
	}
	if i, err := strconv.ParseInt(n.s, 10, 64); err == nil {
		return i, true
	}
	f, ok := n.float()
	if !ok || f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}

// text is the value for error messages; the router's numeric codes are not
// sensitive, and the length cap keeps a malformed answer out of logs.
func (n flexNum) text() string {
	if !n.set {
		return "missing"
	}
	if len(n.s) > 16 {
		return n.s[:16] + "..."
	}
	return n.s
}

// flag reads a 0/1 field.
func (n flexNum) flag() *bool {
	i, ok := n.int()
	if !ok {
		return nil
	}
	b := i != 0
	return &b
}

// nonNegative returns the value when it is a usable measurement.
func (n flexNum) nonNegative() *float64 {
	f, ok := n.float()
	if !ok || f < 0 {
		return nil
	}
	return &f
}

func (n flexNum) count() *int {
	i, ok := n.int()
	if !ok || i < 0 || i > math.MaxInt32 {
		return nil
	}
	v := int(i)
	return &v
}

func (n flexNum) bytes() *int64 {
	i, ok := n.int()
	if !ok || i < 0 {
		return nil
	}
	return &i
}
