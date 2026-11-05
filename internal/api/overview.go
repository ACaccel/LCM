package api

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
)

// Availability window of the overview: hourly buckets over the last day.
const (
	overviewBuckets = 24
	overviewStep    = time.Hour
)

type overviewTypeDTO struct {
	Type    string `json:"type"`
	Total   int    `json:"total"`
	Retired int    `json:"retired"`
	// Health counts the managed resources by health.
	Health map[string]int `json:"health"`
}

type overviewAttentionDTO struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	State  string `json:"state"`
	Health string `json:"health"`
	// Since is when the combined health last changed.
	Since *time.Time `json:"since"`
	// FiringAlerts is the number of external alerts firing for the resource.
	FiringAlerts int `json:"firingAlerts"`
	// Reasons explain the health.
	Reasons []message.Message `json:"reasons"`
}

type availabilityBucketDTO struct {
	// Ratio is the share of passing results, null when no check ran.
	Ratio    *float64 `json:"ratio"`
	Failures int64    `json:"failures"`
}

type overviewResourceDTO struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	State  string `json:"state"`
	Health string `json:"health"`
	// DeployedVersion and LastDeployedAt describe a service's last
	// deployment; empty and null for other types and undeployed services.
	DeployedVersion string        `json:"deployedVersion"`
	LastDeployedAt  *time.Time    `json:"lastDeployedAt"`
	ManagedBy       *managedByDTO `json:"managedBy"`
	HasChecks       bool          `json:"hasChecks"`
	// Uptime is the share of passing results over the window, null when no
	// check ran.
	Uptime  *float64                `json:"uptime"`
	Buckets []availabilityBucketDTO `json:"buckets"`
}

type overviewDeploysDTO struct {
	Since time.Time `json:"since"`
	Count int       `json:"count"`
}

type overviewDTO struct {
	GeneratedAt time.Time              `json:"generatedAt"`
	Types       []overviewTypeDTO      `json:"types"`
	Attention   []overviewAttentionDTO `json:"attention"`
	// Resources are the managed resources, by name.
	Resources    []overviewResourceDTO `json:"resources"`
	ActiveRuns   int                   `json:"activeRuns"`
	Deploys      overviewDeploysDTO    `json:"deploys"`
	FiringAlerts int                   `json:"firingAlerts"`
}

func ratio(count, failures int64) *float64 {
	if count <= 0 {
		return nil
	}
	v := 1 - float64(failures)/float64(count)
	return &v
}

// handleOverview answers the whole overview page in one request: counts per
// type and health, what needs attention, a day of availability per resource
// and the headline numbers.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.now()
	since, ok := timeParam(w, r, "since")
	if !ok {
		return
	}
	if since.IsZero() {
		since = now.Add(-24 * time.Hour)
	}

	recs, err := s.resources.List(ctx, resource.Filter{IncludeRetired: true})
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	statuses := map[string]monitor.ResourceStatus{}
	for _, st := range s.monitor.Resources() {
		statuses[st.Resource] = st
	}
	alerts, err := s.monitor.ListAlerts(ctx, monitor.AlertsFiring, "")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	firingBy := map[string]int{}
	for _, a := range alerts {
		firingBy[a.Resource]++
	}
	avail, err := s.monitor.AvailabilityAll(ctx, overviewBuckets, overviewStep)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	activeRuns, err := s.engine.CountRuns(ctx, engine.RunQueued, engine.RunRunning, engine.RunSuspended)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	deploys, err := s.catalog.CountDeploymentsSince(ctx, since)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	managed := s.managedIndex(r, model.KindResource)
	items := make([]monitor.OverviewResource, 0, len(recs))
	cards := make([]overviewResourceDTO, 0, len(recs))
	for _, rec := range recs {
		st, err := s.status(ctx, rec)
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		ms := statuses[rec.Name]
		health := ms.Health
		if health == "" {
			health = monitor.HealthUnknown
		}
		isManaged := rec.Phase == resource.PhaseManaged
		items = append(items, monitor.OverviewResource{
			Name: rec.Name, Type: rec.Type, Managed: isManaged, State: st.State,
			Health: health, Since: ms.Since, FiringAlerts: firingBy[rec.Name],
		})
		if !isManaged {
			continue
		}
		card := overviewResourceDTO{
			Name: rec.Name, Type: rec.Type, State: st.State, Health: string(health),
			ManagedBy: managed[rec.Name], HasChecks: len(ms.Checks) > 0, Buckets: []availabilityBucketDTO{},
		}
		if a, ok := avail[rec.Name]; ok {
			card.Uptime = ratio(a.Count, a.Failures)
			for _, b := range a.Buckets {
				card.Buckets = append(card.Buckets, availabilityBucketDTO{Ratio: ratio(b.Count, b.Failures), Failures: b.Failures})
			}
		}
		if rec.Type == model.ResourceService {
			deps, err := s.catalog.Deployments(ctx, rec.Name, 20, 0)
			if err != nil {
				s.fail(w, r, err, nil)
				return
			}
			card.DeployedVersion = deployedVersion(deps)
			if len(deps) > 0 {
				card.LastDeployedAt = lastDeploymentTime(deps[0])
			}
		}
		cards = append(cards, card)
	}
	slices.SortFunc(cards, func(a, b overviewResourceDTO) int { return cmp.Compare(a.Name, b.Name) })

	order := make([]string, 0, len(model.ResourceTypes()))
	for _, t := range model.ResourceTypes() {
		order = append(order, t.Type)
	}
	counts, needing := monitor.Summarize(items, order)

	out := overviewDTO{
		GeneratedAt: now.UTC(), Types: make([]overviewTypeDTO, 0, len(counts)), Attention: make([]overviewAttentionDTO, 0, len(needing)),
		Resources: cards, ActiveRuns: activeRuns, FiringAlerts: len(alerts),
		Deploys: overviewDeploysDTO{Since: since.UTC(), Count: deploys},
	}
	for _, c := range counts {
		d := overviewTypeDTO{Type: c.Type, Total: c.Total, Retired: c.Retired, Health: map[string]int{}}
		for _, h := range []monitor.Health{monitor.HealthHealthy, monitor.HealthDegraded, monitor.HealthDown, monitor.HealthUnknown} {
			d.Health[string(h)] = c.Health[h]
		}
		out.Types = append(out.Types, d)
	}
	for _, a := range needing {
		d := overviewAttentionDTO{
			Name: a.Name, Type: a.Type, State: a.State, Health: string(a.Health), Since: optTime(a.Since),
			FiringAlerts: a.FiringAlerts, Reasons: []message.Message{},
		}
		if ms, ok := statuses[a.Name]; ok {
			d.Reasons = healthOf(ms).ReasonMessages
		}
		out.Attention = append(out.Attention, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// lastDeploymentTime is when a deployment ended, or started if it has not.
func lastDeploymentTime(d catalog.Deployment) *time.Time {
	if !d.FinishedAt.IsZero() {
		return optTime(d.FinishedAt)
	}
	return optTime(d.StartedAt)
}
