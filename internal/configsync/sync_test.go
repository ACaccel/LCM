package configsync_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
)

const (
	webPath  = "deploy/web.yaml"
	echoPath = "deploy/echo.yaml"
)

func baseRepo(t *testing.T) (*gitRepo, string) {
	t.Helper()
	r := newRepo(t)
	c := r.commit("initial", map[string]string{
		webPath:                 serviceYAML("web", ""),
		echoPath:                policyYAML("echo", echoNodes),
		"other/ignored.yaml":    "this: is: not valid\n",
		"deploy/echo.test.yaml": "kind: Test\n",
		"deploy/notes.txt":      "not yaml",
	})
	return r, c
}

func alerts(sub *signal.Subscription) []signal.Signal {
	var out []signal.Signal
	for {
		select {
		case sig := <-sub.C:
			out = append(out, sig)
		default:
			return out
		}
	}
}

func TestSyncAppliesAndOwns(t *testing.T) {
	e := newEnv(t)
	r, c1 := baseRepo(t)
	e.addSource("infra", r, "deploy")

	rep := e.syncNow("infra")
	if rep.Commit != c1 || !rep.Changed || len(rep.Items) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	wantAction(t, rep.Items, "Service/web", configsync.ActionCreate)
	wantAction(t, rep.Items, "Policy/echo", configsync.ActionCreate)

	own, err := e.sync.Ownership(e.ctx, "")
	if err != nil || len(own) != 2 {
		t.Fatalf("ownership = %+v, %v", own, err)
	}
	for _, o := range own {
		if o.Source != "infra" || o.Commit != c1 || o.Drifted || o.Conflict || o.Orphaned || o.AppliedVersion != 1 {
			t.Fatalf("ownership = %+v", o)
		}
	}
	if o := e.owner("service", "web"); o == nil || o.Path != webPath {
		t.Fatalf("web ownership = %+v", o)
	}
	src, _ := e.sync.GetSource(e.ctx, "infra")
	if src.LastCommit != c1 || src.LastError != "" || !src.LastSyncAt.Equal(epoch) {
		t.Fatalf("source state = %+v", src)
	}

	// Documents are applied under the source's own actor.
	vs, err := e.cat.Versions(e.ctx, "web")
	if err != nil || vs[0].CreatedBy.ID != "configsync:infra" {
		t.Fatalf("versions = %+v, %v", vs, err)
	}

	// Syncing again re-reads the commit and finds nothing to do.
	rep = e.syncNow("infra")
	if rep.Changed {
		t.Fatalf("same commit reported as changed: %+v", rep)
	}
	for _, it := range rep.Items {
		if it.Action != configsync.ActionUnchanged {
			t.Fatalf("resync: %+v", it)
		}
	}

	// A new commit updates what changed and records the commit; a
	// multi-document file is split.
	c2 := r.commit("more", map[string]string{
		webPath:         serviceYAML("web", "  dependsOn: [db]\n"),
		"deploy/m.yaml": moduleYAML("greet", "1") + "---\n" + serviceYAML("db", ""),
	})
	rep = e.syncNow("infra")
	if rep.Commit != c2 || !rep.Changed {
		t.Fatalf("report = %+v", rep)
	}
	wantAction(t, rep.Items, "Service/web", configsync.ActionUpdate)
	wantAction(t, rep.Items, "Policy/echo", configsync.ActionUnchanged)
	wantAction(t, rep.Items, "Module/greet", configsync.ActionCreate)
	wantAction(t, rep.Items, "Service/db", configsync.ActionCreate)
	if o := e.owner("service", "web"); o.Commit != c2 || o.AppliedVersion != 2 || o.Drifted {
		t.Fatalf("web after update = %+v", o)
	}
	if o := e.owner("policy", "echo"); o.Commit != c2 && o.Commit != c1 {
		t.Fatalf("echo commit = %s", o.Commit)
	}

	// The clone lives in LCM's own directory.
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.work), "work", ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestSyncWholeRepositoryPath(t *testing.T) {
	e := newEnv(t)
	r := newRepo(t)
	r.commit("one file", map[string]string{"lcm.yaml": serviceYAML("solo", "")})
	e.addSource("root", r, "")
	rep := e.syncNow("root")
	wantAction(t, rep.Items, "Service/solo", configsync.ActionCreate)
	if src, _ := e.sync.GetSource(e.ctx, "root"); src.Path != "." {
		t.Fatalf("path = %q", src.Path)
	}
}

func TestManualApplyOfOwnedResource(t *testing.T) {
	e := newEnv(t)
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	e.syncNow("infra")

	edited := serviceYAML("web", "  dependsOn: [cache]\n")
	res := e.apply(configsync.Options{}, edited)
	got := wantAction(t, res, "Service/web", configsync.ActionSkipped)
	if !strings.Contains(got.Reason, `source "infra"`) || !strings.Contains(got.Reason, "force") {
		t.Fatalf("reason = %q", got.Reason)
	}
	if rec, _ := e.cat.Get(e.ctx, "web"); rec.Version != 1 {
		t.Fatalf("a refused apply wrote version %d", rec.Version)
	}

	// Applying what the resource already is asks for nothing, so it is not refused.
	wantAction(t, e.apply(configsync.Options{}, serviceYAML("web", "")), "Service/web", configsync.ActionUnchanged)

	// Dry-run says the same without --force and previews with it.
	wantAction(t, e.apply(configsync.Options{DryRun: true}, edited), "Service/web", configsync.ActionSkipped)
	prev := wantAction(t, e.apply(configsync.Options{DryRun: true, Force: true}, edited), "Service/web", configsync.ActionUpdate)
	if !strings.Contains(prev.Diff, "+    - cache") {
		t.Fatalf("preview diff = %q", prev.Diff)
	}

	// Force writes it, keeps the ownership and so leaves the service drifted.
	wantAction(t, e.apply(configsync.Options{Force: true}, edited), "Service/web", configsync.ActionUpdate)
	o := e.owner("service", "web")
	if o == nil || o.Source != "infra" || !o.Drifted {
		t.Fatalf("after force = %+v", o)
	}

	// An unowned resource needs no force.
	wantAction(t, e.apply(configsync.Options{}, serviceYAML("free", "")), "Service/free", configsync.ActionCreate)
	if e.owner("service", "free") != nil {
		t.Fatal("a manual apply created an owner")
	}
}

func TestDriftConflictAndResolveCurrent(t *testing.T) {
	e := newEnv(t)
	sub := e.bus.Subscribe("test", 16)
	defer sub.Close()
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	e.syncNow("infra")

	// An edit through the API takes effect and marks the service drifted.
	local := serviceYAML("web", "  dependsOn: [local]\n")
	e.editService("web", local)
	o := e.owner("service", "web")
	if !o.Drifted || o.Conflict {
		t.Fatalf("after edit = %+v", o)
	}
	if len(alerts(sub)) != 0 {
		t.Fatal("drift alone must not notify")
	}

	// Another service file changes: the drifted one is untouched and calm.
	r.commit("unrelated", map[string]string{echoPath: policyYAML("echo", echoNodes+"    - { id: later, type: data.set, props: { values: { a: 1 } } }\n")})
	rep := e.syncNow("infra")
	got := wantAction(t, rep.Items, "Service/web", configsync.ActionSkipped)
	if got.Conflict || !got.Drifted || !strings.Contains(got.Reason, "repository has not changed") {
		t.Fatalf("unrelated commit: %+v", got)
	}
	wantAction(t, rep.Items, "Policy/echo", configsync.ActionUpdate)
	if o := e.owner("service", "web"); o.Conflict {
		t.Fatal("conflict raised although the repository document did not change")
	}

	// The repository changes the drifted service: skipped, flagged, notified.
	c3 := r.commit("repo edit", map[string]string{webPath: serviceYAML("web", "  dependsOn: [repo]\n")})
	rep = e.syncNow("infra")
	got = wantAction(t, rep.Items, "Service/web", configsync.ActionSkipped)
	if !got.Conflict || !strings.Contains(got.Diff, "-    - local") || !strings.Contains(got.Diff, "+    - repo") {
		t.Fatalf("conflict result = %+v", got)
	}
	if rec, _ := e.cat.Get(e.ctx, "web"); rec.Spec.Spec.DependsOn[0] != "local" {
		t.Fatalf("the local edit was overwritten: %v", rec.Spec.Spec.DependsOn)
	}
	o = e.owner("service", "web")
	if !o.Conflict || o.ConflictCommit != c3 || !o.Drifted {
		t.Fatalf("after conflict = %+v", o)
	}
	sigs := alerts(sub)
	if len(sigs) != 1 || sigs[0].Kind != signal.KindAlert || sigs[0].Source != configsync.SourceName ||
		sigs[0].Severity != signal.SeverityWarning || sigs[0].Data["status"] != "firing" ||
		sigs[0].Labels["service"] != "web" || sigs[0].Data["commit"] != c3 {
		t.Fatalf("signals = %+v", sigs)
	}
	tl, err := e.cat.Timeline(e.ctx, catalog.TimelineQuery{Service: "web", Kinds: []string{configsync.KindConflict}})
	if err != nil || len(tl.Entries) != 1 || !strings.Contains(tl.Entries[0].Summary, "edited locally") {
		t.Fatalf("timeline = %+v, %v", tl, err)
	}

	// Syncing the same commit again does not notify twice.
	e.syncNow("infra")
	if n := len(alerts(sub)); n != 0 {
		t.Fatalf("%d repeated notifications", n)
	}

	// Keep the current content: the conflict closes (and says so), the
	// service stays drifted and acknowledged.
	resolved, err := e.sync.Resolve(e.ctx, model.KindService, "web", false)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Conflict || !resolved.Acknowledged || !resolved.Drifted {
		t.Fatalf("after keep-current = %+v", resolved)
	}
	if sigs := alerts(sub); len(sigs) != 1 || sigs[0].Data["status"] != "resolved" {
		t.Fatalf("resolve signals = %+v", sigs)
	}
	// The same commit no longer conflicts.
	got = wantAction(t, e.syncNow("infra").Items, "Service/web", configsync.ActionSkipped)
	if got.Conflict {
		t.Fatalf("acknowledged commit conflicts again: %+v", got)
	}

	// A later change of the document conflicts again.
	r.commit("repo edit 2", map[string]string{webPath: serviceYAML("web", "  dependsOn: [repo2]\n")})
	if got := wantAction(t, e.syncNow("infra").Items, "Service/web", configsync.ActionSkipped); !got.Conflict {
		t.Fatalf("a newer change did not conflict: %+v", got)
	}
	if o := e.owner("service", "web"); o.Acknowledged {
		t.Fatal("acknowledgement survived a new conflict")
	}

	// The repository catches up with the current content: drift clears by itself.
	r.commit("catch up", map[string]string{webPath: local})
	got = wantAction(t, e.syncNow("infra").Items, "Service/web", configsync.ActionUnchanged)
	if got.Drifted || !strings.Contains(got.Reason, "match the repository") {
		t.Fatalf("catch-up result = %+v", got)
	}
	o = e.owner("service", "web")
	if o.Drifted || o.Conflict || o.Acknowledged {
		t.Fatalf("after catch-up = %+v", o)
	}
	if sigs := alerts(sub); len(sigs) < 1 || sigs[len(sigs)-1].Data["status"] != "resolved" {
		t.Fatalf("catch-up signals = %+v", sigs)
	}
}

func TestResolveUseGit(t *testing.T) {
	e := newEnv(t)
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	e.syncNow("infra")

	e.editService("web", serviceYAML("web", "  dependsOn: [local]\n"))
	repoDoc := serviceYAML("web", "  dependsOn: [repo]\n")
	c2 := r.commit("repo edit", map[string]string{webPath: repoDoc})
	wantAction(t, e.syncNow("infra").Items, "Service/web", configsync.ActionSkipped)

	o, err := e.sync.Resolve(e.ctx, model.KindService, "web", true)
	if err != nil {
		t.Fatal(err)
	}
	if o.Drifted || o.Conflict || o.Commit != c2 {
		t.Fatalf("after use-git = %+v", o)
	}
	rec, _ := e.cat.Get(e.ctx, "web")
	if rec.Spec.Spec.DependsOn[0] != "repo" || rec.Version != 3 {
		t.Fatalf("service after use-git = v%d %v", rec.Version, rec.Spec.Spec.DependsOn)
	}
	// Using git on a drifted resource without a newer commit reverts it.
	e.editService("web", serviceYAML("web", "  dependsOn: [again]\n"))
	if o := e.owner("service", "web"); !o.Drifted {
		t.Fatal("not drifted after a second edit")
	}
	if o, err := e.sync.Resolve(e.ctx, model.KindService, "web", true); err != nil || o.Drifted {
		t.Fatalf("revert = %+v, %v", o, err)
	}
	if rec, _ := e.cat.Get(e.ctx, "web"); rec.Spec.Spec.DependsOn[0] != "repo" {
		t.Fatalf("revert left %v", rec.Spec.Spec.DependsOn)
	}

	// A resource deleted locally comes back.
	if err := e.cat.Delete(e.ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if o := e.owner("service", "web"); !o.Drifted || !o.Missing {
		t.Fatalf("deleted service = %+v", o)
	}
	if _, err := e.sync.Resolve(e.ctx, model.KindService, "web", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cat.Get(e.ctx, "web"); err != nil {
		t.Fatalf("service not restored: %v", err)
	}

	if _, err := e.sync.Resolve(e.ctx, model.KindService, "nobody", true); err == nil {
		t.Fatal("resolving an unowned resource succeeded")
	}
}

func TestPolicyDrift(t *testing.T) {
	e := newEnv(t)
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	e.syncNow("infra")

	if _, err := e.eng.Deploy(e.ctx, []byte(policyYAML("echo", echoNodes+"    - { id: x, type: data.set, props: { values: { a: 1 } } }\n"))); err != nil {
		t.Fatal(err)
	}
	if o := e.owner("policy", "echo"); !o.Drifted {
		t.Fatalf("policy edit not drifted: %+v", o)
	}
	if _, err := e.sync.Resolve(e.ctx, model.KindPolicy, "echo", true); err != nil {
		t.Fatal(err)
	}
	if o := e.owner("policy", "echo"); o.Drifted {
		t.Fatalf("policy still drifted: %+v", o)
	}
}

func TestOrphanedAndRelease(t *testing.T) {
	e := newEnv(t)
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	e.syncNow("infra")

	r.commit("drop policy", map[string]string{echoPath: ""})
	rep := e.syncNow("infra")
	if len(rep.Orphaned) != 1 || rep.Orphaned[0].String() != "Policy/echo" {
		t.Fatalf("orphaned = %v", rep.Orphaned)
	}
	if o := e.owner("policy", "echo"); !o.Orphaned || o.Drifted {
		t.Fatalf("echo = %+v", o)
	}
	if infos, _ := e.eng.Policies(e.ctx); len(infos) != 1 || infos[0].DeployedVersion == 0 {
		t.Fatalf("the orphaned policy was removed: %+v", infos)
	}
	// Flagged once, not at every sync.
	if rep := e.syncNow("infra"); len(rep.Orphaned) != 0 {
		t.Fatalf("orphaned again: %v", rep.Orphaned)
	}

	r.commit("restore policy", map[string]string{echoPath: policyYAML("echo", echoNodes)})
	e.syncNow("infra")
	if o := e.owner("policy", "echo"); o.Orphaned {
		t.Fatal("orphan flag stayed after the document returned")
	}

	// Release hands the resource to the web UI.
	if err := e.sync.Release(e.ctx, model.KindPolicy, "echo"); err != nil {
		t.Fatal(err)
	}
	if e.owner("policy", "echo") != nil {
		t.Fatal("still owned after release")
	}
	if err := e.sync.Release(e.ctx, model.KindPolicy, "echo"); err == nil {
		t.Fatal("releasing twice succeeded")
	}
	wantAction(t, e.apply(configsync.Options{}, policyYAML("echo", echoNodes+"    - { id: y, type: data.set, props: { values: { a: 1 } } }\n")),
		"Policy/echo", configsync.ActionUpdate)
}

func TestTwoSourcesClaimTheSameResource(t *testing.T) {
	e := newEnv(t)
	r1, _ := baseRepo(t)
	r2 := newRepo(t)
	r2.commit("dup", map[string]string{"web.yaml": serviceYAML("web", "  dependsOn: [other]\n"), "extra.yaml": serviceYAML("extra", "")})
	e.addSource("first", r1, "deploy")
	e.addSource("second", r2, ".")
	e.syncNow("first")

	rep := e.syncNow("second")
	got := wantAction(t, rep.Items, "Service/web", configsync.ActionFailed)
	if !strings.Contains(got.Reason, `source "first"`) {
		t.Fatalf("reason = %q", got.Reason)
	}
	wantAction(t, rep.Items, "Service/extra", configsync.ActionCreate)
	if o := e.owner("service", "web"); o.Source != "first" {
		t.Fatalf("owner = %+v", o)
	}
	if rec, _ := e.cat.Get(e.ctx, "web"); len(rec.Spec.Spec.DependsOn) != 0 {
		t.Fatal("the refused source changed the service")
	}
	src, _ := e.sync.GetSource(e.ctx, "second")
	if !strings.Contains(src.LastError, "Service/web") {
		t.Fatalf("lastError = %q", src.LastError)
	}
}

func TestSourceAdoptsIdenticalResource(t *testing.T) {
	e := newEnv(t)
	sub := e.bus.Subscribe("test", 16)
	defer sub.Close()
	// The export, commit, register path: the resources already exist and the
	// repository holds exactly what they are.
	e.apply(configsync.Options{}, serviceYAML("web", ""), policyYAML("echo", echoNodes))
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	rep := e.syncNow("infra")
	for _, ref := range []string{"Service/web", "Policy/echo"} {
		got := wantAction(t, rep.Items, ref, configsync.ActionUnchanged)
		if !strings.Contains(got.Reason, "adopted") || got.Version != 1 || got.Conflict {
			t.Fatalf("%s: %+v", ref, got)
		}
	}
	o := e.owner("service", "web")
	if o == nil || o.Source != "infra" || o.Drifted || o.Conflict || o.AppliedVersion != 1 {
		t.Fatalf("owner = %+v", o)
	}
	if vs, _ := e.cat.Versions(e.ctx, "web"); len(vs) != 1 {
		t.Fatalf("adoption wrote a version: %+v", vs)
	}
	if n := len(alerts(sub)); n != 0 {
		t.Fatalf("%d notifications for a clean adoption", n)
	}
}

func TestSourceDoesNotOverwriteDifferentResource(t *testing.T) {
	e := newEnv(t)
	sub := e.bus.Subscribe("test", 16)
	defer sub.Close()
	guiWeb := serviceYAML("web", "  dependsOn: [gui]\n")
	guiAPI := serviceYAML("api", "  dependsOn: [gui]\n")
	e.apply(configsync.Options{}, guiWeb, guiAPI)
	r := newRepo(t)
	c1 := r.commit("repo", map[string]string{
		"deploy/web.yaml": serviceYAML("web", ""),
		"deploy/api.yaml": serviceYAML("api", ""),
		"deploy/new.yaml": serviceYAML("new", ""),
	})
	e.addSource("infra", r, "deploy")

	rep := e.syncNow("infra")
	web := wantAction(t, rep.Items, "Service/web", configsync.ActionSkipped)
	if !web.Conflict || !strings.Contains(web.Diff, "-    - gui") || !strings.Contains(web.Reason, "no source manages it") {
		t.Fatalf("adoption conflict = %+v", web)
	}
	wantAction(t, rep.Items, "Service/new", configsync.ActionCreate)
	for _, n := range []string{"web", "api"} {
		if rec, _ := e.cat.Get(e.ctx, n); rec.Version != 1 || rec.Spec.Spec.DependsOn[0] != "gui" {
			t.Fatalf("%s was written: v%d %v", n, rec.Version, rec.Spec.Spec.DependsOn)
		}
	}
	o := e.owner("service", "web")
	if o == nil || !o.Conflict || !o.Drifted || o.ConflictCommit != c1 || o.Source != "infra" {
		t.Fatalf("ownership = %+v", o)
	}
	sigs := alerts(sub)
	if len(sigs) != 2 || sigs[0].Source != configsync.SourceName || sigs[0].Severity != signal.SeverityWarning || sigs[0].Data["status"] != "firing" {
		t.Fatalf("signals = %+v", sigs)
	}
	tl, _ := e.cat.Timeline(e.ctx, catalog.TimelineQuery{Service: "web", Kinds: []string{configsync.KindConflict}})
	if len(tl.Entries) != 1 || !strings.Contains(tl.Entries[0].Summary, "not managed by a source") {
		t.Fatalf("timeline = %+v", tl)
	}

	// The same commit again neither writes nor notifies a second time.
	got := wantAction(t, e.syncNow("infra").Items, "Service/web", configsync.ActionSkipped)
	if got.Conflict {
		t.Fatalf("resync: %+v", got)
	}
	if n := len(alerts(sub)); n != 0 {
		t.Fatalf("%d repeated notifications", n)
	}

	// Use git: the file is applied and the source owns the service.
	resolved, err := e.sync.Resolve(e.ctx, model.KindService, "web", true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Drifted || resolved.Conflict || resolved.Commit != c1 {
		t.Fatalf("after use-git = %+v", resolved)
	}
	if rec, _ := e.cat.Get(e.ctx, "web"); rec.Version != 2 || len(rec.Spec.Spec.DependsOn) != 0 {
		t.Fatalf("web after use-git: v%d %v", rec.Version, rec.Spec.Spec.DependsOn)
	}

	// Use current: the service is kept, owned, drifted and acknowledged.
	kept, err := e.sync.Resolve(e.ctx, model.KindService, "api", false)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Conflict || !kept.Drifted || !kept.Acknowledged || kept.Source != "infra" {
		t.Fatalf("after keep-current = %+v", kept)
	}
	if rec, _ := e.cat.Get(e.ctx, "api"); rec.Version != 1 || rec.Spec.Spec.DependsOn[0] != "gui" {
		t.Fatalf("api changed: v%d", rec.Version)
	}
	if got := wantAction(t, e.syncNow("infra").Items, "Service/api", configsync.ActionSkipped); got.Conflict {
		t.Fatalf("acknowledged adoption conflicts again: %+v", got)
	}

	// It clears once the repository matches the resource.
	r.commit("catch up", map[string]string{"deploy/api.yaml": guiAPI})
	wantAction(t, e.syncNow("infra").Items, "Service/api", configsync.ActionUnchanged)
	if o := e.owner("service", "api"); o.Drifted || o.Acknowledged || o.Conflict {
		t.Fatalf("after catch-up = %+v", o)
	}
}

func TestInvalidDocumentKeepsOldConfiguration(t *testing.T) {
	e := newEnv(t)
	sub := e.bus.Subscribe("test", 16)
	defer sub.Close()
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	e.syncNow("infra")

	c2 := r.commit("break it", map[string]string{
		webPath:  serviceYAML("web", "  host: mars\n"),
		echoPath: policyYAML("echo", "    - { id: start, type: trigger.manual, next: { out: [ghost] } }\n"),
	})
	rep := e.syncNow("infra")
	web := wantAction(t, rep.Items, "Service/web", configsync.ActionFailed)
	wantAction(t, rep.Items, "Policy/echo", configsync.ActionFailed)
	if len(web.Issues) == 0 || web.Issues[0].Path != "spec.host" {
		t.Fatalf("issues = %v", web.Issues)
	}
	if rec, _ := e.cat.Get(e.ctx, "web"); rec.Version != 1 {
		t.Fatalf("an invalid document changed the service to v%d", rec.Version)
	}
	if o := e.owner("service", "web"); o.Commit == c2 || o.Drifted {
		t.Fatalf("ownership moved to the broken commit: %+v", o)
	}
	src, _ := e.sync.GetSource(e.ctx, "infra")
	if !strings.Contains(src.LastError, "(and 1 more)") || src.LastCommit != c2 {
		t.Fatalf("source = %+v", src)
	}
	var invalid int
	for _, s := range alerts(sub) {
		if s.Data["alert"] == configsync.KindInvalid && s.Severity == signal.SeverityWarning {
			invalid++
		}
	}
	if invalid != 2 {
		t.Fatalf("%d invalid-document alerts", invalid)
	}
	if tl, _ := e.cat.Timeline(e.ctx, catalog.TimelineQuery{Service: "web", Kinds: []string{configsync.KindInvalid}}); len(tl.Entries) != 1 {
		t.Fatalf("timeline = %+v", tl)
	}

	// A fix applies and clears the error; re-syncing the broken commit
	// before did not alert again.
	r.commit("fix", map[string]string{
		webPath:  serviceYAML("web", ""),
		echoPath: policyYAML("echo", echoNodes),
	})
	e.syncNow("infra")
	if src, _ := e.sync.GetSource(e.ctx, "infra"); src.LastError != "" {
		t.Fatalf("lastError = %q", src.LastError)
	}
}

func TestSyncNoRepeatedInvalidAlerts(t *testing.T) {
	e := newEnv(t)
	sub := e.bus.Subscribe("test", 16)
	defer sub.Close()
	r := newRepo(t)
	r.commit("bad", map[string]string{"a.yaml": "kind: Service\n"})
	e.addSource("s", r, ".")
	e.syncNow("s")
	e.syncNow("s")
	if n := len(alerts(sub)); n != 1 {
		t.Fatalf("%d alerts for one bad commit", n)
	}
}

func TestSyncReportsFetchFailures(t *testing.T) {
	e := newEnv(t)
	missing := filepath.Join(t.TempDir(), "nowhere.git")
	if _, err := e.sync.CreateSource(e.ctx, configsync.Source{Name: "gone", Repo: missing, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	rep, err := e.sync.SyncNow(e.ctx, "gone")
	if err != nil || rep.Error == "" || rep.Commit != "" {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	if src, _ := e.sync.GetSource(e.ctx, "gone"); src.LastError == "" || src.LastCommit != "" || src.LastSyncAt.IsZero() {
		t.Fatalf("source = %+v", src)
	}

	// A branch that does not exist is reported the same way.
	r := newRepo(t)
	r.commit("x", map[string]string{"a.yaml": serviceYAML("a", "")})
	if _, err := e.sync.CreateSource(e.ctx, configsync.Source{Name: "nobranch", Repo: r.remote, Branch: "release", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := e.sync.SyncNow(e.ctx, "nobranch"); rep.Error == "" || !strings.Contains(rep.Error, "release") {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := e.sync.SyncNow(e.ctx, "ghost"); err == nil {
		t.Fatal("syncing an unknown source succeeded")
	}
}

func TestSourceCRUD(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []configsync.Source{
		{Name: "Bad Name", Repo: "/x"},
		{Name: "../escape", Repo: "/x"},
		{Name: "ok", Repo: ""},
		{Name: "ok", Repo: "--upload-pack=evil"},
		{Name: "ok", Repo: "ext::sh -c evil"},
		{Name: "ok", Repo: "https://user:secret@example.com/r.git"}, //nolint:gosec // fixture for the rejected credentials-in-URL case
		{Name: "ok", Repo: "/x", Branch: "a..b"},
		{Name: "ok", Repo: "/x", Branch: "-dash"},
		{Name: "ok", Repo: "/x", Path: "../up"},
		{Name: "ok", Repo: "/x", Path: "/abs"},
		{Name: "ok", Repo: "/x", Interval: time.Second},
		{Name: "ok", Repo: "/x", Interval: 48 * time.Hour},
	} {
		_, err := e.sync.CreateSource(e.ctx, bad)
		var is model.Issues
		if err == nil || !asIssues(err, &is) {
			t.Errorf("CreateSource(%+v) = %v, want validation issues", bad, err)
		}
	}
	if _, err := e.sync.CreateSource(context.Background(), configsync.Source{Name: "ok", Repo: "/x"}); err == nil {
		t.Fatal("creating without an actor succeeded")
	}

	src, err := e.sync.CreateSource(e.ctx, configsync.Source{Name: "ok", Repo: "git@example.com:o/r.git", Path: "deploy//lcm/", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if src.Branch != "main" || src.Path != "deploy/lcm" || src.Interval != time.Minute || !src.Enabled || !src.CreatedAt.Equal(epoch) {
		t.Fatalf("defaults = %+v", src)
	}
	if _, err := e.sync.CreateSource(e.ctx, configsync.Source{Name: "ok", Repo: "/y"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate = %v", err)
	}
	list, _ := e.sync.ListSources(e.ctx)
	if len(list) != 1 || list[0].Name != "ok" {
		t.Fatalf("list = %+v", list)
	}

	// Moving a source forgets what it read.
	r, _ := baseRepo(t)
	e.addSource("infra", r, "deploy")
	e.syncNow("infra")
	moved, err := e.sync.UpdateSource(e.ctx, configsync.Source{Name: "infra", Repo: r.remote, Branch: "main", Path: "other", Enabled: false, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if moved.LastCommit != "" || moved.Enabled || moved.Interval != time.Hour || moved.Path != "other" {
		t.Fatalf("updated = %+v", moved)
	}
	if _, err := e.sync.UpdateSource(e.ctx, configsync.Source{Name: "ghost", Repo: "/x"}); err == nil {
		t.Fatal("updating an unknown source succeeded")
	}

	// Deleting releases what the source owned and removes the clone.
	if _, err := e.sync.UpdateSource(e.ctx, configsync.Source{Name: "infra", Repo: r.remote, Branch: "main", Path: "deploy", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.syncNow("infra")
	if err := e.sync.DeleteSource(e.ctx, "infra"); err != nil {
		t.Fatal(err)
	}
	if e.owner("service", "web") != nil {
		t.Fatal("ownership survived the source")
	}
	if _, err := e.cat.Get(e.ctx, "web"); err != nil {
		t.Fatalf("deleting the source removed the service: %v", err)
	}
	if err := e.sync.DeleteSource(e.ctx, "infra"); err == nil {
		t.Fatal("deleting twice succeeded")
	}
	entries, _ := e.audit.List(e.ctx, 200, 0)
	actions := map[string]bool{}
	for _, en := range entries {
		actions[en.Action] = true
	}
	for _, want := range []string{"config.source.create", "config.source.update", "config.source.delete", "config.sync", "config.apply"} {
		if !actions[want] {
			t.Errorf("no audit entry %s", want)
		}
	}
}

func asIssues(err error, dst *model.Issues) bool {
	return errors.As(err, dst)
}

func TestRunSyncsOnItsInterval(t *testing.T) {
	e := newEnv(t)
	r, c1 := baseRepo(t)
	e.addSource("infra", r, "deploy")
	if _, err := e.sync.CreateSource(e.ctx, configsync.Source{Name: "off", Repo: r.remote, Enabled: false}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.sync.Run(ctx)
	}()
	var stopped bool
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	defer stop()

	wait := func() {
		t.Helper()
		wctx, wcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer wcancel()
		if err := e.clk.WaitForTimers(wctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	// The first pass syncs at once and then waits for the interval.
	wait()
	if src, _ := e.sync.GetSource(e.ctx, "infra"); src.LastCommit != c1 {
		t.Fatalf("first pass: %+v", src)
	}
	if src, _ := e.sync.GetSource(e.ctx, "off"); !src.LastSyncAt.IsZero() {
		t.Fatal("a disabled source was synced")
	}
	if _, err := e.cat.Get(e.ctx, "web"); err != nil {
		t.Fatal(err)
	}

	c2 := r.commit("next", map[string]string{webPath: serviceYAML("web", "  dependsOn: [db]\n")})
	e.clk.Advance(30 * time.Second)
	if src, _ := e.sync.GetSource(e.ctx, "infra"); src.LastCommit != c1 {
		t.Fatal("synced before the interval ended")
	}
	e.clk.Advance(30 * time.Second)
	wait()
	if src, _ := e.sync.GetSource(e.ctx, "infra"); src.LastCommit != c2 {
		t.Fatalf("after the interval: %+v", src)
	}

	// A push notification marks the matching source due, whatever its
	// interval; the loop syncs it as soon as it runs.
	stop()
	c3 := r.commit("pushed", map[string]string{webPath: serviceYAML("web", "  dependsOn: [db, cache]\n")})
	if n, err := e.sync.NotifyPush(e.ctx, "https://example.com/other.git", "main"); err != nil || n != 0 {
		t.Fatalf("unrelated push matched %d, %v", n, err)
	}
	if n, _ := e.sync.NotifyPush(e.ctx, r.remote, "develop"); n != 0 {
		t.Fatal("another branch matched")
	}
	if n, err := e.sync.NotifyPush(e.ctx, r.remote, "main"); err != nil || n != 1 {
		t.Fatalf("push matched %d, %v", n, err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		e.sync.Run(ctx2)
	}()
	defer func() {
		cancel2()
		<-done2
	}()
	wait()
	if src, _ := e.sync.GetSource(e.ctx, "infra"); src.LastCommit != c3 {
		t.Fatalf("after the push: %+v", src)
	}
}

func TestRequestSyncRunsInTheBackground(t *testing.T) {
	e := newEnv(t)
	r, c1 := baseRepo(t)
	e.addSource("infra", r, "deploy")
	if _, err := e.sync.RequestSync(context.Background(), "infra"); err == nil {
		t.Fatal("a request without an actor succeeded")
	}
	if _, err := e.sync.RequestSync(e.ctx, "ghost"); err == nil {
		t.Fatal("a request for an unknown source succeeded")
	}
	started, err := e.sync.RequestSync(e.ctx, "infra")
	if err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}
	e.sync.Wait()
	src, _ := e.sync.GetSource(e.ctx, "infra")
	if src.Syncing || src.LastCommit != c1 || src.LastError != "" {
		t.Fatalf("source = %+v", src)
	}
	entries, _ := e.audit.List(e.ctx, 100, 0)
	var found bool
	for _, en := range entries {
		found = found || (en.Action == "config.sync" && en.Actor.Name == "alice")
	}
	if !found {
		t.Fatal("the request was not audited under the caller")
	}

	// After Close nothing new starts.
	e.sync.Close()
	if started, err := e.sync.RequestSync(e.ctx, "infra"); err != nil || started {
		t.Fatalf("after Close: started=%v err=%v", started, err)
	}
}
