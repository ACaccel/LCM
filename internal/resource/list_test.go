package resource_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ACaccel/lcm/internal/resource"
)

func TestListPagesByNameAfterFiltering(t *testing.T) {
	h := newHarness(t)
	for i := range 7 {
		tier := "web"
		if i%2 == 1 {
			tier = "api"
		}
		h.put(t, fmt.Appendf(nil, "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: s%d, labels: { tier: %s, \"app.example/x\": \"1\" } }\nspec: { type: service, adapter: external }\n", i, tier), 0)
	}
	h.put(t, machine("host"), 0)
	if err := h.store.SetPhase(h.ctx, "s2", resource.PhaseRetired, 1, ""); err != nil {
		t.Fatal(err)
	}
	pages := func(f resource.Filter) [][]string {
		t.Helper()
		var out [][]string
		for {
			recs, err := h.store.List(h.ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) == 0 {
				return out
			}
			var page []string
			for _, r := range recs {
				page = append(page, r.Name)
			}
			out = append(out, page)
			f.After = page[len(page)-1]
		}
	}
	got := pages(resource.Filter{IncludeRetired: true, Labels: map[string]string{"tier": "web"}, Limit: 2})
	if want := [][]string{{"s0", "s2"}, {"s4", "s6"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("web pages = %v, want %v", got, want)
	}
	got = pages(resource.Filter{Phase: resource.PhaseManaged, Labels: map[string]string{"tier": "web", "app.example/x": "1"}, Limit: 2})
	if want := [][]string{{"s0", "s4"}, {"s6"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("managed web pages = %v, want %v", got, want)
	}
	got = pages(resource.Filter{Phase: resource.PhaseRetired, Limit: 5})
	if want := [][]string{{"s2"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("retired = %v, want %v", got, want)
	}
	got = pages(resource.Filter{Type: "service", Labels: map[string]string{"tier": "nope"}})
	if len(got) != 0 {
		t.Fatalf("unmatched label = %v", got)
	}
	got = pages(resource.Filter{Phase: resource.PhaseManaged, After: "host", Limit: 3})
	if want := [][]string{{"s0", "s1", "s3"}, {"s4", "s5", "s6"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("after host = %v, want %v", got, want)
	}
	if _, err := h.store.List(h.ctx, resource.Filter{Phase: resource.PhaseUnmanaged}); !errors.Is(err, resource.ErrPrecondition) {
		t.Fatalf("unmanaged phase: %v", err)
	}
}
