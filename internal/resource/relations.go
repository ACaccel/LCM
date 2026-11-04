package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ACaccel/lcm/internal/model"
)

// Relations returns the relations of every resource that is not unmanaged,
// by name. Targets may name resources that do not exist.
func (s *Store) Relations(ctx context.Context) (map[string][]model.Relation, error) {
	return loadRelations(ctx, s.db.Reader())
}

// graphNode is one resource that is not unmanaged, as the relation graph
// sees it.
type graphNode struct {
	Name, Type, Phase string
	Relations         []model.Relation
}

func loadNodes(ctx context.Context, q querier) (map[string]graphNode, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT r.name, r.type, r.phase, v.document FROM resources r
		 JOIN resource_versions v ON v.resource_id = r.id AND v.version = r.version
		 WHERE r.phase != ?`, PhaseUnmanaged)
	if err != nil {
		return nil, fmt.Errorf("resource: load relations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]graphNode{}
	for rows.Next() {
		var n graphNode
		var doc string
		if err := rows.Scan(&n.Name, &n.Type, &n.Phase, &doc); err != nil {
			return nil, fmt.Errorf("resource: load relations: %w", err)
		}
		// Only the relations are needed; stored documents are canonical,
		// so a partial decode is enough and avoids validating every one.
		var d struct {
			Spec struct {
				Relations []model.Relation `json:"relations"`
			} `json:"spec"`
		}
		if err := json.Unmarshal([]byte(doc), &d); err != nil {
			return nil, fmt.Errorf("resource: stored document of %s is invalid: %w", n.Name, err)
		}
		n.Relations = d.Spec.Relations
		out[n.Name] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resource: load relations: %w", err)
	}
	return out, nil
}

func loadRelations(ctx context.Context, q querier) (map[string][]model.Relation, error) {
	nodes, err := loadNodes(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]model.Relation, len(nodes))
	for name, n := range nodes {
		out[name] = n.Relations
	}
	return out, nil
}

// checkRelations resolves the relations self is about to store. It returns
// the targets that do not exist and rejects a dependsOn/runsOn cycle. The
// stored graph has no cycle, so a new one must pass through self.
func checkRelations(ctx context.Context, q querier, self string, rels []model.Relation) ([]string, error) {
	if len(rels) == 0 {
		return nil, nil
	}
	all, err := loadRelations(ctx, q)
	if err != nil {
		return nil, err
	}
	var unresolved []string
	for _, r := range rels {
		if _, ok := all[r.Target]; !ok && !slices.Contains(unresolved, r.Target) {
			unresolved = append(unresolved, r.Target)
		}
	}
	slices.Sort(unresolved)
	var is model.Issues
	for i, r := range rels {
		if !ordering(r.Type) {
			continue
		}
		if path := findPath(all, r.Target, self); path != nil {
			is = append(is, model.Issue{
				Path:    fmt.Sprintf("spec.relations[%d]", i),
				Message: "creates a dependsOn/runsOn cycle: " + strings.Join(append([]string{self}, path...), " -> "),
			})
		}
	}
	if len(is) > 0 {
		return nil, invalid(is)
	}
	return unresolved, nil
}

// ordering reports whether a relation type orders resources and therefore
// must not form a cycle. A control path may lead back: a router can be
// reached through the machine it serves.
func ordering(typ string) bool {
	return typ == model.RelationDependsOn || typ == model.RelationRunsOn
}

// findPath returns the shortest path of ordering relations from start to
// goal (both included), or nil. The edges of goal itself are not followed:
// they are being replaced.
func findPath(all map[string][]model.Relation, start, goal string) []string {
	parent := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n == goal {
			var path []string
			for ; n != ""; n = parent[n] {
				path = append(path, n)
			}
			slices.Reverse(path)
			return path
		}
		for _, r := range all[n] {
			if _, seen := parent[r.Target]; seen || !ordering(r.Type) {
				continue
			}
			parent[r.Target] = n
			queue = append(queue, r.Target)
		}
	}
	return nil
}
