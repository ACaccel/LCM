package resource

import (
	"cmp"
	"context"
	"slices"

	"github.com/ACaccel/lcm/internal/model"
)

// Affected is one resource an action on another resource reaches.
type Affected struct {
	Name string
	Type string
	// Relation is the relation of Affected to Via: dependsOn or runsOn for
	// a dependent, controlPath for a control dependent.
	Relation string
	// Via is the resource the relation targets: the acted-on resource
	// itself at depth 1, otherwise a dependent closer to it.
	Via   string
	Depth int
}

// Impact is what an action on Resource reaches through the relations of
// managed resources.
type Impact struct {
	Resource string
	// Dependents depend on Resource directly or transitively through
	// dependsOn and runsOn.
	Dependents []Affected
	// ControlDependents are reached through Resource or one of its
	// dependents (controlPath): acting on it can cut LCM off from them.
	ControlDependents []Affected
	// Unresolved are relation targets of Resource that do not exist.
	Unresolved []string
}

// Empty reports whether nothing else is affected. Unresolved targets do not
// count: they name nothing that could be disrupted.
func (i Impact) Empty() bool { return len(i.Dependents) == 0 && len(i.ControlDependents) == 0 }

// Names is the audit form of the impact: names only, by relation kind.
func (i Impact) Names() map[string][]string {
	names := func(as []Affected) []string {
		out := make([]string, len(as))
		for k, a := range as {
			out[k] = a.Name
		}
		return out
	}
	return map[string][]string{"dependents": names(i.Dependents), "controlDependents": names(i.ControlDependents)}
}

// Impact computes what acting on the resource ref reaches. Only managed
// resources count as affected: retired ones are neither observed nor
// controlled, so they cannot be disrupted, and their relations are ignored.
// An archive (unmanaged) has no impact.
func (s *Store) Impact(ctx context.Context, ref string) (Impact, error) {
	rec, err := s.Get(ctx, ref)
	if err != nil {
		return Impact{}, err
	}
	if rec.Phase == PhaseUnmanaged {
		return Impact{Resource: rec.Name, Dependents: []Affected{}, ControlDependents: []Affected{}, Unresolved: []string{}}, nil
	}
	nodes, err := loadNodes(ctx, s.db.Reader())
	if err != nil {
		return Impact{}, err
	}
	return computeImpact(nodes, rec.Name), nil
}

type reverseEdge struct{ from, typ string }

func computeImpact(nodes map[string]graphNode, name string) Impact {
	imp := Impact{Resource: name, Dependents: []Affected{}, ControlDependents: []Affected{}, Unresolved: []string{}}
	for _, r := range nodes[name].Relations {
		if _, ok := nodes[r.Target]; !ok && !slices.Contains(imp.Unresolved, r.Target) {
			imp.Unresolved = append(imp.Unresolved, r.Target)
		}
	}
	slices.Sort(imp.Unresolved)

	reverse := map[string][]reverseEdge{}
	for _, n := range nodes {
		if n.Phase != PhaseManaged {
			continue
		}
		for _, r := range n.Relations {
			reverse[r.Target] = append(reverse[r.Target], reverseEdge{from: n.Name, typ: r.Type})
		}
	}
	for _, edges := range reverse {
		// Visit in name order so that equal-depth paths resolve the same
		// way on every call.
		slices.SortFunc(edges, func(a, b reverseEdge) int {
			return cmp.Or(cmp.Compare(a.from, b.from), cmp.Compare(a.typ, b.typ))
		})
	}

	// Breadth-first over reversed ordering relations: the first visit is
	// the shortest path, and the visited set makes cycles harmless.
	seen := map[string]bool{name: true}
	type step struct {
		name  string
		depth int
	}
	frontier := []step{{name, 0}}
	for i := 0; i < len(frontier); i++ {
		cur := frontier[i]
		for _, e := range reverse[cur.name] {
			if !ordering(e.typ) || seen[e.from] {
				continue
			}
			seen[e.from] = true
			imp.Dependents = append(imp.Dependents, Affected{
				Name: e.from, Type: nodes[e.from].Type, Relation: e.typ, Via: cur.name, Depth: cur.depth + 1,
			})
			frontier = append(frontier, step{e.from, cur.depth + 1})
		}
	}
	controlled := map[string]bool{name: true}
	for _, cur := range frontier {
		for _, e := range reverse[cur.name] {
			if e.typ != model.RelationControlPath || controlled[e.from] {
				continue
			}
			controlled[e.from] = true
			imp.ControlDependents = append(imp.ControlDependents, Affected{
				Name: e.from, Type: nodes[e.from].Type, Relation: e.typ, Via: cur.name, Depth: cur.depth + 1,
			})
		}
	}
	byDepth := func(a, b Affected) int { return cmp.Or(cmp.Compare(a.Depth, b.Depth), cmp.Compare(a.Name, b.Name)) }
	slices.SortStableFunc(imp.Dependents, byDepth)
	slices.SortStableFunc(imp.ControlDependents, byDepth)
	return imp
}

// DependencyOrder sorts names so that every resource comes after the
// resources it depends on, directly or through resources outside names
// (dependsOn and runsOn of resources that are not unmanaged). Unrelated
// resources keep name order. Stored relations have no cycle; should one
// appear anyway, its members are appended in name order instead of failing.
func (s *Store) DependencyOrder(ctx context.Context, names []string) ([]string, error) {
	nodes, err := loadNodes(ctx, s.db.Reader())
	if err != nil {
		return nil, err
	}
	return dependencyOrder(nodes, names), nil
}

func dependencyOrder(nodes map[string]graphNode, names []string) []string {
	selected := map[string]bool{}
	for _, n := range names {
		selected[n] = true
	}
	// needs[n] are the selected resources n reaches through ordering
	// relations; the search stops at selected resources, whose own needs
	// order them in turn.
	needs := map[string]map[string]bool{}
	for n := range selected {
		needs[n] = map[string]bool{}
		seen := map[string]bool{n: true}
		stack := []string{n}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, r := range nodes[cur].Relations {
				if !ordering(r.Type) || seen[r.Target] {
					continue
				}
				seen[r.Target] = true
				if selected[r.Target] {
					needs[n][r.Target] = true
					continue
				}
				stack = append(stack, r.Target)
			}
		}
	}
	remaining := slices.Sorted(func(yield func(string) bool) {
		for n := range selected {
			if !yield(n) {
				return
			}
		}
	})
	out := make([]string, 0, len(remaining))
	done := map[string]bool{}
	for len(remaining) > 0 {
		i := slices.IndexFunc(remaining, func(n string) bool {
			for dep := range needs[n] {
				if !done[dep] {
					return false
				}
			}
			return true
		})
		if i < 0 {
			return append(out, remaining...)
		}
		done[remaining[i]] = true
		out = append(out, remaining[i])
		remaining = slices.Delete(remaining, i, i+1)
	}
	return out
}
