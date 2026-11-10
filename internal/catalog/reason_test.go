package catalog

import (
	"errors"
	"strings"
	"testing"
)

func TestWithReasonIsSanitizedAndBounded(t *testing.T) {
	base := errors.New("exit status 128")
	for name, tc := range map[string]struct {
		output string
		want   string
	}{
		"last line":      {"Cloning...\nfatal: detected dubious ownership\n", "exit status 128: fatal: detected dubious ownership"},
		"control codes":  {"fatal: \x1b[31mred\x1b[0m\rend\n", "exit status 128: fatal: ?[31mred?[0m?end"},
		"no output":      {"", "exit status 128"},
		"blank output":   {" \n\n", "exit status 128"},
		"invalid UTF-8":  {"fatal: \xff\xfe bad", "exit status 128: fatal: ? bad"},
		"long line":      {"fatal: " + strings.Repeat("x", 500), "exit status 128: fatal: " + strings.Repeat("x", reasonLimit-7) + " ..."},
		"multiple lines": {"a\nb\n\nfatal: last", "exit status 128: fatal: last"},
	} {
		got := withReason(base, tc.output)
		if got.Error() != tc.want || !errors.Is(got, base) {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
