package hostsetup

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func securityHost(t *testing.T) *testHost {
	t.Helper()
	th := converged(t)
	th.cfg.Components.Security = &Security{MachineID: "m-1", Watch: map[string][]string{"deploy": {"/opt/extra"}}}
	th.writeConfig(th.cfg)
	return th
}

func TestSecurityInstall(t *testing.T) {
	th := securityHost(t)
	res, out, err := th.apply(Options{Check: true})
	if err != nil || res.Manual != 0 {
		t.Fatalf("check: %+v %v\n%s", res, err, out)
	}
	for _, want := range []string{
		"security:        would change: install " + SecurityRuntime,
		"would change: install " + SecuritySudoers,
		"would change: write " + SecurityEnvFile,
		"note: apply creates the ingest token with: lcm token create --scope ingest --name security-collector",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output lacks %q:\n%s", want, out)
		}
	}
	if w := th.fs.writes(); len(w) > 0 {
		t.Fatalf("check wrote %v", w)
	}
	if !slices.Equal(th.r.sec.actions, []string{"plan"}) || len(th.r.called("[lcm] "+Binary+" token")) != 0 {
		t.Fatalf("check ran %v", th.r.calls)
	}
	in := th.r.sec.inputs[0]
	if in.MachineID != "m-1" || in.Endpoint != SecurityEndpoint || !slices.Equal(in.Watch["deploy"], []string{"/opt/extra"}) || in.Token != "" {
		t.Errorf("installer input = %+v", in)
	}

	th.r.resetCalls()
	_, out, err = th.apply(Options{})
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !slices.Equal(th.r.sec.actions, []string{"plan", "plan", "apply"}) {
		t.Errorf("installer actions = %v", th.r.sec.actions)
	}
	if got := th.r.sec.inputs[2].Token; got != testToken {
		t.Errorf("the installer received token %q", got)
	}
	for _, c := range th.r.calls {
		if strings.Contains(c, testToken) {
			t.Errorf("the token is in a command line: %s", c)
		}
	}
	if len(th.r.called("[lcm] "+Binary+" token create --name security-collector --scope ingest --data-dir "+DataDir)) != 1 {
		t.Errorf("no ingest token was created as lcm: %v", th.r.calls)
	}
	if strings.Contains(out, testToken) {
		t.Errorf("the token reached the output:\n%s", out)
	}
	// The grant comes after the installer verified its installation.
	if py, grant := th.r.index(python3), th.r.index("visudo -c"); py < 0 || grant < py {
		t.Errorf("sudoers rule before the installer: %v", th.r.calls)
	}
	if n := th.fs.get(SecuritySudoers); n == nil || string(n.data) != string(asset("security/sudoers")) || n.mode != 0o440 {
		t.Errorf("sudoers rule = %+v", n)
	}
	lcm := th.r.accounts["lcm"]
	if n := th.fs.get(SecurityEnvFile); n == nil || string(n.data) != "LCM_SECURITY_ENABLED=true\nLCM_SECURITY_MACHINE_ID=m-1\n" || n.mode != 0o640 || n.gid != lcm.GID {
		t.Errorf("env file = %+v", n)
	}
	if th.fs.content(SecurityDropIn) != securityDropIn || len(th.r.called("systemctl restart lcm.service")) != 1 {
		t.Errorf("LCM does not read the security settings: %v", th.r.calls)
	}
	for _, want := range []string{"changed: install " + SecurityRuntime, "note: recorded as transaction " + testTransaction} {
		if !strings.Contains(out, want) {
			t.Errorf("apply output lacks %q:\n%s", want, out)
		}
	}
	th.expectConverged()
}

// TestSecurityAfterIngress: the security component converges after the
// deploy helper and the ingress site, so that it sees the helper's files
// and the hand-made site no longer includes the hooks adoption removes.
func TestSecurityAfterIngress(t *testing.T) {
	th := ingressHost(t, true)
	th.cfg.Components.Security = &Security{MachineID: "m-1"}
	th.writeConfig(th.cfg)
	_, out, err := th.apply(Options{Check: true})
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	helper, site, security := strings.Index(out, DeployHelper), strings.Index(out, "ingress:"), strings.Index(out, "security:")
	if helper < 0 || site < 0 || security < 0 || helper > security || site > security {
		t.Fatalf("order: helper %d, ingress %d, security %d\n%s", helper, site, security, out)
	}
}

func TestSecurityInputWatchesGrantedServices(t *testing.T) {
	a := &applier{cfg: Config{Services: map[string]ServiceGrant{"web": {Port: 3000}, "site": {Port: 3001}, "docs": {External: true}}}}
	in := a.securityInput(&Security{MachineID: "m-1", Watch: map[string][]string{"deploy": {"/opt/web", "/opt/extra"}, "cron": {"/etc/cron.extra"}}})
	// An external grant gets hooks for its route but no deploy watches.
	if !slices.Equal(in.Services, []string{"docs", "site", "web"}) {
		t.Errorf("services = %v", in.Services)
	}
	if want := []string{"/etc/site", "/etc/web", "/opt/extra", "/opt/site", "/opt/web"}; !slices.Equal(in.Watch["deploy"], want) {
		t.Errorf("deploy watches = %v, want %v", in.Watch["deploy"], want)
	}
	if want := []string{"/opt/site", "/opt/web"}; !slices.Equal(in.AIDEIgnore, want) {
		t.Errorf("AIDE exclusions = %v, want %v", in.AIDEIgnore, want)
	}
	if !slices.Equal(in.Watch["cron"], []string{"/etc/cron.extra"}) {
		t.Errorf("cron watches = %v", in.Watch["cron"])
	}
}

func TestSecurityReviewStopsBeforeTheGrant(t *testing.T) {
	th := securityHost(t)
	th.r.sec.manual = []string{"the installed helper policy is for Machine 'm-2', the configuration names 'm-1'"}
	res, out, err := th.apply(Options{})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "security:        needs manual action: the installed helper policy is for Machine 'm-2'") {
		t.Fatalf("%+v %v\n%s", res, err, out)
	}
	if slices.Contains(th.r.sec.actions, "apply") || th.fs.get(SecuritySudoers) != nil || th.fs.get(SecurityEnvFile) != nil {
		t.Fatalf("apply changed the component: %v", th.r.sec.actions)
	}
}

func TestSecurityInstallerFailureKeepsTheGrantAway(t *testing.T) {
	th := securityHost(t)
	th.r.sec.failApply = true
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "install.py apply: nginx -t failed; the changes of transaction "+testTransaction+" were undone") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if th.fs.get(SecuritySudoers) != nil {
		t.Fatal("the grant was installed after a failed installation")
	}
	// The token created for the failed run is revoked.
	if !strings.Contains(err.Error(), "the new ingest token 2 was revoked") || !th.r.revoked[2] || th.r.revoked[1] {
		t.Errorf("tokens %v revoked %v: %v", th.r.tokens, th.r.revoked, err)
	}
}

func TestSecurityServiceLimit(t *testing.T) {
	th := securityHost(t)
	th.cfg.Services = map[string]ServiceGrant{}
	for i := range maxSecurityServices + 1 {
		th.cfg.Services[fmt.Sprintf("svc%02d", i)] = ServiceGrant{External: true, Route: &RouteGrant{Prefix: fmt.Sprintf("/s%02d/", i), Static: "/srv/site"}}
	}
	a := &applier{cfg: th.cfg, h: th.host}
	err := a.securityComponent(context.Background(), &step{a: a, name: "security"})
	if err == nil || !strings.Contains(err.Error(), "at most 64 services") || len(th.r.sec.actions) != 0 {
		t.Fatalf("err = %v, installer actions %v", err, th.r.sec.actions)
	}
}

func TestSecuritySettingsOfAnotherMachine(t *testing.T) {
	th := securityHost(t)
	th.converge()
	lcm := th.r.accounts["lcm"]
	th.fs.file(SecurityEnvFile, "LCM_SECURITY_ENABLED=true\nLCM_SECURITY_MACHINE_ID=m-2\nLCM_SECURITY_KONATA_URL=\n", 0o640, 0, lcm.GID)
	res, out, err := th.apply(Options{Check: true})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "LCM_SECURITY_MACHINE_ID=m-1; edit it") {
		t.Fatalf("%+v %v\n%s", res, err, out)
	}
	th.fs.file(SecurityEnvFile, "# kept\nLCM_SECURITY_ENABLED=true\nLCM_SECURITY_MACHINE_ID=m-1\nLCM_SECURITY_KONATA_URL=http://127.0.0.1:4100/x\n", 0o640, 0, lcm.GID)
	th.expectConverged()
}

func TestSecurityRemovalNeedsRemove(t *testing.T) {
	th := securityHost(t)
	th.converge()
	th.cfg.Components.Security = nil
	th.writeConfig(th.cfg)
	th.r.sec.actions = nil
	th.fs.resetOps()
	for _, check := range []bool{true, false} {
		res, out, err := th.apply(Options{Check: check})
		if err != nil || res.Manual != 1 || !strings.Contains(out, "sudo bin/lcm host apply --remove removes it") ||
			!strings.Contains(out, "note: would remove "+SecurityRuntime) {
			t.Fatalf("check=%v: %+v %v\n%s", check, res, err, out)
		}
	}
	if w := th.fs.writes(); len(w) > 0 || slices.Contains(th.r.sec.actions, "remove") {
		t.Fatalf("removed without --remove: %v %v", w, th.r.sec.actions)
	}
	res, out, err := th.apply(Options{Check: true, Remove: true})
	if err != nil || res.Changes != 2 || th.fs.get(SecuritySudoers) == nil {
		t.Fatalf("check --remove: %+v %v\n%s", res, err, out)
	}
	th.fs.resetOps()
	_, out, err = th.apply(Options{Remove: true})
	if err != nil {
		t.Fatalf("apply --remove: %v\n%s", err, out)
	}
	if th.fs.get(SecuritySudoers) != nil || th.fs.get(SecurityRuntime) != nil {
		t.Fatal("the component was not removed")
	}
	// The grant goes before the files it grants.
	if ops := strings.Join(th.fs.writes(), "\n"); !strings.HasPrefix(ops, "remove "+SecuritySudoers) {
		t.Errorf("removal order:\n%s", ops)
	}
	if !strings.Contains(out, "changed: remove "+SecurityRuntime) || !strings.Contains(out, "note: kept: the AIDE baseline") {
		t.Errorf("removal output:\n%s", out)
	}
	th.expectConverged()
}

func TestSecurityMaintenance(t *testing.T) {
	th := newTestHost(t)
	th.r.sec.replies = map[string]securityReply{
		"cleanup-plan": {Changes: []string{"remove /var/lib/workstation-security-deploy-20261029 (with its content)"},
			Notes: []string{"/var/lib/lcm-security-console-20261102 is not a known leftover and was left alone; review it by hand"}},
		"revert": {Manual: []string{"/etc/nginx/conf.d/20-workstation-security.conf changed after transaction " + testTransaction}},
	}
	var out bytes.Buffer
	res, err := SecurityCleanup(context.Background(), th.host, SecurityOptions{Check: true, Out: &out})
	if err != nil || res.Changes != 1 || !strings.Contains(out.String(), "security cleanup: would change: remove /var/lib/workstation-security-deploy-20261029") ||
		!strings.Contains(out.String(), "note: /var/lib/lcm-security-console-20261102 is not a known leftover") {
		t.Fatalf("%+v %v\n%s", res, err, out.String())
	}
	out.Reset()
	res, err = SecurityRevert(context.Background(), th.host, SecurityOptions{Transaction: testTransaction, Out: &out})
	if err != nil || res.Manual != 1 || !strings.Contains(out.String(), "needs manual action: /etc/nginx/conf.d/20-workstation-security.conf changed") {
		t.Fatalf("%+v %v\n%s", res, err, out.String())
	}
	if !slices.Equal(th.r.sec.actions, []string{"cleanup-plan", "revert --transaction " + testTransaction}) {
		t.Errorf("installer actions = %v", th.r.sec.actions)
	}
	if _, err := SecurityRevert(context.Background(), th.host, SecurityOptions{Transaction: "../x"}); err == nil {
		t.Error("a malformed transaction ID was passed on")
	}
	th.host.Privileged = false
	if _, err := SecurityCleanup(context.Background(), th.host, SecurityOptions{}); err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Errorf("unprivileged cleanup: %v", err)
	}
	th.host.Privileged = true
	th.host.Executable = Binary
	if _, err := SecurityCleanup(context.Background(), th.host, SecurityOptions{}); err == nil || !strings.Contains(err.Error(), "self-deployed binary") {
		t.Errorf("self-deployed cleanup: %v", err)
	}
}
