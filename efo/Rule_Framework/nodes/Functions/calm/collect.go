package calm

/*
Control-plane and Jaeger collection.

The cluster/node resource summation and the Jaeger trace scan were written for
IEEE1935_Resourceoptimizer. The CALM telemetry and post-verification stages need
the same reads, so they live here and that node calls in: same endpoints, same
order, same error text as before the move.

Every entry point takes an explicit context and http.Client. The caller decides
the timeout and binds the context to its run (utils.RunContext), so an in-flight
read is abandoned when the run ends instead of holding up the next slot.

This file keeps the package's dependency rule: standard library only, no node
package, no engine package.
*/

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// DoJSON sends a request bound to ctx. Every read in this file goes through it,
// so a cancelled run drops the connection rather than waiting out the client
// timeout.
func DoJSON(ctx context.Context, client *http.Client, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

// ClusterResource is one cluster's total capacity. The JSON tags are the wire
// form the external ResourceAllocationApp expects.
type ClusterResource struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
}

// ClusterResourceMap holds the capacity of every cluster, keyed by cluster name.
type ClusterResourceMap map[string]ClusterResource

// FetchClusterNodeTotals sums Total_cpu and Total_memory over the nodes of one
// cluster. A node whose totals are missing or not numeric contributes zero,
// which is what the control plane reports for a machine it has not measured.
func FetchClusterNodeTotals(ctx context.Context, client *http.Client, baseURL, clusterName string) (ClusterResource, error) {
	res, err := DoJSON(ctx, client, http.MethodGet, fmt.Sprintf("%s/api/clusters/%s/nodes", baseURL, clusterName), nil)
	if err != nil {
		return ClusterResource{}, fmt.Errorf("cannot fetch nodes on cluster %s: %w", clusterName, err)
	}
	defer res.Body.Close()

	var nodes []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&nodes); err != nil {
		return ClusterResource{}, fmt.Errorf("cannot decode nodes data on cluster %s: %w", clusterName, err)
	}

	var total ClusterResource
	for _, node := range nodes {
		if cpuVal, ok := node["Total_cpu"].(float64); ok {
			total.CPU += cpuVal
		}
		if memVal, ok := node["Total_memory"].(float64); ok {
			total.Memory += memVal
		}
	}
	return total, nil
}

// FetchClusterResources returns the total capacity of every cluster, in the
// order the control plane lists them. A cluster entry without a usable name is
// skipped.
func FetchClusterResources(ctx context.Context, client *http.Client, baseURL string) (ClusterResourceMap, error) {
	clusterResource := make(ClusterResourceMap)

	res, err := DoJSON(ctx, client, http.MethodGet, fmt.Sprintf("%s/api/clusters", baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch clusters failed: %w", err)
	}
	defer res.Body.Close()

	var clusters []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&clusters); err != nil {
		return nil, fmt.Errorf("decode clusters failed: %w", err)
	}

	for _, item := range clusters {
		name, ok := item["Cluster_Name"].(string)
		if !ok {
			continue
		}
		total, err := FetchClusterNodeTotals(ctx, client, baseURL, name)
		if err != nil {
			return nil, err
		}
		clusterResource[name] = total
	}

	return clusterResource, nil
}

// FetchAppNames maps every application's inventory id to its name. The mapping
// is needed because the cluster-scoped listings identify an application by id
// while Jaeger and the scaling API address it by name.
func FetchAppNames(ctx context.Context, client *http.Client, baseURL string) (map[string]string, error) {
	res, err := DoJSON(ctx, client, http.MethodGet, fmt.Sprintf("%s/api/apps", baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch apps failed: %w", err)
	}
	defer res.Body.Close()

	var apps []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&apps); err != nil {
		return nil, fmt.Errorf("decode apps data failed: %w", err)
	}

	appNames := make(map[string]string, len(apps))
	for _, app := range apps {
		if appID, ok := app["Application_ID"].(string); ok {
			if appName, ok := app["Application_Name"].(string); ok {
				appNames[appID] = appName
			}
		}
	}
	return appNames, nil
}

// Deployment is one row of GET /api/clusters/{cluster}/deployments, with the
// numeric fields parsed.
type Deployment struct {
	// ApplicationID is the deployed application's inventory id. The control
	// plane sends it under the JSON key "Application_Name", which despite its
	// name carries the id; FetchAppNames resolves it to the real name.
	ApplicationID string
	// Replicas is the replica count currently recorded for the deployment.
	Replicas int
	// AllocatedCPU and AllocatedMemory are the per-replica reservations. The
	// domain's reserved total is the reservation times the replica count.
	AllocatedCPU    float64
	AllocatedMemory float64
}

// deploymentRow is the wire shape. Every field is a string in the inventory,
// including the numeric ones, and the memory key carries the control plane's
// spelling.
type deploymentRow struct {
	ApplicationID   string `json:"Application_Name"`
	Replicas        string `json:"Replicas"`
	AllocatedCPU    string `json:"Allocated_cpu"`
	AllocatedMemory string `json:"Alocated_memory"`
}

// FetchDeployments lists the deployments of one cluster.
//
// A replica count that is present but unreadable is an error: it drives the
// autoscaler's decision and a silent zero would read as an undeployed service.
// An unreadable allocation only feeds a utilisation ratio, so it counts as zero
// and the rest of the row still stands.
func FetchDeployments(ctx context.Context, client *http.Client, baseURL, clusterName string) ([]Deployment, error) {
	res, err := DoJSON(ctx, client, http.MethodGet, fmt.Sprintf("%s/api/clusters/%s/deployments", baseURL, url.PathEscape(clusterName)), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch deployments for cluster %s failed: %w", clusterName, err)
	}
	defer res.Body.Close()

	var rows []deploymentRow
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode deployments data for cluster %s failed: %w", clusterName, err)
	}

	deployments := make([]Deployment, 0, len(rows))
	for _, row := range rows {
		replicas := 0
		if row.Replicas != "" {
			n, err := strconv.Atoi(row.Replicas)
			if err != nil {
				return nil, fmt.Errorf("deployment %s on cluster %s has unreadable replicas %q: %w",
					row.ApplicationID, clusterName, row.Replicas, err)
			}
			replicas = n
		}
		deployments = append(deployments, Deployment{
			ApplicationID:   row.ApplicationID,
			Replicas:        replicas,
			AllocatedCPU:    parseAllocation(row.AllocatedCPU),
			AllocatedMemory: parseAllocation(row.AllocatedMemory),
		})
	}
	return deployments, nil
}

// parseAllocation reads a reservation string, treating anything unreadable as
// no reservation.
func parseAllocation(raw string) float64 {
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value
}

// JaegerResponse is the part of a Jaeger traces page this package reads.
type JaegerResponse struct {
	Data []struct {
		TraceID string `json:"traceID"`
		Spans   []struct {
			SpanID    string `json:"spanID"`
			StartTime int64  `json:"startTime"` // microseconds since epoch
			Duration  int64  `json:"duration"`
			Tags      []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"tags"`
		} `json:"spans"`
	} `json:"data"`
}

// ServicesRequests is what one service's trace scan produced. ArrivalTime and
// Latencies are milliseconds and are only filled when the scan asked for delay
// samples.
type ServicesRequests struct {
	ArrivalTime []int64
	Latencies   []int64
	RequestIDs  []string
	Number      int
	// Truncated reports that the scan stopped at its page limit rather than at
	// the end of the requested window, so Number is a lower bound.
	Truncated bool
}

const (
	// jaegerTracePageLimit is the page size. The orchestrator's Jaeger proxy
	// truncates a single response at about 4KB; limit=1 keeps each page under
	// that, at the cost of one round trip per trace.
	jaegerTracePageLimit = "1"
	// MaxJaegerTracePages bounds a scan that asks for no page limit of its own.
	MaxJaegerTracePages = 5000
)

// JaegerScan describes one service's trace scan.
type JaegerScan struct {
	BaseURL     string
	ClusterName string
	ServiceName string
	// CollectDelay asks for the per-request arrival and latency samples on top
	// of the request count.
	CollectDelay bool
	// SinceMicros drops requests that started before it, bounding the scan to a
	// sampling window. Zero means no lower bound.
	SinceMicros int64
	// EndMicros seeds the first page's "end" parameter. Zero omits it, which
	// asks Jaeger for the most recent traces.
	EndMicros int64
	// MaxPages bounds the walk. Zero means MaxJaegerTracePages.
	MaxPages int
}

// getRequestStats parses one Jaeger page. It returns the trace count and the
// oldest span's startTime (microseconds), which drive the pagination.
//
// sinceMicros bounds which requests are counted; the oldest start time is
// reported unfiltered, because the walk has to step past the window boundary to
// know it has reached it.
func getRequestStats(body io.Reader, collectDelay bool, sinceMicros int64) (ServicesRequests, int, int64, error) {
	var response JaegerResponse
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		return ServicesRequests{}, 0, 0, err
	}
	traceCount := len(response.Data)

	var stats ServicesRequests
	oldestStart := int64(0)
	hasSpan := false
	for _, trace := range response.Data {
		for _, span := range trace.Spans {
			if !hasSpan || span.StartTime < oldestStart {
				oldestStart = span.StartTime
				hasSpan = true
			}
			// Only the entry span (kind=server) counts as one request.
			isServerSpan := false
			for _, tag := range span.Tags {
				if tag.Key == "span.kind" && tag.Value == "server" {
					isServerSpan = true
					break
				}
			}

			if isServerSpan {
				if sinceMicros > 0 && span.StartTime < sinceMicros {
					// The request predates the sampling window.
					break
				}
				stats.Number += 1
				if collectDelay {
					// Jaeger reports microseconds; the consumers want
					// milliseconds.
					latencyMs := span.Duration / 1000
					arrivalTimeMs := span.StartTime / 1000
					stats.ArrivalTime = append(stats.ArrivalTime, arrivalTimeMs)
					stats.Latencies = append(stats.Latencies, latencyMs)
					stats.RequestIDs = append(stats.RequestIDs, trace.TraceID)
				}
				break
			}
		}
	}
	if !hasSpan {
		oldestStart = 0
	}
	return stats, traceCount, oldestStart, nil
}

func fetchJaegerTracesPage(ctx context.Context, client *http.Client, baseURL, clusterName, serviceName, limit string, endMicros int64) ([]byte, error) {
	tracesURL, err := url.Parse(fmt.Sprintf("%s/api/clusters/%s/jaeger/api/traces", baseURL, clusterName))
	if err != nil {
		return nil, fmt.Errorf("parse jaeger traces url for cluster %s failed: %w", clusterName, err)
	}
	q := tracesURL.Query()
	q.Set("service", serviceName)
	q.Set("limit", limit)
	if endMicros > 0 {
		q.Set("end", strconv.FormatInt(endMicros, 10))
	}
	tracesURL.RawQuery = q.Encode()

	res, err := DoJSON(ctx, client, http.MethodGet, tracesURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch jaeger traces for cluster %s and service %s failed: %w", clusterName, serviceName, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("fetch jaeger traces for cluster %s and service %s: HTTP %d: %s",
			clusterName, serviceName, res.StatusCode, string(body))
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("read jaeger traces body for cluster %s and service %s failed: %w", clusterName, serviceName, err)
	}
	return body, nil
}

// FetchJaegerTraces walks the trace pages of one service backwards in time and
// aggregates them. It stops at the first empty page, when the walk has stepped
// past SinceMicros, or when MaxPages have been read.
//
// Partial statistics are returned alongside an error: a caller that treats a
// failed collection as unusable ignores them, and one that only logs keeps what
// arrived.
func FetchJaegerTraces(ctx context.Context, client *http.Client, scan JaegerScan) (ServicesRequests, error) {
	var stats ServicesRequests
	maxPages := scan.MaxPages
	if maxPages <= 0 {
		maxPages = MaxJaegerTracePages
	}
	endMicros := scan.EndMicros

	for page := 0; page < maxPages; page++ {
		body, err := fetchJaegerTracesPage(ctx, client, scan.BaseURL, scan.ClusterName, scan.ServiceName, jaegerTracePageLimit, endMicros)
		if err != nil {
			return stats, err
		}
		pageStats, traceCount, oldestStart, err := getRequestStats(bytes.NewReader(body), scan.CollectDelay, scan.SinceMicros)
		if err != nil {
			return stats, fmt.Errorf("get request stats for cluster %s and service %s (page %d): %w",
				scan.ClusterName, scan.ServiceName, page, err)
		}
		if traceCount == 0 {
			return stats, nil
		}
		stats.Number += pageStats.Number
		stats.ArrivalTime = append(stats.ArrivalTime, pageStats.ArrivalTime...)
		stats.Latencies = append(stats.Latencies, pageStats.Latencies...)
		stats.RequestIDs = append(stats.RequestIDs, pageStats.RequestIDs...)

		if oldestStart <= 0 {
			return stats, nil
		}
		if scan.SinceMicros > 0 && oldestStart <= scan.SinceMicros {
			return stats, nil
		}
		endMicros = oldestStart - 1
	}

	stats.Truncated = true
	return stats, nil
}

// FetchClusterServiceNames lists the application names of the services
// instantiated in one cluster, in the order the control plane returns them.
//
// A service whose id has no matching application is skipped and logged: it is
// an inventory inconsistency, and silence here would hide it.
func FetchClusterServiceNames(ctx context.Context, client *http.Client, baseURL, clusterName string, appNames map[string]string) ([]string, error) {
	res, err := DoJSON(ctx, client, http.MethodGet, fmt.Sprintf("%s/api/clusters/%s/services", baseURL, clusterName), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch services for cluster %s failed: %w", clusterName, err)
	}
	defer res.Body.Close()

	var services []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&services); err != nil {
		return nil, fmt.Errorf("decode services data for cluster %s failed: %w", clusterName, err)
	}

	names := make([]string, 0, len(services))
	for _, svc := range services {
		serviceID := fmt.Sprint(svc["Service_ID"])
		name, exists := appNames[serviceID]
		if !exists {
			fmt.Printf("Unknown serviceId(%s)", serviceID)
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// FetchServicesRequests collects the request statistics of every service in
// every cluster, keyed by cluster name and then by service name.
func FetchServicesRequests(ctx context.Context, client *http.Client, baseURL string, collectDelay bool) (map[string]map[string]ServicesRequests, error) {
	res, err := DoJSON(ctx, client, http.MethodGet, fmt.Sprintf("%s/api/clusters", baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch clusters failed: %w", err)
	}
	defer res.Body.Close()

	var clusters []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&clusters); err != nil {
		return nil, fmt.Errorf("decode clusters data failed: %w", err)
	}

	appNames, err := FetchAppNames(ctx, client, baseURL)
	if err != nil {
		return nil, err
	}

	servicesInCluster := make(map[string][]string)
	allRequests := make(map[string]map[string]ServicesRequests)

	for _, item := range clusters {
		clusterName, ok := item["Cluster_Name"].(string)
		if !ok {
			continue
		}
		names, err := FetchClusterServiceNames(ctx, client, baseURL, clusterName, appNames)
		if err != nil {
			return nil, err
		}
		for _, serviceName := range names {
			servicesInCluster[clusterName] = append(servicesInCluster[clusterName], serviceName)

			servicesRequests, err := FetchJaegerTraces(ctx, client, JaegerScan{
				BaseURL:      baseURL,
				ClusterName:  clusterName,
				ServiceName:  serviceName,
				CollectDelay: collectDelay,
			})
			if err != nil {
				return nil, err
			}
			if allRequests[clusterName] == nil {
				allRequests[clusterName] = make(map[string]ServicesRequests)
			}
			allRequests[clusterName][serviceName] = servicesRequests
		}
	}

	fmt.Printf("All app:\n %v\n", clusters)
	fmt.Printf("All clusters:\n %v\n", clusters)
	fmt.Printf("Services in each cluster:\n %v\n", servicesInCluster)

	return allRequests, nil
}
