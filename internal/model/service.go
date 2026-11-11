package model

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/ingress"
)

// RuntimeType selects how LCM controls a service's process. The adapter of a
// service resource is one of these values.
type RuntimeType string

// Runtime types.
const (
	RuntimeSystemd  RuntimeType = "systemd"
	RuntimeDocker   RuntimeType = "docker"
	RuntimeCompose  RuntimeType = "compose"
	RuntimeCommand  RuntimeType = "command"
	RuntimeExternal RuntimeType = "external"
	// RuntimeManaged runs a service that the deploy helper builds, installs
	// and runs as its own account from releases under /opt/<name>.
	RuntimeManaged RuntimeType = "managed"
)

// Systemd scopes.
const (
	ScopeSystem = "system"
	ScopeUser   = "user"
)

// Defaults applied by the accessors below when a field is left empty.
const (
	DefaultSourceTimeout  = Duration(15 * time.Minute)
	DefaultCommandTimeout = Duration(30 * time.Minute)
)

// ServiceConfig is the config of a service resource: the runtime fields of
// its adapter, where its source comes from, how to back it up and which jobs
// it offers. Only the runtime fields of the chosen adapter may be set.
type ServiceConfig struct {
	// systemd
	Unit  string `yaml:"unit,omitempty" json:"unit,omitempty"`
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
	// docker
	Container string `yaml:"container,omitempty" json:"container,omitempty"`
	// compose
	Project string `yaml:"project,omitempty" json:"project,omitempty"`
	File    string `yaml:"file,omitempty" json:"file,omitempty"`
	// command: shell commands per action; each is optional and an action
	// without one is not offered. managed: start is the release-relative
	// file the service runs, see below; a managed service without start
	// only publishes static content.
	Start   string `yaml:"start,omitempty" json:"start,omitempty"`
	Stop    string `yaml:"stop,omitempty" json:"stop,omitempty"`
	Restart string `yaml:"restart,omitempty" json:"restart,omitempty"`
	Status  string `yaml:"status,omitempty" json:"status,omitempty"`
	// managed: the build output directory installed as a release, the
	// arguments of start, the runtime named in the host configuration, an
	// optional migration run before the switch, and how many releases the
	// helper keeps. Documents carry no privileges: the host configuration
	// decides which runtimes, ports and inputs a service may use.
	Artifact     string   `yaml:"artifact,omitempty" json:"artifact,omitempty"`
	StartArgs    []string `yaml:"startArgs,omitempty" json:"startArgs,omitempty"`
	RuntimeName  string   `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Migrate      []string `yaml:"migrate,omitempty" json:"migrate,omitempty"`
	KeepReleases int      `yaml:"keepReleases,omitempty" json:"keepReleases,omitempty"`
	// managed: the static content of a release that the helper publishes
	// to the service's fixed publish directory, and the limits of its unit.
	Publish *Publish    `yaml:"publish,omitempty" json:"publish,omitempty"`
	Limits  *UnitLimits `yaml:"limits,omitempty" json:"limits,omitempty"`
	// managed: the processes the service runs from one release, by name.
	// Each instance runs as its own unit <name>@<instance>.service; start,
	// startArgs and limits above are the defaults of every instance. Ports
	// and other privileges of an instance come from the host configuration.
	Instances map[string]InstanceConfig `yaml:"instances,omitempty" json:"instances,omitempty"`

	Source *Source            `yaml:"source,omitempty" json:"source,omitempty"`
	Backup *Backup            `yaml:"backup,omitempty" json:"backup,omitempty"`
	Jobs   map[string]Command `yaml:"jobs,omitempty" json:"jobs,omitempty"`
	// Route is how the service is presented by the host's nginx site. It is
	// valid for every adapter and carries no privileges: the prefix, port
	// and static directory of the route come from the host configuration.
	Route *ingress.Options `yaml:"route,omitempty" json:"route,omitempty"`
}

func (*ServiceConfig) resourceConfig() {}

// Runtime returns the runtime value the adapters consume for the given
// adapter name. Service, which only the managed runtime uses, is the
// resource name and is left for the caller to set.
func (c *ServiceConfig) Runtime(adapter string) Runtime {
	return Runtime{
		Type: RuntimeType(adapter),
		Unit: c.Unit, Scope: c.Scope,
		Container: c.Container,
		Project:   c.Project, File: c.File,
		Start: c.Start, Stop: c.Stop, Restart: c.Restart, Status: c.Status,
		Instances: c.InstanceNames(),
	}
}

// InstanceConfig overrides the process settings of one instance of a
// managed service. start replaces the service's start; startArgs, when
// present (even empty), replaces the service's startArgs as a whole; limits
// are merged field by field over the service's limits.
type InstanceConfig struct {
	Start     string      `yaml:"start,omitempty" json:"start,omitempty"`
	StartArgs *[]string   `yaml:"startArgs,omitempty" json:"startArgs,omitempty"`
	Limits    *UnitLimits `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// ResolvedInstance is one instance with the service defaults applied.
type ResolvedInstance struct {
	Name      string
	Start     string
	StartArgs []string
	// Limits is nil when neither the service nor the instance sets one.
	Limits *UnitLimits
}

// ResolvedInstances returns the instances sorted by name with the service
// defaults applied, or nil when the service has none.
func (c *ServiceConfig) ResolvedInstances() []ResolvedInstance {
	if len(c.Instances) == 0 {
		return nil
	}
	out := make([]ResolvedInstance, 0, len(c.Instances))
	for _, name := range sortedKeys(c.Instances) {
		in := c.Instances[name]
		r := ResolvedInstance{Name: name, Start: c.Start, StartArgs: c.StartArgs, Limits: MergeLimits(c.Limits, in.Limits)}
		if in.Start != "" {
			r.Start = in.Start
		}
		if in.StartArgs != nil {
			r.StartArgs = *in.StartArgs
		}
		out = append(out, r)
	}
	return out
}

// InstanceNames returns the sorted instance names, or nil without
// instances.
func (c *ServiceConfig) InstanceNames() []string {
	if len(c.Instances) == 0 {
		return nil
	}
	return sortedKeys(c.Instances)
}

// MergeLimits returns the fields of over set on top of base, or nil when
// both are nil.
func MergeLimits(base, over *UnitLimits) *UnitLimits {
	if base == nil && over == nil {
		return nil
	}
	var out UnitLimits
	if base != nil {
		out = *base
	}
	if over != nil {
		if over.MemoryHigh != "" {
			out.MemoryHigh = over.MemoryHigh
		}
		if over.MemoryMax != "" {
			out.MemoryMax = over.MemoryMax
		}
		if over.StopTimeout != 0 {
			out.StopTimeout = over.StopTimeout
		}
	}
	return &out
}

// Runtime configures a runtime adapter. Only the fields of the chosen type
// may be set.
type Runtime struct {
	Type RuntimeType `yaml:"type" json:"type"`
	// systemd
	Unit  string `yaml:"unit,omitempty" json:"unit,omitempty"`
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
	// docker
	Container string `yaml:"container,omitempty" json:"container,omitempty"`
	// compose
	Project string `yaml:"project,omitempty" json:"project,omitempty"`
	File    string `yaml:"file,omitempty" json:"file,omitempty"`
	// command
	Start   string `yaml:"start,omitempty" json:"start,omitempty"`
	Stop    string `yaml:"stop,omitempty" json:"stop,omitempty"`
	Restart string `yaml:"restart,omitempty" json:"restart,omitempty"`
	Status  string `yaml:"status,omitempty" json:"status,omitempty"`
	// managed: the service name the deploy helper knows, which is the
	// resource name
	Service string `yaml:"service,omitempty" json:"service,omitempty"`
	// managed: the sorted instance names; empty for a service that runs a
	// single unit.
	Instances []string `yaml:"instances,omitempty" json:"instances,omitempty"`
}

// RunsProcess reports whether a managed runtime runs a process: it has a
// start or instances. A managed service without either only publishes
// static content.
func (r Runtime) RunsProcess() bool { return r.Start != "" || len(r.Instances) > 0 }

// EffectiveScope returns the systemd scope, defaulting to system.
func (r Runtime) EffectiveScope() string {
	if r.Scope == "" {
		return ScopeSystem
	}
	return r.Scope
}

// Source says where new versions of a service come from and how to build
// and install them. URL is a remote repository or an absolute path to a
// local development repository. LCM clones it into a build checkout it owns
// (one per resource in the work directory of the data directory) and
// deploys from there; deploys detach that checkout's HEAD, which is why a
// document never names it.
type Source struct {
	URL     string   `yaml:"url" json:"url"`
	Branch  string   `yaml:"branch" json:"branch"`
	Build   string   `yaml:"build,omitempty" json:"build,omitempty"`
	Install string   `yaml:"install,omitempty" json:"install,omitempty"`
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	// Repo and Remote are the fields URL replaced in storage schema 15.
	// They are decoded only so that a document still using them is refused
	// with an explanation rather than an unknown-field error.
	Repo   string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Remote string `yaml:"remote,omitempty" json:"remote,omitempty"`
}

// EffectiveTimeout returns the timeout of each build step.
func (s Source) EffectiveTimeout() time.Duration {
	if s.Timeout == 0 {
		return DefaultSourceTimeout.D()
	}
	return s.Timeout.D()
}

// Publish names the static content of a managed release. The deploy helper
// copies it to the service's publish directory, which the host
// configuration fixes; the document only chooses what of the release goes
// there and which files outlive the release that brought them.
type Publish struct {
	// Dir is the release-relative directory to publish; "." is the whole
	// release.
	Dir string `yaml:"dir" json:"dir"`
	// Retain are directories below Dir, each ending with "/", whose files
	// stay RetainDays after the last release that contained them, so that
	// pages loaded before a deployment still find their assets.
	Retain     []string `yaml:"retain,omitempty" json:"retain,omitempty"`
	RetainDays int      `yaml:"retainDays,omitempty" json:"retainDays,omitempty"`
}

// EffectiveRetainDays returns retainDays, defaulting to
// DefaultRetainDays.
func (p Publish) EffectiveRetainDays() int {
	if p.RetainDays == 0 {
		return DefaultRetainDays
	}
	return p.RetainDays
}

// UnitLimits tighten the unit of a managed service: its memory and how
// long systemd waits for it to stop. Sizes are a number with K, M or G
// (powers of 1024).
type UnitLimits struct {
	MemoryHigh  string   `yaml:"memoryHigh,omitempty" json:"memoryHigh,omitempty"`
	MemoryMax   string   `yaml:"memoryMax,omitempty" json:"memoryMax,omitempty"`
	StopTimeout Duration `yaml:"stopTimeout,omitempty" json:"stopTimeout,omitempty"`
}

// Command is an operator-defined shell command (run with sh -c).
type Command struct {
	Command string   `yaml:"command" json:"command"`
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// EffectiveTimeout returns the timeout, defaulting to DefaultCommandTimeout.
func (c Command) EffectiveTimeout() time.Duration {
	if c.Timeout == 0 {
		return DefaultCommandTimeout.D()
	}
	return c.Timeout.D()
}

// Backup says how a service is backed up: either by a shell command (every
// adapter but managed) or by typed sources that the deploy helper backs up
// and restores (managed only). The two forms exclude each other.
type Backup struct {
	// Command and Timeout are the command form.
	Command string   `yaml:"command,omitempty" json:"command,omitempty"`
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	// Sources, Mode and Keep are the typed form. Timeout also bounds the
	// producing process of a typed backup.
	Sources []BackupSource `yaml:"sources,omitempty" json:"sources,omitempty"`
	Mode    string         `yaml:"mode,omitempty" json:"mode,omitempty"`
	Keep    *BackupKeep    `yaml:"keep,omitempty" json:"keep,omitempty"`
}

// Backup source types and modes of the typed form.
const (
	BackupFiles    = "files"
	BackupSQLite   = "sqlite"
	BackupPostgres = "postgres"
	BackupMongoDB  = "mongodb"
	BackupCommand  = "command"

	BackupOnline  = "online"
	BackupStopped = "stopped"
)

// BackupSource is one thing to back up. Paths are relative to the service's
// data directory (/var/lib/<name>); argv names a program of the current
// release. Each type takes only its own fields.
type BackupSource struct {
	Type     string `yaml:"type" json:"type"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Database string `yaml:"database,omitempty" json:"database,omitempty"`
	// All backs up every database of a mongodb source instead of Database.
	All   bool     `yaml:"all,omitempty" json:"all,omitempty"`
	Users bool     `yaml:"users,omitempty" json:"users,omitempty"`
	Argv  []string `yaml:"argv,omitempty" json:"argv,omitempty"`
}

// BackupKeep is the retention of a typed backup: the newest set of each of
// the last Daily days and Weekly weeks that have one.
type BackupKeep struct {
	Daily  int `yaml:"daily,omitempty" json:"daily,omitempty"`
	Weekly int `yaml:"weekly,omitempty" json:"weekly,omitempty"`
}

// Defaults and bounds of the typed form.
const (
	DefaultKeepDaily  = 7
	DefaultKeepWeekly = 4
	MaxKeepDaily      = 366
	MaxKeepWeekly     = 260
	MaxBackupSources  = 16
	// MaxBackupTimeout bounds timeout of a typed backup.
	MaxBackupTimeout = Duration(2 * time.Hour)
)

// Typed reports whether the backup uses sources instead of a command.
func (b Backup) Typed() bool { return len(b.Sources) > 0 }

// EffectiveTimeout returns the timeout, defaulting to DefaultCommandTimeout.
func (b Backup) EffectiveTimeout() time.Duration {
	if b.Timeout == 0 {
		return DefaultCommandTimeout.D()
	}
	return b.Timeout.D()
}

// EffectiveMode returns the mode of a typed backup, defaulting to online.
func (b Backup) EffectiveMode() string {
	if b.Mode == "" {
		return BackupOnline
	}
	return b.Mode
}

// EffectiveKeep returns the retention of a typed backup; without keep it is
// seven days and four weeks.
func (b Backup) EffectiveKeep() BackupKeep {
	if b.Keep == nil || (b.Keep.Daily == 0 && b.Keep.Weekly == 0) {
		return BackupKeep{Daily: DefaultKeepDaily, Weekly: DefaultKeepWeekly}
	}
	return *b.Keep
}

// Restorable reports whether a typed backup has a source that a restore
// puts back: command output has no restore.
func (b Backup) Restorable() bool {
	for _, s := range b.Sources {
		if s.Type != BackupCommand {
			return true
		}
	}
	return false
}

// Limits on service configs.
const (
	MaxJobs    = 50
	maxCmdLen  = 16 << 10
	maxPathLen = 4096
)

var (
	// Unit names are limited to what systemd accepts without escaping.
	// The strict pattern also keeps a name from looking like an option
	// when it is passed as an argument.
	unitRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]*\.(service|socket|timer|target)$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	projectRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	// scpLikeRe matches git's [user@]host:path address form.
	scpLikeRe = regexp.MustCompile(`^([A-Za-z0-9._-]+@)?[A-Za-z0-9][A-Za-z0-9.-]*:`)
	branchRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	jobRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

func (c *ServiceConfig) normalize(adapter string) {
	if RuntimeType(adapter) == RuntimeSystemd && c.Scope == "" {
		c.Scope = ScopeSystem
	}
	// "dist/" names the same directory as "dist".
	if RuntimeType(adapter) == RuntimeManaged && len(c.Artifact) > 1 {
		c.Artifact = strings.TrimSuffix(c.Artifact, "/")
	}
	if RuntimeType(adapter) == RuntimeManaged && c.Publish != nil && len(c.Publish.Dir) > 1 {
		c.Publish.Dir = strings.TrimSuffix(c.Publish.Dir, "/")
	}
	// The same for the paths of file sources.
	if c.Backup != nil {
		for i, src := range c.Backup.Sources {
			if (src.Type == BackupFiles || src.Type == BackupSQLite) && len(src.Path) > 1 {
				c.Backup.Sources[i].Path = strings.TrimSuffix(src.Path, "/")
			}
		}
	}
}

func (c *ServiceConfig) validate(adapter string, base string, is *Issues) {
	validateRuntime(c.Runtime(adapter), base, is)
	validateManaged(c, RuntimeType(adapter), base, is)
	if c.Source != nil {
		validateSource(c.Source, base+".source", is)
	}
	if c.Backup != nil {
		validateBackup(c.Backup, RuntimeType(adapter), base+".backup", is)
	}
	if c.Route != nil {
		for _, i := range c.Route.Validate() {
			at := base + ".route"
			if i.Path != "" {
				at += "." + i.Path
			}
			is.addf(at, "%s", i.Message)
		}
	}
	if len(c.Jobs) > MaxJobs {
		is.addf(base+".jobs", "at most %d jobs are allowed", MaxJobs)
	}
	for _, name := range sortedKeys(c.Jobs) {
		p := base + ".jobs." + name
		if !jobRe.MatchString(name) || len(name) > maxNameLen {
			is.addf(p, "job names are lowercase letters, digits, '-' and '_', starting with a letter (max %d)", maxNameLen)
		}
		validateCommand(c.Jobs[name], p, is)
	}
}

// validateRuntime checks the runtime fields of a known adapter; base is the
// path holding them. The adapter name itself is checked by the caller.
func validateRuntime(r Runtime, base string, is *Issues) {
	// forbid reports every field set that the chosen adapter does not use.
	forbid := func(fields map[string]string) {
		for _, name := range sortedKeys(fields) {
			if fields[name] != "" {
				is.addf(base+"."+name, "is not valid for adapter %s", r.Type)
			}
		}
	}
	systemd := map[string]string{"unit": r.Unit, "scope": r.Scope}
	docker := map[string]string{"container": r.Container}
	compose := map[string]string{"project": r.Project, "file": r.File}
	command := map[string]string{"start": r.Start, "stop": r.Stop, "restart": r.Restart, "status": r.Status}

	switch r.Type {
	case RuntimeSystemd:
		forbid(merge(docker, compose, command))
		switch {
		case r.Unit == "":
			is.addf(base+".unit", "is required")
		case len(r.Unit) > 255 || !unitRe.MatchString(r.Unit):
			is.addf(base+".unit", "must be a unit name such as app.service")
		}
		if r.Scope != "" && r.Scope != ScopeSystem && r.Scope != ScopeUser {
			is.addf(base+".scope", "must be system or user")
		}
	case RuntimeDocker:
		forbid(merge(systemd, compose, command))
		switch {
		case r.Container == "":
			is.addf(base+".container", "is required")
		case len(r.Container) > 255 || !containerRe.MatchString(r.Container):
			is.addf(base+".container", "invalid container name")
		}
	case RuntimeCompose:
		forbid(merge(systemd, docker, command))
		switch {
		case r.Project == "":
			is.addf(base+".project", "is required")
		case len(r.Project) > maxNameLen || !projectRe.MatchString(r.Project):
			is.addf(base+".project", "must be lowercase letters, digits, '-' and '_'")
		}
		switch {
		case r.File == "":
			is.addf(base+".file", "is required")
		case !validAbsPath(r.File):
			is.addf(base+".file", "must be a clean absolute path")
		}
	case RuntimeCommand:
		// Every command is optional: a missing one makes the action
		// unsupported instead of pretending to perform it.
		forbid(merge(systemd, docker, compose))
		for _, name := range sortedKeys(command) {
			if len(command[name]) > maxCmdLen {
				is.addf(base+"."+name, "exceeds %d bytes", maxCmdLen)
			}
		}
	case RuntimeExternal:
		forbid(merge(systemd, docker, compose, command))
	case RuntimeManaged:
		// start names the file the service runs; the helper's unit does
		// the rest, so the other commands have no meaning.
		forbid(merge(systemd, docker, compose, map[string]string{"stop": r.Stop, "restart": r.Restart, "status": r.Status}))
		// Without start the service only publishes static content, which
		// validateManaged checks.
		if r.Start != "" && (!releasePath(r.Start) || r.Start == ".") {
			is.addf(base+".start", "must be a release-relative file: %s", releasePathRule)
		}
	}
}

// Limits of a managed service. They match the deploy helper's protocol
// (internal/deployhelper), which checks every request again.
const (
	// MaxManagedName bounds the name of a managed service, which becomes
	// its account, unit and directories.
	MaxManagedName = 27
	// DefaultKeepReleases is the number of releases kept when keepReleases
	// is not set.
	DefaultKeepReleases = 3
	MinKeepReleases     = 2
	MaxKeepReleases     = 20
	// MaxManagedBuildTimeout bounds source.timeout, the build's limit.
	MaxManagedBuildTimeout = Duration(2 * time.Hour)
	maxManagedArgs         = 32
	releasePathRule        = `letters, digits and ._-/ without ".." components, not absolute`

	// DefaultRetainDays, MaxRetainDays and MaxRetain bound publish.
	DefaultRetainDays = 14
	MaxRetainDays     = 90
	MaxRetain         = 16
	// MinUnitMemory and MaxUnitMemory bound memoryHigh and memoryMax;
	// MinStopTimeout and MaxStopTimeout bound stopTimeout.
	MinUnitMemory  = 1 << 20
	MaxUnitMemory  = 16 << 30
	MinStopTimeout = Duration(time.Second)
	MaxStopTimeout = Duration(10 * time.Minute)
)

var (
	releasePathRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,256}$`)
	managedArgRe  = regexp.MustCompile(`^[A-Za-z0-9._/=:,@+-]{1,256}$`)
	programNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	runtimeNameRe = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	unitSizeRe    = regexp.MustCompile(`^([1-9][0-9]{0,8})([KMG])$`)
)

// UnitSize returns the bytes of a unit memory size such as 768M, or false
// when s is not one.
func UnitSize(s string) (int64, bool) {
	m := unitSizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	var n int64
	for _, c := range m[1] {
		n = n*10 + int64(c-'0')
	}
	shift := map[string]uint{"K": 10, "M": 20, "G": 30}[m[2]]
	return n << shift, true
}

// releasePath accepts a clean relative path without ".." components.
func releasePath(p string) bool {
	if !releasePathRe.MatchString(p) || path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	return !slices.Contains(strings.Split(p, "/"), "..")
}

// EffectiveKeepReleases returns keepReleases, defaulting to
// DefaultKeepReleases.
func (c *ServiceConfig) EffectiveKeepReleases() int {
	if c.KeepReleases == 0 {
		return DefaultKeepReleases
	}
	return c.KeepReleases
}

// validateManaged checks the fields only the managed runtime takes. Whether
// the host grants the runtime, and everything else a service may use, is
// the deploy helper's to check when the service is deployed.
func validateManaged(c *ServiceConfig, adapter RuntimeType, base string, is *Issues) {
	if adapter != RuntimeManaged {
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"artifact", c.Artifact != ""}, {"keepReleases", c.KeepReleases != 0}, {"migrate", c.Migrate != nil},
			{"runtime", c.RuntimeName != ""}, {"startArgs", c.StartArgs != nil},
			{"publish", c.Publish != nil}, {"limits", c.Limits != nil}, {"instances", c.Instances != nil},
		} {
			if f.set {
				is.addf(base+"."+f.name, "is not valid for adapter %s", adapter)
			}
		}
		return
	}
	switch {
	case c.Artifact == "":
		is.addf(base+".artifact", "is required")
	case !releasePath(c.Artifact):
		is.addf(base+".artifact", `must be a directory of the build tree: %s ("." is the whole tree)`, releasePathRule)
	}
	validateArgs(c.StartArgs, base+".startArgs", is)
	if c.RuntimeName != "" && !runtimeNameRe.MatchString(c.RuntimeName) {
		is.addf(base+".runtime", "must be a runtime name of the host configuration: lowercase letters and digits, starting with a letter (max 16)")
	}
	if c.Migrate != nil {
		switch {
		case len(c.Migrate) == 0:
			is.addf(base+".migrate", "must name a program; omit it to run no migration")
		case !programNameRe.MatchString(c.Migrate[0]) && (!releasePath(c.Migrate[0]) || c.Migrate[0] == "."):
			is.addf(base+".migrate", "must start with a release-relative executable or the name of a program of the runtime")
		default:
			validateArgs(c.Migrate[1:], base+".migrate", is)
		}
	}
	if c.KeepReleases != 0 && (c.KeepReleases < MinKeepReleases || c.KeepReleases > MaxKeepReleases) {
		is.addf(base+".keepReleases", "must be between %d and %d", MinKeepReleases, MaxKeepReleases)
	}
	if c.Instances != nil {
		validateInstances(c, base+".instances", is)
	}
	if c.Start == "" && len(c.Instances) == 0 {
		// A service without a process: the release only brings static
		// content, and nothing that configures a process applies.
		if c.Publish == nil {
			is.addf(base+".start", "is required unless publish is set: a managed service without start only publishes static content")
		}
		for _, f := range []struct {
			name string
			set  bool
		}{{"limits", c.Limits != nil}, {"migrate", c.Migrate != nil}, {"startArgs", c.StartArgs != nil}} {
			if f.set {
				is.addf(base+"."+f.name, "needs start: a managed service without start runs no process")
			}
		}
	}
	if c.Publish != nil {
		validatePublish(c.Publish, base+".publish", is)
	}
	if c.Limits != nil {
		validateLimits(c.Limits, base+".limits", is)
	}
	src := c.Source
	if src == nil {
		is.addf(base+".source", "is required for adapter managed: it is what the service is built from")
		return
	}
	switch {
	case strings.TrimSpace(src.Build) == "":
		is.addf(base+".source.build", "is required for adapter managed: the command that produces the artifact")
	case strings.ContainsRune(src.Build, 0):
		is.addf(base+".source.build", "must not contain NUL characters")
	}
	if src.Install != "" {
		is.addf(base+".source.install", "is not valid for adapter managed: the deploy helper installs the artifact")
	}
	if src.Timeout > MaxManagedBuildTimeout {
		is.addf(base+".source.timeout", "must be at most %s for adapter managed", MaxManagedBuildTimeout.D())
	}
}

// Instance limits.
const (
	// MaxInstances bounds the instances of one managed service.
	MaxInstances = 16
	// MaxInstanceName bounds an instance name, which becomes part of the
	// unit name <service>@<instance>.service.
	MaxInstanceName = 24
)

// instanceNameRe allows no leading, trailing or double "-", which keeps the
// prefixes systemd uses for drop-in directories unambiguous.
var instanceNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// ValidInstanceName reports whether name is a valid instance name. "backup"
// is reserved: the host keeps the backup worker's env file where an
// instance named so would have its own.
func ValidInstanceName(name string) bool {
	return len(name) <= MaxInstanceName && instanceNameRe.MatchString(name) && name != "backup"
}

// validateInstances checks the instances of a managed service and the
// settings each resolves to.
func validateInstances(c *ServiceConfig, base string, is *Issues) {
	switch {
	case len(c.Instances) == 0:
		is.addf(base, "must name at least one instance; omit it for a service with a single unit")
		return
	case len(c.Instances) > MaxInstances:
		is.addf(base, "at most %d instances are allowed", MaxInstances)
		return
	}
	for _, name := range sortedKeys(c.Instances) {
		p := base + "." + name
		in := c.Instances[name]
		if !ValidInstanceName(name) {
			is.addf(p, "instance names are lowercase letters and digits in groups joined by single '-', starting with a letter (max %d), and not backup", MaxInstanceName)
		}
		switch {
		case in.Start == "" && c.Start == "":
			is.addf(p+".start", "is required: the service sets no start for its instances to share")
		case in.Start != "" && (!releasePath(in.Start) || in.Start == "."):
			is.addf(p+".start", "must be a release-relative file: %s", releasePathRule)
		}
		if in.StartArgs != nil {
			validateArgs(*in.StartArgs, p+".startArgs", is)
		}
		if in.Limits == nil {
			continue
		}
		var own Issues
		validateLimits(in.Limits, p+".limits", &own)
		if len(own) > 0 {
			*is = append(*is, own...)
			continue
		}
		// The merged limits must hold together too, such as an instance
		// memoryHigh above the service's memoryMax.
		m := MergeLimits(c.Limits, in.Limits)
		high, okHigh := UnitSize(m.MemoryHigh)
		ceiling, okMax := UnitSize(m.MemoryMax)
		if okHigh && okMax && high > ceiling {
			is.addf(p+".limits", "memoryHigh %s must not be larger than memoryMax %s once merged with the service's limits", m.MemoryHigh, m.MemoryMax)
		}
	}
}

// validatePublish checks the static content a release publishes. Where it
// goes is the host's to decide: the service's publish directory.
func validatePublish(p *Publish, base string, is *Issues) {
	if !releasePath(p.Dir) {
		is.addf(base+".dir", `must be a directory of the release: %s ("." is the whole release)`, releasePathRule)
	}
	if len(p.Retain) > MaxRetain {
		is.addf(base+".retain", "at most %d directories are allowed", MaxRetain)
	}
	seen := map[string]bool{}
	for _, r := range p.Retain {
		if !ValidRetainPrefix(r) {
			is.addf(base+".retain", `%q must be a directory below publish.dir ending with "/" (%s), without a component that starts with "."`, r, releasePathRule)
		}
		if seen[r] {
			is.addf(base+".retain", "%s is listed twice", r)
		}
		seen[r] = true
	}
	switch {
	case p.RetainDays != 0 && len(p.Retain) == 0:
		is.addf(base+".retainDays", "needs retain: it is how long the files of the retained directories stay")
	case p.RetainDays < 0 || p.RetainDays > MaxRetainDays:
		is.addf(base+".retainDays", "must be between 1 and %d (default %d)", MaxRetainDays, DefaultRetainDays)
	}
}

// ValidRetainPrefix accepts a directory such as "assets/" or
// "media/emoji/": a release path ending with "/" whose components do not
// start with ".", which publishing skips.
func ValidRetainPrefix(r string) bool {
	dir, ok := strings.CutSuffix(r, "/")
	if !ok || !releasePath(dir) || dir == "." {
		return false
	}
	for _, c := range strings.Split(dir, "/") {
		if strings.HasPrefix(c, ".") {
			return false
		}
	}
	return true
}

func validateLimits(l *UnitLimits, base string, is *Issues) {
	var high, ceiling int64
	for _, f := range []struct {
		name, value string
		bytes       *int64
	}{{"memoryHigh", l.MemoryHigh, &high}, {"memoryMax", l.MemoryMax, &ceiling}} {
		if f.value == "" {
			continue
		}
		n, ok := UnitSize(f.value)
		if !ok || n < MinUnitMemory || n > MaxUnitMemory {
			is.addf(base+"."+f.name, "must be a size such as 768M or 1G (K, M or G, powers of 1024) between 1M and 16G")
			continue
		}
		*f.bytes = n
	}
	if high > 0 && ceiling > 0 && high > ceiling {
		is.addf(base+".memoryHigh", "must not be larger than memoryMax")
	}
	if t := l.StopTimeout; t != 0 {
		switch {
		case t < MinStopTimeout || t > MaxStopTimeout:
			is.addf(base+".stopTimeout", "must be between %s and %s", MinStopTimeout.D(), MaxStopTimeout.D())
		case t.D()%time.Second != 0:
			is.addf(base+".stopTimeout", "must be whole seconds")
		}
	}
}

var backupDBRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)

// validateBackup checks the form of a backup. Whether the host can run it
// (a backup directory, the tools of a database) is the deploy helper's to
// check when the backup runs.
func validateBackup(b *Backup, adapter RuntimeType, base string, is *Issues) {
	if !b.Typed() {
		for name, set := range map[string]bool{"mode": b.Mode != "", "keep": b.Keep != nil} {
			if set {
				is.addf(base+"."+name, "needs sources: it belongs to the typed form of backup")
			}
		}
		if adapter == RuntimeManaged {
			is.addf(base+".sources", "is required for adapter managed: the deploy helper backs up the sources you name; the command form is not available")
			return
		}
		validateCommand(Command{Command: b.Command, Timeout: b.Timeout}, base, is)
		return
	}
	if b.Command != "" {
		is.addf(base+".command", "cannot be combined with sources: use either the command form or the typed form")
	}
	if adapter != RuntimeManaged {
		is.addf(base+".sources", "are only valid for adapter managed: the deploy helper backs up the data directory of a managed service")
	}
	switch {
	case b.Timeout < 0:
		is.addf(base+".timeout", "must not be negative")
	case b.Timeout > MaxBackupTimeout:
		is.addf(base+".timeout", "must be at most %s", MaxBackupTimeout.D())
	}
	switch b.Mode {
	case "", BackupOnline, BackupStopped:
	default:
		is.addf(base+".mode", "must be %s or %s", BackupOnline, BackupStopped)
	}
	if k := b.Keep; k != nil {
		switch {
		case k.Daily < 0 || k.Daily > MaxKeepDaily:
			is.addf(base+".keep.daily", "must be between 0 and %d", MaxKeepDaily)
		case k.Weekly < 0 || k.Weekly > MaxKeepWeekly:
			is.addf(base+".keep.weekly", "must be between 0 and %d", MaxKeepWeekly)
		}
	}
	if len(b.Sources) > MaxBackupSources {
		is.addf(base+".sources", "at most %d sources are allowed", MaxBackupSources)
		return
	}
	var paths []string
	mongo, allDBs := 0, false
	for i, s := range b.Sources {
		p := fmt.Sprintf("%s.sources[%d]", base, i)
		allowed := map[string]bool{}
		switch s.Type {
		case BackupFiles, BackupSQLite:
			allowed["path"] = true
			if !releasePath(s.Path) || s.Path == "." || strings.HasSuffix(path.Base(s.Path), ".lcm-new") {
				is.addf(p+".path", `must be a path below the data directory, not "." and not absolute: %s`, releasePathRule)
			} else {
				paths = append(paths, s.Path)
			}
		case BackupPostgres, BackupMongoDB:
			allowed["database"] = true
			if s.Type == BackupMongoDB {
				allowed["users"] = true
				allowed["all"] = true
				mongo++
				allDBs = allDBs || s.All
			}
			switch {
			case s.All && s.Type == BackupMongoDB && s.Database != "":
				is.addf(p+".database", "cannot be combined with all: a mongodb source backs up one database or all of them")
			case s.All && s.Type == BackupMongoDB:
			case !backupDBRe.MatchString(s.Database) && s.Type == BackupMongoDB:
				is.addf(p+".database", "must be a database name of letters, digits, _ and -, or set all: true to back up every database")
			case !backupDBRe.MatchString(s.Database):
				is.addf(p+".database", "must be a database name of letters, digits, _ and -")
			}
		case BackupCommand:
			allowed["argv"] = true
			if len(s.Argv) == 0 || !releasePath(s.Argv[0]) || s.Argv[0] == "." {
				is.addf(p+".argv", "must begin with a release-relative executable: %s", releasePathRule)
			} else {
				validateArgs(s.Argv[1:], p+".argv", is)
			}
		default:
			is.addf(p+".type", "must be one of %s, %s, %s, %s, %s", BackupFiles, BackupSQLite, BackupPostgres, BackupMongoDB, BackupCommand)
			continue
		}
		for name, set := range map[string]bool{"path": s.Path != "", "database": s.Database != "", "all": s.All, "users": s.Users, "argv": s.Argv != nil} {
			if set && !allowed[name] {
				is.addf(p+"."+name, "is not valid for source type %s", s.Type)
			}
		}
	}
	if allDBs && mongo > 1 {
		is.addf(base+".sources", "an all-databases mongodb source must be the only mongodb source")
	}
	for i, a := range paths {
		for _, o := range paths[i+1:] {
			if a == o || strings.HasPrefix(a, o+"/") || strings.HasPrefix(o, a+"/") {
				is.addf(base+".sources", "the paths %s and %s overlap", a, o)
			}
		}
	}
}

func validateArgs(args []string, p string, is *Issues) {
	if len(args) > maxManagedArgs {
		is.addf(p, "at most %d arguments are allowed", maxManagedArgs)
		return
	}
	for _, a := range args {
		if !managedArgRe.MatchString(a) {
			is.addf(p, "argument %q may only contain letters, digits and ._/=:,@+- (1-256 characters)", a)
		}
	}
}

// validateManagedName checks the name of a managed service resource,
// which names the service's account, unit and directories on the host.
func validateManagedName(name string, is *Issues) {
	if len(name) > MaxManagedName || strings.HasPrefix(name, "lcm") {
		is.addf("metadata.name", `must be at most %d characters and must not start with "lcm" for adapter managed: it names the service's account, unit and directories`, MaxManagedName)
	}
}

func validateSource(s *Source, base string, is *Issues) {
	if s.Repo != "" || s.Remote != "" {
		field := "repo"
		if s.Repo == "" {
			field = "remote"
		}
		is.addf(base+"."+field, "source.repo and source.remote were replaced by source.url: set url to the repository LCM clones "+
			"(for an existing checkout, the output of git -C <repo> remote get-url origin) and remove repo and remote")
	}
	legacyOnly := s.URL == "" && (s.Repo != "" || s.Remote != "")
	if msg := checkSourceURL(s.URL); msg != "" && !legacyOnly {
		is.addf(base+".url", "%s", msg)
	}
	switch {
	case s.Branch == "":
		is.addf(base+".branch", "is required")
	case len(s.Branch) > 255 || !branchRe.MatchString(s.Branch) || hasDotDot(s.Branch):
		is.addf(base+".branch", "invalid branch name")
	}
	if len(s.Build) > maxCmdLen {
		is.addf(base+".build", "exceeds %d bytes", maxCmdLen)
	}
	if len(s.Install) > maxCmdLen {
		is.addf(base+".install", "exceeds %d bytes", maxCmdLen)
	}
	if s.Timeout < 0 {
		is.addf(base+".timeout", "must not be negative")
	}
}

// gitSchemes are the URL schemes a source may use: the transports
// execx.GitAllowProtocol permits.
var gitSchemes = []string{"file", "git", "http", "https", "ssh", "git+ssh", "ssh+git"}

// checkSourceURL returns why u is not a usable source URL, or "". The URL is
// stored, shown and logged, so it must not carry credentials.
func checkSourceURL(u string) string {
	switch {
	case u == "":
		return "is required"
	case len(u) > maxPathLen:
		return "is too long"
	case strings.HasPrefix(u, "-"):
		return "must not start with '-'"
	case strings.IndexFunc(u, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0:
		return "must not contain spaces or control characters"
	case strings.Contains(u, "::"):
		return "remote helpers (transport::address) are not supported"
	case strings.HasPrefix(u, "/"):
		if !validAbsPath(u) {
			return "must be a clean absolute path"
		}
		return ""
	}
	if scheme, rest, ok := strings.Cut(u, "://"); ok {
		known := false
		for _, s := range gitSchemes {
			known = known || strings.EqualFold(scheme, s)
		}
		switch {
		case !known:
			return "must use one of the schemes " + strings.Join(gitSchemes, ", ")
		case strings.EqualFold(scheme, "file") && !validAbsPath(rest):
			return "must name a clean absolute path"
		case strings.Trim(rest, "/") == "":
			return "names no repository"
		case execx.CheckGitRemote(u) != nil:
			return execx.ErrGitCredentials.Error()
		}
		return ""
	}
	if scpLikeRe.MatchString(u) {
		return ""
	}
	return "must be a remote URL (https://, ssh://, user@host:path) or an absolute path to a local repository"
}

// LocalRepoPath returns the filesystem path of a repository URL that names
// a local repository: an absolute path or a file:// URL.
func LocalRepoPath(u string) (string, bool) {
	if strings.HasPrefix(u, "/") {
		return path.Clean(u), true
	}
	if scheme, rest, ok := strings.Cut(u, "://"); ok && strings.EqualFold(scheme, "file") && strings.HasPrefix(rest, "/") {
		return path.Clean(rest), true
	}
	return "", false
}

// PathWithin reports whether p is dir or a path below it. The comparison is
// lexical, on clean absolute paths.
func PathWithin(p, dir string) bool {
	p, dir = path.Clean(p), path.Clean(dir)
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

func validateCommand(c Command, p string, is *Issues) {
	switch {
	case c.Command == "":
		is.addf(p+".command", "is required")
	case len(c.Command) > maxCmdLen:
		is.addf(p+".command", "exceeds %d bytes", maxCmdLen)
	}
	if c.Timeout < 0 {
		is.addf(p+".timeout", "must not be negative")
	}
}

func validAbsPath(p string) bool {
	return len(p) <= maxPathLen && path.IsAbs(p) && path.Clean(p) == p
}

func hasDotDot(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}

func merge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
