package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
)

// validateDef checks a definition's structure; type-specific props are
// validated by the registry.
func validateDef(d Def) model.Issues {
	id := d.ID()
	if !serviceNameRe.MatchString(d.Service) {
		return model.Issues{{Path: id, Message: fmt.Sprintf("invalid service name %q", d.Service)}}
	}
	err := model.ValidateChecks([]model.Check{d.Check}, "checks")
	var is model.Issues
	if errors.As(err, &is) {
		out := make(model.Issues, len(is))
		for i, x := range is {
			out[i] = model.Issue{Path: strings.Replace(x.Path, "checks[0]", id, 1), Message: x.Message}
		}
		return out
	}
	return nil
}

// fingerprint identifies a check definition so that Sync can tell whether
// anything that affects behaviour changed.
func fingerprint(c model.Check) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("props are not JSON-compatible: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// restore carries the runtime state of a replaced entry over.
func (e *entry) restore(old *entry) {
	e.status, e.since, e.okRun, e.failRun, e.stale = old.status, old.since, old.okRun, old.failRun, old.stale
	e.lastAt, e.lastOK, e.lastLatency, e.lastMessage = old.lastAt, old.lastOK, old.lastLatency, old.lastMessage
	e.lastBeat, e.anchor = old.lastBeat, old.anchor
}

// restorePersisted resumes from stored state. A status older than twice the
// interval no longer says anything about the present, so it is marked stale
// and its streak counters start over.
func (e *entry) restorePersisted(row checkRow, now time.Time) {
	e.status, e.since = row.status, row.since
	e.lastAt, e.lastOK, e.lastLatency, e.lastMessage = row.lastAt, row.lastOK, row.lastLatency, row.lastMessage
	e.lastBeat = row.lastBeat
	if row.status != StatusUnknown && (row.lastAt.IsZero() || now.Sub(row.lastAt) > 2*e.check.Interval.D()) {
		e.stale = true
		return
	}
	e.okRun, e.failRun = row.okRun, row.failRun
}

// serviceLocked returns the named service, creating it from stored state
// when it is first seen.
func (m *Monitor) serviceLocked(name string, now time.Time) *service {
	if svc := m.services[name]; svc != nil {
		return svc
	}
	svc := &service{name: name, health: HealthUnknown, since: now, checks: map[string]*entry{}}
	if row, ok := m.persistedSvc[name]; ok {
		svc.health, svc.since, svc.labels = row.health, row.since, row.labels
	}
	m.services[name] = svc
	return svc
}

func (s *service) row(now time.Time) serviceRow {
	return serviceRow{service: s.name, health: s.health, since: s.since, labels: s.labels, at: now}
}

func equalLabels(a, b map[string]string) bool { return maps.Equal(a, b) }

func cloneLabels(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return maps.Clone(m)
}

// firstDelay spreads first runs over StartSpread, deterministically per
// check so that a restart reproduces the same schedule.
func (m *Monitor) firstDelay(id string, interval time.Duration) time.Duration {
	spread := min(interval, m.startSpread)
	if spread <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return time.Duration(int64(h.Sum32()) % int64(spread))
}

// fire is the timer callback of a check: it re-arms the timer (so the
// interval is measured between starts, not completions) and starts one run.
func (m *Monitor) fire(e *entry) {
	m.mu.Lock()
	if m.closed || e.removed {
		m.mu.Unlock()
		return
	}
	interval := e.check.Interval.D()
	e.timer = m.clock.AfterFunc(interval, func() { m.fire(e) })
	if e.built.Probe == nil {
		m.checkHeartbeatLocked(e)
		m.mu.Unlock()
		return
	}
	if e.running {
		// The previous run is still waiting for a probe slot or finishing;
		// piling up runs would only add load to a struggling service.
		m.mu.Unlock()
		return
	}
	e.running = true
	m.beginLocked()
	m.mu.Unlock()
	go m.run(e)
}

// checkHeartbeatLocked records a failure when no heartbeat arrived within
// interval plus grace. Successes are recorded by Heartbeat itself, so a
// healthy heartbeat check produces no result here.
func (m *Monitor) checkHeartbeatLocked(e *entry) {
	now := m.clock.Now()
	ref := e.anchor
	if e.lastBeat.After(ref) {
		ref = e.lastBeat
	}
	limit := e.check.Interval.D() + e.built.Grace
	if age := now.Sub(ref); age > limit {
		m.applyLocked(e, now, Result{Message: fmt.Sprintf("no heartbeat for %s", age.Round(time.Second))})
	}
}

func (m *Monitor) run(e *entry) {
	defer m.end()
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-m.ctx.Done():
		m.finish(e, nil)
		return
	}
	ctx, cancel := clock.WithTimeoutCause(m.ctx, m.clock, e.check.Timeout.D(), errCheckTimeout)
	defer cancel()
	res := m.probe(ctx, e)
	if m.ctx.Err() != nil {
		// Shutdown interrupted the probe; its failure says nothing about
		// the service.
		m.finish(e, nil)
		return
	}
	m.finish(e, &res)
}

func (m *Monitor) probe(ctx context.Context, e *entry) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			m.log.Error("check probe panicked", "check", e.id, "panic", r)
			res = Result{Message: "probe panicked"}
		}
	}()
	res = e.built.Probe(ctx)
	res.Message = truncate(res.Message, maxMessage)
	return res
}

func (m *Monitor) finish(e *entry, res *Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.running = false
	if res == nil || e.removed {
		return
	}
	m.applyLocked(e, m.clock.Now(), *res)
}

// applyLocked records one result: it updates the check, derives status and
// service health changes, and queues the storage writes. Signals are
// published after the writes commit, so a subscriber that reads storage
// never sees a change that is not stored yet. The returned channel closes
// once that has happened.
func (m *Monitor) applyLocked(e *entry, now time.Time, res Result) <-chan struct{} {
	svc := m.services[e.service]
	e.lastAt, e.lastOK, e.lastLatency, e.lastMessage = now, res.OK, res.Latency, res.Message
	e.stale = false
	target, run := StatusDown, 0
	if res.OK {
		e.okRun, e.failRun = min(e.okRun+1, maxRun), 0
		target, run = StatusUp, e.okRun
	} else {
		e.failRun, e.okRun = min(e.failRun+1, maxRun), 0
		run = e.failRun
	}
	recs := []record{resultRec{checkID: e.id, at: now, ok: res.OK, latency: res.Latency, message: res.Message}}
	var sigs []signal.Signal
	if target != e.status && run >= e.check.Threshold {
		prev, prevSince := e.status, e.since
		e.status, e.since = target, now
		sigs = append(sigs, m.checkSignal(svc, e, prev, prevSince))
		sigs, recs = m.healthChangeLocked(svc, now, sigs, recs)
		recs = append(recs, svc.row(now))
	}
	recs = append(recs, e.row())
	return m.commitLocked(sigs, recs)
}

// healthChangeLocked recomputes the service health and, when it changed,
// appends the signal and the stored row.
func (m *Monitor) healthChangeLocked(svc *service, now time.Time, sigs []signal.Signal, recs []record) ([]signal.Signal, []record) {
	h := healthOf(svc.checks, false)
	if h == svc.health {
		return sigs, recs
	}
	prev, prevSince := svc.health, svc.since
	svc.health, svc.since = h, now
	return append(sigs, m.healthSignal(svc, prev, prevSince)), append(recs, svc.row(now))
}

func (m *Monitor) commitLocked(sigs []signal.Signal, recs []record) <-chan struct{} {
	committed := make(chan struct{})
	m.w.submit(func(err error) {
		defer close(committed)
		if err != nil && !errors.Is(err, errWriterClosed) {
			m.log.Error("monitor state not stored", "error", err)
		}
		// A failed write must not swallow the change: the in-memory state
		// has already moved on and later results build on it.
		for _, s := range sigs {
			m.bus.Publish(s)
		}
	}, recs...)
	return committed
}

func (m *Monitor) checkSignal(svc *service, e *entry, prev Status, prevSince time.Time) signal.Signal {
	sev := signal.SeverityInfo
	if e.status == StatusDown {
		sev = signal.SeverityWarning
		if e.check.IsCritical() {
			sev = signal.SeverityCritical
		}
	}
	return signal.Signal{
		Kind: signal.KindCheck, Time: e.since, Service: e.service, Source: signalSource, Severity: sev,
		Labels: cloneLabels(svc.labels),
		Data: map[string]any{
			"check":         e.check.Name,
			"type":          e.check.Type,
			"critical":      e.check.IsCritical(),
			"status":        string(e.status),
			"previous":      string(prev),
			"message":       e.lastMessage,
			"since":         e.since.UTC().Format(time.RFC3339Nano),
			"previousSince": prevSince.UTC().Format(time.RFC3339Nano),
		},
	}
}

func (m *Monitor) healthSignal(svc *service, prev Health, prevSince time.Time) signal.Signal {
	sev := signal.SeverityInfo
	switch svc.health {
	case HealthDown:
		sev = signal.SeverityCritical
	case HealthDegraded:
		sev = signal.SeverityWarning
	}
	failing := failingChecks(svc.checks)
	if failing == nil {
		failing = []string{}
	}
	return signal.Signal{
		Kind: signal.KindHealth, Time: svc.since, Service: svc.name, Source: signalSource, Severity: sev,
		Labels: cloneLabels(svc.labels),
		Data: map[string]any{
			"health":        string(svc.health),
			"previous":      string(prev),
			"failing":       failing,
			"since":         svc.since.UTC().Format(time.RFC3339Nano),
			"previousSince": prevSince.UTC().Format(time.RFC3339Nano),
		},
	}
}

// Heartbeat records a received heartbeat for a heartbeat check. payload is
// whatever the sender posted; only a short single-line excerpt is kept as
// the result message. It returns once the heartbeat is stored.
func (m *Monitor) Heartbeat(ctx context.Context, service, check string, payload []byte) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	id := checkID(service, check)
	e := m.checks[id]
	switch {
	case e == nil:
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownCheck, id)
	case e.built.Probe != nil:
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotHeartbeat, id)
	}
	now := m.clock.Now()
	e.lastBeat = now
	committed := m.applyLocked(e, now, Result{OK: true, Message: beatMessage(payload)})
	m.mu.Unlock()
	select {
	case <-committed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func beatMessage(payload []byte) string {
	s := strings.ToValidUTF8(string(payload[:min(len(payload), 4*maxBeatMessage)]), "")
	if line := firstLine(s); line != "" {
		return truncate(line, maxBeatMessage)
	}
	return "heartbeat received"
}

const maxBeatMessage = 120
