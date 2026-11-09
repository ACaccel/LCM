package deployhelper

import (
	"context"
	"os"
	"testing"

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
