package observe

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func validCursor(s string) bool {
	if len(s) == 0 || len(s) > 4096 {
		return false
	}
	for _, c := range s {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

// parseJournal accepts JSONL objects, unique keys, and scalar strings for the
// v1 fields. journalctl arrays (binary/multiple values) and nulls are rejected
// for selected fields rather than guessed. Unknown fields are discarded.
func parseJournal(output string, received time.Time) ([]JournalEvent, error) {
	if !utf8.ValidString(output) {
		return nil, errInvalid
	}
	var rows []JournalEvent
	seen := make(map[string]bool)
	for line := range strings.SplitSeq(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields, err := journalFields(line)
		if err != nil {
			return nil, err
		}
		event, err := journalEvent(fields, received)
		if err != nil || seen[event.Cursor] {
			return nil, errInvalid
		}
		seen[event.Cursor] = true
		rows = append(rows, event)
	}
	return rows, nil
}

func journalFields(line string) (map[string]string, error) {
	decoder := json.NewDecoder(strings.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errInvalid
	}
	values := make(map[string]string)
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errInvalid
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return nil, errInvalid
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, errInvalid
		}
		switch key {
		case "__CURSOR", "__REALTIME_TIMESTAMP", "__MONOTONIC_TIMESTAMP", "_BOOT_ID", "_SYSTEMD_UNIT", "UNIT", "_PID", "_UID", "PRIORITY":
			var value string
			if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
				return nil, errInvalid
			}
			values[key] = value
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errInvalid
	}
	return values, nil
}

func journalEvent(v map[string]string, received time.Time) (JournalEvent, error) {
	cursor := v["__CURSOR"]
	if !validCursor(cursor) {
		return JournalEvent{}, errInvalid
	}
	micros, err := strconv.ParseInt(v["__REALTIME_TIMESTAMP"], 10, 64)
	if err != nil || micros < 0 || !digits(v["__REALTIME_TIMESTAMP"]) {
		return JournalEvent{}, errInvalid
	}
	boot, err := parseBootID([]byte(v["_BOOT_ID"]))
	if err != nil {
		return JournalEvent{}, errInvalid
	}
	for _, key := range []string{"__MONOTONIC_TIMESTAMP", "_PID", "_UID"} {
		if value, ok := v[key]; ok {
			if _, err := strconv.ParseUint(value, 10, 64); err != nil || !digits(value) {
				return JournalEvent{}, errInvalid
			}
		}
	}
	for _, key := range []string{"_SYSTEMD_UNIT", "UNIT"} {
		if value, ok := v[key]; ok && !validUnit(value) {
			return JournalEvent{}, errInvalid
		}
	}
	unit := v["_SYSTEMD_UNIT"]
	// UNIT is supplied by the sender. Only PID 1 running as root may attribute
	// manager records to another unit; ordinary log writers cannot spoof it.
	if v["_PID"] == "1" && v["_UID"] == "0" && v["UNIT"] != "" {
		unit = v["UNIT"]
	}
	severity := SeverityUnknown
	if priority, exists := v["PRIORITY"]; exists {
		if len(priority) != 1 || priority[0] < '0' || priority[0] > '7' {
			return JournalEvent{}, errInvalid
		}
		levels := [...]Severity{SeverityEmergency, SeverityAlert, SeverityCritical, SeverityError, SeverityWarning, SeverityNotice, SeverityInfo, SeverityDebug}
		severity = levels[int(priority[0]-'0')]
	}
	return JournalEvent{FieldVersion: FieldVersion, Cursor: cursor, Source: "system-journal",
		SourceTime: time.UnixMicro(micros).UTC(), ReceivedAt: received,
		BootID: boot, Unit: unit, Severity: severity, Classification: UnitLog}, nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
