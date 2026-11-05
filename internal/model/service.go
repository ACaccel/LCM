package model

import (
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ACaccel/lcm/internal/execx"
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
	// without one is not offered
	Start   string `yaml:"start,omitempty" json:"start,omitempty"`
	Stop    string `yaml:"stop,omitempty" json:"stop,omitempty"`
	Restart string `yaml:"restart,omitempty" json:"restart,omitempty"`
	Status  string `yaml:"status,omitempty" json:"status,omitempty"`

	Source *Source            `yaml:"source,omitempty" json:"source,omitempty"`
	Backup *Command           `yaml:"backup,omitempty" json:"backup,omitempty"`
	Jobs   map[string]Command `yaml:"jobs,omitempty" json:"jobs,omitempty"`
}

func (*ServiceConfig) resourceConfig() {}

// Runtime returns the runtime value the adapters consume for the given
// adapter name.
func (c *ServiceConfig) Runtime(adapter string) Runtime {
	return Runtime{
		Type: RuntimeType(adapter),
		Unit: c.Unit, Scope: c.Scope,
		Container: c.Container,
		Project:   c.Project, File: c.File,
		Start: c.Start, Stop: c.Stop, Restart: c.Restart, Status: c.Status,
	}
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
}

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
}

func (c *ServiceConfig) validate(adapter string, base string, is *Issues) {
	validateRuntime(c.Runtime(adapter), base, is)
	if c.Source != nil {
		validateSource(c.Source, base+".source", is)
	}
	if c.Backup != nil {
		validateCommand(*c.Backup, base+".backup", is)
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
