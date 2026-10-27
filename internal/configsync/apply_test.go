package configsync_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/modules"
)

func TestApplyCreateUpdateUnchanged(t *testing.T) {
	e := newEnv(t)
	mod := moduleYAML("greet", "1")
	svc := serviceYAML("web", "")
	pol := policyYAML("echo", echoNodes)

	// Results come back in the order of the input.
	res := e.apply(configsync.Options{}, pol, svc, mod)
	order := []string{res[0].Ref().String(), res[1].Ref().String(), res[2].Ref().String()}
	if strings.Join(order, ",") != "Policy/echo,Service/web,Module/greet" || res[2].Index != 2 {
		t.Fatalf("result order = %v", order)
	}
	for _, r := range res {
		if r.Action != configsync.ActionCreate || r.Version != 1 {
			t.Fatalf("first apply: %+v", r)
		}
		if !strings.HasPrefix(r.Diff, "--- /dev/null\n+++ b/") || !strings.Contains(r.Diff, "\n+kind: ") {
			t.Fatalf("create diff of %s = %q", r.Ref(), r.Diff)
		}
	}
	if _, err := e.cat.Get(e.ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if infos, _ := e.eng.Policies(e.ctx); len(infos) != 1 || infos[0].DeployedVersion != 1 {
		t.Fatalf("policies = %+v", infos)
	}

	// The same documents again change nothing.
	for _, r := range e.apply(configsync.Options{}, mod, svc, pol) {
		if r.Action != configsync.ActionUnchanged || r.Diff != "" || r.Version != 1 {
			t.Fatalf("second apply: %+v", r)
		}
	}

	// A change shows up as a diff of canonical YAML and bumps the version.
	changed := serviceYAML("web", "  dependsOn: [db]\n")
	r := wantAction(t, e.apply(configsync.Options{}, changed), "Service/web", configsync.ActionUpdate)
	if r.Version != 2 || !strings.Contains(r.Diff, "+  dependsOn:\n") || !strings.Contains(r.Diff, "+    - db\n") {
		t.Fatalf("update result = %+v", r)
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	// The policy uses a module that only this batch creates, so the dry run
	// has to compile it against the pending module.
	uses := policyYAML("caller", "    - { id: start, type: trigger.manual, next: { out: [m] } }\n    - { id: m, type: module, props: { module: greet } }\n")
	res := e.apply(configsync.Options{DryRun: true}, uses, moduleYAML("greet", "1"), serviceYAML("web", ""))
	for _, r := range res {
		if r.Action != configsync.ActionCreate || r.Diff == "" {
			t.Fatalf("dry run: %+v", r)
		}
	}
	if recs, _ := e.cat.List(e.ctx); len(recs) != 0 {
		t.Fatalf("dry run created services: %v", recs)
	}
	if recs, _ := e.mods.List(e.ctx); len(recs) != 0 {
		t.Fatalf("dry run created modules: %v", recs)
	}
	if infos, _ := e.eng.Policies(e.ctx); len(infos) != 0 {
		t.Fatalf("dry run deployed policies: %v", infos)
	}
	if entries, _ := e.audit.List(e.ctx, 100, 0); len(entries) != 0 {
		t.Fatalf("dry run was audited: %v", entries)
	}

	// For real, in one call, the same batch works.
	for _, r := range e.apply(configsync.Options{}, uses, moduleYAML("greet", "1"), serviceYAML("web", "")) {
		if r.Action != configsync.ActionCreate {
			t.Fatalf("real apply: %+v", r)
		}
	}
}

func TestApplyReportsEachDocument(t *testing.T) {
	e := newEnv(t)
	res := e.apply(configsync.Options{},
		serviceYAML("good", ""),
		"apiVersion: lcm/v1\nkind: Service\nmetadata: { name: broken }\nspec:\n  runtime: { type: systemd }\n",
		serviceYAML("badhost", "  host: mars\n"),
		serviceYAML("badcheck", "  checks:\n    - { name: c, type: http }\n"),
		"kind: Nonsense\n",
		policyYAML("nomodule", "    - { id: start, type: trigger.manual, next: { out: [m] } }\n    - { id: m, type: module, props: { module: absent } }\n"),
		serviceYAML("good", "  dependsOn: [x]\n"),
	)
	wantAction(t, res, "Service/good", configsync.ActionCreate)
	if r := wantAction(t, res, "Service/broken", configsync.ActionFailed); len(r.Issues) == 0 || !strings.HasPrefix(r.Issues[0].Path, "spec.runtime") {
		t.Fatalf("broken issues = %v", r.Issues)
	}
	if r := wantAction(t, res, "Service/badhost", configsync.ActionFailed); r.Issues[0].Path != "spec.host" {
		t.Fatalf("badhost issues = %v", r.Issues)
	}
	wantAction(t, res, "Policy/nomodule", configsync.ActionFailed)
	var dup, unknown int
	for _, r := range res {
		if r.Action == configsync.ActionFailed && strings.Contains(r.Reason, "defined twice") {
			dup++
		}
		if r.Name == "" && r.Action == configsync.ActionFailed {
			unknown++
		}
	}
	if dup != 1 || unknown != 1 {
		t.Fatalf("duplicates=%d unnamed failures=%d in %+v", dup, unknown, res)
	}
	// Failures did not stop the valid document, and the first definition won.
	rec, err := e.cat.Get(e.ctx, "good")
	if err != nil || len(rec.Spec.Spec.DependsOn) != 0 {
		t.Fatalf("good = %+v, %v", rec, err)
	}
}

func TestApplyNeedsActor(t *testing.T) {
	e := newEnv(t)
	if _, err := e.sync.Apply(context.Background(), docs("x", serviceYAML("a", "")), configsync.Options{}); err == nil {
		t.Fatal("apply without an actor succeeded")
	}
}

func TestApplyPolicyPicksUpChangedModule(t *testing.T) {
	e := newEnv(t)
	uses := policyYAML("caller", "    - { id: start, type: trigger.manual, next: { out: [m] } }\n    - { id: m, type: module, props: { module: greet } }\n")
	e.apply(configsync.Options{}, moduleYAML("greet", "1"), uses)
	// Only the module changes; the policy document is the same text.
	res := e.apply(configsync.Options{}, moduleYAML("greet", "2"), uses)
	wantAction(t, res, "Module/greet", configsync.ActionUpdate)
	r := wantAction(t, res, "Policy/caller", configsync.ActionUpdate)
	if !strings.Contains(r.Reason, "module") || r.Version != 2 {
		t.Fatalf("policy result = %+v", r)
	}
	for _, r := range e.apply(configsync.Options{}, moduleYAML("greet", "2"), uses) {
		if r.Action != configsync.ActionUnchanged {
			t.Fatalf("third apply: %+v", r)
		}
	}
}

func TestApplyAuditsTheCaller(t *testing.T) {
	e := newEnv(t)
	e.apply(configsync.Options{}, serviceYAML("web", ""), serviceYAML("bad", "  host: mars\n"))
	entries, err := e.audit.List(e.ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var create, apply *audit.Entry
	for i := range entries {
		switch entries[i].Action {
		case "service.create":
			create = &entries[i]
		case "config.apply":
			apply = &entries[i]
		}
	}
	if create == nil || create.Actor.Name != "alice" || apply == nil || apply.Actor.Name != "alice" ||
		apply.Outcome != audit.OutcomeError || apply.Target != "manual" {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestExportRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.apply(configsync.Options{},
		moduleYAML("greet", "1"),
		serviceYAML("web", "  checks:\n    - { name: c, type: tcp, props: { address: \"127.0.0.1:1\" } }\n  jobs:\n    reindex: { command: \"app reindex\" }\n"),
		serviceYAML("api", "  dependsOn: [web]\n"),
		policyYAML("echo", echoNodes),
	)
	out, err := e.sync.Export(e.ctx, configsync.Selector{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	order := []string{"kind: Module", "name: api", "name: web", "kind: Policy"}
	last := -1
	for _, want := range order {
		i := strings.Index(text, want)
		if i < last {
			t.Fatalf("%q out of order in:\n%s", want, text)
		}
		last = i
	}
	if strings.Contains(text, "version") || strings.Contains(text, "state") {
		t.Fatalf("export holds server-only fields:\n%s", text)
	}
	again, _ := e.sync.Export(e.ctx, configsync.Selector{})
	if string(again) != text {
		t.Fatal("export is not deterministic")
	}

	// Applying the export changes nothing.
	parts := configsync.SplitDocuments("export.yaml", out)
	if len(parts) != 4 {
		t.Fatalf("export holds %d documents:\n%s", len(parts), text)
	}
	res, err := e.sync.Apply(e.ctx, parts, configsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Action != configsync.ActionUnchanged {
			t.Fatalf("round trip: %+v", r)
		}
	}

	// And a fresh system created from it gets the same export back.
	e2 := newEnv(t)
	if _, err := e2.sync.Apply(e2.ctx, parts, configsync.Options{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e2.sync.Export(e2.ctx, configsync.Selector{}); string(got) != text {
		t.Fatalf("second system exports:\n%s\nwant:\n%s", got, text)
	}

	// Selectors.
	one, err := e.sync.Export(e.ctx, configsync.Selector{Kind: model.KindService, Name: "web"})
	if err != nil || strings.Count(string(one), "kind:") != 1 || !strings.Contains(string(one), "name: web") {
		t.Fatalf("export web = %q, %v", one, err)
	}
	svcs, _ := e.sync.Export(e.ctx, configsync.Selector{Kind: model.KindService})
	if strings.Count(string(svcs), "kind: Service") != 2 || strings.Contains(string(svcs), "---\nkind: Policy") {
		t.Fatalf("export services = %q", svcs)
	}
	if _, err := e.sync.Export(e.ctx, configsync.Selector{Kind: model.KindService, Name: "ghost"}); err == nil {
		t.Fatal("exporting a missing service succeeded")
	}
	if _, err := e.sync.Export(e.ctx, configsync.Selector{Name: "web"}); err == nil {
		t.Fatal("a name without a kind was accepted")
	}
}

func TestSplitDocuments(t *testing.T) {
	in := "# header\n---\na: 1\n---   # next\nb: |\n  text\n  ---\n---\n\n# only a comment\n...\nc: 3\n"
	got := configsync.SplitDocuments("f.yaml", []byte(in))
	if len(got) != 3 || !strings.HasPrefix(string(got[0].Data), "a: 1") || !strings.Contains(string(got[1].Data), "  ---") ||
		strings.TrimSpace(string(got[2].Data)) != "c: 3" {
		t.Fatalf("documents = %q", got)
	}
	if got[0].Path != "f.yaml" {
		t.Fatalf("path = %q", got[0].Path)
	}
	if len(configsync.SplitDocuments("f", []byte("\n# nothing\n---\n---\n"))) != 0 {
		t.Fatal("empty documents were kept")
	}
	if configsync.StreamError(nil) == nil {
		t.Fatal("no documents is not an error")
	}
	bad := configsync.SplitDocuments("f", []byte("a: [\n---\nb: 1\n"))
	if err := configsync.StreamError(bad); err != nil {
		t.Fatalf("one good document among bad ones: %v", err)
	}
	if err := configsync.StreamError(configsync.SplitDocuments("f", []byte("a: [\n"))); err == nil {
		t.Fatal("unparseable stream accepted")
	}
	if k, n := configsync.Head([]byte("kind: Service\nmetadata: { name: x }\nspec: [")); k != model.KindService || n != "x" {
		// A body that cannot be parsed at all has no head.
		if k != "" || n != "" {
			t.Fatalf("head = %s %s", k, n)
		}
	}
}

func TestParseKind(t *testing.T) {
	for in, want := range map[string]model.Kind{
		"service": model.KindService, "Services": model.KindService, "policies": model.KindPolicy,
		"Policy": model.KindPolicy, "module": model.KindModule, "modules": model.KindModule,
	} {
		if got, err := configsync.ParseKind(in); err != nil || got != want {
			t.Errorf("ParseKind(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := configsync.ParseKind("host"); err == nil {
		t.Error("ParseKind accepted host")
	}
}

// keep the imports used by helpers in other files from drifting.
var (
	_ = catalog.ErrNotFound
	_ = engine.ErrPolicyNotFound
	_ = modules.ErrNotFound
	_ = auth.CLI
)
