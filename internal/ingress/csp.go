package ingress

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Limits of a Content-Security-Policy.
const (
	MaxCSP           = 4096
	maxCSPDirectives = 64
	maxCSPToken      = 512
)

var (
	cspName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	// cspKeyword is a quoted source expression: 'self', 'none',
	// 'unsafe-inline', 'nonce-...', 'sha256-...' and the like.
	cspKeyword = regexp.MustCompile(`^'[A-Za-z0-9+/=_-]{1,200}'$`)
	// cspSource is an unquoted source expression or value: a scheme
	// ("data:"), a host source ("https://*.example.org:443/path") or a
	// reporting URL. It has no quotes, backslashes, "$", braces, ";", ",",
	// "#", whitespace or characters outside printable ASCII.
	cspSource = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9*.:/?&=%_~+@!-]*$`)
)

// ParseCSP checks a Content-Security-Policy and returns it in canonical
// form. The grammar is a strict subset of CSP that nginx can carry inside
// a double-quoted string without any interpretation:
//
//	policy    = directive *( ";" directive ) [ ";" ]
//	directive = *SP name *( 1*SP value ) *SP
//	name      = [a-z][a-z0-9-]{0,63}
//	value     = "'" [A-Za-z0-9+/=_-]{1,200} "'"      ('self', 'nonce-...')
//	          / [A-Za-z0-9*] *[A-Za-z0-9*.:/?&=%_~+@!-] (data:, https://x.org/p)
//
// Only spaces separate values (no tabs or newlines), a directive name
// appears once, there are at most 64 directives, a value has at most 512
// characters and the policy at most 4096. The canonical form joins
// directives with "; " and values with one space. So the characters that
// nginx would interpret ('"', '\', '$', '{', '}') and line breaks can never
// appear.
func ParseCSP(s string) (string, error) {
	if len(s) > MaxCSP {
		return "", fmt.Errorf("must be at most %d characters", MaxCSP)
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return "", errors.New("must be printable ASCII on one line (no tabs, line breaks or other characters)")
		}
	}
	parts := strings.Split(s, ";")
	if strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return "", errors.New("must contain at least one directive")
	}
	if len(parts) > maxCSPDirectives {
		return "", fmt.Errorf("must have at most %d directives", maxCSPDirectives)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			return "", fmt.Errorf("directive %d is empty", i+1)
		}
		name := fields[0]
		if !cspName.MatchString(name) {
			return "", fmt.Errorf("directive %d: %q is not a directive name (lower-case letters, digits and -)", i+1, printable(name))
		}
		if seen[name] {
			return "", fmt.Errorf("directive %s appears twice", name)
		}
		seen[name] = true
		for _, v := range fields[1:] {
			if len(v) > maxCSPToken || !cspKeyword.MatchString(v) && !cspSource.MatchString(v) {
				return "", fmt.Errorf("directive %s: %q is not a source expression of the accepted form ('keyword' or a scheme, host or URL without quotes, backslashes, $, braces, commas or #)", name, printable(v))
			}
		}
		out = append(out, strings.Join(fields, " "))
	}
	return strings.Join(out, "; "), nil
}
