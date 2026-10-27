package api_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
)

// realGit lets the site's command runner pass git commands through to the
// real binary while everything else stays faked.
func (x *site) realGit() {
	x.t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		x.t.Skip("git is not installed")
	}
	real := execx.New()
	x.runner.On(func(c execx.Cmd) bool { return len(c.Argv) > 0 && c.Argv[0] == "git" },
		func(c execx.Cmd) (execx.Result, error) { return real.Run(context.Background(), c) })
}

type testRepo struct {
	t            *testing.T
	remote, work string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	//nolint:gosec // test helper; the arguments are literals of the tests
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	root := t.TempDir()
	r := &testRepo{t: t, remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work")}
	runGit(t, root, "init", "--bare", "--quiet", "-b", "main", r.remote)
	runGit(t, root, "clone", "--quiet", r.remote, r.work)
	runGit(t, r.work, "checkout", "-q", "-B", "main")
	return r
}

func (r *testRepo) commit(files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	runGit(r.t, r.work, "add", "-A")
	runGit(r.t, r.work, "commit", "--quiet", "-m", "change")
	runGit(r.t, r.work, "push", "--quiet", "origin", "main")
	return runGit(r.t, r.work, "rev-parse", "HEAD")
}

func TestApplyEndpoint(t *testing.T) {
	x := newSite(t)
	body := policyDoc("echo", manualPolicyNodes) + "---\n" + serviceDoc("web", "") + "---\n" + "kind: Nope\n"

	// A dry run reports and writes nothing.
	r := x.call("POST", v1+"/apply?dryRun=true", body, yamlBody()).want(http.StatusOK).obj()
	items := r["items"].([]any)
	if r["dryRun"] != true || len(items) != 3 {
		t.Fatalf("dry run = %v", r)
	}
	// Results follow the order of the documents in the body.
	first := items[1].(map[string]any)
	if first["kind"] != "Service" || first["action"] != "create" || !strings.Contains(first["diff"].(string), "+kind: Service") || first["index"] != float64(1) {
		t.Fatalf("service result = %v", first)
	}
	if items[2].(map[string]any)["action"] != "failed" || len(items[2].(map[string]any)["issues"].([]any)) == 0 {
		t.Fatalf("bad document = %v", items[2])
	}
	x.call("GET", v1+"/services/web", nil).wantError(http.StatusNotFound, "not_found")

	// For real; the failed document does not stop the others (still a 200).
	r = x.call("POST", v1+"/apply", body, yamlBody()).want(http.StatusOK).obj()
	if r["dryRun"] != false {
		t.Fatalf("apply = %v", r)
	}
	x.call("GET", v1+"/services/web", nil).want(http.StatusOK)
	x.call("GET", v1+"/policies/echo", nil).want(http.StatusOK)
	again := x.call("POST", v1+"/apply", body, yamlBody()).want(http.StatusOK).items()
	if again[0]["action"] != "unchanged" || again[1]["action"] != "unchanged" || again[2]["action"] != "failed" {
		t.Fatalf("second apply = %v", again)
	}

	// Malformed requests.
	x.call("POST", v1+"/apply", "", yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/apply", body, header("Content-Type", "text/plain")).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/apply?dryRun=maybe", body, yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/apply", "# only a comment\n", yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/apply", "a: [\n", yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")

	// Admin only.
	x.call("POST", v1+"/apply", body, yamlBody(), bearer(x.ingest)).wantError(http.StatusForbidden, "forbidden")
	x.anon("POST", v1+"/apply", body, yamlBody()).wantError(http.StatusUnauthorized, "unauthenticated")
}

func TestExportEndpoint(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	x.createService(serviceDoc("api", "  dependsOn: [web]\n"))
	x.putPolicy("echo", manualPolicyNodes)

	r := x.call("GET", v1+"/export", nil).want(http.StatusOK)
	if ct := r.Head.Get("Content-Type"); ct != "application/yaml" {
		t.Fatalf("Content-Type = %q", ct)
	}
	all := r.String()
	if strings.Index(all, "name: api") > strings.Index(all, "name: web") || !strings.Contains(all, "kind: Policy") {
		t.Fatalf("export =\n%s", all)
	}
	// What was exported applies back unchanged.
	for _, it := range x.call("POST", v1+"/apply", all, yamlBody()).want(http.StatusOK).items() {
		if it["action"] != "unchanged" {
			t.Fatalf("round trip = %v", it)
		}
	}

	one := x.call("GET", v1+"/export?kind=services&name=web", nil).want(http.StatusOK).String()
	if strings.Count(one, "kind:") != 1 || !strings.Contains(one, "name: web") {
		t.Fatalf("export web =\n%s", one)
	}
	if got := x.call("GET", v1+"/export?kind=Policy", nil).want(http.StatusOK).String(); !strings.Contains(got, "kind: Policy") || strings.Contains(got, "Service") {
		t.Fatalf("export policies =\n%s", got)
	}
	x.call("GET", v1+"/export?kind=host", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/export?name=web", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/export?kind=service&name=ghost", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/export", nil, bearer(x.ingest)).wantError(http.StatusForbidden, "forbidden")
}

func TestConfigSourceEndpoints(t *testing.T) {
	x := newSite(t)
	body := map[string]any{"name": "infra", "repo": "git@example.com:o/infra.git", "path": "lcm", "interval": "5m"}
	r := x.call("POST", v1+"/config/sources", body).want(http.StatusCreated)
	src := r.obj()
	if r.Head.Get("Location") != v1+"/config/sources/infra" || src["branch"] != "main" || src["interval"] != "5m0s" ||
		src["enabled"] != true || src["lastCommit"] != "" || src["lastSyncAt"] != nil || src["path"] != "lcm" {
		t.Fatalf("created = %v", src)
	}
	x.call("POST", v1+"/config/sources", body).wantError(http.StatusConflict, "already_exists")
	x.call("POST", v1+"/config/sources", map[string]any{"name": "Bad Name", "repo": "x"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/config/sources", map[string]any{"name": "b", "repo": "x", "interval": "soon"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/config/sources", map[string]any{"name": "b", "repo": "x", "interval": "1s"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	//nolint:gosec // fixture for the rejected credentials-in-URL case
	x.call("POST", v1+"/config/sources", map[string]any{"name": "b", "repo": "https://u:p@example.com/r"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/config/sources", map[string]any{"name": "b", "repo": "x", "bogus": 1}).wantError(http.StatusBadRequest, "bad_request")

	if got := x.call("GET", v1+"/config/sources", nil).want(http.StatusOK).items(); len(got) != 1 || got[0]["name"] != "infra" {
		t.Fatalf("list = %v", got)
	}
	x.call("GET", v1+"/config/sources/infra", nil).want(http.StatusOK)
	x.call("GET", v1+"/config/sources/ghost", nil).wantError(http.StatusNotFound, "not_found")

	put := x.call("PUT", v1+"/config/sources/infra", map[string]any{"repo": "git@example.com:o/infra.git", "branch": "prod", "enabled": false}).
		want(http.StatusOK).obj()
	if put["branch"] != "prod" || put["enabled"] != false || put["path"] != "." || put["interval"] != "1m0s" {
		t.Fatalf("replaced = %v", put)
	}
	x.call("PUT", v1+"/config/sources/infra", map[string]any{"name": "other", "repo": "x"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("PUT", v1+"/config/sources/ghost", map[string]any{"repo": "x"}).wantError(http.StatusNotFound, "not_found")

	x.call("DELETE", v1+"/config/sources/infra", nil).want(http.StatusNoContent)
	x.call("DELETE", v1+"/config/sources/infra", nil).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/config/sources/ghost/sync", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/config/sources", nil, bearer(x.ingest)).wantError(http.StatusForbidden, "forbidden")
	if n := len(x.call("GET", v1+"/config/sources", nil).want(http.StatusOK).items()); n != 0 {
		t.Fatalf("%d sources after delete", n)
	}
}

func TestConfigSyncDriftAndResolve(t *testing.T) {
	x := newSite(t)
	x.realGit()
	repo := newTestRepo(t)
	repo.commit(map[string]string{
		"lcm/web.yaml":  serviceDoc("web", ""),
		"lcm/echo.yaml": policyDoc("echo", manualPolicyNodes),
	})
	x.call("POST", v1+"/config/sources", map[string]any{"name": "infra", "repo": repo.remote, "path": "lcm"}).want(http.StatusCreated)

	src := x.syncSource("infra")
	commit := runGit(t, repo.work, "rev-parse", "HEAD")
	if src["lastError"] != "" || src["lastCommit"] != commit || src["lastSyncAt"] == nil || src["syncing"] != false {
		t.Fatalf("source = %v", src)
	}

	// The resources show who manages them.
	mb := x.call("GET", v1+"/services/web", nil).want(http.StatusOK).obj()["managedBy"].(map[string]any)
	if mb["source"] != "infra" || mb["path"] != "lcm/web.yaml" || mb["drifted"] != false || mb["conflict"] != false || mb["commit"] != commit {
		t.Fatalf("managedBy = %v", mb)
	}
	if got := x.call("GET", v1+"/policies", nil).items(); len(got) != 1 || got[0]["managedBy"].(map[string]any)["source"] != "infra" {
		t.Fatalf("policies = %v", got)
	}
	x.createService(serviceDoc("free", ""))
	for _, sv := range x.call("GET", v1+"/services", nil).items() {
		if (sv["name"] == "free") != (sv["managedBy"] == nil) {
			t.Fatalf("managedBy of %v = %v", sv["name"], sv["managedBy"])
		}
	}

	// An edit through the API marks the service drifted; apply refuses it.
	x.call("PUT", v1+"/services/web", serviceDoc("web", "  dependsOn: [local]\n"), yamlBody(), ifMatch("1")).want(http.StatusOK)
	if mb := x.call("GET", v1+"/services/web", nil).obj()["managedBy"].(map[string]any); mb["drifted"] != true || mb["conflict"] != false {
		t.Fatalf("after edit = %v", mb)
	}
	res := x.call("POST", v1+"/apply", serviceDoc("web", "  dependsOn: [apply]\n"), yamlBody()).want(http.StatusOK).items()
	if res[0]["action"] != "skipped" || !strings.Contains(res[0]["reason"].(string), "infra") {
		t.Fatalf("apply on owned = %v", res[0])
	}

	// A commit on the drifted service is a conflict, not an overwrite.
	repo.commit(map[string]string{"lcm/web.yaml": serviceDoc("web", "  dependsOn: [repo]\n")})
	x.syncSource("infra")
	own := x.call("GET", v1+"/config/ownership?kind=service", nil).want(http.StatusOK).items()
	if len(own) != 1 || own[0]["conflict"] != true || own[0]["drifted"] != true || own[0]["conflictCommit"] == "" {
		t.Fatalf("ownership = %v", own)
	}
	if n := len(x.call("GET", v1+"/config/ownership", nil).items()); n != 2 {
		t.Fatalf("%d owned resources", n)
	}
	if n := len(x.call("GET", v1+"/config/ownership?source=other", nil).items()); n != 0 {
		t.Fatalf("%d owned by another source", n)
	}
	x.call("GET", v1+"/config/ownership?kind=host", nil).wantError(http.StatusBadRequest, "bad_request")

	// Resolve: invalid choices first, then keep current, then use git.
	resolve := v1 + "/config/ownership/service/web/resolve"
	x.call("POST", resolve, map[string]string{"use": "both"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/config/ownership/service/ghost/resolve", map[string]string{"use": "git"}).wantError(http.StatusNotFound, "not_found")
	x.call("POST", v1+"/config/ownership/host/web/resolve", map[string]string{"use": "git"}).wantError(http.StatusNotFound, "not_found")
	kept := x.call("POST", resolve, map[string]string{"use": "current"}).want(http.StatusOK).obj()
	if kept["conflict"] != false || kept["acknowledged"] != true || kept["drifted"] != true {
		t.Fatalf("keep current = %v", kept)
	}
	back := x.call("POST", resolve, map[string]string{"use": "git"}).want(http.StatusOK).obj()
	if back["drifted"] != false || back["kind"] != "Service" {
		t.Fatalf("use git = %v", back)
	}
	doc := x.call("GET", v1+"/services/web", nil).obj()["document"].(map[string]any)["spec"].(map[string]any)
	if deps := doc["dependsOn"].([]any); len(deps) != 1 || deps[0] != "repo" {
		t.Fatalf("service after use git = %v", doc)
	}

	// Release hands the service back to the web UI.
	x.call("DELETE", v1+"/config/ownership/service/web", nil).want(http.StatusNoContent)
	x.call("DELETE", v1+"/config/ownership/service/web", nil).wantError(http.StatusNotFound, "not_found")
	if x.call("GET", v1+"/services/web", nil).obj()["managedBy"] != nil {
		t.Fatal("still managed after release")
	}

	// Deleting the source releases the rest.
	x.call("DELETE", v1+"/config/sources/infra", nil).want(http.StatusNoContent)
	if n := len(x.call("GET", v1+"/config/ownership", nil).items()); n != 0 {
		t.Fatalf("%d resources still owned", n)
	}
	x.call("GET", v1+"/policies/echo", nil).want(http.StatusOK)

	// A repository that cannot be read is an answer, not a failure.
	x.call("POST", v1+"/config/sources", map[string]any{"name": "gone", "repo": filepath.Join(t.TempDir(), "none.git")}).want(http.StatusCreated)
	bad := x.syncSource("gone")
	if bad["lastError"] == "" || bad["lastCommit"] != "" || bad["lastSyncAt"] == nil {
		t.Fatalf("failed sync = %v", bad)
	}
}

func TestGitHubPushSyncsMatchingSources(t *testing.T) {
	x := newSite(t)
	x.call("POST", v1+"/config/sources", map[string]any{"name": "infra", "repo": "https://github.com/acme/infra.git", "branch": "main"}).want(http.StatusCreated)
	x.call("POST", v1+"/config/sources", map[string]any{"name": "off", "repo": "https://github.com/acme/infra.git", "enabled": false}).want(http.StatusCreated)
	push := func(repo, ref string) map[string]any {
		body := `{"ref":"` + ref + `","after":"abc1234","repository":{"clone_url":"` + repo + `"}}`
		return x.call("POST", v1+"/hooks/github", body, header("X-GitHub-Event", "push"), header("Content-Type", "application/json")).
			want(http.StatusAccepted).obj()
	}
	if got := push("git@github.com:Acme/Infra.git", "refs/heads/main"); got["sources"] != float64(1) {
		t.Fatalf("matching push = %v", got)
	}
	if got := push("https://github.com/acme/other.git", "refs/heads/main"); got["sources"] != float64(0) {
		t.Fatalf("other repository = %v", got)
	}
	if got := push("https://github.com/acme/infra.git", "refs/heads/dev"); got["sources"] != float64(0) {
		t.Fatalf("other branch = %v", got)
	}
}

// syncSource requests a sync, waits for it to finish and returns the source.
func (x *site) syncSource(name string) map[string]any {
	x.t.Helper()
	x.call("POST", v1+"/config/sources/"+name+"/sync", nil).want(http.StatusAccepted)
	x.app.ConfigSync.Wait()
	return x.call("GET", v1+"/config/sources/"+name, nil).want(http.StatusOK).obj()
}

func TestSyncRequestsAreAsynchronousAndCoalesce(t *testing.T) {
	x := newSite(t)
	repo := newTestRepo(t)
	repo.commit(map[string]string{"web.yaml": serviceDoc("web", "")})

	// The first git fetch blocks until the test lets it go; everything else
	// passes through to the real binary.
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	real := execx.New()
	x.runner.On(func(c execx.Cmd) bool { return len(c.Argv) > 1 && c.Argv[0] == "git" && c.Argv[1] == "fetch" },
		func(c execx.Cmd) (execx.Result, error) {
			once.Do(func() { close(entered) })
			<-release
			return real.Run(context.Background(), c)
		})
	x.realGit()
	x.call("POST", v1+"/config/sources", map[string]any{"name": "infra", "repo": repo.remote}).want(http.StatusCreated)

	first := x.call("POST", v1+"/config/sources/infra/sync", nil).want(http.StatusAccepted)
	if first.Head.Get("Location") != v1+"/config/sources/infra" || first.obj()["syncing"] != true {
		t.Fatalf("first = %s %v", first, first.Head)
	}
	<-entered
	// While it runs, another request joins it and nothing else is fetched.
	if second := x.call("POST", v1+"/config/sources/infra/sync", nil).want(http.StatusAccepted).obj(); second["syncing"] != true {
		t.Fatalf("second = %v", second)
	}
	if src := x.call("GET", v1+"/config/sources/infra", nil).obj(); src["syncing"] != true || src["lastCommit"] != "" {
		t.Fatalf("while syncing = %v", src)
	}
	if got := x.call("GET", v1+"/config/sources", nil).items(); got[0]["syncing"] != true {
		t.Fatalf("list while syncing = %v", got)
	}
	close(release)
	x.app.ConfigSync.Wait()
	fetches := 0
	for _, c := range x.runner.Calls() {
		if len(c.Argv) > 1 && c.Argv[1] == "fetch" {
			fetches++
		}
	}
	if fetches != 1 {
		t.Fatalf("%d fetches for two requests", fetches)
	}
	src := x.call("GET", v1+"/config/sources/infra", nil).obj()
	if src["syncing"] != false || src["lastCommit"] == "" || src["lastError"] != "" {
		t.Fatalf("after syncing = %v", src)
	}
	x.call("GET", v1+"/services/web", nil).want(http.StatusOK)

	// A later request starts a new sync.
	x.call("POST", v1+"/config/sources/infra/sync", nil).want(http.StatusAccepted)
	x.app.ConfigSync.Wait()
}
