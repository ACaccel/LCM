package engine

import (
	"context"
	"fmt"
	"strings"
)

// CountRuns returns how many stored runs are in one of statuses. Like
// ListRuns it reads storage, so it trails an active run by at most one
// checkpoint commit.
func (e *Engine) CountRuns(ctx context.Context, statuses ...RunStatus) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	args := make([]any, len(statuses))
	for i, s := range statuses {
		if !s.Valid() {
			return 0, fmt.Errorf("engine: unknown run status %q", s)
		}
		args[i] = string(s)
	}
	var n int
	err := e.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE status IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("engine: count runs: %w", err)
	}
	return n, nil
}
