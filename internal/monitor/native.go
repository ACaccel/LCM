package monitor

import (
	"slices"
)

// SetNative records the health a resource type evaluated for one of its
// resources (machine or router thresholds and reachability) and combines it
// with the resource's checks. Types call it on every evaluation; a
// health.changed signal is published only when the combined health changes.
//
// Native input is kept in memory: the types evaluate again after a restart
// and report before long. The combined health is stored like the checks'
// health, so the first report after a restart is compared with the stored
// value and does not look like a change.
func (m *Monitor) SetNative(resource, typ string, labels map[string]string, h Health, reasons []string) {
	if !resourceNameRe.MatchString(resource) {
		m.log.Warn("native health for an invalid resource name ignored", "resource", resource)
		return
	}
	switch h {
	case HealthHealthy, HealthDegraded, HealthDown, HealthUnknown:
	default:
		m.log.Warn("invalid native health treated as unknown", "resource", resource, "health", h)
		h = HealthUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	now := m.clock.Now()
	r := m.resourceLocked(resource, now)
	changed := r.native == nil || !equalLabels(r.labels, labels) || (typ != "" && r.typ != typ)
	if typ != "" {
		r.typ = typ
	}
	r.labels = cloneLabels(labels)
	switch {
	case r.native == nil || r.native.Health != h:
		r.native = &NativeStatus{Health: h, Reasons: slices.Clone(reasons), Since: now}
	case !slices.Equal(r.native.Reasons, reasons):
		r.native.Reasons = slices.Clone(reasons)
	}
	sigs, recs := m.healthChangeLocked(r, now, nil, nil)
	if changed && len(recs) == 0 {
		recs = append(recs, r.row(now))
	}
	if len(sigs) > 0 || len(recs) > 0 {
		m.commitLocked(sigs, recs)
	}
}

// Forget drops everything the monitor holds for a resource that leaves
// observation (retired or unmanaged): its native input, its checks and
// their current state, and its combined health. Nothing is published:
// recomputing the health from what is left would announce a change of a
// resource that is no longer observed. Check history stays, keyed by name.
// Definitions of the resource in a Sync built before the Forget are
// ignored (see SyncAt).
func (m *Monitor) Forget(resource string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.gen++
	m.forgotten[resource] = m.gen
	var recs []record
	if r := m.resources[resource]; r != nil {
		for _, e := range r.checks {
			e.removed = true
			if e.timer != nil {
				e.timer.Stop()
			}
			delete(m.checks, e.id)
			recs = append(recs, deleteCheckRec{id: e.id})
		}
		delete(m.resources, resource)
	}
	// Stored state that no Sync has restored yet.
	for id, row := range m.persisted {
		if row.resource == resource {
			delete(m.persisted, id)
			recs = append(recs, deleteCheckRec{id: id})
		}
	}
	delete(m.persistedRes, resource)
	recs = append(recs, deleteResourceRec{resource: resource})
	m.commitLocked(nil, recs)
}
