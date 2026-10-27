package IEEE1935_Resourceoptimizer

import (
	"context"
	"net/http"

	"1935/efo/Rule_Framework/nodes/Functions/calm"
)

// The Jaeger scan lives in the calm package, which the CALM telemetry and
// post-verification stages read from as well. The aliases keep this package's
// type names; the scan itself is unbounded in time and page count, which is
// what this node has always asked for.
type (
	JaegerResponse   = calm.JaegerResponse
	ServicesRequests = calm.ServicesRequests
)

// FetchServicesRequest collects the per-service request statistics of every
// cluster.
func (n *IEEE1935_Resourceoptimizer) FetchServicesRequest(ctx context.Context, baseURL string, collectDelay bool) (map[string]map[string]ServicesRequests, error) {
	client := &http.Client{}
	return calm.FetchServicesRequests(ctx, client, baseURL, collectDelay)
}
