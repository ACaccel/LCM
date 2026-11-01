package power

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
)

const deploymentDir = "/etc/lcm"
const receiptDir = "/var/lib/lcm-machine-power"

// DeploymentConfig is read only from the fixed root-owned deployment file.
// RecoveryReady must only be set after controller startup gate verification.
// SystemdVersion pins the administrator-validated systemd major version.
type DeploymentConfig struct {
	Version        int    `json:"version"`
	ControllerID   string `json:"controllerID"`
	SystemdVersion int    `json:"systemdVersion"`
	RecoveryReady  bool   `json:"recoveryReady"`
}

// ServeLocal is the privileged entry point. It accepts no paths, flags or env
// configuration. Input must be a pipe; its duplicate is registered for bounded
// reads. Regular files and TTYs fail closed.
func ServeLocal(ctx context.Context, in io.Reader, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("machine power helper requires root")
	}
	configRoot, err := secureRoot(deploymentDir)
	if err != nil {
		return errors.New("untrusted helper configuration")
	}
	defer func() { _ = configRoot.Close() }()
	var cfg DeploymentConfig
	if err := readConfig(configRoot, &cfg); err != nil {
		return errors.New("invalid helper configuration")
	}
	if cfg.Version != Version || !uuid.MatchString(cfg.ControllerID) {
		return errors.New("helper identity or version not configured")
	}
	root, err := secureRoot(receiptDir)
	if err != nil {
		return errors.New("untrusted receipt directory")
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(".")
	if err != nil || info.Mode().Perm() != 0700 {
		return errors.New("receipt directory must have mode 0700")
	}
	runner := execx.New(execx.WithEnvAllow())
	h := Helper{ControlDisabled: !cfg.RecoveryReady || cfg.SystemdVersion < 248, ControllerID: cfg.ControllerID, Store: &fileStore{root: root, owner: 0}, System: &localSystem{runner: runner, version: cfg.SystemdVersion, disabled: !cfg.RecoveryReady || cfg.SystemdVersion < 248}, Clock: clock.Real()}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	input, err := deadlineInput(in, deadline)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	return h.Serve(ctx, input, out)
}

// Inherited os.Stdin may not be registered with Go's poller. A nonblocking
// duplicate makes deadlines effective without reopening a caller-chosen path.
func deadlineInput(in io.Reader, deadline time.Time) (*os.File, error) {
	file, ok := in.(*os.File)
	if !ok {
		return nil, errors.New("helper stdin requires a pipe")
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, errors.New("helper stdin requires a pipe")
	}
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, errors.New("cannot duplicate helper stdin")
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, errors.New("cannot bound helper stdin")
	}
	input := os.NewFile(uintptr(fd), "machine-helper-stdin")
	if err := input.SetReadDeadline(deadline); err != nil {
		_ = input.Close()
		return nil, errors.New("cannot bound helper stdin")
	}
	return input, nil
}

type localSystem struct {
	disabled bool
	runner   execx.Runner
	version  int
}

func (s *localSystem) run(ctx context.Context, argv ...string) (string, error) {
	result, err := s.runner.Run(ctx, execx.Cmd{Argv: argv, Timeout: 5 * time.Second, MaxOutput: 16 << 10, Env: []string{"LC_ALL=C", "SYSTEMD_PAGER=cat", "SYSTEMD_COLORS=0"}})
	if err != nil || result.Truncated {
		return "", errors.New("system command unavailable")
	}
	return strings.TrimSpace(result.Output), nil
}
func (s *localSystem) BootID() (string, error) {
	f, err := os.Open("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 64))
	id := strings.TrimSpace(string(raw))
	if err != nil || !uuid.MatchString(id) {
		return "", errors.New("invalid boot identity")
	}
	return id, nil
}
func (s *localSystem) bus(ctx context.Context, method, signature string) ([]json.RawMessage, error) {
	raw, err := s.run(ctx, "/usr/bin/busctl", "--system", "--no-pager", "--allow-interactive-authorization=no", "--json=short", "call", "org.freedesktop.login1", "/org/freedesktop/login1", "org.freedesktop.login1.Manager", method)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Type string            `json:"type"`
		Data []json.RawMessage `json:"data"`
	}
	if err := decodeStrict(strings.NewReader(raw), 16<<10, &reply); err != nil || reply.Type != signature || len(reply.Data) != 1 {
		return nil, errors.New("unsupported logind response")
	}
	return reply.Data, nil
}
func (s *localSystem) Check(ctx context.Context, action Action) error {
	if s.disabled {
		return errors.New("power control is disabled; receipt queries remain available")
	}
	if action != Reboot && action != GracefulShutdown {
		return errors.New("unsupported action")
	}
	raw, err := s.run(ctx, "/usr/bin/systemctl", "--version")
	if err != nil {
		return err
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 || fields[0] != "systemd" {
		return errors.New("unsupported init")
	}
	version, err := strconv.Atoi(fields[1])
	if err != nil || version < 248 || version != s.version {
		return errors.New("unvalidated systemd version")
	}
	enabled, err := s.run(ctx, "/usr/bin/systemctl", "--system", "--no-pager", "is-enabled", "lcm.service")
	if err != nil || enabled != "enabled" {
		return errors.New("controller autostart unconfirmed")
	}
	method := "CanReboot"
	if action == GracefulShutdown {
		method = "CanPowerOff"
	}
	data, err := s.bus(ctx, method, "s")
	if err != nil || string(data[0]) != `"yes"` {
		return errors.New("power capability unavailable")
	}
	// Conservatively reject every login session and inhibitor, even idle sessions
	// and inhibitors unrelated to shutdown. There is no caller-provided override.
	for _, check := range []struct{ method, signature string }{{"ListSessions", "a(susso)"}, {"ListInhibitors", "a(ssssuu)"}} {
		data, err := s.bus(ctx, check.method, check.signature)
		if err != nil {
			return err
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(data[0], &entries); err != nil || string(data[0]) == "null" || len(entries) != 0 {
			return errors.New("active sessions or inhibitors")
		}
	}
	return nil
}
func (s *localSystem) Request(ctx context.Context, action Action) error {
	verb := ""
	switch action {
	case Reboot:
		verb = "reboot"
	case GracefulShutdown:
		verb = "poweroff"
	default:
		return errors.New("unsupported action")
	}
	_, err := s.run(ctx, "/usr/bin/systemctl", "--system", "--no-ask-password", "--check-inhibitors=yes", verb)
	if err != nil {
		return fmt.Errorf("OS handoff uncertain: %w", err)
	}
	return nil
}
