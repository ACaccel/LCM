package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const resourcesMigration = 14

// migrateResources is the Go part of 0014_resources.sql. It converts every
// stored document to lcm/v2 with the model's converters, so the stored text
// equals what the current code writes and config sync sees no false drift,
// and moves services into the resource registry under new IDs.
func migrateResources(ctx context.Context, tx *sql.Tx) error {
	now := time.Now().UnixMilli()
	steps := []func(context.Context, *sql.Tx, int64) error{
		convertMachineVersions,
		migrateServices,
		convertPolicyVersions,
		convertModuleVersions,
		migrateOwnership,
		renameRunEvents,
		renameRunNodes,
	}
	for _, step := range steps {
		if err := step(ctx, tx, now); err != nil {
			return err
		}
	}
	for _, table := range []string{"config_ownership_v1", "services", "service_versions", "hosts"} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+table); err != nil {
			return fmt.Errorf("drop %s: %w", table, err)
		}
	}
	return nil
}

// convertMachineVersions converts the machine documents copied by the SQL
// part; at this point every resource version is a machine's.
func convertMachineVersions(ctx context.Context, tx *sql.Tx, _ int64) error {
	type row struct {
		id      string
		version int
		doc     string
	}
	var rows []row
	err := queryRows(ctx, tx, `SELECT resource_id, version, document FROM resource_versions`, func(rs *sql.Rows) error {
		var r row
		if err := rs.Scan(&r.id, &r.version, &r.doc); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		b, _, err := model.ConvertMachineV1JSON([]byte(r.doc))
		if err != nil {
			return fmt.Errorf("convert machine %s version %d: %w", r.id, r.version, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE resource_versions SET document = ? WHERE resource_id = ? AND version = ?`, string(b), r.id, r.version); err != nil {
			return err
		}
	}
	return nil
}

type serviceVersion struct {
	version      int
	doc          []byte
	restoredFrom int
	createdAt    int64
	kind, id     string
	name         string
}

type serviceRow struct {
	version                     int
	state                       string
	since, createdAt, updatedAt int64
}

// migrateServices gives every service name a resource. A name with a
// services row becomes managed (or retired, if it was); a name that only
// has versions was deleted and becomes an unmanaged archive holding them.
func migrateServices(ctx context.Context, tx *sql.Tx, _ int64) error {
	active := map[string]bool{}
	err := queryRows(ctx, tx, `SELECT name FROM resources WHERE phase != 'unmanaged'`, func(rs *sql.Rows) error {
		var name string
		if err := rs.Scan(&name); err != nil {
			return err
		}
		active[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	services := map[string]serviceRow{}
	err = queryRows(ctx, tx, `SELECT name, version, state, state_since, created_at, updated_at FROM services`, func(rs *sql.Rows) error {
		var name string
		var s serviceRow
		if err := rs.Scan(&name, &s.version, &s.state, &s.since, &s.createdAt, &s.updatedAt); err != nil {
			return err
		}
		services[name] = s
		return nil
	})
	if err != nil {
		return err
	}
	versions := map[string][]serviceVersion{}
	var names []string
	err = queryRows(ctx, tx, `SELECT name, version, document, COALESCE(restored_from, 0), created_at, created_by_kind, created_by_id, created_by_name
		FROM service_versions ORDER BY name, version`, func(rs *sql.Rows) error {
		var name, doc string
		var v serviceVersion
		if err := rs.Scan(&name, &v.version, &doc, &v.restoredFrom, &v.createdAt, &v.kind, &v.id, &v.name); err != nil {
			return err
		}
		b, _, err := convertServiceV1([]byte(doc))
		if err != nil {
			return fmt.Errorf("convert service %s version %d: %w", name, v.version, err)
		}
		v.doc = b
		if _, seen := versions[name]; !seen {
			names = append(names, name)
		}
		versions[name] = append(versions[name], v)
		return nil
	})
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := insertService(ctx, tx, name, versions[name], services, active); err != nil {
			return err
		}
	}
	return nil
}

func insertService(ctx context.Context, tx *sql.Tx, name string, vs []serviceVersion, services map[string]serviceRow, active map[string]bool) error {
	last := vs[len(vs)-1]
	version, phase, state, since := last.version, "unmanaged", "", int64(0)
	created, updated := vs[0].createdAt, last.createdAt
	s, ok := services[name]
	if ok {
		version, phase, state, since, created, updated = s.version, "managed", s.state, s.since, s.createdAt, s.updatedAt
		if state == "retired" {
			phase, state = "retired", "stopped"
		}
		if active[name] {
			return fmt.Errorf("service and machine are both named %q: rename one of them with the previous version before upgrading", name)
		}
	}
	var current []byte
	for _, v := range vs {
		if v.version == version {
			current = v.doc
		}
	}
	// The converter validated the document, except a source that migration
	// 15 rewrites, so only the adapter is read here.
	var doc struct {
		Spec struct {
			Adapter string `json:"adapter"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(current, &doc); err != nil {
		return fmt.Errorf("service %s version %d: %w", name, version, err)
	}
	if doc.Spec.Adapter == "" {
		return fmt.Errorf("service %s version %d: no adapter in the converted document", name, version)
	}
	id := NewID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO resources (id, name, type, adapter, phase, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, model.ResourceService, doc.Spec.Adapter, phase, version, created, updated); err != nil {
		return fmt.Errorf("service %s: %w", name, err)
	}
	for _, v := range vs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_versions (resource_id, version, document, restored_from, created_at, actor_kind, actor_id, actor_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, v.version, string(v.doc), v.restoredFrom, v.createdAt, v.kind, v.id, v.name); err != nil {
			return err
		}
	}
	if phase == "unmanaged" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO service_states (resource_id, state, state_since) VALUES (?, ?, ?)`, id, state, since)
	return err
}

// convertPolicyVersions converts stored policies and the module snapshots
// compiled into them, which is what deployed plans are loaded from.
func convertPolicyVersions(ctx context.Context, tx *sql.Tx, now int64) error {
	type row struct {
		name      string
		version   int
		doc, mods string
	}
	var rows []row
	err := queryRows(ctx, tx, `SELECT name, version, document, modules FROM policy_versions`, func(rs *sql.Rows) error {
		var r row
		if err := rs.Scan(&r.name, &r.version, &r.doc, &r.mods); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		b, warns, err := model.ConvertPolicyV1JSON([]byte(r.doc))
		if err != nil {
			return fmt.Errorf("convert policy %s version %d: %w", r.name, r.version, err)
		}
		doc, err := sameLayout(r.doc, b)
		if err != nil {
			return err
		}
		mods := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(r.mods), &mods); err != nil {
			return fmt.Errorf("policy %s version %d modules: %w", r.name, r.version, err)
		}
		for name, m := range mods {
			mb, mw, err := model.ConvertModuleV1JSON(m)
			if err != nil {
				return fmt.Errorf("convert module %s in policy %s version %d: %w", name, r.name, r.version, err)
			}
			mods[name] = mb
			warns = append(warns, mw...)
		}
		mb, err := json.Marshal(mods)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE policy_versions SET document = ?, modules = ? WHERE name = ? AND version = ?`, doc, string(mb), r.name, r.version); err != nil {
			return err
		}
		if err := recordWarnings(ctx, tx, now, "Policy", r.name, r.version, warns); err != nil {
			return err
		}
	}
	return nil
}

func convertModuleVersions(ctx context.Context, tx *sql.Tx, now int64) error {
	type row struct {
		name    string
		version int
		doc     string
	}
	var rows []row
	err := queryRows(ctx, tx, `SELECT name, version, document FROM module_versions`, func(rs *sql.Rows) error {
		var r row
		if err := rs.Scan(&r.name, &r.version, &r.doc); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		b, warns, err := model.ConvertModuleV1JSON([]byte(r.doc))
		if err != nil {
			return fmt.Errorf("convert module %s version %d: %w", r.name, r.version, err)
		}
		doc, err := sameLayout(r.doc, b)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE module_versions SET document = ? WHERE name = ? AND version = ?`, doc, r.name, r.version); err != nil {
			return err
		}
		if err := recordWarnings(ctx, tx, now, "Module", r.name, r.version, warns); err != nil {
			return err
		}
	}
	return nil
}

// migrateOwnership copies Git ownership with Service and Machine merged into
// Resource. The stored repository documents are converted the same way
// "lcm convert" converts the repository files, so a converted repository
// matches them and nothing looks drifted.
func migrateOwnership(ctx context.Context, tx *sql.Tx, _ int64) error {
	type row struct {
		kind, name, source, path, commit string
		applied                          int
		doc, pending, pendingPath        string
		conflict                         string
		acknowledged, orphaned           int
		appliedAt                        int64
	}
	var rows []row
	err := queryRows(ctx, tx, `SELECT kind, name, source, path, commit_id, applied_version, document, pending, pending_path,
		conflict_commit, acknowledged, orphaned, applied_at FROM config_ownership_v1`, func(rs *sql.Rows) error {
		var r row
		if err := rs.Scan(&r.kind, &r.name, &r.source, &r.path, &r.commit, &r.applied, &r.doc, &r.pending, &r.pendingPath,
			&r.conflict, &r.acknowledged, &r.orphaned, &r.appliedAt); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return err
	}
	converters := map[string]func([]byte) ([]byte, []string, error){
		"Service": convertServiceV1,
		"Machine": model.ConvertMachineV1JSON,
		"Policy":  model.ConvertPolicyV1JSON,
		"Module":  model.ConvertModuleV1JSON,
	}
	seen := map[string]string{}
	for _, r := range rows {
		convert, ok := converters[r.kind]
		if !ok {
			return fmt.Errorf("config ownership of %s %s: unknown kind", r.kind, r.name)
		}
		kind := r.kind
		if kind == "Service" || kind == "Machine" {
			kind = string(model.KindResource)
		}
		key := kind + "/" + r.name
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("config ownership of %s %s and %s %s would merge into Resource %s: release one of them with the previous version before upgrading", prev, r.name, r.kind, r.name, r.name)
		}
		seen[key] = r.kind
		doc, _, err := convert([]byte(r.doc))
		if err != nil {
			return fmt.Errorf("convert owned %s %s: %w", r.kind, r.name, err)
		}
		pending := ""
		if r.pending != "" {
			b, _, err := convert([]byte(r.pending))
			if err != nil {
				return fmt.Errorf("convert pending %s %s: %w", r.kind, r.name, err)
			}
			pending = string(b)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO config_ownership (kind, name, source, path, commit_id, applied_version, document, pending, pending_path,
			conflict_commit, acknowledged, orphaned, applied_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			kind, r.name, r.source, r.path, r.commit, r.applied, string(doc), pending, r.pendingPath,
			r.conflict, r.acknowledged, r.orphaned, r.appliedAt); err != nil {
			return err
		}
	}
	return nil
}

// renameRunEvents renames the service field of signal events that started
// runs which are still active, because their converted policies now read
// trigger.resource.
func renameRunEvents(ctx context.Context, tx *sql.Tx, _ int64) error {
	type row struct{ id, event string }
	var rows []row
	err := queryRows(ctx, tx, `SELECT id, event FROM runs WHERE status IN ('queued', 'running', 'suspended')`, func(rs *sql.Rows) error {
		var r row
		if err := rs.Scan(&r.id, &r.event); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		var ev map[string]json.RawMessage
		if json.Unmarshal([]byte(r.event), &ev) != nil {
			continue
		}
		_, hasKind := ev["kind"]
		_, hasSource := ev["source"]
		svc, hasService := ev["service"]
		_, hasResource := ev["resource"]
		if !hasKind || !hasSource || !hasService || hasResource {
			continue
		}
		ev["resource"] = svc
		delete(ev, "service")
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET event = ? WHERE id = ?`, string(b), r.id); err != nil {
			return err
		}
	}
	return nil
}

// renameRunNodes renames the service key of the checkpointed output and
// props of lifecycle nodes in runs that are still active. Their converted
// policies read nodes.<id>.resource, and a recovered run restores the
// output of completed nodes from these checkpoints.
func renameRunNodes(ctx context.Context, tx *sql.Tx, _ int64) error {
	type row struct {
		run, node     string
		props, output sql.NullString
		doc, mods     string
	}
	var rows []row
	err := queryRows(ctx, tx, `SELECT n.run_id, n.node_id, n.props, n.output, v.document, v.modules FROM run_nodes n
		JOIN runs r ON r.id = n.run_id JOIN policy_versions v ON v.name = r.policy AND v.version = r.version
		WHERE r.status IN ('queued', 'running', 'suspended')`, func(rs *sql.Rows) error {
		var r row
		if err := rs.Scan(&r.run, &r.node, &r.props, &r.output, &r.doc, &r.mods); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return err
	}
	types := map[string]map[string]string{}
	for _, r := range rows {
		key := r.doc + "\x00" + r.mods
		nt, ok := types[key]
		if !ok {
			nt = runNodeTypes(r.doc, r.mods)
			types[key] = nt
		}
		if !strings.HasPrefix(nt[r.node], "lifecycle.") {
			continue
		}
		props, pchanged := renameServiceKey(r.props)
		output, ochanged := renameServiceKey(r.output)
		if !pchanged && !ochanged {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_nodes SET props = ?, output = ? WHERE run_id = ? AND node_id = ?`, props, output, r.run, r.node); err != nil {
			return err
		}
	}
	return nil
}

// runNodeTypes maps the node IDs of a policy's runs to node types, with the
// "<module node>/<inner node>" IDs the engine gives nodes of modules. A
// document it cannot read yields no types, so its runs are left alone.
func runNodeTypes(doc, mods string) map[string]string {
	type node struct {
		ID    string         `json:"id"`
		Type  string         `json:"type"`
		Props map[string]any `json:"props"`
	}
	type spec struct {
		Spec struct {
			Nodes []node `json:"nodes"`
		} `json:"spec"`
	}
	modules := map[string]spec{}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(mods), &raw) == nil {
		for name, m := range raw {
			var s spec
			if json.Unmarshal(m, &s) == nil {
				modules[name] = s
			}
		}
	}
	out := map[string]string{}
	var walk func(nodes []node, prefix string, depth int)
	walk = func(nodes []node, prefix string, depth int) {
		for _, n := range nodes {
			out[prefix+n.ID] = n.Type
			name, _ := n.Props["module"].(string)
			if m, ok := modules[name]; ok && n.Type == "module" && depth < 16 {
				walk(m.Spec.Nodes, prefix+n.ID+"/", depth+1)
			}
		}
	}
	var p spec
	if json.Unmarshal([]byte(doc), &p) == nil {
		walk(p.Spec.Nodes, "", 0)
	}
	return out
}

// renameServiceKey renames the top-level service key of a JSON object to
// resource. Anything else, including an object that already has resource,
// is returned unchanged.
func renameServiceKey(v sql.NullString) (sql.NullString, bool) {
	if !v.Valid {
		return v, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(v.String), &obj) != nil {
		return v, false
	}
	svc, hasService := obj["service"]
	if _, hasResource := obj["resource"]; !hasService || hasResource {
		return v, false
	}
	obj["resource"] = svc
	delete(obj, "service")
	b, err := json.Marshal(obj)
	if err != nil {
		return v, false
	}
	return sql.NullString{String: string(b), Valid: true}, true
}

func recordWarnings(ctx context.Context, tx *sql.Tx, now int64, kind, name string, version int, warns []string) error {
	for _, w := range warns {
		if _, err := tx.ExecContext(ctx, `INSERT INTO migration_warnings (migration, kind, name, version, message, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			resourcesMigration, kind, name, version, w, now); err != nil {
			return err
		}
	}
	return nil
}

// sameLayout returns canon in the layout of the text it replaces: the
// stores that keep indented JSON compare new saves with indented text.
func sameLayout(old string, canon []byte) (string, error) {
	if !strings.Contains(old, "\n") {
		return string(canon), nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, canon, "", "  "); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// queryRows runs a query and calls scan for each row. Rows are always read
// completely before the caller writes, so reads and writes on the
// transaction never interleave.
func queryRows(ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows) error, args ...any) error {
	rs, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		if err := scan(rs); err != nil {
			return err
		}
	}
	return rs.Err()
}
