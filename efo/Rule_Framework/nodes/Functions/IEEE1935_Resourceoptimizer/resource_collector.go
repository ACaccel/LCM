package IEEE1935_Resourceoptimizer

import (
	"context"
	"net/http"
	"time"

	"1935/efo/Rule_Framework/nodes/Functions/calm"
)

// The collection logic lives in the calm package, which the CALM telemetry and
// post-verification stages read from as well. The aliases keep this package's
// type names, and the method keeps its client timeout, so nothing outside
// changes.
type (
	ClusterResource    = calm.ClusterResource
	ClusterResourceMap = calm.ClusterResourceMap
)

// FetchClusterResources sums each cluster's node capacity.
func (n *IEEE1935_Resourceoptimizer) FetchClusterResources(ctx context.Context, baseURL string) (ClusterResourceMap, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return calm.FetchClusterResources(ctx, client, baseURL)
}
