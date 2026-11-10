package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/upgrademarker"
)

const (
	upgradeOldCommit = "aaaaaaaaaaaa"
	upgradeNewCommit = "bbbbbbbbbbbb"
)

func upgradeMarkerFile(t *testing.T, dir string, to string) string {
	t.Helper()
	data, err := upgrademarker.Encode(upgrademarker.Marker{
		At:   epoch.Add(-time.Minute),
		From: upgrademarker.Build{Version: "v1", Commit: upgradeOldCommit, Schema: 16},
		To:   upgrademarker.Build{Version: "v2", Commit: to, Schema: 17},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, upgrademarker.File)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// selfDeployed makes the service lcm exist with a successful deployment of
// the old commit, as the catalog of a host that deployed itself before.
func selfDeployed(t *testing.T, h *harness) {
	t.Helper()
	h.putService("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: lcm }\nspec:\n  type: service\n  adapter: external\n  config: {}\n")
	id, err := h.app.Catalog.BeginDeployment(h.ctx, catalog.Deployment{
		Resource: "lcm", Action: catalog.ActionDeploy, Version: upgradeOldCommit, Actor: auth.CLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Catalog.FinishDeployment(h.ctx, id, catalog.OutcomeSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
}

func deploymentsOf(t *testing.T, h *harness) []catalog.Deployment {
	t.Helper()
	deps, err := h.app.Catalog.Deployments(h.ctx, "lcm", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

// The upgraded server records the upgrade the root command attested, once,
// and removes the marker.
func TestHostUpgradeMarkerIsRecordedAtStart(t *testing.T) {
	h := newHarness(t, func(o *testOpts) { o.commit = upgradeNewCommit + "0123456789" })
	selfDeployed(t, h)
	marker := upgradeMarkerFile(t, h.dir, upgradeNewCommit)

	h.reopen(h.clk)
	deps := deploymentsOf(t, h)
	if len(deps) != 2 {
		t.Fatalf("deployments = %+v, want the old one and the recorded upgrade", deps)
	}
	d := deps[0]
	if d.Outcome != catalog.OutcomeSucceeded || d.Version != upgradeNewCommit || d.PreviousVersion != upgradeOldCommit ||
		d.Action != catalog.ActionDeploy || d.Actor != catalog.HostUpgradeActor || !d.StartedAt.Equal(epoch.Add(-time.Minute)) {
		t.Fatalf("recorded deployment = %+v", d)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Errorf("the marker was not removed: %v", err)
	}
	tl, err := h.app.Catalog.Timeline(h.ctx, catalog.TimelineQuery{Resource: "lcm", Kinds: []string{catalog.KindDeployment}})
	if err != nil || len(tl.Entries) == 0 || tl.Entries[0].Data["actor"] != catalog.HostUpgradeActor.Name {
		t.Fatalf("timeline = %+v %v", tl, err)
	}
	// The next start finds nothing to do.
	h.reopen(h.clk)
	if got := deploymentsOf(t, h); len(got) != 2 {
		t.Fatalf("deployments after another start = %d, want 2", len(got))
	}
}

// A stale marker file of an earlier start (the removal failed) adds nothing
// when the catalog already agrees.
func TestHostUpgradeMarkerAlreadyRecordedIsDropped(t *testing.T) {
	h := newHarness(t, func(o *testOpts) { o.commit = upgradeNewCommit })
	selfDeployed(t, h)
	id, err := h.app.Catalog.BeginDeployment(h.ctx, catalog.Deployment{
		Resource: "lcm", Action: catalog.ActionDeploy, Version: upgradeNewCommit + "0123456789abcdef", Actor: auth.CLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Catalog.FinishDeployment(h.ctx, id, catalog.OutcomeSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	marker := upgradeMarkerFile(t, h.dir, upgradeNewCommit)
	h.reopen(h.clk)
	if got := deploymentsOf(t, h); len(got) != 2 {
		t.Fatalf("deployments = %d, want 2", len(got))
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Errorf("the marker was not removed: %v", err)
	}
}

// LCM records only what it can match to itself: a marker for another build
// says nothing about this process and is left alone.
func TestHostUpgradeMarkerForAnotherBuildIsIgnored(t *testing.T) {
	for _, running := range []string{"cccccccccccc", ""} {
		h := newHarness(t, func(o *testOpts) { o.commit = running })
		selfDeployed(t, h)
		marker := upgradeMarkerFile(t, h.dir, upgradeNewCommit)
		h.reopen(h.clk)
		if got := deploymentsOf(t, h); len(got) != 1 {
			t.Fatalf("running %q: deployments = %+v, want only the old one", running, got)
		}
		if _, err := os.Lstat(marker); err != nil {
			t.Errorf("running %q: the marker of another build was removed: %v", running, err)
		}
	}
}

func TestHostUpgradeMarkerWithoutServiceIsDropped(t *testing.T) {
	h := newHarness(t, func(o *testOpts) { o.commit = upgradeNewCommit })
	marker := upgradeMarkerFile(t, h.dir, upgradeNewCommit)
	h.reopen(h.clk)
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Errorf("the marker was not removed: %v", err)
	}
}

// An unreadable marker never fails the start and records nothing.
func TestHostUpgradeBadMarkerIsIgnored(t *testing.T) {
	h := newHarness(t, func(o *testOpts) { o.commit = upgradeNewCommit })
	selfDeployed(t, h)
	marker := filepath.Join(h.dir, upgrademarker.File)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.json")
	good := upgradeMarkerFile(t, filepath.Dir(elsewhere), upgradeNewCommit)
	if err := os.Rename(good, elsewhere); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(){
		"garbage":     func() { _ = os.WriteFile(marker, []byte(`{"format":1,"extra":true}`), 0o600) },
		"oversized":   func() { _ = os.WriteFile(marker, make([]byte, upgrademarker.MaxSize+1), 0o600) },
		"a link":      func() { _ = os.Symlink(elsewhere, marker) },
		"a directory": func() { _ = os.Mkdir(marker, 0o700) },
	} {
		_ = os.RemoveAll(marker)
		setup()
		h.reopen(h.clk)
		if got := deploymentsOf(t, h); len(got) != 1 {
			t.Errorf("%s: deployments = %+v, want only the old one", name, got)
		}
	}
}
