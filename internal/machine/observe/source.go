package observe

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"

	"github.com/ACaccel/lcm/internal/execx"
)

var errInvalid = errors.New("invalid source data")
var errTruncated = errors.New("source exceeds byte limit")

func sample[T any](value T, err error) Sample[T] {
	if err == nil {
		return Sample[T]{Value: value, Coverage: Coverage{State: Available}}
	}
	return Sample[T]{Coverage: failed(err)}
}

func failed(err error) Coverage {
	c := Coverage{State: Unavailable, Reason: ReasonRead}
	switch {
	case errors.Is(err, context.Canceled):
		c.Reason = ReasonCancelled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, execx.ErrTimeout):
		c.Reason = ReasonTimeout
	case errors.Is(err, fs.ErrPermission):
		c.Reason = ReasonPermission
	case errors.Is(err, exec.ErrNotFound):
		c.State, c.Reason = Unsupported, ReasonMissing
	case errors.Is(err, fs.ErrNotExist):
		c.Reason = ReasonMissing
	case errors.Is(err, errTruncated):
		c.Reason = ReasonTruncated
	case errors.Is(err, errInvalid):
		c.Reason = ReasonInvalid
	}
	return c
}

var commandEnv = []string{"LC_ALL=C", "SYSTEMD_COLORS=0", "SYSTEMD_URLIFY=0"}
