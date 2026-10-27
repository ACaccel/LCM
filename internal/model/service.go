package model

import (
	"fmt"
	"path"
	"regexp"
	"time"
)

// KindService is the document kind of a managed service.
const KindService Kind = "Service"

// RuntimeType selects how LCM controls a service's process.
type RuntimeType string

// Runtime types.
const (
	RuntimeSystemd  RuntimeType = "systemd"
	RuntimeDocker   RuntimeType = "docker"
	RuntimeCompose  RuntimeType = "compose"
	RuntimeCommand  RuntimeType = "command"
	RuntimeExternal RuntimeType = "external"
)

// Systemd scopes.
const (
	ScopeSystem = "system"
	ScopeUser   = "user"
)

// HostLocal is the only host supported so far.
const HostLocal = "local"

// Defaults applied by the accessors below when a field is left empty.
const (
	DefaultSourceRemote   = "origin"
	DefaultSourceTimeout  = Duration(15 * time.Minute)
	DefaultCommandTimeout = Duration(30 * time.Minute)
)

// Service describes one managed service: how to control it, where its
// source comes from, how to back it up and how to check it.
type Service struct {
	Header `yaml:",inline"`
	Spec   ServiceSpec `yaml:"spec" json:"spec"`
}

// DocKind implements Document.
func (s *Service) DocKind() Kind { return KindService }

// DocName implements Document.
func (s *Service) DocName() string { return s.Metadata.Name }

// ServiceSpec is the body of a Service.
type ServiceSpec struct {
	Host      string             `yaml:"host,omitempty" json:"host,omitempty"`
	Runtime   Runtime            `yaml:"runtime" json:"runtime"`
	Source    *Source            `yaml:"source,omitempty" json:"source,omitempty"`
	Backup    *Command           `yaml:"backup,omitempty" json:"backup,omitempty"`
	Jobs      map[string]Command `yaml:"jobs,omitempty" json:"jobs,omitempty"`
	DependsOn []string           `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	Checks    []Check            `yaml:"checks,omitempty" json:"checks,omitempty"`
}

// Runtime configures the adapter. Only the fields of the chosen type may be
// set.
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
	// command: shell commands per action
	Start   string `yaml:"start,omitempty" json:"start,omitempty"`
	Stop    string `yaml:"stop,omitempty" json:"stop,omitempty"`
	Restart string `yaml:"restart,omitempty" json:"restart,omitempty"`
	Status  string `yaml:"status,omitempty" json:"status,omitempty"`
}

// Source is the local working copy LCM deploys from. LCM owns this
// checkout: deploys detach its HEAD, so it must not be a developer's.
type Source struct {
	Repo    string   `yaml:"repo" json:"repo"`
	Remote  string   `yaml:"remote,omitempty" json:"remote,omitempty"`
	Branch  string   `yaml:"branch" json:"branch"`
	Build   string   `yaml:"build,omitempty" json:"build,omitempty"`
	Install string   `yaml:"install,omitempty" json:"install,omitempty"`
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// EffectiveRemote returns the remote name, defaulting to origin.
func (s Source) EffectiveRemote() string {
	if s.Remote == "" {
		return DefaultSourceRemote
	}
	return s.Remote
}

// EffectiveTimeout returns the timeout of each build step.
func (s Source) EffectiveTimeout() time.Duration {
	if s.Timeout == 0 {
		return DefaultSourceTimeout.D()
	}
	return s.Timeout.D()
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

// EffectiveHost returns the host name, defaulting to local.
func (s ServiceSpec) EffectiveHost() string {
	if s.Host == "" {
		return HostLocal
	}
	return s.Host
}

// EffectiveScope returns the systemd scope, defaulting to system.
func (r Runtime) EffectiveScope() string {
	if r.Scope == "" {
		return ScopeSystem
	}
	return r.Scope
}

// Limits on service documents.
const (
	MaxJobs      = 50
	MaxDependsOn = 100
	maxCmdLen    = 16 << 10
	maxPathLen   = 4096
)

var (
	// Unit names are limited to what systemd accepts without escaping.
	// The strict pattern also keeps a name from looking like an option
	// when it is passed as an argument.
	unitRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]*\.(service|socket|timer|target)$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	projectRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	remoteRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	branchRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	jobRe       = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// ValidateService checks the structure of a decoded service.
func ValidateService(s *Service) error {
	var is Issues
	validateHeader(&s.Header, KindService, &is)
	sp := &s.Spec

	if sp.Host != "" && sp.Host != HostLocal {
		is.addf("spec.host", "only %q is supported", HostLocal)
	}
	validateRuntime(&sp.Runtime, &is)
	if sp.Source != nil {
		validateSource(sp.Source, &is)
	}
	if sp.Backup != nil {
		validateCommand(*sp.Backup, "spec.backup", &is)
	}
	if len(sp.Jobs) > MaxJobs {
		is.addf("spec.jobs", "at most %d jobs are allowed", MaxJobs)
	}
	for _, name := range sortedKeys(sp.Jobs) {
		p := "spec.jobs." + name
		if !jobRe.MatchString(name) || len(name) > maxNameLen {
			is.addf(p, "job names are lowercase letters, digits, '-' and '_', starting with a letter (max %d)", maxNameLen)
		}
		validateCommand(sp.Jobs[name], p, &is)
	}
	if len(sp.DependsOn) > MaxDependsOn {
		is.addf("spec.dependsOn", "at most %d dependencies are allowed", MaxDependsOn)
	}
	seen := map[string]bool{}
	for i, d := range sp.DependsOn {
		p := fmt.Sprintf("spec.dependsOn[%d]", i)
		switch {
		case !nameRe.MatchString(d) || len(d) > maxNameLen:
			is.addf(p, "invalid service name %q", d)
		case d == s.Metadata.Name:
			is.addf(p, "a service cannot depend on itself")
		case seen[d]:
			is.addf(p, "duplicate dependency %q", d)
		}
		seen[d] = true
	}
	validateChecks(sp.Checks, "spec.checks", &is)
	return is.Err()
}

func validateRuntime(r *Runtime, is *Issues) {
	const base = "spec.runtime"
	// forbid reports every field set that the chosen type does not use.
	forbid := func(fields map[string]string) {
		for _, name := range sortedKeys(fields) {
			if fields[name] != "" {
				is.addf(base+"."+name, "is not valid for runtime type %s", r.Type)
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
		forbid(merge(systemd, docker, compose))
		for _, f := range []struct{ name, value string }{{"start", r.Start}, {"stop", r.Stop}} {
			if f.value == "" {
				is.addf(base+"."+f.name, "is required")
			}
		}
		for name, v := range command {
			if len(v) > maxCmdLen {
				is.addf(base+"."+name, "exceeds %d bytes", maxCmdLen)
			}
		}
	case RuntimeExternal:
		forbid(merge(systemd, docker, compose, command))
	case "":
		is.addf(base+".type", "is required")
	default:
		is.addf(base+".type", "must be one of systemd, docker, compose, command, external")
	}
}

func validateSource(s *Source, is *Issues) {
	const base = "spec.source"
	switch {
	case s.Repo == "":
		is.addf(base+".repo", "is required")
	case !validAbsPath(s.Repo):
		is.addf(base+".repo", "must be a clean absolute path")
	}
	if s.Remote != "" && (len(s.Remote) > maxNameLen || !remoteRe.MatchString(s.Remote)) {
		is.addf(base+".remote", "invalid remote name")
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

func (s *Service) normalize() error {
	var is Issues
	for i := range s.Spec.Checks {
		if s.Spec.Checks[i].Props == nil {
			continue
		}
		v, err := Normalize(s.Spec.Checks[i].Props)
		if err != nil {
			is.addf(fmt.Sprintf("spec.checks[%d].props", i), "%v", err)
			continue
		}
		s.Spec.Checks[i].Props, _ = v.(map[string]any)
	}
	if s.Spec.Host == "" {
		s.Spec.Host = HostLocal
	}
	if s.Spec.Runtime.Type == RuntimeSystemd && s.Spec.Runtime.Scope == "" {
		s.Spec.Runtime.Scope = ScopeSystem
	}
	if src := s.Spec.Source; src != nil && src.Remote == "" {
		src.Remote = DefaultSourceRemote
	}
	return is.Err()
}
