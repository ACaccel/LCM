package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/catalog"
)

// statusOnlyDoc is a command runtime that can report status but neither
// start, stop nor restart the process, as LCM's own service does.
func statusOnlyDoc(name, extra string) string {
	return "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: " + name + " }\nspec:\n" +
		"  runtime: { type: command, status: \"true\" }\n" + extra
}

func TestUnsupportedRuntimeActionsAreRefused(t *testing.T) {
	e := newEnv(t)
	e.put(statusOnlyDoc("self", "  source: { repo: /srv/self, branch: main, install: ./install.sh }\n"), 0)
	e.setState("self", catalog.StateRunning)
	ad := e.runtime.adapter("self")
	ctx := context.Background()

	for _, action := range []string{catalog.ActionStop, catalog.ActionRestart} {
		if _, err := e.mgr.Plan(ctx, "self", action, catalog.ActionOptions{}); !errors.Is(err, catalog.ErrUnsupported) {
			t.Errorf("plan %s = %v, want ErrUnsupported", action, err)
		}
	}
	_, err := e.mgr.Stop(e.ctx, "self", catalog.ActionOptions{})
	if !errors.Is(err, catalog.ErrUnsupported) || !errors.Is(err, catalog.ErrPrecondition) {
		t.Fatalf("stop = %v, want ErrUnsupported as a precondition", err)
	}
	_, err = e.mgr.Restart(e.ctx, "self", catalog.ActionOptions{})
	if !errors.Is(err, catalog.ErrUnsupported) {
		t.Fatalf("restart = %v, want ErrUnsupported", err)
	}
	if e.state("self") != catalog.StateRunning || len(ad.Calls()) != 0 {
		t.Fatalf("refused actions changed something: state %s calls %v", e.state("self"), ad.Calls())
	}

	p, err := e.mgr.Plan(ctx, "self", catalog.ActionDeploy, catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(p.Steps, "\n")
	if !strings.Contains(joined, "skip restart") || strings.Contains(joined, "\nrestart ") {
		t.Fatalf("deploy plan must skip the restart:\n%s", joined)
	}

	p, err = e.mgr.Plan(ctx, "self", catalog.ActionRetire, catalog.ActionOptions{})
	if err != nil || strings.Contains(strings.Join(p.Steps, "\n"), "stop ") {
		t.Fatalf("retire plan must not stop: %+v %v", p, err)
	}
	if _, err := e.mgr.Retire(e.ctx, "self", catalog.ActionOptions{}); err != nil || len(ad.Calls()) != 0 {
		t.Fatalf("retire = %v, calls %v", err, ad.Calls())
	}
}

func TestDeployWithoutRestartLeavesItToInstall(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	e.put(statusOnlyDoc("self", fmt.Sprintf("  source:\n    repo: %s\n    branch: main\n    build: %q\n    install: %q\n", g.repo, okBuild, okInstall)), 0)
	ad := e.runtime.adapter("self")

	res, err := e.mgr.Deploy(e.ctx, "self", catalog.ActionOptions{})
	if err != nil || res.To != catalog.StateRunning {
		t.Fatalf("deploy = %+v %v", res, err)
	}
	if g.installed() != "v1" || len(ad.Calls()) != 0 {
		t.Fatalf("installed %q, runtime calls %v", g.installed(), ad.Calls())
	}
	deps := deployments(t, e, "self")
	if len(deps) != 1 || !strings.Contains(deps[0].LogExcerpt, "skipping restart") {
		t.Fatalf("deployment log must mention the skipped restart: %+v", deps)
	}
}

func TestAvailableActions(t *testing.T) {
	e := newEnv(t)
	e.put(statusOnlyDoc("self", "  source: { repo: /srv/self, branch: main }\n"), 0)
	e.setState("self", catalog.StateRunning)
	e.put(svcDoc("app", "  backup: { command: \"tar c /srv/app\" }\n"), 0)
	e.setState("app", catalog.StateRunning)

	cases := map[string]map[string]string{
		"self": {
			catalog.ActionStart: catalog.ReasonUnsupported, catalog.ActionStop: catalog.ReasonUnsupported,
			catalog.ActionRestart: catalog.ReasonUnsupported, catalog.ActionDeploy: "", catalog.ActionRollback: "",
			catalog.ActionBackup: catalog.ReasonNotConfigured, catalog.ActionRunJob: catalog.ReasonNotConfigured,
			catalog.ActionRetire: "",
		},
		"app": {
			catalog.ActionStart: catalog.ReasonInvalidState, catalog.ActionStop: "", catalog.ActionRestart: "",
			catalog.ActionDeploy: catalog.ReasonNotConfigured, catalog.ActionRollback: catalog.ReasonNotConfigured,
			catalog.ActionBackup: "", catalog.ActionRunJob: catalog.ReasonNotConfigured, catalog.ActionRetire: "",
		},
	}
	for name, want := range cases {
		rec, err := e.cat.Get(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		got := catalog.AvailableActions(rec)
		if len(got) != len(catalog.Actions) {
			t.Fatalf("%s: %d entries, want %d", name, len(got), len(catalog.Actions))
		}
		for _, a := range got {
			if a.Reason != want[a.Action] || a.Allowed != (want[a.Action] == "") {
				t.Errorf("%s %s = %+v, want reason %q", name, a.Action, a, want[a.Action])
			}
		}
	}
}
