package trigger

import (
	"context"
	"slices"
	"strings"

	"github.com/ACaccel/lcm/internal/signal"
)

// signalBuffer absorbs bursts (for example every check of a resource
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
	// Config sync notifications (resource.<conflict>) are not part of the
	// signal kinds a filter can name, so filters without kinds must not
	// start receiving them.
	if strings.HasPrefix(string(sig.Kind), "resource.") && !slices.Contains(p.Kinds, string(sig.Kind)) {
		return false
	}
	if len(p.Kinds) > 0 && !slices.Contains(p.Kinds, string(sig.Kind)) {
		return false
	}
	if len(p.Resources) > 0 && !slices.Contains(p.Resources, sig.Resource) {
		return false
	}
	if len(p.Types) > 0 && !slices.Contains(p.Types, sig.ResourceType) {
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

// ConcernsResource reports whether a trigger's static props select the
// resource: the resource selectors (resources, types, labels) that are set
// must all match it. A trigger without any resource selector, or of another
// type than trigger.signal, is not attributed to one resource.
func ConcernsResource(triggerType string, props any, name, resourceType string, labels map[string]string) bool {
	if triggerType != TypeSignal {
		return false
	}
	var p SignalProps
	switch v := props.(type) {
	case SignalProps:
		p = v
	case *SignalProps:
		p = *v
	default:
		return false
	}
	if len(p.Resources)+len(p.Types)+len(p.Labels) == 0 {
		return false
	}
	if len(p.Resources) > 0 && !slices.Contains(p.Resources, name) {
		return false
	}
	if len(p.Types) > 0 && !slices.Contains(p.Types, resourceType) {
		return false
	}
	for k, v := range p.Labels {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}
