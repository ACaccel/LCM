package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

const fullSum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestHostComponents(t *testing.T) {
	calls := 0
	status := []hostsetup.ComponentStatus{
		{Name: hostsetup.ComponentCore, State: hostsetup.StateOutdated, Files: []hostsetup.FileStatus{
			{Path: hostsetup.Unit, State: hostsetup.StateOutdated, Installed: fullSum, Embedded: "fedcba9876543210"},
			{Path: hostsetup.Sudoers, State: hostsetup.StateUnknown, Detail: "not readable as this user"},
		}},
		{Name: hostsetup.ComponentMachinePower, State: hostsetup.StateNotInstalled, Files: []hostsetup.FileStatus{
			{Path: hostsetup.PowerWrapper, State: hostsetup.StateMissing},
		}},
		{Name: hostsetup.ComponentSecurityHost, State: hostsetup.StateUpToDate, Files: []hostsetup.FileStatus{
			{Path: hostsetup.SecurityHelper, State: hostsetup.StateUpToDate, Installed: fullSum, Embedded: fullSum},
		}},
	}
	x := newSiteWith(t, []app.Option{app.WithHostStatus(func() ([]hostsetup.ComponentStatus, error) {
		calls++
		return status, nil
	})})

	r := x.call("GET", v1+"/host/components", nil).want(http.StatusOK)
	var body struct {
		Items []struct {
			Name, State string
			Files       []struct{ Path, State, Installed, Embedded, Detail string }
		}
		CheckedAt time.Time
	}
	r.decode(&body)
	if len(body.Items) != 3 || body.Items[0].State != "outdated" || body.Items[1].State != "not_installed" || body.Items[2].State != "up_to_date" {
		t.Fatalf("items = %s", r)
	}
	if f := body.Items[0].Files[0]; f.Installed != fullSum[:12] || f.Embedded != "fedcba987654" || f.Path != hostsetup.Unit {
		t.Errorf("digests are not shortened: %+v", f)
	}
	if f := body.Items[0].Files[1]; f.State != "unknown" || f.Detail == "" || f.Installed != "" {
		t.Errorf("unreadable file = %+v", f)
	}
	if !body.CheckedAt.Equal(epoch) {
		t.Errorf("checkedAt = %v, want %v", body.CheckedAt, epoch)
	}

	// Callers within the cache interval share one read of the files.
	x.clk.Advance(10 * time.Second)
	x.call("GET", v1+"/host/components", nil).want(http.StatusOK)
	if calls != 1 {
		t.Errorf("status read %d times within the interval", calls)
	}
	x.clk.Advance(time.Minute)
	status[0].State = hostsetup.StateUpToDate
	r = x.call("GET", v1+"/host/components", nil).want(http.StatusOK)
	r.decode(&body)
	if calls != 2 || body.Items[0].State != "up_to_date" {
		t.Errorf("after the interval: %d reads, state %s", calls, body.Items[0].State)
	}

	x.anon("GET", v1+"/host/components", nil).want(http.StatusUnauthorized)
	x.call("GET", v1+"/host/components", nil, bearer(x.ingest)).want(http.StatusForbidden)
	x.call("POST", v1+"/host/components", nil).want(http.StatusMethodNotAllowed)
}
