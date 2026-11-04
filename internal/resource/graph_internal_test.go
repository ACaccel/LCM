package resource

import (
	"slices"
	"testing"

	"github.com/ACaccel/lcm/internal/model"
)

func rel(typ, target string) model.Relation { return model.Relation{Type: typ, Target: target} }

func TestImpactIsCycleSafe(t *testing.T) {
	// Save-time validation keeps cycles out of the store; the traversal must
	// still terminate if one appears (for example in data from an older
	// version).
	nodes := map[string]graphNode{
		"a": {Name: "a", Type: "service", Phase: PhaseManaged, Relations: []model.Relation{rel(model.RelationDependsOn, "b")}},
		"b": {Name: "b", Type: "service", Phase: PhaseManaged, Relations: []model.Relation{rel(model.RelationDependsOn, "c")}},
		"c": {Name: "c", Type: "service", Phase: PhaseManaged, Relations: []model.Relation{rel(model.RelationDependsOn, "a")}},
		"r": {Name: "r", Type: "router", Phase: PhaseManaged, Relations: []model.Relation{rel(model.RelationControlPath, "a")}},
	}
	imp := computeImpact(nodes, "a")
	var got []string
	for _, d := range imp.Dependents {
		got = append(got, d.Name)
	}
	if !slices.Equal(got, []string{"c", "b"}) || imp.Dependents[1].Depth != 2 || imp.Dependents[1].Via != "c" {
		t.Fatalf("dependents = %+v", imp.Dependents)
	}
	if len(imp.ControlDependents) != 1 || imp.ControlDependents[0].Name != "r" {
		t.Fatalf("control dependents = %+v", imp.ControlDependents)
	}
	if order := dependencyOrder(nodes, []string{"c", "a", "b"}); !slices.Equal(order, []string{"a", "b", "c"}) {
		t.Fatalf("cyclic order = %v", order)
	}
}
