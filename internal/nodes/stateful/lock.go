package stateful

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/internal/poll"
)

// PortBusy is the port lock takes when the lock could not be acquired.
const PortBusy = "busy"

// OwnerPrefix starts the owner string of locks taken by runs. The owner is
// the prefix plus the run id, so a lock can always be traced to its run and
// released when that run ends.
const OwnerPrefix = "run:"

// LockManager is the part of lock.Manager the nodes use.
type LockManager interface {
	TryAcquire(ctx context.Context, name, owner string, ttl time.Duration) (lock.Info, error)
	Release(ctx context.Context, name, owner string) (bool, error)
}

// LockProps configures lock.
type LockProps struct {
	Name         string        `json:"name" lcm:"required" desc:"lock name, e.g. host:db1"`
	TTL          node.Duration `json:"ttl" default:"15m" desc:"the lock frees itself after this long; choose longer than the guarded steps take"`
	IfBusy       string        `json:"ifBusy" enum:"wait,skip" default:"wait" desc:"wait: retry until waitTimeout; skip: take the busy port at once"`
	WaitTimeout  node.Duration `json:"waitTimeout" default:"10m" desc:"how long ifBusy=wait keeps trying before taking the busy port"`
	PollInterval node.Duration `json:"pollInterval" default:"2s" desc:"time between attempts while waiting"`
}

// UnlockProps configures unlock.
type UnlockProps struct {
	Name string `json:"name" lcm:"required" desc:"lock name taken earlier in this run"`
}

func registerLocks(r *node.Registry, locks LockManager) {
	node.Register(r, node.Spec[LockProps]{
		Type: "lock", Category: CategoryState, Effect: node.EffectWrite,
		Description: "Takes a named lock for this run. The lock is held until an unlock node, the end of the run, or its ttl, whichever comes first. If it is busy, waits (without holding a worker) or takes the busy port.",
		Ports:       []string{node.PortOut, PortBusy},
		Validate: func(p LockProps) error {
			switch {
			case !lock.ValidName(p.Name):
				return fmt.Errorf("name: invalid lock name %q", p.Name)
			case p.TTL.D() <= 0 || p.TTL.D() > lock.MaxTTL:
				return fmt.Errorf("ttl: must be between 0 and %s", lock.MaxTTL)
			case p.PollInterval.D() < 100*time.Millisecond:
				return errors.New("pollInterval: must be at least 100ms")
			case p.WaitTimeout.D() < 0:
				return errors.New("waitTimeout: must not be negative")
			}
			return nil
		},
		Run: func(ctx context.Context, rc *node.RunContext, p LockProps) (node.Result, error) {
			return runLock(ctx, rc, locks, p)
		},
	})
	node.Register(r, node.Spec[UnlockProps]{
		Type: "unlock", Category: CategoryState, Effect: node.EffectWrite,
		Description: "Releases a lock taken by this run before the run ends. Output: released.",
		Validate: func(p UnlockProps) error {
			if !lock.ValidName(p.Name) {
				return fmt.Errorf("name: invalid lock name %q", p.Name)
			}
			return nil
		},
		Run: func(ctx context.Context, rc *node.RunContext, p UnlockProps) (node.Result, error) {
			ok, err := locks.Release(ctx, p.Name, OwnerPrefix+rc.RunID)
			if err != nil {
				return node.Result{}, err
			}
			return node.Result{Output: map[string]any{"released": ok}}, nil
		},
	})
}

func runLock(ctx context.Context, rc *node.RunContext, locks LockManager, p LockProps) (node.Result, error) {
	now := rc.Now()
	owner := OwnerPrefix + rc.RunID
	info, err := locks.TryAcquire(ctx, p.Name, owner, p.TTL.D())
	if err == nil {
		return node.Result{Output: map[string]any{
			"name": p.Name, "owner": owner, "expiresAt": info.ExpiresAt.Format(time.RFC3339),
		}}, nil
	}
	var busy *lock.BusyError
	if !errors.As(err, &busy) {
		if errors.Is(err, lock.ErrInvalid) {
			return node.Result{}, node.Permanent(err)
		}
		return node.Result{}, err
	}

	held := map[string]any{"name": p.Name, "heldBy": busy.Holder.Owner}
	if !busy.Holder.ExpiresAt.IsZero() {
		held["heldUntil"] = busy.Holder.ExpiresAt.Format(time.RFC3339)
	}
	if p.IfBusy == "skip" {
		return node.Result{Port: PortBusy, Output: held}, nil
	}
	st, resumed := poll.Load(rc.Resumed)
	if !resumed {
		st = poll.Begin(now, p.WaitTimeout.D())
	}
	st.Polls++
	if st.Expired(now) {
		return node.Result{Port: PortBusy, Output: held}, nil
	}
	at := st.NextWake(now, p.PollInterval.D())
	return node.Result{Suspend: &node.Suspend{ResumeAt: &at, Output: st.Output(map[string]any{"heldBy": busy.Holder.Owner})}}, nil
}

// RunLookup is the part of the engine the Releaser needs.
type RunLookup interface {
	Run(ctx context.Context, runID string) (engine.RunView, error)
}

// LockReleaser is the part of lock.Manager the Releaser needs.
type LockReleaser interface {
	ReleaseOwner(ctx context.Context, owner string) ([]string, error)
	Held(ctx context.Context, ownerPrefix string) ([]lock.Info, error)
}

// DefaultSweepInterval is how often a Releaser looks for locks of runs that
// ended without an observed finish event.
const DefaultSweepInterval = time.Minute

// Releaser frees the locks of a run when the run ends, whatever its final
// status. Register it as an engine observer for the prompt path. Observer
// delivery is lossy, so Run also sweeps periodically, which additionally
// frees locks of runs that were interrupted by a crash; the ttl is the last
// line of defence.
type Releaser struct {
	locks    LockReleaser
	clock    clock.Clock
	log      *slog.Logger
	interval time.Duration
}

// NewReleaser returns a Releaser. clk and log may be nil. The engine is not
// a parameter because it is created with the releaser as one of its
// observers; Sweep and Run take it instead.
func NewReleaser(locks LockReleaser, clk clock.Clock, log *slog.Logger) *Releaser {
	if clk == nil {
		clk = clock.Real()
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Releaser{locks: locks, clock: clk, log: log, interval: DefaultSweepInterval}
}

// Observe implements engine.Observer.
func (r *Releaser) Observe(e engine.Event) {
	if e.Kind != engine.EventRunFinished || e.RunID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	names, err := r.locks.ReleaseOwner(ctx, OwnerPrefix+e.RunID)
	if err != nil {
		r.log.Warn("release locks of finished run failed; the sweeper or ttl will free them", "run", e.RunID, "error", err)
		return
	}
	if len(names) > 0 {
		r.log.Debug("released locks of finished run", "run", e.RunID, "locks", names)
	}
}

// Sweep frees locks whose run is finished or unknown and returns how many.
func (r *Releaser) Sweep(ctx context.Context, runs RunLookup) (int, error) {
	held, err := r.locks.Held(ctx, OwnerPrefix)
	if err != nil {
		return 0, err
	}
	released := 0
	seen := map[string]bool{}
	for _, info := range held {
		id := strings.TrimPrefix(info.Owner, OwnerPrefix)
		if seen[id] {
			continue
		}
		seen[id] = true
		view, err := runs.Run(ctx, id)
		switch {
		case errors.Is(err, engine.ErrRunNotFound):
		case err != nil:
			return released, err
		case !view.Status.Terminal():
			continue
		}
		names, err := r.locks.ReleaseOwner(ctx, info.Owner)
		if err != nil {
			return released, err
		}
		released += len(names)
	}
	return released, nil
}

// Run sweeps once and then every interval until ctx ends. Start it after
// Engine.Recover so that runs resumed by recovery are seen as active.
func (r *Releaser) Run(ctx context.Context, runs RunLookup) {
	for {
		if n, err := r.Sweep(ctx, runs); err != nil && ctx.Err() == nil {
			r.log.Warn("lock sweep failed", "error", err)
		} else if n > 0 {
			r.log.Info("released locks of finished runs", "count", n)
		}
		done := make(chan struct{})
		t := r.clock.AfterFunc(r.interval, func() { close(done) })
		select {
		case <-done:
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}
