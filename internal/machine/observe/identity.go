package observe

import (
	"encoding/hex"
	"fmt"
	"strings"
)

func parseBootID(b []byte) (string, error) {
	s := strings.TrimSpace(string(b))
	if len(s) == 36 {
		if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
			return "", errInvalid
		}
		s = strings.ReplaceAll(s, "-", "")
	}
	if !hexID(s, 32) || strings.Trim(s, "0") == "" {
		return "", errInvalid
	}
	s = strings.ToLower(s)
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:]), nil
}

func hexID(s string, length int) bool {
	if len(s) != length {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
