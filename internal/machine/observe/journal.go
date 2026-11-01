package observe

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
)

// JournalLimits bound one polling call. Zero values select the configured
// maxima. Limits may be reduced by a request but cannot increase host limits.
type JournalLimits struct {
	Events      int
	OutputBytes int
	Lookback    time.Duration
}

type JournalConfig struct {
	Units          []string
	Limits         JournalLimits
	CommandTimeout time.Duration
	FieldVersion   int
}

type JournalRequest struct {
	// Cursor is opaque and belongs to an unchanged source/unit selection. The
	// caller must reset it explicitly when changing that selection or accepting
	// a gap. Since can bound an initial read; it never widens Lookback.
	Cursor string
	Since  time.Time
	Units  []string
	Limits JournalLimits
	// Historical marks a whole recovery batch. HistoricalBefore additionally
	// tags individual events older than a caller-owned live watermark.
	Historical       bool
	HistoricalBefore time.Time
}

type JournalReader interface {
	Poll(context.Context, JournalRequest) (JournalBatch, error)
}

type Severity string

const (
	SeverityUnknown   Severity = "unknown"
	SeverityEmergency Severity = "emergency"
	SeverityAlert     Severity = "alert"
	SeverityCritical  Severity = "critical"
	SeverityError     Severity = "error"
	SeverityWarning   Severity = "warning"
	SeverityNotice    Severity = "notice"
	SeverityInfo      Severity = "info"
	SeverityDebug     Severity = "debug"
)

type Classification string

// UnitLog intentionally makes no OOM, crash, or failure inference from MESSAGE.
const UnitLog Classification = "unit_log"

type JournalEvent struct {
	FieldVersion   int            `json:"fieldVersion"`
	Cursor         string         `json:"cursor"`
	Source         string         `json:"source"`
	SourceTime     time.Time      `json:"sourceTime"`
	ReceivedAt     time.Time      `json:"receivedAt"`
	BootID         string         `json:"bootId"`
	Unit           string         `json:"unit"`
	Severity       Severity       `json:"severity"`
	Classification Classification `json:"classification"`
	Historical     bool           `json:"historical"`
}

// JournalGap records an interval that cannot be verified. No lost-event count
// is available from this reader. Rotation/vacuum cannot always be distinguished
// from inaccessible retention or a changed unit selection.
type JournalGap struct {
	Reason Reason    `json:"reason"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
}

type JournalBatch struct {
	FieldVersion int            `json:"fieldVersion"`
	Events       []JournalEvent `json:"events"`
	// NextCursor advances over validated scanned rows, including intentionally
	// filtered rows. Persist it atomically with events and gaps, then publish.
	// On any incomplete/invalid command output it remains the request cursor.
	NextCursor string       `json:"nextCursor"`
	Coverage   Coverage     `json:"coverage"`
	Gaps       []JournalGap `json:"gaps"`
	More       bool         `json:"more"`
	Scanned    int          `json:"scanned"`
}

// Journal runs bounded polling commands without a follow goroutine, cursor
// files, elevated permissions, or raw message retention. It reads only system
// journal entries the invoking account can access, for explicit selected units.
type Journal struct {
	config JournalConfig
	runner execx.Runner
	clock  clock.Clock
}

func NewJournal(config JournalConfig, runner execx.Runner, c clock.Clock) (*Journal, error) {
	if err := validateUnits(config.Units); err != nil {
		return nil, err
	}
	if config.FieldVersion == 0 {
		config.FieldVersion = FieldVersion
	}
	if config.FieldVersion != FieldVersion {
		return nil, errors.New("unsupported journal field version")
	}
	var err error
	config.CommandTimeout, config.Limits.OutputBytes, err = commandLimits(config.CommandTimeout, config.Limits.OutputBytes)
	if err != nil {
		return nil, err
	}
	if config.Limits.Events == 0 {
		config.Limits.Events = 256
	}
	if config.Limits.Lookback == 0 {
		config.Limits.Lookback = 24 * time.Hour
	}
	if config.Limits.Events < 1 || config.Limits.Events > 2048 {
		return nil, errors.New("journal event limit must be between 1 and 2048")
	}
	if config.Limits.Lookback < time.Second || config.Limits.Lookback > 7*24*time.Hour {
		return nil, errors.New("journal lookback must be between 1s and 7d")
	}
	if len(config.Units) > 0 && runner == nil {
		return nil, errors.New("journal observations require a runner")
	}
	config.Units = slices.Clone(config.Units)
	if c == nil {
		c = clock.Real()
	}
	return &Journal{config: config, runner: runner, clock: c}, nil
}

func (j *Journal) request(r JournalRequest) (JournalRequest, error) {
	if r.Cursor != "" && !validCursor(r.Cursor) {
		return r, errors.New("invalid journal cursor")
	}
	if len(r.Units) == 0 {
		r.Units = slices.Clone(j.config.Units)
	}
	if err := validateUnits(r.Units); err != nil {
		return r, err
	}
	for _, unit := range r.Units {
		if !slices.Contains(j.config.Units, unit) {
			return r, errors.New("journal unit is not allowlisted")
		}
	}
	limits := &r.Limits
	if limits.Events == 0 {
		limits.Events = j.config.Limits.Events
	}
	if limits.OutputBytes == 0 {
		limits.OutputBytes = j.config.Limits.OutputBytes
	}
	if limits.Lookback == 0 {
		limits.Lookback = j.config.Limits.Lookback
	}
	if limits.Events < 1 || limits.Events > j.config.Limits.Events || limits.OutputBytes < 1024 || limits.OutputBytes > j.config.Limits.OutputBytes || limits.Lookback < time.Second || limits.Lookback > j.config.Limits.Lookback {
		return r, errors.New("journal request exceeds configured limits")
	}
	return r, nil
}

func (j *Journal) Poll(ctx context.Context, request JournalRequest) (JournalBatch, error) {
	r, err := j.request(request)
	b := JournalBatch{FieldVersion: FieldVersion, NextCursor: request.Cursor, Coverage: journalCoverage()}
	if err != nil {
		return b, err
	}
	if err := ctx.Err(); err != nil {
		b.Coverage = failed(err)
		return b, err
	}
	if len(r.Units) == 0 {
		b.Coverage = Coverage{State: Disabled, Reason: ReasonNotConfigured}
		return b, nil
	}
	now := j.clock.Now().UTC()
	if now.Unix() < 0 || (!r.Since.IsZero() && (r.Since.After(now) || r.Since.Unix() < 0)) {
		return b, errors.New("invalid journal since time")
	}
	floor := now.Add(-r.Limits.Lookback)
	since := r.Since
	if since.IsZero() {
		since = floor
	}
	if since.Before(floor) {
		b.Gaps = append(b.Gaps, JournalGap{Reason: ReasonLookback, From: since, To: floor})
		since = floor
		b.Coverage.Reason = ReasonLookback
	}
	count := r.Limits.Events + 1
	if r.Cursor != "" {
		count++
	}
	argv := []string{"journalctl", "--system", "--no-pager", "--output=json",
		"--output-fields=__CURSOR,__REALTIME_TIMESTAMP,_BOOT_ID,_SYSTEMD_UNIT,UNIT,_PID,_UID,PRIORITY",
		"--lines=+" + strconv.Itoa(count), "--since=" + journalTime(since), "--until=" + journalTime(now)}
	if r.Cursor != "" {
		argv = append(argv, "--cursor="+r.Cursor)
	}
	for _, unit := range r.Units {
		argv = append(argv, "--unit="+unit)
	}
	result, runErr := j.runner.Run(ctx, execx.Cmd{Argv: argv, Env: slices.Clone(commandEnv), Timeout: j.config.CommandTimeout, MaxOutput: r.Limits.OutputBytes})
	if result.Truncated || len(result.Output) > r.Limits.OutputBytes {
		return journalFailure(b, Coverage{State: Unavailable, Reason: ReasonTruncated}, since, now), ctx.Err()
	}
	if runErr != nil || result.ExitCode != 0 {
		return journalFailure(b, commandFailure(result.Output, runErr), since, now), ctx.Err()
	}
	rows, parseErr := parseJournal(result.Output, now)
	if parseErr != nil {
		coverage := Coverage{State: Unavailable, Reason: ReasonInvalid}
		if diagnostic := commandFailure(result.Output, nil); diagnostic.Reason != ReasonCommand {
			coverage = diagnostic
		}
		return journalFailure(b, coverage, since, now), ctx.Err()
	}
	if len(rows) > count {
		return journalFailure(b, Coverage{State: Unavailable, Reason: ReasonTruncated}, since, now), ctx.Err()
	}
	if r.Cursor != "" {
		// Inclusive seek plus exact equality verifies the anchor. journalctl may
		// otherwise seek near a vacuumed cursor without proving it still exists.
		if len(rows) == 0 || rows[0].Cursor != r.Cursor {
			return journalFailure(b, Coverage{State: Unavailable, Reason: ReasonCursor}, since, now), nil
		}
		rows = rows[1:]
	}
	if len(rows) > r.Limits.Events {
		rows = rows[:r.Limits.Events]
		b.More = true
		b.Coverage.Reason = ReasonLimit
	}
	for _, row := range rows {
		b.Scanned++
		b.NextCursor = row.Cursor
		if !slices.Contains(r.Units, row.Unit) {
			continue
		}
		row.Historical = r.Historical || (!r.HistoricalBefore.IsZero() && row.SourceTime.Before(r.HistoricalBefore))
		b.Events = append(b.Events, row)
	}
	if err := ctx.Err(); err != nil {
		b.NextCursor = request.Cursor
		return journalFailure(b, failed(err), since, now), err
	}
	return b, nil
}

func journalTime(t time.Time) string { return fmt.Sprintf("@%d.%06d", t.Unix(), t.Nanosecond()/1000) }

func journalCoverage() Coverage {
	return Coverage{State: Partial, Limitations: []string{
		"selected_system_units_only", "caller_permissions_only", "bounded_time_window",
		"retention_and_persistence_unverified", "journal_gaps_not_always_detectable", "normalized_fields_v1_only",
	}}
}

func journalFailure(b JournalBatch, c Coverage, from, to time.Time) JournalBatch {
	// Never expose the tail of truncated output or advance past a malformed row.
	b.Events = nil
	b.Scanned = 0
	b.More = false
	c.Limitations = journalCoverage().Limitations
	b.Coverage = c
	b.Gaps = append(b.Gaps, JournalGap{Reason: c.Reason, From: from, To: to})
	return b
}

func commandFailure(output string, err error) Coverage {
	if err != nil {
		c := failed(err)
		if c.Reason != ReasonRead {
			return c
		}
	}
	c := Coverage{State: Unavailable, Reason: ReasonCommand}
	// Combined stderr is never returned. Locale is fixed so known diagnostics
	// can improve coverage without storing arbitrary, potentially sensitive text.
	var diagnostics strings.Builder
	for line := range strings.SplitSeq(output, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			diagnostics.WriteString(line)
			diagnostics.WriteByte('\n')
		}
	}
	lower := strings.ToLower(diagnostics.String())
	switch {
	case strings.Contains(lower, "permission"), strings.Contains(lower, "access denied"), strings.Contains(lower, "not permitted"), strings.Contains(lower, "not seeing messages"):
		c.Reason = ReasonPermission
	case strings.Contains(lower, "no journal files were found"):
		c.Reason = ReasonMissing
	case strings.Contains(lower, "unrecognized option"), strings.Contains(lower, "unknown option"), strings.Contains(lower, "invalid option"):
		c.State, c.Reason = Unsupported, ReasonVersion
	case strings.Contains(lower, "truncat"):
		c.Reason = ReasonTruncated
	case strings.Contains(lower, "rotat"), strings.Contains(lower, "vacuum"), strings.Contains(lower, "corrupt"):
		c.Reason = ReasonRotation
	case strings.Contains(lower, "cursor"), strings.Contains(lower, "failed to seek"):
		c.Reason = ReasonCursor
	}
	return c
}
