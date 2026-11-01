package observe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"strings"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
)

// Linux reads only fixed proc/sys sources and explicit allowlists. It is safe to
// reuse concurrently when the injected System and Runner are concurrency safe.
type Linux struct {
	config Config
	system System
	runner execx.Runner
	clock  clock.Clock
}

// NewLinux validates all limits before performing any I/O. Nil system and clock
// select local OS reads and the real clock; a runner is required only for units.
func NewLinux(config Config, runner execx.Runner, system System, c clock.Clock) (*Linux, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if len(config.Units) > 0 && runner == nil {
		return nil, errors.New("unit observations require a runner")
	}
	if system == nil {
		system = localSystem{}
	}
	if c == nil {
		c = clock.Real()
	}
	return &Linux{config: config, system: system, runner: runner, clock: c}, nil
}

// Observe returns independent coverage for every attempted source. A nil error
// means polling completed, not that every source was available. Cancellation
// returns both the partial snapshot and ctx.Err(). No previous values are kept.
func (p *Linux) Observe(ctx context.Context) (Snapshot, error) {
	s := Snapshot{FieldVersion: FieldVersion, ObservedAt: p.clock.Now()}
	read := func(path string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := p.system.ReadFile(path, maxFile)
		if err == nil && len(b) > maxFile {
			return nil, errTruncated
		}
		return b, err
	}
	s.Identity = readSample(read, "/etc/machine-id", parseIdentity)
	s.BootID = readSample(read, "/proc/sys/kernel/random/boot_id", parseBootID)
	s.OS = readSample(func(path string) ([]byte, error) {
		b, err := read(path)
		if errors.Is(err, fs.ErrNotExist) {
			return read("/usr/lib/os-release")
		}
		return b, err
	}, "/etc/os-release", parseOS)
	s.Init = readSample(read, "/proc/1/comm", func(b []byte) (Init, error) {
		name := strings.TrimSpace(string(b))
		if len(name) == 0 || len(name) > 64 || !tokenPattern.MatchString(name) {
			return Init{}, errInvalid
		}
		return Init{Name: name, Systemd: name == "systemd"}, nil
	})
	s.Uptime = readSample(read, "/proc/uptime", parseUptime)
	s.CPU = readSample(read, "/proc/stat", parseCPU)
	s.Load = readSample(read, "/proc/loadavg", parseLoad)
	s.Memory = readSample(read, "/proc/meminfo", parseMemory)
	for _, path := range p.config.Filesystems {
		var value Capacity
		err := ctx.Err()
		if err == nil {
			var stat FSStats
			stat, err = p.system.StatFS(path)
			if err == nil {
				value, err = capacity(stat)
			}
		}
		s.Filesystems = append(s.Filesystems, Filesystem{Path: path, Sample: sample(value, err)})
	}
	for _, name := range p.config.Interfaces {
		v := readSample(read, "/sys/class/net/"+name+"/operstate", parseLink)
		s.Interfaces = append(s.Interfaces, Interface{Name: name, Sample: v})
	}
	if p.config.PSI {
		for _, resource := range []string{"cpu", "memory", "io"} {
			v := readSample(read, "/proc/pressure/"+resource, parsePSI)
			if v.Coverage.Reason == ReasonMissing {
				v.Coverage.State = Unsupported
			}
			s.Pressure = append(s.Pressure, Pressure{Resource: resource, Sample: v})
		}
	}
	for _, name := range p.config.Units {
		v := Sample[UnitState]{Coverage: Coverage{State: Unsupported, Reason: ReasonNotSystemd}}
		if s.Init.Coverage.State != Available {
			v.Coverage = s.Init.Coverage
		} else if s.Init.Value.Systemd {
			v = p.unit(ctx, name)
		}
		s.Units = append(s.Units, Unit{Name: name, Sample: v})
	}
	return s, ctx.Err()
}

func readSample[T any](read func(string) ([]byte, error), path string, parse func([]byte) (T, error)) Sample[T] {
	b, err := read(path)
	var v T
	if err == nil {
		v, err = parse(b)
	}
	return sample(v, err)
}

func (p *Linux) unit(ctx context.Context, name string) Sample[UnitState] {
	result, err := p.runner.Run(ctx, execx.Cmd{
		Argv: []string{"systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState", "--", name},
		Env:  append([]string(nil), commandEnv...), Timeout: p.config.CommandTimeout, MaxOutput: p.config.MaxOutput,
	})
	if result.Truncated || len(result.Output) > p.config.MaxOutput {
		return sample(UnitState{}, errTruncated)
	}
	if err != nil || result.ExitCode != 0 {
		c := commandFailure(result.Output, err)
		return Sample[UnitState]{Coverage: c}
	}
	v, err := parseUnit(result.Output)
	return sample(v, err)
}

func parseIdentity(b []byte) (Identity, error) {
	id := strings.TrimSpace(string(b))
	if !hexID(id, 32) || strings.Trim(id, "0") == "" {
		return Identity{}, errInvalid
	}
	h := sha256.Sum256([]byte("lcm.machine-id.v1\x00" + strings.ToLower(id)))
	return Identity{Hash: hex.EncodeToString(h[:]), Scheme: "sha256:machine-id:v1"}, nil
}

func parseUnit(s string) (UnitState, error) {
	values := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || values[k] != "" || len(v) > 64 || !tokenPattern.MatchString(v) {
			return UnitState{}, errInvalid
		}
		switch k {
		case "LoadState", "ActiveState", "SubState":
		default:
			return UnitState{}, errInvalid
		}
		values[k] = v
	}
	if len(values) != 3 {
		return UnitState{}, errInvalid
	}
	return UnitState{LoadState: values["LoadState"], ActiveState: values["ActiveState"], SubState: values["SubState"]}, nil
}
