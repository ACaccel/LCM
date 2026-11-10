package deployhelper

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// TestOnlyLCMAccountsAndUnitsAreUsed checks that the helper itself, not only
// host setup, refuses accounts LCM did not create and units that something
// else changes.
func TestOnlyLCMAccountsAndUnitsAreUsed(t *testing.T) {
	e := newEnv(t)
	_, rel := e.deploy(commit1)
	act := Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "node24"}
	lookup := e.h.Lookup
	for name, comment := range map[string]string{"web": "LCM service web", "lcmb-web": "LCM build web"} {
		e.h.Lookup = func(ctx context.Context, n string) (hostsetup.Account, bool, error) {
			acc, ok, err := lookup(ctx, n)
			if n == name {
				acc.GECOS = "www-data"
			}
			return acc, ok, err
		}
		req := act
		if name == "lcmb-web" {
			req = buildReq(commit1)
		}
		e.refused(req, CodeRefused, "account "+name+" exists and was not created by LCM (its comment is not \""+comment+"\")")
	}
	e.h.Lookup = lookup

	for _, dropIn := range []string{"/etc/systemd/system/web.service.d", "/run/systemd/system.control/web.service.d"} {
		e.mkdir(dropIn, 0o755)
		e.refused(act, CodeRefused, "the drop-in directory "+dropIn+" exists")
		e.refused(Request{Op: OpRestart, Service: "web"}, CodeRefused, "the drop-in directory "+dropIn+" exists")
		if err := os.RemoveAll(e.real(dropIn)); err != nil {
			t.Fatal(err)
		}
	}
	e.ok(act)
}

// TestRealLookupFeedsTheAccountCheck chains the helper's getent lookup and
// its parser, from passwd-format output as host setup leaves it, into the
// account check, so that a fake Lookup cannot hide a field the parser drops.
func TestRealLookupFeedsTheAccountCheck(t *testing.T) {
	e := newEnv(t)
	uid, gid := os.Getuid(), os.Getgid()
	passwd := map[string]string{
		"lcm":      fmt.Sprintf("lcm:x:%d:%d:LCM:%s:/usr/sbin/nologin\n", uid, gid, hostsetup.DataDir),
		"lcmb-web": fmt.Sprintf("lcmb-web:x:%d:%d:LCM build web:/var/lib/lcm-build/web:/usr/sbin/nologin\n", uid, gid),
		"web":      fmt.Sprintf("web:x:%d:%d:LCM service web:/var/lib/web:/usr/sbin/nologin\n", uid, gid),
		"alice":    fmt.Sprintf("alice:x:%d:%d:Alice,,,:/home/alice:/bin/bash\n", uid, gid),
	}
	getentRunner := &execx.Fake{}
	getentRunner.On(func(c execx.Cmd) bool { return c.Argv[0] == getent }, func(c execx.Cmd) (execx.Result, error) {
		out, ok := passwd[c.Argv[2]]
		if !ok {
			return execx.Result{ExitCode: 2}, &execx.ExitError{Code: 2}
		}
		return execx.Result{Output: out}, nil
	})
	e.h.Lookup = func(ctx context.Context, name string) (hostsetup.Account, bool, error) {
		return lookup(ctx, getentRunner, name)
	}
	_, rel := e.deploy(commit1)

	passwd["web"] = fmt.Sprintf("web:x:%d:%d:www-data:/var/lib/web:/usr/sbin/nologin\n", uid, gid)
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "node24"}, CodeRefused,
		"account web exists and was not created by LCM")
}
