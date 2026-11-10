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
	"fmt"
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
	// Executable is the path of the running lcm binary, which decides
	// whether it may run as root.
	Executable string
	// OpenExecutable opens the content of the running binary, the one
	// apply installs. It is not read by path: the file there may have
	// changed since the program started.
	OpenExecutable func() (io.ReadCloser, error)
	// Probe checks a route of the ingress site after nginx was reloaded;
	// ProbeAttempts (zero is one) and ProbeWait pace it.
	Probe         func(ctx context.Context, p Probe) (int, error)
	ProbeAttempts int
	ProbeWait     time.Duration
	// LockIngress takes the ingress lock and, while it exists, the legacy
	// lock named.
	LockIngress func(legacy string) (func(), error)
}

// OSHost returns the real host.
func OSHost() (Host, error) {
	exe, err := os.Executable()
	if err != nil {
		return Host{}, err
	}
	return Host{
		FS: OS{}, Runner: OSRunner{}, Clock: clock.Real(), Health: httpHealth,
		Privileged: os.Geteuid() == 0, Executable: exe, OpenExecutable: openSelf,
		Probe: ProbeHTTP, ProbeAttempts: 10, ProbeWait: 500 * time.Millisecond,
		LockIngress: func(legacy string) (func(), error) { return LockIngress("", 0, legacy, time.Minute) },
	}, nil
}

// openSelf opens the running program's own file through the kernel, which
// keeps it even if the path it was started from now names another file.
func openSelf() (io.ReadCloser, error) {
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, fmt.Errorf("open /proc/self/exe (is /proc mounted?): %w", err)
	}
	return f, nil
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
