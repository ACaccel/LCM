package deployhelper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// readDeadline bounds how long the helper waits for its request.
const readDeadline = 30 * time.Second

// ServeLocal is the privileged entry point of "lcm deploy-helper". It
// accepts no arguments, paths or environment: everything comes from the
// request on stdin, which must be a pipe, and from the root-owned host
// configuration.
func ServeLocal(ctx context.Context, in io.Reader, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("the deploy helper must run as root, through /usr/local/libexec/lcm-deploy")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if hostsetup.SelfDeployed(exe) {
		return fmt.Errorf("refusing to run %s as the deploy helper: it is the self-deployed binary", exe)
	}
	input, err := deadlineInput(in, time.Now().Add(readDeadline))
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(input, MaxRequest+1))
	_ = input.Close()
	if err != nil {
		_, err = out.Write(encodeResponse(Response{Version: Version, Code: CodeRefused, Message: "the request could not be read"}))
		return err
	}
	fsys := hostsetup.OS{}
	runner := execx.New(execx.WithEnvAllow())
	host := Host{
		FS: fsys, Runner: runner, Clock: clock.Real(),
		Config: func() (hostsetup.Config, error) { return hostsetup.LoadConfig(fsys, hostsetup.ConfigPath) },
		Lookup: func(ctx context.Context, name string) (hostsetup.Account, bool, error) {
			return lookup(ctx, runner, name)
		},
		Free: statfsFree,
	}
	h := &Helper{Host: host}
	ledger, err := openTrustedLedger(h, "")
	if err != nil {
		_, err = out.Write(encodeResponse(Response{Version: Version, Code: CodeFailed,
			Message: hostsetup.Printable("the ledger "+hostsetup.DeployLedger+" cannot be used ("+err.Error()+"); run sudo bin/lcm host apply", 2000)}))
		return err
	}
	defer func() { _ = ledger.Close() }()
	h.Ledger = ledger
	_, err = out.Write(encodeResponse(h.Handle(ctx, raw)))
	return err
}

// openTrustedLedger checks the ledger directory's ancestors as host paths
// and opens it below root (a test root, or "" on a host).
func openTrustedLedger(h *Helper, root string) (*Ledger, error) {
	dir := hostsetup.DeployLedger
	if _, err := h.trustedPath(filepath.Dir(dir), false); err != nil {
		return nil, err
	}
	return OpenLedger(realPath(root, dir), h.RootUID)
}

// deadlineInput duplicates the stdin pipe as a non-blocking descriptor, so
// that its read deadline applies. A regular file or a terminal is refused.
func deadlineInput(in io.Reader, deadline time.Time) (*os.File, error) {
	file, ok := in.(*os.File)
	if !ok {
		return nil, errors.New("the deploy helper reads its request from a pipe")
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, errors.New("the deploy helper reads its request from a pipe")
	}
	fd, err := syscall.Dup(int(file.Fd())) //nolint:gosec // descriptors fit in int
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	input := os.NewFile(uintptr(fd), "deploy-helper-stdin")
	if err := input.SetReadDeadline(deadline); err != nil {
		_ = input.Close()
		return nil, err
	}
	return input, nil
}

// lookup reads an account with getent; a missing account returns found
// false.
func lookup(ctx context.Context, r execx.Runner, name string) (hostsetup.Account, bool, error) {
	res, err := r.Run(ctx, execx.Cmd{Argv: []string{getent, "passwd", name}, Dir: "/", Env: commandEnv, Timeout: 10 * time.Second, MaxOutput: 4096})
	if execx.ExitCode(err) == 2 {
		return hostsetup.Account{}, false, nil
	}
	if err != nil {
		return hostsetup.Account{}, false, fmt.Errorf("getent passwd %s: %w", name, err)
	}
	return parsePasswd(res.Output, name)
}

func parsePasswd(out, name string) (hostsetup.Account, bool, error) {
	f := strings.Split(strings.TrimSpace(out), ":")
	if len(f) != 7 || f[0] != name {
		return hostsetup.Account{}, false, fmt.Errorf("getent passwd %s: unexpected output", name)
	}
	uid, err1 := strconv.Atoi(f[2])
	gid, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil || uid < 0 || gid < 0 || !filepath.IsAbs(f[5]) {
		return hostsetup.Account{}, false, fmt.Errorf("getent passwd %s: unexpected output", name)
	}
	return hostsetup.Account{Name: name, UID: uid, GID: gid, Home: filepath.Clean(f[5]), Shell: f[6]}, true, nil
}
