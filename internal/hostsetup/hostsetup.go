// Package hostsetup installs LCM's host components and keeps them converged
// with the files embedded in the binary: "lcm host init", "lcm host apply"
// and "lcm host status".
//
// Apply validates everything it can before it changes anything, then runs a
// fixed sequence of steps. Each step compares the host with the wanted state
// and reports "unchanged", "would change: ..." (check mode) or
// "changed: ...". It never removes a component that the configuration
// turns off, and never runs the self-deployed binary as root: whoever can
// push to the deployed branch controls that binary.
//
// Every effect goes through Host, so that tests run against a fake
// filesystem and fake commands instead of the machine.
package hostsetup

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// Host is everything host setup reads and changes.
type Host struct {
	FS     FS
	Runner Runner
	Clock  clock.Clock
	// Health fetches LCM's health endpoint and fails unless it answers 200.
	Health func(ctx context.Context) error
	// Privileged reports that the process may change the host: it runs as
	// root, or the host is a test host.
	Privileged bool
	// Executable is the FS path of the running lcm binary, the one apply
	// installs.
	Executable string
}

// OSHost returns the real host.
func OSHost() (Host, error) {
	exe, err := os.Executable()
	if err != nil {
		return Host{}, err
	}
	return Host{
		FS: OS{}, Runner: OSRunner{}, Clock: clock.Real(), Health: httpHealth,
		Privileged: os.Geteuid() == 0, Executable: exe,
	}, nil
}

func httpHealth(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, HealthURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{code: resp.StatusCode}
	}
	return nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return "health endpoint answered " + http.StatusText(e.code)
}
