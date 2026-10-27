package configsync

import (
	"context"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/trigger"
)

// idleWait is how long Run sleeps when no source is due. Source changes and
// push notifications wake it earlier.
const idleWait = time.Hour

// Report is the outcome of syncing one source.
type Report struct {
	Source string
	// Commit is the head commit that was read; empty when the fetch failed.
	Commit   string
	Previous string
	// Changed: Commit differs from the one the previous sync read.
	Changed bool
	// Error is set when the sync could not read the repository.
	Error string
	Items []Result
	// Orphaned lists resources newly found missing from the repository.
	Orphaned []Ref
}

// SyncNow syncs a source immediately, whatever its interval, and re-applies
// the head commit even if it was read before. The caller needs an actor for
// the audit entry; the documents themselves are applied as
// "configsync:<source>".
func (s *Syncer) SyncNow(ctx context.Context, name string) (Report, error) {
	if _, ok := actorOf(ctx); !ok {
		return Report{}, audit.ErrNoActor
	}
	src, err := s.GetSource(ctx, name)
	if err != nil {
		return Report{}, err
	}
	s.begin(name)
	rep := s.syncSource(ctx, src, true)
	s.end(name)
	outcome := audit.OutcomeOK
	if rep.Error != "" {
		outcome = audit.OutcomeError
	}
	s.record(ctx, "config.sync", "source:"+name, outcome, map[string]any{"commit": rep.Commit, "documents": len(rep.Items)})
	return rep, nil
}

func (s *Syncer) begin(name string) {
	s.mu.Lock()
	s.inflight[name]++
	s.mu.Unlock()
}

func (s *Syncer) end(name string) {
	s.mu.Lock()
	if s.inflight[name]--; s.inflight[name] <= 0 {
		delete(s.inflight, name)
	}
	s.mu.Unlock()
}

func (s *Syncer) syncing(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight[name] > 0
}

// RequestSync starts a forced sync of a source in the background and returns
// at once; clients read the outcome from the source (lastSyncAt, lastCommit,
// lastError, and Syncing while it runs). A request while a sync of the same
// source is already running joins it instead of starting another, and
// reports started=false. The sync runs until it ends or Close is called, not
// only for the length of the caller's context; the actor of ctx is kept for
// the audit entry.
func (s *Syncer) RequestSync(ctx context.Context, name string) (started bool, err error) {
	actor, ok := actorOf(ctx)
	if !ok {
		return false, audit.ErrNoActor
	}
	src, err := s.GetSource(ctx, name)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	if s.life.Err() != nil || s.inflight[name] > 0 {
		s.mu.Unlock()
		return false, nil
	}
	s.inflight[name]++
	s.jobs.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.jobs.Done()
		defer s.end(name)
		jctx := auth.WithActor(s.life, actor)
		rep := s.syncSource(jctx, src, true)
		outcome := audit.OutcomeOK
		if rep.Error != "" {
			outcome = audit.OutcomeError
		}
		s.record(jctx, "config.sync", "source:"+name, outcome, map[string]any{"commit": rep.Commit, "documents": len(rep.Items)})
	}()
	return true, nil
}

// Wait blocks until the syncs started by RequestSync have finished.
func (s *Syncer) Wait() { s.jobs.Wait() }

// Close stops the syncs started by RequestSync and waits for them. It does
// not stop Run, which ends with its context.
func (s *Syncer) Close() {
	s.cancel()
	s.jobs.Wait()
}

// syncSource fetches the source and applies its documents. A sync that
// finds the same commit as last time does nothing unless force is set.
func (s *Syncer) syncSource(ctx context.Context, src Source, force bool) Report {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	rep := Report{Source: src.Name, Previous: src.LastCommit}
	fail := func(err error) Report {
		rep.Error = redact(err.Error())
		s.finishSync(ctx, src.Name, src.LastCommit, rep.Error)
		s.log.Warn("config sync failed", "source", src.Name, "error", rep.Error)
		return rep
	}

	commit, dir, err := s.fetch(ctx, src)
	if err != nil {
		return fail(err)
	}
	rep.Commit, rep.Changed = commit, commit != src.LastCommit
	if !rep.Changed && !force {
		s.finishSync(ctx, src.Name, commit, src.LastError)
		return rep
	}
	docs, failures, err := s.readDocuments(ctx, src, dir, commit)
	if err != nil {
		rep.Commit = ""
		return fail(err)
	}

	actor := "configsync:" + src.Name
	actx := auth.WithActor(ctx, auth.Actor{Kind: auth.KindSystem, ID: actor, Name: actor})
	results, err := s.apply(actx, docs, applyOpts{source: src.Name, commit: commit, notify: rep.Changed})
	if err != nil {
		return fail(err)
	}
	rep.Items = append(failures, results...)
	rep.Orphaned = s.markOrphans(ctx, src.Name, rep.Items)
	s.finishSync(ctx, src.Name, commit, summarizeErrors(rep.Items))
	s.log.Info("config source synced", "source", src.Name, "commit", shortCommit(commit), "documents", len(rep.Items))
	return rep
}

// summarizeErrors condenses failed results into the source's lastError.
func summarizeErrors(items []Result) string {
	var first string
	n := 0
	for _, r := range items {
		if r.Action != ActionFailed {
			continue
		}
		if n == 0 {
			detail := r.Reason
			if len(r.Issues) > 0 {
				detail = r.Issues.Error()
			}
			first = fmt.Sprintf("%s: %s", joinRef(r), detail)
		}
		n++
	}
	switch n {
	case 0:
		return ""
	case 1:
		return first
	}
	return fmt.Sprintf("%s (and %d more)", first, n-1)
}

func joinRef(r Result) string {
	if r.Name == "" {
		return r.Path
	}
	return r.Ref().String() + " (" + r.Path + ")"
}

// markOrphans flags the source's resources whose documents are gone from
// the repository, and clears the flag for those that came back. When a
// file could not be understood at all the set of present documents is
// unknown, so nothing is flagged.
func (s *Syncer) markOrphans(ctx context.Context, source string, items []Result) []Ref {
	present := map[Ref]bool{}
	for _, r := range items {
		if r.Name == "" {
			return nil
		}
		present[r.Ref()] = true
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	rows, err := s.listOwn(ctx, `WHERE source = ?`, source)
	if err != nil {
		s.log.Warn("cannot check for orphaned resources", "source", source, "error", err)
		return nil
	}
	var orphaned []Ref
	for _, row := range rows {
		gone := !present[row.Ref]
		if gone == row.Orphaned {
			continue
		}
		row.Orphaned = gone
		if err := s.saveOwn(ctx, row); err != nil {
			s.log.Warn("cannot update orphan flag", "resource", row.String(), "error", err)
			continue
		}
		if gone {
			orphaned = append(orphaned, row.Ref)
			s.log.Warn("resource is no longer in its config source", "source", source, "resource", row.String())
		}
	}
	return orphaned
}

func (s *Syncer) finishSync(ctx context.Context, name, commit, lastError string) {
	err := s.dbExec(ctx, `UPDATE config_sources SET last_commit = ?, last_sync_at = ?, last_error = ? WHERE name = ?`,
		commit, s.now().UnixMilli(), lastError, name)
	if err != nil {
		s.log.Warn("cannot record the sync state", "source", name, "error", err)
	}
}

// NotifyPush tells the syncer that branch of repo has a new commit, as a
// git host's webhook reports it. Enabled sources that watch that repository
// and branch are synced as soon as the loop gets to it; the call itself
// does not wait for git. It returns the number of matching sources.
func (s *Syncer) NotifyPush(ctx context.Context, repo, branch string) (int, error) {
	srcs, err := s.ListSources(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	s.mu.Lock()
	for _, src := range srcs {
		if src.Enabled && src.Branch == branch && trigger.SameRepo(src.Repo, repo) {
			s.forced[src.Name] = true
			n++
		}
	}
	s.mu.Unlock()
	if n > 0 {
		s.wake()
	}
	return n, nil
}

// wake nudges Run to look at the sources again.
func (s *Syncer) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run syncs every enabled source on its interval until ctx ends. A source
// that was synced recently is not synced again right after a restart.
func (s *Syncer) Run(ctx context.Context) {
	next := map[string]time.Time{}
	for ctx.Err() == nil {
		srcs, err := s.ListSources(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Error("cannot list config sources", "error", err)
		}
		s.mu.Lock()
		forced := s.forced
		s.forced = map[string]bool{}
		s.mu.Unlock()

		live := map[string]bool{}
		for _, src := range srcs {
			if !src.Enabled || ctx.Err() != nil {
				continue
			}
			live[src.Name] = true
			due, ok := next[src.Name]
			if !ok {
				due = s.now()
				if !src.LastSyncAt.IsZero() {
					due = src.LastSyncAt.Add(src.Interval)
				}
			}
			if forced[src.Name] || !due.After(s.now()) {
				s.begin(src.Name)
				s.syncSource(ctx, src, false)
				s.end(src.Name)
				due = s.now().Add(src.Interval)
			}
			next[src.Name] = due
		}

		wait := idleWait
		for name, due := range next {
			if !live[name] {
				delete(next, name)
				continue
			}
			wait = min(wait, max(due.Sub(s.now()), time.Millisecond))
		}
		if !s.sleep(ctx, wait) {
			return
		}
	}
}

// sleep waits for d on the injected clock, until woken, or until ctx ends.
// It reports false when ctx ended.
func (s *Syncer) sleep(ctx context.Context, d time.Duration) bool {
	done := make(chan struct{})
	t := s.clk.AfterFunc(d, func() { close(done) })
	select {
	case <-done:
		return true
	case <-s.kick:
		t.Stop()
		return true
	case <-ctx.Done():
		t.Stop()
		return false
	}
}
