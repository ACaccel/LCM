package catalog

import (
	"context"
	"fmt"
	"time"
)

// CountDeploymentsSince returns how many deployments and rollbacks of any
// service started at or after since.
func (c *Catalog) CountDeploymentsSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := c.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deployments WHERE started_at >= ?`, since.UnixMilli()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("catalog: count deployments: %w", err)
	}
	return n, nil
}
