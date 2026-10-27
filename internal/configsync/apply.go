package configsync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
)

// Action is what happened (or, in a dry run, would happen) to a document.
type Action string

// Actions.
const (
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionUnchanged Action = "unchanged"
	// ActionSkipped: a rule kept the document from being applied; Reason
	// says which. It is not an error in the document.
	ActionSkipped Action = "skipped"
	ActionFailed  Action = "failed"
)

// Options tune Apply.
type Options struct {
	// DryRun validates and diffs without writing anything.
	DryRun bool
	// Force applies documents of resources that a config source owns. The
	// resource stays owned (and so drifted from the repository).
	Force bool
}

// Result is the outcome for one document.
type Result struct {
	// Index is the position of the document in the input of Apply, so that
	// callers can map a result back to the file it came from.
	Index  int
	Kind   model.Kind
	Name   string
	Path   string
	Action Action
	// Version is the resource version after the apply; zero when nothing
	// was written or known.
	Version int
	// Diff is a unified diff of the canonical YAML, current to new.
	Diff string
	// Reason explains skipped and failed results and notable successes.
	Reason string
	// Issues are the validation problems of a failed document.
	Issues model.Issues
	// Warnings are non-fatal findings, such as unreachable policy nodes.
	Warnings model.Issues
	// Conflict: the repository changed a resource that was edited locally.
	Conflict bool
	// Drifted: the resource differs from its repository document.
	Drifted bool
}

// Ref returns the resource the result is about.
func (r Result) Ref() Ref { return Ref{r.Kind, r.Name} }

// applyOpts adds the source context that only the sync supplies.
type applyOpts struct {
	Options
	// source, when set, makes the apply a sync on behalf of that source.
	source string
	commit string
	// notify allows notifications about invalid documents; conflicts are
	// de-duplicated by their commit instead.
	notify bool
}

// item is a document after decoding.
type item struct {
	doc   Document
	index int
	Ref
	parsed model.Document
	// canon is the canonical JSON of the decoded document.
	canon string
	err   error
}

func parseItem(d Document) *item {
	it := &item{doc: d}
	parsed, err := model.Decode(d.Data)
	if err != nil {
		it.Kind, it.Name = Head(d.Data)
		it.err = err
		return it
	}
	b, err := model.Encode(parsed)
	if err != nil {
		it.Kind, it.Name = parsed.DocKind(), parsed.DocName()
		it.err = err
		return it
	}
	it.parsed, it.canon = parsed, string(b)
	it.Kind, it.Name = parsed.DocKind(), parsed.DocName()
	return it
}

func issuesOf(err error) model.Issues {
	var is model.Issues
	if errors.As(err, &is) {
		return is
	}
	return model.Issues{{Message: err.Error()}}
}

func actorOf(ctx context.Context) (auth.Actor, bool) { return auth.ActorFrom(ctx) }

// overlayModules resolves modules from a pending set before the store, so
// that a dry run can compile a policy against modules the same batch
// creates.
type overlayModules struct {
	pending map[string]*model.Module
	next    engine.ModuleResolver
}

func (o overlayModules) ResolveModule(ctx context.Context, name string) (*model.Module, error) {
	if m, ok := o.pending[name]; ok {
		return m, nil
	}
	return o.next.ResolveModule(ctx, name)
}

// validate runs the checks that need the running system on top of the
// structural ones Decode already did.
func (s *Syncer) validate(ctx context.Context, it *item, overlay engine.ModuleResolver) (issues, warnings model.Issues) {
	switch d := it.parsed.(type) {
	case *model.Service:
		hosts, err := s.cat.Hosts(ctx)
		if err != nil {
			return model.Issues{{Message: err.Error()}}, nil
		}
		known := false
		for _, h := range hosts {
			known = known || h.Name == d.Spec.EffectiveHost()
		}
		if !known {
			issues = append(issues, model.Issue{Path: "spec.host", Message: fmt.Sprintf("unknown host %q", d.Spec.EffectiveHost())})
		}
		if s.checks != nil {
			issues = append(issues, s.checks.ValidateChecks(d.Spec.Checks, "spec.checks")...)
		}
	case *model.Policy:
		var res engine.ModuleResolver = s.mods
		if overlay != nil {
			res = overlay
		}
		w, err := s.eng.ValidateWith(ctx, it.doc.Data, res)
		if err != nil {
			return issuesOf(err), nil
		}
		warnings = w
	}
	return issues, warnings
}

// write stores a document through its component, which audits the change
// under the actor in ctx. expected is the current version, 0 to create.
func (s *Syncer) write(ctx context.Context, it *item, expected int) (int, error) {
	switch it.Kind {
	case model.KindService:
		res, err := s.cat.Put(ctx, it.doc.Data, expected)
		return res.Version, err
	case model.KindModule:
		res, err := s.mods.Put(ctx, it.doc.Data, expected)
		return res.Version, err
	case model.KindPolicy:
		return s.eng.Deploy(ctx, it.doc.Data)
	}
	return 0, fmt.Errorf("configsync: unsupported kind %q", it.Kind)
}

// diff renders the change from one canonical document to another; either
// may be empty.
func diff(ref Ref, from, to string) (string, error) {
	render := func(canon string) (string, error) {
		if canon == "" {
			return "", nil
		}
		b, err := canonicalYAML([]byte(canon))
		return string(b), err
	}
	a, err := render(from)
	if err != nil {
		return "", err
	}
	b, err := render(to)
	if err != nil {
		return "", err
	}
	return unifiedDiff(ref.String(), ref.String(), a, b), nil
}

// batch is the state shared by the documents of one apply.
type batch struct {
	o applyOpts
	// moduleChanged is set once a module was created or updated, so that
	// policies that did not change themselves are recompiled against it.
	moduleChanged bool
	overlay       engine.ModuleResolver
}

// Apply validates and applies documents: modules first, then services,
// then policies. It is not transactional; every document gets its own
// result, and a failure does not stop the others. The error is only for
// problems with the call itself, such as a missing actor.
func (s *Syncer) Apply(ctx context.Context, docs []Document, opts Options) ([]Result, error) {
	return s.apply(ctx, docs, applyOpts{Options: opts})
}

func (s *Syncer) apply(ctx context.Context, docs []Document, o applyOpts) ([]Result, error) {
	if _, ok := actorOf(ctx); !ok {
		return nil, audit.ErrNoActor
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	items := make([]*item, len(docs))
	for i, d := range docs {
		items[i] = parseItem(d)
		items[i].index = i
	}
	rank := func(it *item) int {
		if it.err != nil {
			return 0
		}
		return kindRank(it.Kind)
	}
	sort.SliceStable(items, func(i, j int) bool { return rank(items[i]) < rank(items[j]) })

	b := &batch{o: o}
	if o.DryRun {
		pending := map[string]*model.Module{}
		for _, it := range items {
			if m, ok := it.parsed.(*model.Module); ok && it.err == nil {
				pending[m.Metadata.Name] = m
			}
		}
		b.overlay = overlayModules{pending: pending, next: s.mods}
	}

	seen := map[Ref]string{}
	results := make([]Result, 0, len(items))
	for _, it := range items {
		var res Result
		switch {
		case it.err != nil:
			res = Result{Kind: it.Kind, Name: it.Name, Path: it.doc.Path, Action: ActionFailed, Reason: "invalid document", Issues: issuesOf(it.err)}
		case seen[it.Ref] != "":
			res = Result{Kind: it.Kind, Name: it.Name, Path: it.doc.Path, Action: ActionFailed,
				Reason: fmt.Sprintf("%s is defined twice; the other definition is in %s", it.Ref, seen[it.Ref])}
		default:
			seen[it.Ref] = it.doc.Path
			res = s.applyOne(ctx, it, b)
		}
		if res.Action == ActionFailed && len(res.Issues) > 0 && o.source != "" && o.notify && !o.DryRun {
			s.notifyInvalid(ctx, res, o)
		}
		res.Index = it.index
		results = append(results, res)
	}
	if !o.DryRun {
		s.auditApply(ctx, results, o)
	}
	// Documents were processed in dependency order; callers read the results
	// in the order they supplied the documents.
	sort.SliceStable(results, func(i, j int) bool { return results[i].Index < results[j].Index })
	return results, nil
}

func (s *Syncer) auditApply(ctx context.Context, results []Result, o applyOpts) {
	counts := map[string]any{}
	quiet := true
	for _, r := range results {
		n, _ := counts[string(r.Action)].(int)
		counts[string(r.Action)] = n + 1
		quiet = quiet && r.Action == ActionUnchanged
	}
	if quiet {
		return
	}
	target, outcome := "manual", audit.OutcomeOK
	if o.source != "" {
		target = "source:" + o.source
		counts["commit"] = o.commit
	}
	if o.Force {
		counts["force"] = true
	}
	if counts[string(ActionFailed)] != nil {
		outcome = audit.OutcomeError
	}
	s.record(ctx, "config.apply", target, outcome, counts)
}

// applyOne decides and performs the apply of one valid document.
func (s *Syncer) applyOne(ctx context.Context, it *item, b *batch) Result {
	res := Result{Kind: it.Kind, Name: it.Name, Path: it.doc.Path}
	fail := func(reason string, err error) Result {
		res.Action, res.Reason = ActionFailed, reason
		if err != nil {
			var is model.Issues
			if errors.As(err, &is) {
				res.Issues = is
			} else if reason == "" {
				res.Reason = err.Error()
			} else {
				res.Reason = reason + ": " + err.Error()
			}
		}
		return res
	}

	issues, warnings := s.validate(ctx, it, b.overlay)
	res.Warnings = warnings
	if len(issues) > 0 {
		res.Issues = issues
		return fail("validation failed", nil)
	}
	cur, err := s.currentOf(ctx, it.Ref)
	if err != nil {
		return fail("cannot read the current resource", err)
	}
	own, err := s.loadOwn(ctx, it.Ref)
	if err != nil {
		return fail("cannot read ownership", err)
	}

	claim := false
	switch {
	case b.o.source == "":
		// A document that equals the current content asks for no change, so
		// it is not refused: exporting everything and applying it back must
		// work whatever is owned.
		if own != nil && !b.o.Force && (cur == nil || cur.canon != it.canon) {
			res.Action = ActionSkipped
			res.Reason = fmt.Sprintf("managed by source %q (%s); change the repository, or use force to override", own.Source, own.Path)
			res.Drifted = isDrifted(*own, cur)
			return res
		}
	case own != nil && own.Source != b.o.source:
		return fail(fmt.Sprintf("already managed by source %q (%s)", own.Source, own.Path), nil)
	case own != nil:
		if isDrifted(*own, cur) {
			return s.applyDrifted(ctx, it, res, own, cur, b)
		}
		claim = true
	default:
		claim = true
		if cur != nil {
			if cur.canon != it.canon {
				return s.applyAdoption(ctx, it, res, cur, b)
			}
			res.Reason = "adopted an existing resource with the same content"
		}
	}

	curCanon := ""
	if cur != nil {
		curCanon = cur.canon
	}
	if res.Diff, err = diff(it.Ref, curCanon, it.canon); err != nil {
		return fail("cannot render the diff", err)
	}
	version := 0
	if cur != nil {
		version = cur.version
	}

	switch {
	case cur != nil && cur.canon == it.canon:
		res.Action = ActionUnchanged
		// A policy whose own document is unchanged still has to pick up a
		// module that changed in this batch.
		if it.Kind == model.KindPolicy && b.moduleChanged && !b.o.DryRun {
			v, err := s.write(ctx, it, version)
			if err != nil {
				return fail("deploy failed", err)
			}
			if v != version {
				res.Action, res.Reason = ActionUpdate, joinReason(res.Reason, "recompiled against a changed module")
				version = v
			}
		}
	case b.o.DryRun:
		res.Action = ActionUpdate
		if cur == nil {
			res.Action = ActionCreate
		}
	default:
		res.Action = ActionUpdate
		if cur == nil {
			res.Action = ActionCreate
		}
		v, err := s.write(ctx, it, version)
		if err != nil {
			return fail("apply failed", err)
		}
		version = v
	}
	res.Version = version
	if it.Kind == model.KindModule && res.Action != ActionUnchanged {
		b.moduleChanged = true
	}

	if claim && !b.o.DryRun {
		row := ownRow{
			Ref: it.Ref, Source: b.o.source, Path: it.doc.Path, Commit: b.o.commit, AppliedVersion: version,
			Document: it.canon, AppliedAt: s.now(),
		}
		if err := s.saveOwn(ctx, row); err != nil {
			return fail("cannot record ownership", err)
		}
		if own != nil && own.ConflictCommit != "" {
			s.notifyResolved(ctx, it.Ref, b.o.source, "the repository and the resource agree again")
		}
	}
	return res
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// applyAdoption handles a document whose resource already exists, is not
// owned and differs. The resource is a web UI or API creation, so the sync
// does not overwrite it: it records the claim as a conflict, in the form of
// a drifted resource whose repository document is pending, and notifies.
// Resolving works as for any conflict. A document that equals the existing
// resource is adopted without a conflict (the export, commit, register path).
func (s *Syncer) applyAdoption(ctx context.Context, it *item, res Result, cur *current, b *batch) Result {
	d, err := diff(it.Ref, cur.canon, it.canon)
	if err != nil {
		res.Action, res.Reason = ActionFailed, err.Error()
		return res
	}
	res.Diff, res.Version = d, cur.version
	res.Action, res.Conflict, res.Drifted = ActionSkipped, true, true
	res.Reason = "the resource already exists with different content and no source manages it; resolve it with git or current"
	if b.o.DryRun {
		return res
	}
	// Applied version 0 never equals a real version, so the resource counts
	// as drifted until its content equals the document, and the document is
	// both the reference and the pending one.
	row := ownRow{
		Ref: it.Ref, Source: b.o.source, Path: it.doc.Path, Commit: b.o.commit, AppliedVersion: 0,
		Document: it.canon, Pending: it.canon, PendingPath: it.doc.Path, ConflictCommit: b.o.commit, AppliedAt: s.now(),
	}
	if err := s.saveOwn(ctx, row); err != nil {
		res.Action, res.Reason = ActionFailed, err.Error()
		return res
	}
	s.notifyConflict(ctx, it.Ref, b.o.source, it.doc.Path, b.o.commit, true)
	return res
}

// applyDrifted handles a document of a resource that was edited outside its
// repository. Local edits are never overwritten.
func (s *Syncer) applyDrifted(ctx context.Context, it *item, res Result, own *ownRow, cur *current, b *batch) Result {
	res.Drifted = true
	curCanon := ""
	if cur != nil {
		curCanon = cur.canon
	}
	d, err := diff(it.Ref, curCanon, it.canon)
	if err != nil {
		res.Action, res.Reason = ActionFailed, err.Error()
		return res
	}
	res.Diff = d
	if cur != nil {
		res.Version = cur.version
	}

	row := *own
	switch {
	case cur != nil && cur.canon == it.canon:
		// The local edits now equal the repository: the drift is over.
		res.Action, res.Drifted, res.Diff = ActionUnchanged, false, ""
		res.Reason = "local changes now match the repository"
		if !b.o.DryRun {
			row.Path, row.Commit, row.AppliedVersion, row.Document = it.doc.Path, b.o.commit, cur.version, it.canon
			row.Pending, row.PendingPath, row.ConflictCommit, row.Acknowledged, row.Orphaned = "", "", "", false, false
			if err := s.saveOwn(ctx, row); err != nil {
				res.Action, res.Reason = ActionFailed, err.Error()
				return res
			}
			if own.ConflictCommit != "" {
				s.notifyResolved(ctx, it.Ref, b.o.source, "local changes now match the repository")
			}
		}

	case it.canon == own.Document:
		// The repository still says what LCM last accounted for.
		res.Action = ActionSkipped
		res.Reason = "edited locally since the last sync; the repository has not changed"
		if cur == nil {
			res.Reason = "deleted locally since the last sync; the repository has not changed"
		}
		if !b.o.DryRun && own.Orphaned {
			row.Orphaned = false
			if err := s.saveOwn(ctx, row); err != nil {
				res.Action, res.Reason = ActionFailed, err.Error()
			}
		}

	default:
		res.Action, res.Conflict = ActionSkipped, true
		res.Reason = "the repository changed a resource that was edited locally; resolve it with git or current"
		if cur == nil {
			res.Reason = "the repository changed a resource that was deleted locally; resolve it with git or current"
		}
		if b.o.DryRun {
			return res
		}
		first := own.ConflictCommit != b.o.commit || own.Pending != it.canon
		row.Pending, row.PendingPath, row.ConflictCommit, row.Acknowledged, row.Orphaned = it.canon, it.doc.Path, b.o.commit, false, false
		if err := s.saveOwn(ctx, row); err != nil {
			res.Action, res.Reason = ActionFailed, err.Error()
			return res
		}
		if first {
			s.notifyConflict(ctx, it.Ref, b.o.source, it.doc.Path, b.o.commit, false)
		}
	}
	return res
}

// Selector picks what Export writes. The zero value selects everything.
type Selector struct {
	Kind model.Kind
	// Name narrows Kind to one resource.
	Name string
}

// Export returns the canonical YAML of existing resources as one
// multi-document stream, in apply order (modules, services, policies) and
// by name within a kind, so repeated exports of an unchanged system are
// identical. Only document fields are written; versions and state are not
// part of a document. Applying the output back changes nothing.
func (s *Syncer) Export(ctx context.Context, sel Selector) ([]byte, error) {
	if sel.Name != "" && sel.Kind == "" {
		return nil, model.Issues{{Path: "kind", Message: "a name needs a kind"}}
	}
	var docs []string
	add := func(ref Ref, canon string) error {
		y, err := canonicalYAML([]byte(canon))
		if err != nil {
			return fmt.Errorf("export %s: %w", ref, err)
		}
		docs = append(docs, string(y))
		return nil
	}
	want := func(kind model.Kind, name string) bool {
		return (sel.Kind == "" || sel.Kind == kind) && (sel.Name == "" || sel.Name == name)
	}

	mods, err := s.mods.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range mods {
		if !want(model.KindModule, m.Name) {
			continue
		}
		b, err := model.Encode(m.Spec)
		if err == nil {
			err = add(Ref{model.KindModule, m.Name}, string(b))
		}
		if err != nil {
			return nil, err
		}
	}
	svcs, err := s.cat.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range svcs {
		if !want(model.KindService, r.Name) {
			continue
		}
		b, err := model.Encode(r.Spec)
		if err == nil {
			err = add(Ref{model.KindService, r.Name}, string(b))
		}
		if err != nil {
			return nil, err
		}
	}
	pols, err := s.eng.Policies(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pols {
		if !want(model.KindPolicy, p.Name) {
			continue
		}
		cur, err := s.currentOf(ctx, Ref{model.KindPolicy, p.Name})
		if err == nil && cur != nil {
			err = add(Ref{model.KindPolicy, p.Name}, cur.canon)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(docs) == 0 && sel.Name != "" {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, Ref(sel))
	}
	return []byte(strings.Join(docs, "---\n")), nil
}
