// Package configsync is policy-as-code: it applies Service, Policy and
// Module documents from files or from git repositories, tracks which
// resources a repository owns, and tells the operator when local edits and
// the repository disagree.
//
// # Ownership and drift
//
// Applying a document on behalf of a config source (a repository, branch
// and directory that LCM pulls) makes that source the owner of the
// resource. The web UI and the API keep working on owned resources and
// their edits take effect at once; the resource is then "drifted", which is
// computed, never stored: it is drifted when its current version differs
// from the version LCM wrote and its content differs from the repository
// document LCM last accounted for. Nothing hooks into the write paths.
//
// # Conflicts
//
// A new commit that changes the document of a drifted resource is never
// applied. The sync keeps the resource as it is, remembers the newer
// document as pending, and notifies (a timeline entry for services and a
// warning signal with source "configsync"). The operator resolves it by
// applying the repository version (Resolve with useGit) or by keeping the
// current one (Resolve without), which accepts the pending document as the
// reference so that the resource stays drifted until the repository content
// equals the current document, at which point the drift clears by itself.
//
// # Manual applies
//
// Apply without a source (lcm apply) works for resources that nobody owns.
// For an owned resource it refuses unless Force is set; a forced apply
// writes the document but leaves the resource owned, so it is drifted from
// the repository and git stays the source of truth.
//
// A source never overwrites an existing, unowned resource (one made in the
// web UI or API) on first sight. If the document equals the resource, the
// source adopts it and nothing changes: this is the path of exporting,
// committing and registering the repository. If it differs, nothing is
// written; the claim is recorded as a conflict in the same form as a drift
// conflict (the resource counts as drifted, the document is pending) and
// notified, and Resolve settles it: git applies the document and takes
// ownership, current keeps the resource and takes ownership in the drifted,
// acknowledged state. A resource claimed by one source is refused to every
// other. Documents that disappear from a repository mark their resource
// orphaned but never delete it.
//
// # Privileges
//
// Documents only describe services, policies and modules. Command
// allowlists, the privilege prefix and the units LCM may touch live in the
// host configuration and are never read from a document.
package configsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/modules"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
)

// Errors returned by the Syncer.
var (
	// ErrNotFound means a source or an ownership record does not exist.
	ErrNotFound = errors.New("configsync: not found")
	// ErrExists means a source with that name already exists.
	ErrExists = errors.New("configsync: source already exists")
)

// SourceName is the signal source of notifications.
const SourceName = "configsync"

// DefaultGitTimeout bounds each git command.
const DefaultGitTimeout = 2 * time.Minute

// CheckValidator validates the health checks of a service document. The
// monitor implements it.
type CheckValidator interface {
	ValidateChecks(checks []model.Check, path string) model.Issues
}

// Config configures a Syncer.
type Config struct {
	DB      *store.DB
	Audit   *audit.Log
	Catalog *catalog.Catalog
	Modules *modules.Store
	Engine  *engine.Engine
	// Checks validates service health checks; optional.
	Checks CheckValidator
	// Bus receives conflict notifications; optional.
	Bus *signal.Bus
	// Runner runs git; required for sources.
	Runner execx.Runner
	// Git is the git program; empty selects "git".
	Git string
	// GitTimeout bounds each git command; zero selects DefaultGitTimeout.
	GitTimeout time.Duration
	// Dir is where LCM keeps its own clones, one directory per source. It is
	// created on first use; clones are never placed in a user's directory.
	Dir   string
	Clock clock.Clock
	Log   *slog.Logger
}

// Syncer applies documents, owns the ownership table and runs the sources.
type Syncer struct {
	db      *store.DB
	audit   *audit.Log
	cat     *catalog.Catalog
	mods    *modules.Store
	eng     *engine.Engine
	checks  CheckValidator
	bus     *signal.Bus
	runner  execx.Runner
	gitBin  string
	gitTime time.Duration
	dir     string
	clk     clock.Clock
	log     *slog.Logger

	// applyMu serializes applies so that ownership decisions never race
	// with each other.
	applyMu sync.Mutex
	// syncMu serializes source syncs; they hold applyMu only while applying.
	syncMu sync.Mutex

	// mu guards forced and inflight; kick wakes Run.
	mu     sync.Mutex
	forced map[string]bool
	kick   chan struct{}
	// inflight counts the syncs running per source, for Source.Syncing and
	// for coalescing requests.
	inflight map[string]int

	// life ends when Close is called; syncs started by RequestSync run on
	// it. jobs tracks them.
	life   context.Context
	cancel context.CancelFunc
	jobs   sync.WaitGroup
}

// New returns a Syncer.
func New(cfg Config) (*Syncer, error) {
	if cfg.DB == nil || cfg.Audit == nil || cfg.Catalog == nil || cfg.Modules == nil || cfg.Engine == nil {
		return nil, errors.New("configsync: DB, Audit, Catalog, Modules and Engine are required")
	}
	s := &Syncer{
		db: cfg.DB, audit: cfg.Audit, cat: cfg.Catalog, mods: cfg.Modules, eng: cfg.Engine, checks: cfg.Checks,
		bus: cfg.Bus, runner: cfg.Runner, gitBin: cfg.Git, gitTime: cfg.GitTimeout, dir: cfg.Dir, clk: cfg.Clock, log: cfg.Log,
		forced: map[string]bool{}, kick: make(chan struct{}, 1), inflight: map[string]int{},
	}
	s.life, s.cancel = context.WithCancel(context.Background())
	if s.gitBin == "" {
		s.gitBin = "git"
	}
	if s.gitTime <= 0 {
		s.gitTime = DefaultGitTimeout
	}
	if s.clk == nil {
		s.clk = clock.Real()
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s, nil
}

// kindRank orders kinds for applying: modules before the policies that use
// them, services before the policies that act on them.
func kindRank(k model.Kind) int {
	switch k {
	case model.KindModule:
		return 1
	case model.KindService:
		return 2
	case model.KindPolicy:
		return 3
	}
	return 0
}

// cloneDir returns the directory of a source's clone, creating the root.
func (s *Syncer) cloneDir(name string) (string, error) {
	if s.dir == "" {
		return "", errors.New("configsync: no directory configured for source clones")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", fmt.Errorf("configsync: create clone directory: %w", err)
	}
	return filepath.Join(s.dir, name), nil
}

func (s *Syncer) now() time.Time { return s.clk.Now() }

func (s *Syncer) record(ctx context.Context, action, target string, outcome audit.Outcome, details map[string]any) {
	if err := s.audit.Record(ctx, action, target, outcome, details); err != nil {
		s.log.Error("audit record failed", "action", action, "target", target, "error", err)
	}
}
