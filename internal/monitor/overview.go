package monitor

import (
	"cmp"
	"slices"
	"time"
)

// OverviewResource is one resource as the overview counts it. Retired
// resources are counted but never need attention.
type OverviewResource struct {
	Name    string
	Type    string
	Managed bool
	// State is the resource type's own state, such as "failed".
	State  string
	Health Health
	// Since is when the combined health last changed; zero when unknown.
	Since time.Time
	// FiringAlerts is the number of external alerts firing for it.
	FiringAlerts int
}

// TypeCount is the number of resources of one type. Health counts only the
// managed resources.
type TypeCount struct {
	Type    string
	Total   int
	Retired int
	Health  map[Health]int
}

// StateFailed is the state that marks a resource as failed.
const StateFailed = "failed"

// NeedsAttention reports why an overview should surface the resource: its
// combined health is down or degraded, its state is failed, or an alert is
// firing for it. Healthy and unknown resources without those are quiet.
func (r OverviewResource) NeedsAttention() bool {
	return r.Managed && r.attentionRank() >= 0
}

// attentionRank orders the reasons from most to least urgent; -1 means none.
func (r OverviewResource) attentionRank() int {
	switch {
	case r.Health == HealthDown:
		return 0
	case r.Health == HealthDegraded:
		return 1
	case r.State == StateFailed:
		return 2
	case r.FiringAlerts > 0:
		return 3
	default:
		return -1
	}
}

// Summarize counts resources per type in the given type order (types the
// order lacks follow, sorted) and lists the managed resources that need
// attention, most urgent first, then longest-standing, then by name.
func Summarize(items []OverviewResource, typeOrder []string) ([]TypeCount, []OverviewResource) {
	byType := map[string]*TypeCount{}
	var attention []OverviewResource
	for _, r := range items {
		c := byType[r.Type]
		if c == nil {
			c = &TypeCount{Type: r.Type, Health: map[Health]int{}}
			byType[r.Type] = c
		}
		c.Total++
		if !r.Managed {
			c.Retired++
			continue
		}
		h := r.Health
		if h == "" {
			h = HealthUnknown
		}
		c.Health[h]++
		if r.NeedsAttention() {
			attention = append(attention, r)
		}
	}
	slices.SortFunc(attention, func(a, b OverviewResource) int {
		if c := cmp.Compare(a.attentionRank(), b.attentionRank()); c != 0 {
			return c
		}
		// An unset Since sorts last: nothing is known about how long.
		switch {
		case a.Since.IsZero() != b.Since.IsZero():
			if a.Since.IsZero() {
				return 1
			}
			return -1
		case !a.Since.Equal(b.Since):
			return a.Since.Compare(b.Since)
		}
		return cmp.Compare(a.Name, b.Name)
	})

	counts := make([]TypeCount, 0, len(byType))
	for _, t := range typeOrder {
		if c := byType[t]; c != nil {
			counts = append(counts, *c)
			delete(byType, t)
		}
	}
	rest := make([]TypeCount, 0, len(byType))
	for _, c := range byType {
		rest = append(rest, *c)
	}
	slices.SortFunc(rest, func(a, b TypeCount) int { return cmp.Compare(a.Type, b.Type) })
	return append(counts, rest...), attention
}
