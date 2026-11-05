package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/machine/observe"
	"github.com/ACaccel/lcm/internal/machine/power"
	"github.com/ACaccel/lcm/internal/model"
)

type machineObserver struct {
	runner execx.Runner
	clock  clock.Clock
}

type machineRunner struct {
	runner                execx.Runner
	systemctl, journalctl string
}

func (r machineRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if len(c.Argv) > 0 {
		argv := append([]string(nil), c.Argv...)
		switch argv[0] {
		case "systemctl":
			argv[0] = r.systemctl
		case "journalctl":
			argv[0] = r.journalctl
		}
		c.Argv = argv
	}
	return r.runner.Run(ctx, c)
}

func convertCoverage(c observe.Coverage) machine.Coverage {
	return machine.Coverage{State: string(c.State), Reason: string(c.Reason)}
}

func (o machineObserver) Observe(ctx context.Context, c model.MachineConfig) (machine.Snapshot, error) {
	p, err := observe.NewLinux(observe.Config{Filesystems: c.Filesystems, Interfaces: c.Interfaces, Units: c.Units, PSI: true, CommandTimeout: 2 * time.Second}, o.runner, nil, o.clock)
	if err != nil {
		return machine.Snapshot{}, err
	}
	ctx, cancel := clock.WithTimeoutCause(ctx, o.clock, 15*time.Second, execx.ErrTimeout)
	defer cancel()
	r, err := p.Observe(ctx)
	s := machine.Snapshot{BootID: r.BootID.Value, DeviceHash: r.Identity.Value.Hash, OS: strings.TrimSpace(r.OS.Value.ID + " " + r.OS.Value.VersionID), Init: r.Init.Value.Name, UptimeSeconds: r.Uptime.Value.Seconds(), CPUCount: r.CPU.Value.Count, MemoryTotal: r.Memory.Value.TotalBytes, MemoryAvailable: r.Memory.Value.AvailableBytes, SwapTotal: r.Memory.Value.SwapTotalBytes, SwapFree: r.Memory.Value.SwapFreeBytes, Filesystems: []machine.Filesystem{}, Units: map[string]string{}, Interfaces: map[string]string{}, Pressure: map[string]float64{}, Coverage: map[string]machine.Coverage{}}
	s.CPUTotal = r.CPU.Value.User + r.CPU.Value.Nice + r.CPU.Value.System + r.CPU.Value.Idle + r.CPU.Value.IOWait + r.CPU.Value.IRQ + r.CPU.Value.SoftIRQ + r.CPU.Value.Steal
	s.CPUIdle = r.CPU.Value.Idle + r.CPU.Value.IOWait
	if r.Load.Coverage.State == observe.Available {
		s.Load = []float64{r.Load.Value.One, r.Load.Value.Five, r.Load.Value.Fifteen}
	}
	for key, cov := range map[string]observe.Coverage{"identity": r.Identity.Coverage, "boot": r.BootID.Coverage, "os": r.OS.Coverage, "init": r.Init.Coverage, "uptime": r.Uptime.Coverage, "cpu": r.CPU.Coverage, "load": r.Load.Coverage, "memory": r.Memory.Coverage} {
		s.Coverage[key] = convertCoverage(cov)
	}
	for _, fs := range r.Filesystems {
		s.Coverage["filesystem:"+fs.Path] = convertCoverage(fs.Coverage)
		if fs.Coverage.State == observe.Available {
			s.Filesystems = append(s.Filesystems, machine.Filesystem{Path: fs.Path, Total: fs.Value.TotalBytes, Available: fs.Value.AvailableBytes, Inodes: fs.Value.Inodes, FreeInodes: fs.Value.FreeInodes})
		}
	}
	for _, u := range r.Units {
		s.Coverage["unit:"+u.Name] = convertCoverage(u.Coverage)
		if u.Coverage.State == observe.Available {
			s.Units[u.Name] = u.Value.ActiveState
		} else {
			s.Units[u.Name] = "unknown"
		}
	}
	for _, i := range r.Interfaces {
		s.Coverage["interface:"+i.Name] = convertCoverage(i.Coverage)
		if i.Coverage.State == observe.Available {
			s.Interfaces[i.Name] = i.Value.OperState
		}
	}
	for _, p := range r.Pressure {
		s.Coverage["psi:"+p.Resource] = convertCoverage(p.Coverage)
		if p.Coverage.State == observe.Available && p.Value.Some != nil {
			s.Pressure[p.Resource] = p.Value.Some.Avg10
		}
	}
	return s, err
}

func (o machineObserver) ReadJournal(ctx context.Context, c model.MachineConfig, cursor string, last time.Time) (machine.JournalBatch, error) {
	j, err := observe.NewJournal(observe.JournalConfig{Units: c.Units, CommandTimeout: 5 * time.Second}, o.runner, o.clock)
	if err != nil {
		return machine.JournalBatch{}, err
	}
	request := observe.JournalRequest{Cursor: cursor, Historical: last.UnixMilli() <= 0 || o.clock.Now().Sub(last) > c.StaleAfter.D(), HistoricalBefore: last}
	b, err := j.Poll(ctx, request)
	out := machine.JournalBatch{Cursor: b.NextCursor, Coverage: convertCoverage(b.Coverage)}
	for _, ev := range b.Events {
		out.Events = append(out.Events, machine.Event{Key: ev.Cursor, Kind: "machine.journal.unit", Severity: string(ev.Severity), OccurredAt: ev.SourceTime, BootID: ev.BootID, Unit: ev.Unit, Historical: ev.Historical})
	}
	for _, gap := range b.Gaps {
		out.Events = append(out.Events, machine.Event{Key: "gap:" + cursor + ":" + string(gap.Reason), Kind: "machine.collector.gap", Severity: "warning", OccurredAt: gap.To, Historical: true, Reason: string(gap.Reason)})
	}
	if b.Coverage.Reason == observe.ReasonCursor {
		out.ResetCursor = true
	}
	return out, err
}

type machinePower struct {
	enabled bool
	client  power.Client
	runner  execx.Runner
	pending string
}

func (p machinePower) Preflight(ctx context.Context) error {
	if !p.enabled {
		return errors.New("machine power is disabled")
	}
	if _, err := os.Stat(p.pending); err == nil {
		return errors.New("self-update verification is pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := os.Lstat(power.WrapperPath)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 {
		return errors.New("power wrapper is not trusted")
	}
	res, err := p.runner.Run(ctx, execx.Cmd{Argv: []string{"/usr/bin/systemctl", "--system", "is-enabled", "lcm.service"}, Timeout: 5 * time.Second, MaxOutput: 1024})
	if err != nil || res.Truncated || strings.TrimSpace(res.Output) != "enabled" {
		return errors.New("controller autostart is not verified")
	}
	return nil
}

func controllerUpdatePending(context.Context) error {
	_, err := os.Lstat("/run/lcm-self-install.pending")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("controller update verification is pending")
}

func helperBinding(r machine.PowerRequest) power.Binding {
	b := power.Binding{ControllerID: r.ControllerID, OperationID: r.OperationID, Action: power.Action(r.Action), BootID: r.BootID, IssuedAt: r.IssuedAt.UnixMilli(), ExpiresAt: r.ValidUntil.UnixMilli()}
	b.RequestHash = power.Hash(b)
	return b
}

func (p machinePower) call(ctx context.Context, method power.Method, r machine.PowerRequest) (machine.Receipt, error) {
	res, err := p.client.Call(ctx, power.Request{Version: power.Version, Method: method, Binding: helperBinding(r)})
	if err != nil {
		if method == power.Query && res.Code == "receipt-missing" {
			return machine.Receipt{}, machine.ErrReceiptMissing
		}
		return machine.Receipt{}, err
	}
	if res.Receipt == nil {
		return machine.Receipt{}, errors.New("helper receipt missing")
	}
	receipt := res.Receipt
	state := string(receipt.State)
	if receipt.State == power.Accepted && !receipt.OSRequested {
		state = "prepared"
	}
	return machine.Receipt{State: state, AcceptedAt: time.UnixMilli(receipt.UpdatedAt).UTC(), BootID: receipt.Binding.BootID}, nil
}
func (p machinePower) Prepare(ctx context.Context, r machine.PowerRequest) (machine.Receipt, error) {
	if err := p.Preflight(ctx); err != nil {
		return machine.Receipt{}, err
	}
	return p.call(ctx, power.Prepare, r)
}
func (p machinePower) Submit(ctx context.Context, r machine.PowerRequest) (machine.Receipt, error) {
	if err := p.Preflight(ctx); err != nil {
		return machine.Receipt{}, err
	}
	return p.call(ctx, power.Submit, r)
}
func (p machinePower) Query(ctx context.Context, r machine.PowerRequest) (machine.Receipt, error) {
	return p.call(ctx, power.Query, r)
}

// lockController refuses a second serve process using the same state directory.
// The lock file is retained so an inode replacement cannot split ownership.
func lockController(dir string) (*os.File, error) {
	fd, err := syscall.Open(filepath.Join(dir, "controller.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "controller.lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another controller is using this data directory")
	}
	return f, nil
}
