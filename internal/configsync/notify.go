package configsync

import (
	"context"
	"fmt"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
)

// Timeline entry kinds and alert names written by the sync.
const (
	KindConflict = "config.conflict"
	KindInvalid  = "config.invalid"
)

// Signal data keys follow the alert signals of the monitor, so policies
// that react to alerts can react to these as well.
const (
	dataAlert       = "alert"
	dataStatus      = "status"
	dataMessage     = "message"
	dataFingerprint = "fingerprint"
)

// notifyConflict tells the operator that a commit was not applied because
// the resource was edited locally, or, with adopt, because the resource
// already exists with other content and no source manages it.
func (s *Syncer) notifyConflict(ctx context.Context, ref Ref, source, path, commit string, adopt bool) {
	msg := fmt.Sprintf("commit %s of source %s changes %s (%s), which was edited locally; the sync skipped it",
		shortCommit(commit), source, ref, path)
	if adopt {
		msg = fmt.Sprintf("commit %s of source %s defines %s (%s), which already exists with different content and is not managed by a source; the sync did not overwrite it",
			shortCommit(commit), source, ref, path)
	}
	s.timeline(ctx, ref, KindConflict, msg, map[string]any{"source": source, "commit": commit, "path": path})
	s.alert(ref, source, KindConflict, "firing", signal.SeverityWarning, msg, map[string]any{"commit": commit, "path": path})
	s.log.Warn("config sync conflict", "source", source, "resource", ref.String(), "commit", commit)
}

// notifyInvalid tells the operator that a document of a new commit is
// invalid; the resource keeps its previous configuration.
func (s *Syncer) notifyInvalid(ctx context.Context, res Result, o applyOpts) {
	ref := res.Ref()
	if ref.Name == "" {
		ref = Ref{Kind: res.Kind, Name: res.Path}
	}
	msg := fmt.Sprintf("commit %s of source %s has an invalid document %s (%s): %s; the previous configuration stays",
		shortCommit(o.commit), o.source, ref, res.Path, res.Issues.Error())
	s.timeline(ctx, ref, KindInvalid, msg, map[string]any{"source": o.source, "commit": o.commit, "path": res.Path})
	s.alert(ref, o.source, KindInvalid, "firing", signal.SeverityWarning, msg, map[string]any{"commit": o.commit, "path": res.Path})
	s.log.Warn("config sync document invalid", "source", o.source, "resource", ref.String(), "commit", o.commit)
}

// notifyResolved closes a conflict notification.
func (s *Syncer) notifyResolved(ctx context.Context, ref Ref, source, why string) {
	msg := fmt.Sprintf("config conflict on %s (source %s) is over: %s", ref, source, why)
	s.alert(ref, source, KindConflict, "resolved", signal.SeverityInfo, msg, nil)
	s.timeline(ctx, ref, KindConflict, msg, map[string]any{"source": source, "resolved": true})
}

// timeline adds an entry to the service's timeline; only services have one.
// It is written directly rather than through the signal follower so that it
// is reliable and carries its own text.
func (s *Syncer) timeline(ctx context.Context, ref Ref, kind, summary string, data map[string]any) {
	if ref.Kind != model.KindService {
		return
	}
	err := s.cat.AppendTimeline(ctx, catalog.Entry{Service: ref.Name, Kind: kind, Summary: summary, Data: data})
	if err != nil {
		s.log.Warn("config sync timeline entry failed", "service", ref.Name, "error", err)
	}
}

// alert publishes a signal.KindAlert with source "configsync". The signal
// carries the service only as a label: with Signal.Service set, the
// catalog's signal follower would add a second, less specific timeline
// entry for the one written by timeline.
func (s *Syncer) alert(ref Ref, source, name, status, severity, message string, extra map[string]any) {
	if s.bus == nil {
		return
	}
	data := map[string]any{
		dataAlert: name, dataStatus: status, dataMessage: message,
		dataFingerprint: SourceName + ":" + name + ":" + ref.String(),
		"kind":          string(ref.Kind), "resource": ref.Name, "source": source,
	}
	for k, v := range extra {
		data[k] = v
	}
	labels := map[string]string{"kind": string(ref.Kind), "name": ref.Name, "source": source}
	if ref.Kind == model.KindService {
		labels["service"] = ref.Name
	}
	s.bus.Publish(signal.Signal{
		Kind: signal.KindAlert, Source: SourceName, Severity: severity, Labels: labels, Data: data,
	})
}
