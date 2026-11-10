package hostsetup

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrIngressBusy reports that another change of the site holds its lock.
var ErrIngressBusy = errors.New("another change of the nginx site is running")

// LockIngress takes IngressLock and, while it exists, the legacy lock that
// hand-made scripts take, waiting at most wait for each. root prefixes the
// paths (a test root, or "" on a host) and rootUID must own both files.
//
// The legacy lock lives in a directory everyone may write, so it is never
// created or followed: it is opened without O_CREAT and O_NOFOLLOW, and
// must be a regular file of root's.
func LockIngress(root string, rootUID int, legacy string, wait time.Duration) (func(), error) {
	own, err := openLock(root+IngressLock, rootUID, true)
	if err != nil {
		return nil, err
	}
	if err := flockWait(own, wait); err != nil {
		_ = own.Close()
		return nil, err
	}
	unlock := func() { _ = own.Close() }
	if legacy == "" {
		return unlock, nil
	}
	old, err := openLock(root+legacy, rootUID, false)
	if errors.Is(err, os.ErrNotExist) {
		return unlock, nil
	}
	if err == nil {
		err = flockWait(old, wait)
		if err != nil {
			_ = old.Close()
		}
	}
	if err != nil {
		unlock()
		return nil, fmt.Errorf("legacy lock %s: %w", legacy, err)
	}
	return func() {
		_ = old.Close()
		unlock()
	}, nil
}

// openLock opens a lock file without following a link; with create it is
// made (0600) when missing. Its directory must be root's alone when the file
// is created there.
func openLock(p string, rootUID int, create bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC | syscall.O_NOCTTY
	if create {
		dir, err := os.Lstat(filepath.Dir(p))
		if err != nil {
			return nil, err
		}
		if st, ok := dir.Sys().(*syscall.Stat_t); !ok || !dir.IsDir() || int(st.Uid) != rootUID || dir.Mode().Perm()&0o022 != 0 {
			return nil, fmt.Errorf("%s is not a directory only root can change", filepath.Dir(p))
		}
		flags = os.O_RDWR | os.O_CREATE | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	}
	f, err := os.OpenFile(p, flags, 0o600) //nolint:gosec // fixed lock paths, opened without following a link
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || !fi.Mode().IsRegular() || int(st.Uid) != rootUID || st.Nlink != 1 {
			err = fmt.Errorf("%s is not a root-owned regular file", p)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// flockWait takes an exclusive lock, trying again until wait has passed.
func flockWait(f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // descriptors fit in int
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if !time.Now().Before(deadline) {
			return ErrIngressBusy
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ProbeHTTP sends a GET for p.Path to p.Address with p.Host as Host and
// TLS server name, without following redirects, and returns the status.
// The certificate is not verified: the probe checks that nginx serves the
// route, and an expiring certificate must not make a route change fail.
func ProbeHTTP(ctx context.Context, p Probe) (int, error) {
	dialer := &net.Dialer{Timeout: probeTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, p.Address)
		},
		TLSClientConfig:   &tls.Config{ServerName: p.Host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // a local probe of the route; the certificate's validity is the tls check's to report
		DisableKeepAlives: true,
		Proxy:             nil,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.String(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}
