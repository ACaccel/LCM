package trigger

import (
	"context"
	"slices"

	"github.com/ACaccel/lcm/internal/signal"
)

// signalBuffer absorbs bursts (for example every check of a service
// flipping at once) while the engine writes each run to disk.
const signalBuffer = 256

type signalWatcher struct {
	baseWatcher
	props SignalProps
	sub   *signal.Subscription
}

// newSignalWatcher subscribes immediately, not when the goroutine gets
// scheduled, so that no signal published after Sync returns is missed.
func newSignalWatcher(b baseWatcher, p SignalProps) *signalWatcher {
	sub := b.m.cfg.Bus.Subscribe("trigger:"+b.policy+"/"+b.node, signalBuffer)
	return &signalWatcher{baseWatcher: b, props: p, sub: sub}
}

func (w *signalWatcher) run(ctx context.Context) {
	sub := w.sub
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-sub.C:
			if !ok {
				return
			}
			if matchSignal(w.props, sig) {
				w.fire(ctx, sig)
			}
		}
	}
}

// matchSignal reports whether sig passes every configured filter.
func matchSignal(p SignalProps, sig signal.Signal) bool {
	if len(p.Kinds) > 0 && !slices.Contains(p.Kinds, string(sig.Kind)) {
		return false
	}
	if len(p.Services) > 0 && !slices.Contains(p.Services, sig.Service) {
		return false
	}
	if len(p.Severities) > 0 && !slices.Contains(p.Severities, sig.Severity) {
		return false
	}
	for k, v := range p.Labels {
		if got, ok := sig.Labels[k]; !ok || got != v {
			return false
		}
	}
	if len(p.Statuses) > 0 && !slices.Contains(p.Statuses, signalStatus(sig)) {
		return false
	}
	return true
}

// signalStatus is the status a signal reports, whatever its kind calls it.
func signalStatus(sig signal.Signal) string {
	for _, key := range []string{"status", "health", "state"} {
		if s, ok := sig.Data[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
