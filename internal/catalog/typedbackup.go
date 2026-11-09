package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// A managed service is backed up by the deploy helper: an unprivileged
// worker produces the files as the service's account, and the helper moves
// the finished set into the backup directory, which only root can enter.
// Every call gets a fresh operation ID that is stored in the backups table
// before the call, so that the helper's ledger can be asked what became of
// it after a lost reply or a restart of LCM. A restore takes a copy of the
// data it replaces first (a pre-restore set), and LCM undoes the restore
// from that copy when the restored service does not become active.

// ErrRestoreUndone is matched by the error of a restore that was undone
// because the service did not become active afterwards.
var ErrRestoreUndone = errors.New("catalog: the restored service did not become active, so the restore was undone")

func helperSources(src []model.BackupSource) []deployhelper.BackupSource {
	out := make([]deployhelper.BackupSource, len(src))
	for i, s := range src {
		out[i] = deployhelper.BackupSource{Type: s.Type, Path: s.Path, Database: s.Database, Users: s.Users, Argv: s.Argv}
	}
	return out
}

func modelSources(src []deployhelper.BackupSource) []model.BackupSource {
	out := make([]model.BackupSource, len(src))
	for i, s := range src {
		out[i] = model.BackupSource{Type: s.Type, Path: s.Path, Database: s.Database, Users: s.Users, Argv: s.Argv}
	}
	return out
}

// timeoutSeconds is d in whole seconds within the helper's bounds.
func timeoutSeconds(d time.Duration) int {
	return min(max(int(math.Ceil(d.Seconds())), 1), deployhelper.MaxTimeoutSec)
}

// outcomeOf maps a failed helper call to the record's outcome.
func outcomeOf(err error) string {
	var he *rt.HelperError
	if errors.As(err, &he) && he.Unknown {
		return OutcomeInterrupted
	}
	return OutcomeFailed
}

// typedBackup runs the helper's backup and prune and records the set.
func (s *session) typedBackup(ctx context.Context, res *ActionResult) error {
	b := s.cfg.Backup
	rec := BackupRecord{Resource: s.name, Kind: BackupKindBackup, Sources: b.Sources, OperationID: rt.NewOperationID(),
		Actor: s.actor, RunID: s.opts.RunID}
	row, err := s.m.cat.BeginBackup(ctx, rec)
	if err != nil {
		return err
	}
	resp, callErr := rt.CallHelper(ctx, s.m.deploy, deployhelper.Request{
		Op: deployhelper.OpBackup, Service: s.name, OperationID: rec.OperationID, Sources: helperSources(b.Sources),
		Mode: b.EffectiveMode(), TimeoutSec: timeoutSeconds(b.EffectiveTimeout()), Runtime: s.cfg.RuntimeName,
	})
	fctx, cancel := detached(ctx)
	defer cancel()
	done := BackupRecord{}
	outcome := OutcomeSucceeded
	if r := resp.Backup; r != nil {
		done = BackupRecord{BackupID: r.BackupID, Size: r.Size, ManifestSHA256: r.ManifestSHA256, Skipped: r.Skipped}
		res.BackupID = r.BackupID
	}
	if callErr != nil {
		done.Error = callErr.Error()
		// A set that was made stays a set even when the service could not
		// be started again after a stopped-mode backup.
		if resp.Backup == nil {
			outcome = outcomeOf(callErr)
		}
	}
	if err := s.m.cat.FinishBackup(fctx, row, outcome, done); err != nil {
		s.m.log.Error("record backup failed", "service", s.name, "error", err)
	}
	if callErr != nil {
		// A stopped-mode backup may have left the service down.
		if b.EffectiveMode() == model.BackupStopped && s.cur == StateRunning {
			s.settle(ctx, false)
		}
		return callErr
	}
	res.Output = fmt.Sprintf("backup %s: %d files, %d bytes", resp.Backup.BackupID, resp.Backup.Files, resp.Backup.Size)
	if n := resp.Backup.Skipped; n > 0 {
		res.Output += fmt.Sprintf("; %d entries (links, devices, sockets, pipes) are not stored", n)
	}
	if note := s.prune(fctx, b.EffectiveKeep()); note != "" {
		res.Output += "; " + note
	}
	return nil
}

// prune applies the retention of the backup after a successful backup. A
// failure is reported, not returned: the new set exists.
func (s *session) prune(ctx context.Context, keep model.BackupKeep) string {
	resp, err := rt.CallHelper(ctx, s.m.deploy, deployhelper.Request{
		Op: deployhelper.OpPrune, Service: s.name, OperationID: rt.NewOperationID(),
		Keep: &deployhelper.Keep{Daily: keep.Daily, Weekly: keep.Weekly},
	})
	if resp.Prune != nil {
		if mErr := s.m.cat.MarkBackupsPruned(ctx, s.name, resp.Prune.Removed); mErr != nil {
			s.m.log.Error("record pruned backups failed", "service", s.name, "error", mErr)
		}
	}
	if err != nil {
		s.m.log.Warn("prune backups failed", "service", s.name, "error", err)
		return "pruning old backups failed: " + err.Error()
	}
	if n := len(resp.Prune.Removed); n > 0 {
		return fmt.Sprintf("%d old sets removed", n)
	}
	return ""
}

// restoreTarget picks the set a restore puts back.
func (c *Catalog) restoreTarget(ctx context.Context, name, backupID string) (BackupRecord, error) {
	var (
		rec BackupRecord
		err error
	)
	if backupID == "" {
		rec, err = c.LatestBackup(ctx, name)
	} else {
		if !ValidBackupID(backupID) {
			return rec, fmt.Errorf("%w: %q is not a backup ID", ErrUnknownBackup, backupID)
		}
		rec, err = c.Backup(ctx, name, backupID)
	}
	if errors.Is(err, ErrBackupNotFound) {
		return rec, fmt.Errorf("%w: %w", ErrUnknownBackup, err)
	}
	if err != nil {
		return rec, err
	}
	if !rec.Restorable() {
		return rec, fmt.Errorf("%w: %s did not finish, was removed by retention, or is not a set of this service", ErrUnknownBackup, rec.BackupID)
	}
	return rec, nil
}

// Restore puts a backup set of a managed service back. The helper verifies
// the set, stops the service, copies the current data to a pre-restore set,
// replaces it and starts the service again; LCM undoes the restore from the
// pre-restore set when the service does not become active.
func (m *Manager) Restore(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRestore, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		return s.restore(ctx, res)
	})
}

func (s *session) restore(ctx context.Context, res *ActionResult) error {
	b := s.cfg.Backup
	if b == nil || !b.Typed() || !b.Restorable() {
		return ErrNoRestore
	}
	target, err := s.m.cat.restoreTarget(ctx, s.name, s.opts.BackupID)
	if err != nil {
		return err
	}
	res.BackupID = target.BackupID
	wasRunning := s.cur == StateRunning
	rec := BackupRecord{Resource: s.name, Kind: BackupKindRestore, BackupID: target.BackupID, Sources: target.Sources,
		OperationID: rt.NewOperationID(), Actor: s.actor, RunID: s.opts.RunID}
	row, err := s.m.cat.BeginBackup(ctx, rec)
	if err != nil {
		return err
	}
	resp, callErr := rt.CallHelper(ctx, s.m.deploy, deployhelper.Request{
		Op: deployhelper.OpRestore, Service: s.name, OperationID: rec.OperationID, BackupID: target.BackupID, ManifestSHA256: target.ManifestSHA256,
	})
	fctx, cancel := detached(ctx)
	defer cancel()
	if resp.Restore != nil {
		res.PreRestoreID = resp.Restore.PreRestoreID
		s.recordPreRestore(fctx, resp.Restore.PreRestoreID, target.BackupID)
	}
	if callErr != nil {
		if err := s.m.cat.FinishBackup(fctx, row, outcomeOf(callErr), BackupRecord{BackupID: target.BackupID, Error: callErr.Error()}); err != nil {
			s.m.log.Error("record restore failed", "service", s.name, "error", err)
		}
		// The helper put the previous data back itself, or the outcome is
		// unknown; the runtime says what is true now.
		if wasRunning {
			s.settle(ctx, false)
		}
		return callErr
	}
	if err := s.m.cat.FinishBackup(fctx, row, OutcomeSucceeded, BackupRecord{BackupID: target.BackupID}); err != nil {
		s.m.log.Error("record restore failed", "service", s.name, "error", err)
	}
	res.Output = fmt.Sprintf("restored %s; the previous data is in %s", target.BackupID, resp.Restore.PreRestoreID)
	if !wasRunning {
		return nil
	}
	return s.checkRestored(ctx, resp.Restore.PreRestoreID, target.BackupID)
}

// checkRestored waits for the restored service to become active and undoes
// the restore from the pre-restore set when it does not.
func (s *session) checkRestored(ctx context.Context, pre, restored string) error {
	if s.m.settle > 0 {
		if err := s.m.pause(ctx, s.m.settle); err != nil {
			return err
		}
	}
	st, err := s.adapter.Status(ctx, s.rt)
	if err == nil && st.State == rt.StateActive {
		return nil
	}
	cause := "its status is unknown"
	if err == nil {
		cause = "it is " + string(st.State)
	}
	if pre == "" {
		s.settle(ctx, false)
		return fmt.Errorf("%w: %s after restoring %s, and the helper reported no pre-restore set", ErrRestoreUndone, cause, restored)
	}
	rec := BackupRecord{Resource: s.name, Kind: BackupKindRestore, BackupID: pre, OperationID: rt.NewOperationID(), Actor: s.actor, RunID: s.opts.RunID}
	row, err := s.m.cat.BeginBackup(ctx, rec)
	if err != nil {
		s.settle(ctx, false)
		return err
	}
	resp, undoErr := rt.CallHelper(ctx, s.m.deploy, deployhelper.Request{
		Op: deployhelper.OpRestore, Service: s.name, OperationID: rec.OperationID, BackupID: pre,
	})
	fctx, cancel := detached(ctx)
	defer cancel()
	if resp.Restore != nil {
		s.recordPreRestore(fctx, resp.Restore.PreRestoreID, pre)
	}
	if undoErr != nil {
		if err := s.m.cat.FinishBackup(fctx, row, outcomeOf(undoErr), BackupRecord{BackupID: pre, Error: undoErr.Error()}); err != nil {
			s.m.log.Error("record undo failed", "service", s.name, "error", err)
		}
		s.settle(ctx, true)
		return fmt.Errorf("%w: %s after restoring %s, and putting the previous data back from %s failed: %w", ErrRestoreUndone, cause, restored, pre, undoErr)
	}
	if err := s.m.cat.FinishBackup(fctx, row, OutcomeSucceeded, BackupRecord{BackupID: pre}); err != nil {
		s.m.log.Error("record undo failed", "service", s.name, "error", err)
	}
	s.settle(ctx, false)
	return fmt.Errorf("%w: %s after restoring %s; the previous data was put back from %s", ErrRestoreUndone, cause, restored, pre)
}

// pause waits d on the catalog's clock, or until ctx ends.
func (m *Manager) pause(ctx context.Context, d time.Duration) error {
	done := make(chan struct{})
	t := m.cat.clk.AfterFunc(d, func() { close(done) })
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// recordPreRestore adds the pre-restore set a restore made to the table,
// with the details the helper lists for it. A set that cannot be described
// is recorded without them.
func (s *session) recordPreRestore(ctx context.Context, id, restoredFrom string) {
	if id == "" {
		return
	}
	rec := BackupRecord{Resource: s.name, Kind: BackupKindPreRestore, BackupID: id, Outcome: OutcomeSucceeded, Actor: s.actor, RunID: s.opts.RunID,
		Error: "copy of the data that the restore of " + restoredFrom + " replaced"}
	resp, err := rt.CallHelper(ctx, s.m.deploy, deployhelper.Request{Op: deployhelper.OpBackups, Service: s.name, OperationID: rt.NewOperationID()})
	if err == nil {
		for _, info := range resp.Backups {
			if info.BackupID != id {
				continue
			}
			rec.Size, rec.ManifestSHA256, rec.Skipped, rec.Sources = info.Size, info.ManifestSHA256, info.Skipped, modelSources(info.Sources)
			rec.StartedAt = time.UnixMilli(info.CreatedAt).UTC()
			rec.FinishedAt = rec.StartedAt
		}
	} else {
		s.m.log.Warn("list backups failed", "service", s.name, "error", err)
	}
	if _, err := s.m.cat.AddBackup(ctx, rec); err != nil {
		s.m.log.Warn("record pre-restore set failed", "service", s.name, "set", id, "error", err)
	}
}

// typedBackupSteps are the steps of a typed backup.
func typedBackupSteps(b *model.Backup) []message.Message {
	keep := b.EffectiveKeep()
	sources := describeSources(b.Sources)
	steps := []message.Message{
		message.New("step.helperBackup",
			fmt.Sprintf("back up %s through the deploy helper (%s mode) into a new set in the backup directory", sources, b.EffectiveMode()),
			"sources", sources, "mode", b.EffectiveMode()),
		message.New("step.helperPrune",
			fmt.Sprintf("remove old sets beyond the newest of each of the last %d days and %d weeks; the newest set is always kept", keep.Daily, keep.Weekly),
			"daily", fmt.Sprint(keep.Daily), "weekly", fmt.Sprint(keep.Weekly)),
	}
	return steps
}

// restoreSteps are the steps of a restore, or why it cannot run.
func (m *Manager) restoreSteps(ctx context.Context, rec *Record, backupID string) ([]message.Message, error) {
	b := rec.Config.Backup
	if b == nil || !b.Typed() || !b.Restorable() {
		return nil, ErrNoRestore
	}
	target, err := m.cat.restoreTarget(ctx, rec.Name, backupID)
	if err != nil {
		return nil, err
	}
	var restorable []model.BackupSource
	for _, s := range b.Sources {
		if s.Type != model.BackupCommand {
			restorable = append(restorable, s)
		}
	}
	sources := describeSources(restorable)
	return []message.Message{
		message.New("step.restoreVerify", fmt.Sprintf("verify backup %s against the checksums in its manifest", target.BackupID), "backup", target.BackupID),
		message.New("step.restoreStop", "stop "+rec.Name+" if it is running", "service", rec.Name),
		message.New("step.restorePreRestore", fmt.Sprintf("copy the current data (%s) to a pre-restore set", sources), "sources", sources),
		message.New("step.restoreApply", fmt.Sprintf("replace %s with the content of %s; a failure puts the pre-restore copy back", sources, target.BackupID),
			"sources", sources, "backup", target.BackupID),
		message.New("step.restoreStart", "start "+rec.Name+" again if it was running", "service", rec.Name),
		message.New("step.restoreCheck", "check that the service becomes active; if not, restore the pre-restore set", "service", rec.Name),
	}, nil
}
