package observe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
)

var journalNow = time.Unix(1793494810, 0).UTC()

func journalFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/journal.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newTestJournal(t *testing.T, runner execx.Runner, config JournalConfig) *Journal {
	t.Helper()
	j, err := NewJournal(config, runner, clock.NewFake(journalNow))
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func journalRow(cursor, unit string, second int64) string {
	fields := map[string]string{"__CURSOR": cursor, "__REALTIME_TIMESTAMP": strconv.FormatInt(second*1000000, 10), "_BOOT_ID": "11111111222233334444555555555555", "_SYSTEMD_UNIT": unit, "PRIORITY": "6"}
	b, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

func TestJournalNormalizesSelectedFieldsAndCrossBoot(t *testing.T) {
	runner := (&execx.Fake{}).Reply("journalctl", journalFixture(t), 0)
	j := newTestJournal(t, runner, JournalConfig{Units: []string{"demo.service"}})
	var reader JournalReader = j
	batch, err := reader.Poll(context.Background(), JournalRequest{HistoricalBefore: time.Unix(1793494801, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Coverage.State != Partial || len(batch.Coverage.Limitations) < 5 || batch.FieldVersion != FieldVersion || len(batch.Events) != 2 || batch.Scanned != 3 || batch.NextCursor != "opaque:third;no-order=0" || batch.More {
		t.Fatalf("batch: %+v", batch)
	}
	a, b := batch.Events[0], batch.Events[1]
	if a.BootID != "11111111-2222-3333-4444-555555555555" || b.BootID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || !a.Historical || b.Historical || a.Severity != SeverityInfo || b.Severity != SeverityError || a.Classification != UnitLog || b.Unit != "demo.service" || a.Source != "system-journal" || !a.ReceivedAt.Equal(journalNow) {
		t.Fatalf("events: %+v", batch.Events)
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"MESSAGE", "_CMDLINE", "ENV", "STACKTRACE", "synthetic private", "unrelated.service"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("disallowed data retained: %s", s)
		}
	}
	calls := runner.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls: %v", calls)
	}
	cmd := calls[0]
	for _, arg := range []string{"journalctl", "--system", "--no-pager", "--output=json", "--lines=+257", "--unit=demo.service"} {
		if !slices.Contains(cmd.Argv, arg) {
			t.Fatalf("missing %s: %v", arg, cmd.Argv)
		}
	}
	if cmd.Timeout <= 0 || cmd.MaxOutput <= 0 || cmd.Shell != "" || !slices.Contains(cmd.Env, "LC_ALL=C") {
		t.Fatalf("command not bounded: %+v", cmd)
	}
	for _, arg := range cmd.Argv {
		if strings.Contains(arg, "cursor-file") || strings.Contains(arg, "--follow") || arg == "--quiet" || strings.Contains(arg, "MESSAGE") {
			t.Fatalf("unsafe argument: %s", arg)
		}
	}
	if !slices.Contains(cmd.Argv, "--since="+journalTime(journalNow.Add(-24*time.Hour))) || !slices.Contains(cmd.Argv, "--until="+journalTime(journalNow)) {
		t.Fatalf("missing time bounds: %v", cmd.Argv)
	}
}

func TestJournalInclusiveCursorAndFilteredCheckpoint(t *testing.T) {
	runner := (&execx.Fake{}).Reply("journalctl", journalFixture(t), 0)
	j := newTestJournal(t, runner, JournalConfig{Units: []string{"demo.service"}})
	batch, err := j.Poll(context.Background(), JournalRequest{Cursor: "opaque:anchor;no-order=9", Historical: true})
	if err != nil || len(batch.Events) != 1 || batch.Events[0].Cursor != "opaque:second;no-order=1" || !batch.Events[0].Historical || batch.Scanned != 2 || batch.NextCursor != "opaque:third;no-order=0" {
		t.Fatalf("batch: %+v %v", batch, err)
	}
	if !slices.Contains(runner.Calls()[0].Argv, "--cursor=opaque:anchor;no-order=9") {
		t.Fatal("cursor was interpreted or changed")
	}
}

func TestJournalBoundedPaginationAndReplay(t *testing.T) {
	rows := []string{journalRow("opaque:z", "demo.service", 1793494800), journalRow("opaque:a", "demo.service", 1793494801), journalRow("opaque:m", "demo.service", 1793494802)}
	runner := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(c execx.Cmd) (execx.Result, error) {
		start := 0
		if slices.Contains(c.Argv, "--cursor=opaque:a") {
			start = 1
		}
		if slices.Contains(c.Argv, "--cursor=opaque:m") {
			start = 2
		}
		return execx.Result{Output: strings.Join(rows[start:], "")}, nil
	})
	j := newTestJournal(t, runner, JournalConfig{Units: []string{"demo.service"}, Limits: JournalLimits{Events: 2}})
	first, err := j.Poll(context.Background(), JournalRequest{})
	if err != nil || !first.More || len(first.Events) != 2 || first.NextCursor != "opaque:a" || first.Coverage.Reason != ReasonLimit || len(first.Gaps) != 0 {
		t.Fatalf("first: %+v %v", first, err)
	}
	replay, err := j.Poll(context.Background(), JournalRequest{})
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("uncommitted batch cannot be replayed: %+v %v", replay, err)
	}
	second, err := j.Poll(context.Background(), JournalRequest{Cursor: first.NextCursor})
	if err != nil || second.More || len(second.Events) != 1 || second.Events[0].Cursor != "opaque:m" || second.NextCursor != "opaque:m" {
		t.Fatalf("second: %+v %v", second, err)
	}
	empty, err := j.Poll(context.Background(), JournalRequest{Cursor: second.NextCursor})
	if err != nil || len(empty.Events) != 0 || empty.NextCursor != second.NextCursor || len(empty.Gaps) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
}

func TestJournalFailuresNeverAdvanceCheckpoint(t *testing.T) {
	fixture := journalFixture(t)
	for _, tc := range []struct {
		name, output string
		err          error
		truncated    bool
		reason       Reason
		state        CoverageState
	}{
		{name: "truncated tail", output: fixture, truncated: true, reason: ReasonTruncated, state: Unavailable},
		{name: "oversized runner output", output: strings.Repeat("x", (1<<20)+1), reason: ReasonTruncated, state: Unavailable},
		{name: "malformed JSON", output: strings.Split(fixture, "\n")[0] + "\n{not-json}", reason: ReasonInvalid, state: Unavailable},
		{name: "permission", output: "Permission denied synthetic private journal details", err: &execx.ExitError{Code: 1}, reason: ReasonPermission, state: Unavailable},
		{name: "partial permission zero exit", output: fixture + "Hint: You are currently not seeing messages from other users and the system.\n", reason: ReasonPermission, state: Unavailable},
		{name: "journal file truncated", output: "Journal file is truncated, ignoring file", err: &execx.ExitError{Code: 1}, reason: ReasonTruncated, state: Unavailable},
		{name: "rotation", output: "Journal file was rotated or vacuumed", err: &execx.ExitError{Code: 1}, reason: ReasonRotation, state: Unavailable},
		{name: "invalid cursor", output: "Failed to seek to cursor: Invalid argument", err: &execx.ExitError{Code: 1}, reason: ReasonCursor, state: Unavailable},
		{name: "missing anchor", output: journalRow("replacement", "demo.service", 1793494805), reason: ReasonCursor, state: Unavailable},
		{name: "empty missing anchor", reason: ReasonCursor, state: Unavailable},
		{name: "unsupported flags", output: "unrecognized option '--output-fields'", err: &execx.ExitError{Code: 1}, reason: ReasonVersion, state: Unsupported},
		{name: "missing program", err: exec.ErrNotFound, reason: ReasonMissing, state: Unsupported},
		{name: "missing journal", output: "No journal files were found.", reason: ReasonMissing, state: Unavailable},
		{name: "timeout", err: execx.ErrTimeout, reason: ReasonTimeout, state: Unavailable},
		{name: "generic command", err: &execx.ExitError{Code: 1}, reason: ReasonCommand, state: Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
				return execx.Result{Output: tc.output, Truncated: tc.truncated}, tc.err
			})
			j := newTestJournal(t, runner, JournalConfig{Units: []string{"demo.service"}})
			b, err := j.Poll(context.Background(), JournalRequest{Cursor: "opaque:anchor;no-order=9"})
			if err != nil || b.NextCursor != "opaque:anchor;no-order=9" || len(b.Events) != 0 || b.Scanned != 0 || len(b.Gaps) != 1 || b.Gaps[0].Reason != tc.reason || b.Coverage.State != tc.state || b.Coverage.Reason != tc.reason {
				t.Fatalf("failed batch: %+v %v", b, err)
			}
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "synthetic private") {
				t.Fatal("raw diagnostic exposed")
			}
		})
	}
}

func TestJournalSinceClampAndReducedLimits(t *testing.T) {
	runner := (&execx.Fake{}).Reply("journalctl", "", 0)
	j := newTestJournal(t, runner, JournalConfig{Units: []string{"demo.service", "other.service"}})
	since := journalNow.Add(-48 * time.Hour)
	batch, err := j.Poll(context.Background(), JournalRequest{Since: since, Units: []string{"demo.service"}, Limits: JournalLimits{Events: 1, OutputBytes: 2048, Lookback: time.Hour}})
	if err != nil || len(batch.Gaps) != 1 || batch.Gaps[0].Reason != ReasonLookback || !batch.Gaps[0].From.Equal(since) || !batch.Gaps[0].To.Equal(journalNow.Add(-time.Hour)) || batch.NextCursor != "" || batch.Coverage.Reason != ReasonLookback {
		t.Fatalf("clamped batch: %+v %v", batch, err)
	}
	cmd := runner.Calls()[0]
	if cmd.MaxOutput != 2048 || !slices.Contains(cmd.Argv, "--lines=+2") || slices.Contains(cmd.Argv, "--unit=other.service") || !slices.Contains(cmd.Argv, "--since="+journalTime(journalNow.Add(-time.Hour))) {
		t.Fatalf("request limits ignored: %+v", cmd)
	}
}

func TestJournalInvalidConfigFailsWithoutIO(t *testing.T) {
	for name, config := range map[string]JournalConfig{
		"field version":     {FieldVersion: 2},
		"unit glob":         {Units: []string{"*.service"}},
		"negative events":   {Limits: JournalLimits{Events: -1}},
		"too many events":   {Limits: JournalLimits{Events: 2049}},
		"output":            {Limits: JournalLimits{OutputBytes: maxOutput + 1}},
		"lookback":          {Limits: JournalLimits{Lookback: 8 * 24 * time.Hour}},
		"negative lookback": {Limits: JournalLimits{Lookback: -time.Second}},
		"timeout":           {CommandTimeout: 31 * time.Second},
		"missing runner":    {Units: []string{"demo.service"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewJournal(config, nil, nil); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestJournalInvalidRequestAndDisabledReader(t *testing.T) {
	runner := &execx.Fake{}
	config := JournalConfig{Units: []string{"demo.service"}}
	j := newTestJournal(t, runner, config)
	config.Units[0] = "mutated.service"
	for name, r := range map[string]JournalRequest{
		"not allowed":     {Units: []string{"other.service"}},
		"duplicate":       {Units: []string{"demo.service", "demo.service"}},
		"cursor newline":  {Cursor: "opaque\n--all"},
		"cursor too long": {Cursor: strings.Repeat("c", 4097)},
		"events":          {Limits: JournalLimits{Events: 257}},
		"negative events": {Limits: JournalLimits{Events: -1}},
		"bytes":           {Limits: JournalLimits{OutputBytes: 1023}},
		"lookback":        {Limits: JournalLimits{Lookback: 25 * time.Hour}},
		"future since":    {Since: journalNow.Add(time.Second)},
		"before epoch":    {Since: time.Unix(-1, 0)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := j.Poll(context.Background(), r); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	if len(runner.Calls()) != 0 {
		t.Fatal("invalid request executed a command")
	}
	disabled := newTestJournal(t, nil, JournalConfig{})
	b, err := disabled.Poll(context.Background(), JournalRequest{})
	if err != nil || b.Coverage.State != Disabled || b.Coverage.Reason != ReasonNotConfigured {
		t.Fatalf("empty allowlist must disable polling: %+v %v", b, err)
	}
	if _, err := disabled.Poll(context.Background(), JournalRequest{Units: []string{"demo.service"}}); err == nil {
		t.Fatal("request escalated disabled allowlist")
	}
}

func TestJournalCancellationNeverAdvancesCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
		cancel()
		return execx.Result{Output: journalRow("old", "demo.service", 1793494800) + journalRow("new", "demo.service", 1793494801)}, nil
	})
	j := newTestJournal(t, runner, JournalConfig{Units: []string{"demo.service"}})
	b, err := j.Poll(ctx, JournalRequest{Cursor: "old"})
	if !errors.Is(err, context.Canceled) || b.NextCursor != "old" || len(b.Events) != 0 || b.Coverage.Reason != ReasonCancelled {
		t.Fatalf("cancelled batch: %+v %v", b, err)
	}
	runner.Reset()
	if _, err := j.Poll(ctx, JournalRequest{}); !errors.Is(err, context.Canceled) || len(runner.Calls()) != 0 {
		t.Fatal("cancelled request executed command")
	}
}

func TestJournalStrictJSONAndScalarFields(t *testing.T) {
	row := strings.TrimSpace(journalRow("one", "demo.service", 1793494800))
	for name, output := range map[string]string{
		"array object":          "[]",
		"trailing token":        row + " true",
		"duplicate key":         strings.Replace(row, "\"__CURSOR\":\"one\"", "\"__CURSOR\":\"one\",\"__CURSOR\":\"two\"", 1),
		"cursor null":           strings.Replace(row, "\"one\"", "null", 1),
		"cursor array":          strings.Replace(row, "\"one\"", "[\"one\",\"two\"]", 1),
		"binary cursor":         strings.Replace(row, "\"one\"", "[1,2,3]", 1),
		"numeric time":          strings.Replace(row, "\"1793494800000000\"", "1793494800000000", 1),
		"negative time":         strings.Replace(row, "1793494800000000", "-1", 1),
		"time overflow":         strings.Replace(row, "1793494800000000", "999999999999999999999999", 1),
		"boot invalid":          strings.Replace(row, "11111111222233334444555555555555", "bad", 1),
		"priority invalid":      strings.Replace(row, "\"PRIORITY\":\"6\"", "\"PRIORITY\":\"8\"", 1),
		"missing cursor":        strings.Replace(row, "\"__CURSOR\":\"one\",", "", 1),
		"duplicate cursor rows": row + "\n" + row,
		"invalid UTF8":          row + string([]byte{255}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseJournal(output, journalNow); err == nil {
				t.Fatal("malformed journal accepted")
			}
		})
	}
}
