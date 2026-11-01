package observe

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
)

const syntheticMachineID = "0123456789abcdef0123456789abcdef"

type fakeSystem struct {
	files     map[string]string
	errors    map[string]error
	stats     map[string]FSStats
	reads     []string
	statCalls []string
}

func fixtureSystem() *fakeSystem {
	return &fakeSystem{files: map[string]string{
		"/etc/machine-id":                 syntheticMachineID + "\n",
		"/proc/sys/kernel/random/boot_id": "11111111-2222-3333-4444-555555555555\n",
		"/etc/os-release":                 "ID=example\nVERSION_ID=\"1.2\"\nPRETTY_NAME=\"Example OS\"\n",
		"/proc/1/comm":                    "systemd\n",
		"/proc/uptime":                    "123.45 234.56\n",
		"/proc/stat":                      "cpu 100 2 30 400 5 6 7 8 9 1\ncpu0 50 1 15 200 2 3 4 4 5 0\ncpu1 50 1 15 200 3 3 3 4 4 1\nintr 1 2 3\n",
		"/proc/loadavg":                   "1.25 2.50 3.75 1/100 123\n",
		"/proc/meminfo":                   "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 100 kB\nSwapFree: 80 kB\nHugePages_Total: 0\n",
		"/sys/class/net/eth0/operstate":   "up\n",
		"/proc/pressure/cpu":              "some avg10=1.00 avg60=2.00 avg300=3.00 total=1234\n",
		"/proc/pressure/memory":           "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"/proc/pressure/io":               "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
	}, errors: map[string]error{}, stats: map[string]FSStats{
		"/data": {BlockSize: 4096, Blocks: 100, FreeBlocks: 40, AvailableBlocks: 30, Files: 1000, FreeFiles: 700},
	}}
}

func (f *fakeSystem) ReadFile(path string, limit int) ([]byte, error) {
	f.reads = append(f.reads, path)
	if err := f.errors[path]; err != nil {
		return nil, err
	}
	s, ok := f.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if len(s) > limit {
		return nil, errTruncated
	}
	return []byte(s), nil
}

func (f *fakeSystem) StatFS(path string) (FSStats, error) {
	f.statCalls = append(f.statCalls, path)
	if err := f.errors[path]; err != nil {
		return FSStats{}, err
	}
	s, ok := f.stats[path]
	if !ok {
		return FSStats{}, fs.ErrNotExist
	}
	return s, nil
}

func TestLinuxSnapshot(t *testing.T) {
	system := fixtureSystem()
	runner := (&execx.Fake{}).Reply("systemctl show", "LoadState=loaded\nActiveState=active\nSubState=running\n", 0)
	now := time.Unix(1793494803, 0)
	probe, err := NewLinux(Config{Filesystems: []string{"/data"}, Interfaces: []string{"eth0"}, Units: []string{"demo.service"}, PSI: true}, runner, system, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	var contract Probe = probe
	s, err := contract.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.FieldVersion != FieldVersion || !s.ObservedAt.Equal(now) {
		t.Fatalf("snapshot metadata: %+v", s)
	}
	if s.Identity.Coverage.State != Available || len(s.Identity.Value.Hash) != 64 || s.Identity.Value.Scheme != "sha256:machine-id:v1" {
		t.Fatalf("identity: %+v", s.Identity)
	}
	if s.BootID.Value != "11111111-2222-3333-4444-555555555555" || s.OS.Value.ID != "example" || s.OS.Value.VersionID != "1.2" || !s.Init.Value.Systemd || s.Uptime.Value != 123450*time.Millisecond {
		t.Fatalf("basic values: %+v", s)
	}
	if s.CPU.Value != (CPU{User: 100, Nice: 2, System: 30, Idle: 400, IOWait: 5, IRQ: 6, SoftIRQ: 7, Steal: 8, Guest: 9, GuestNice: 1, Count: 2}) {
		t.Fatalf("CPU: %+v", s.CPU)
	}
	if s.Load.Value != (Load{One: 1.25, Five: 2.5, Fifteen: 3.75}) || s.Memory.Value.AvailableBytes != 500*1024 || s.Memory.Value.SwapFreeBytes != 80*1024 {
		t.Fatalf("resources: %+v %+v", s.Load, s.Memory)
	}
	if len(s.Filesystems) != 1 || s.Filesystems[0].Value != (Capacity{TotalBytes: 409600, FreeBytes: 163840, AvailableBytes: 122880, Inodes: 1000, FreeInodes: 700}) {
		t.Fatalf("filesystems: %+v", s.Filesystems)
	}
	if len(s.Interfaces) != 1 || s.Interfaces[0].Value.OperState != "up" || len(s.Pressure) != 3 || s.Pressure[0].Value.Some.TotalMicros != 1234 || s.Pressure[1].Value.Full == nil {
		t.Fatalf("link/PSI: %+v %+v", s.Interfaces, s.Pressure)
	}
	if len(s.Units) != 1 || s.Units[0].Value.ActiveState != "active" {
		t.Fatalf("units: %+v", s.Units)
	}
	calls := runner.Calls()
	wantArgv := []string{"systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState", "--", "demo.service"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, wantArgv) || calls[0].Timeout <= 0 || calls[0].MaxOutput <= 0 || calls[0].Shell != "" || !slices.Contains(calls[0].Env, "LC_ALL=C") {
		t.Fatalf("unbounded or unexpected command: %+v", calls)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), syntheticMachineID) {
		t.Fatal("raw identity exposed")
	}
	next, err := probe.Observe(context.Background())
	if err != nil || next.CPU.Value != s.CPU.Value {
		t.Fatalf("probe must retain cumulative counters: %+v %v", next.CPU, err)
	}
	if !reflect.DeepEqual(system.statCalls, []string{"/data", "/data"}) {
		t.Fatalf("unexpected filesystem scan: %v", system.statCalls)
	}
	for _, path := range system.reads {
		if strings.Contains(path, "cmdline") || strings.Contains(path, "environ") || strings.Contains(path, "journal") {
			t.Fatalf("sensitive source read: %s", path)
		}
	}
}

func TestLinuxIndependentCoverageAndNoStaleValues(t *testing.T) {
	system := fixtureSystem()
	probe, err := NewLinux(Config{Filesystems: []string{"/data"}, Interfaces: []string{"eth0"}, PSI: true}, nil, system, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	system.errors["/etc/machine-id"] = fs.ErrPermission
	system.errors["/proc/sys/kernel/random/boot_id"] = fs.ErrNotExist
	system.errors["/data"] = fs.ErrPermission
	system.errors["/sys/class/net/eth0/operstate"] = errors.New("synthetic private error")
	system.files["/proc/stat"] = "cpu bad\n"
	system.files["/proc/meminfo"] = "MemTotal: 1 kB\n"
	delete(system.files, "/proc/pressure/cpu")
	system.files["/proc/pressure/io"] = "some avg10=NaN avg60=0 avg300=0 total=0"
	s, err := probe.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Identity.Coverage.Reason != ReasonPermission || s.Identity.Value.Hash != "" || s.BootID.Coverage.Reason != ReasonMissing || s.CPU.Coverage.Reason != ReasonInvalid || s.CPU.Value.Count != 0 || s.Memory.Coverage.Reason != ReasonInvalid {
		t.Fatalf("source errors: %+v", s)
	}
	if s.Filesystems[0].Coverage.Reason != ReasonPermission || s.Interfaces[0].Coverage.Reason != ReasonRead || s.Pressure[0].Coverage.State != Unsupported || s.Pressure[2].Coverage.Reason != ReasonInvalid || s.Pressure[1].Coverage.State != Available || s.Load.Coverage.State != Available {
		t.Fatalf("independent coverage: %+v", s)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatal("raw error leaked")
	}
}

func TestLinuxAllowlistValidation(t *testing.T) {
	cases := map[string]Config{
		"relative":         {Filesystems: []string{"data"}},
		"unclean":          {Filesystems: []string{"/data/../etc"}},
		"duplicate":        {Filesystems: []string{"/data", "/data"}},
		"many paths":       {Filesystems: make([]string, maxPaths+1)},
		"iface traversal":  {Interfaces: []string{"../eth0"}},
		"iface dot":        {Interfaces: []string{".."}},
		"iface long":       {Interfaces: []string{strings.Repeat("a", 16)}},
		"many interfaces":  {Interfaces: make([]string, maxInterfaces+1)},
		"unit glob":        {Units: []string{"*.service"}},
		"unit option":      {Units: []string{"--all.service"}},
		"unit newline":     {Units: []string{"demo.service\n"}},
		"unit duplicate":   {Units: []string{"a.service", "a.service"}},
		"many units":       {Units: make([]string, maxUnits+1)},
		"timeout":          {CommandTimeout: 31 * time.Second},
		"negative timeout": {CommandTimeout: -time.Second},
		"output small":     {MaxOutput: 1},
		"output large":     {MaxOutput: maxOutput + 1},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			system := fixtureSystem()
			if _, err := NewLinux(config, &execx.Fake{}, system, nil); err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if len(system.reads) != 0 || len(system.statCalls) != 0 {
				t.Fatal("configuration caused I/O")
			}
		})
	}
	if _, err := NewLinux(Config{Units: []string{"demo.service"}}, nil, fixtureSystem(), nil); err == nil {
		t.Fatal("missing runner accepted")
	}
}

func TestLinuxConfigurationIsCopiedAndEmptyListsDoNotScan(t *testing.T) {
	system := fixtureSystem()
	config := Config{Interfaces: []string{"eth0"}}
	probe, err := NewLinux(config, nil, system, nil)
	if err != nil {
		t.Fatal(err)
	}
	config.Interfaces[0] = "eth1"
	s, err := probe.Observe(context.Background())
	if err != nil || len(s.Interfaces) != 1 || s.Interfaces[0].Name != "eth0" {
		t.Fatalf("configuration mutated: %+v %v", s.Interfaces, err)
	}
	if len(s.Filesystems) != 0 || len(s.Units) != 0 || len(s.Pressure) != 0 || len(system.statCalls) != 0 {
		t.Fatal("empty sources must not be scanned")
	}
}

func TestLinuxInitAndUnitFailures(t *testing.T) {
	for _, tc := range []struct {
		name, init, output string
		err                error
		truncated          bool
		want               CoverageState
		reason             Reason
	}{
		{name: "non systemd", init: "init", want: Unsupported, reason: ReasonNotSystemd},
		{name: "init unreadable", err: fs.ErrPermission, want: Unavailable, reason: ReasonPermission},
		{name: "unit denied", init: "systemd", output: "Access denied synthetic private error", err: &execx.ExitError{Code: 1}, want: Unavailable, reason: ReasonPermission},
		{name: "timeout", init: "systemd", err: execx.ErrTimeout, want: Unavailable, reason: ReasonTimeout},
		{name: "truncated", init: "systemd", truncated: true, want: Unavailable, reason: ReasonTruncated},
		{name: "invalid output", init: "systemd", output: "LoadState=loaded\nActiveState=active", want: Unavailable, reason: ReasonInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			system := fixtureSystem()
			system.files["/proc/1/comm"] = tc.init
			if tc.init == "" {
				system.errors["/proc/1/comm"] = tc.err
			}
			runner := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
				return execx.Result{Output: tc.output, Truncated: tc.truncated}, tc.err
			})
			p, err := NewLinux(Config{Units: []string{"demo.service"}}, runner, system, nil)
			if err != nil {
				t.Fatal(err)
			}
			s, err := p.Observe(context.Background())
			if err != nil || s.Units[0].Coverage.State != tc.want || s.Units[0].Coverage.Reason != tc.reason {
				t.Fatalf("unit: %+v %v", s.Units, err)
			}
			if tc.init != "systemd" && len(runner.Calls()) != 0 {
				t.Fatal("non-systemd invoked systemctl")
			}
		})
	}
}

func TestLinuxCancelledDoesNotRead(t *testing.T) {
	system := fixtureSystem()
	p, err := NewLinux(Config{}, nil, system, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := p.Observe(ctx)
	if !errors.Is(err, context.Canceled) || s.CPU.Coverage.Reason != ReasonCancelled || len(system.reads) != 0 {
		t.Fatalf("cancelled observation: %+v %v", s, err)
	}
}

func TestLinuxParsersRejectInvalidData(t *testing.T) {
	for name, run := range map[string]func() error{
		"identity zero":           func() error { _, err := parseIdentity([]byte(strings.Repeat("0", 32))); return err },
		"identity uninitialized":  func() error { _, err := parseIdentity([]byte("uninitialized")); return err },
		"boot malformed":          func() error { _, err := parseBootID([]byte("111111112-222-3333-4444-555555555555")); return err },
		"uptime overflow":         func() error { _, err := parseUptime([]byte("9999999999999999 0")); return err },
		"uptime infinity":         func() error { _, err := parseUptime([]byte("Inf 0")); return err },
		"load NaN":                func() error { _, err := parseLoad([]byte("NaN 1 2")); return err },
		"cpu negative":            func() error { _, err := parseCPU([]byte("cpu -1 0 0 0\ncpu0 0 0 0 0")); return err },
		"cpu missing":             func() error { _, err := parseCPU([]byte("cpu0 0 0 0 0")); return err },
		"cpu duplicate":           func() error { _, err := parseCPU([]byte("cpu 1 0 0 0\ncpu 1 0 0 0\ncpu0 1 0 0 0")); return err },
		"memory overflow":         func() error { _, err := parseMemory([]byte("MemTotal: 18446744073709551615 kB")); return err },
		"capacity overflow":       func() error { _, err := capacity(FSStats{BlockSize: 4096, Blocks: math.MaxUint64}); return err },
		"capacity invalid free":   func() error { _, err := capacity(FSStats{BlockSize: 1, Blocks: 1, FreeBlocks: 2}); return err },
		"capacity invalid inodes": func() error { _, err := capacity(FSStats{BlockSize: 1, Files: 1, FreeFiles: 2}); return err },
		"link invalid":            func() error { _, err := parseLink([]byte("up down")); return err },
		"PSI duplicate": func() error {
			_, err := parsePSI([]byte("some avg10=0 avg60=0 avg300=0 total=0\nsome avg10=0 avg60=0 avg300=0 total=0"))
			return err
		},
		"PSI percentage": func() error { _, err := parsePSI([]byte("some avg10=101 avg60=0 avg300=0 total=0")); return err },
		"unit duplicate": func() error {
			_, err := parseUnit("LoadState=loaded\nLoadState=loaded\nActiveState=active\nSubState=running")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("malformed data accepted")
			}
		})
	}
}

func TestLocalSystemBoundedReads(t *testing.T) {
	path := t.TempDir() + "/fixture"
	if err := os.WriteFile(path, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	system := localSystem{}
	if _, err := system.ReadFile(path, 4); !errors.Is(err, errTruncated) {
		t.Fatalf("want bounded read, got %v", err)
	}
	b, err := system.ReadFile(path, 5)
	if err != nil || string(b) != "12345" {
		t.Fatalf("exact read: %q %v", b, err)
	}
}
