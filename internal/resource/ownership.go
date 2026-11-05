package resource

import (
	"context"
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
)

// OwnedByConfig reports whether a config source (Git) owns the active
// resource with this name. SetPhase refuses to unmanage such a resource;
// types use this to say so in their actions.
func (s *Store) OwnedByConfig(ctx context.Context, name string) (bool, error) {
	var owned int
	if err := s.db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM config_ownership WHERE kind = ? AND name = ?`,
		string(model.KindResource), name).Scan(&owned); err != nil {
		return false, fmt.Errorf("resource: read ownership: %w", err)
	}
	return owned > 0, nil
}
