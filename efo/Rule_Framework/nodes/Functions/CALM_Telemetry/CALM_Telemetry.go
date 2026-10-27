/*
Package CALM_Telemetry is stage 1 of the CALM adaptive loop: it reads the
control plane and Jaeger once per slot and publishes one flat state vector that
every later stage consumes.

What it produces

  - The domain-wide calm.StateVector, flattened by StateVector.ToMap, so the
    GUI's If/Compare nodes can address a single field.
  - Per-service detail as flat keys (svc_<name>_rate, svc_<name>_replicas,
    svc_<name>_d99_ms), which the policy-mode node turns into calm.ServiceMetrics.
  - telemetry_ok, plus the provenance fields (d99_source, injected_fields,
    sampling_truncated) that let a reader judge how solid the numbers are.

Compute utilisation is reservation-based: the sum of every deployment's
per-replica CPU reservation times its replica count, over the cluster's node
capacity. That is what the inventory can report -- there is no live cgroup
feed -- and it matches the simulator's semantics, where utilisation follows the
provisioned replicas rather than instantaneous usage. It also keeps the loop
closed: changing replicas moves the reading.

Radio and energy have no source on the testbed. They come from the calm
collector interfaces, whose v1 implementations are stubs: the radio sample is
flagged masked, and the context builder substitutes its neutral value for all
four radio dimensions. Wiring a real RAN or power feed later means implementing
the interface, with no change here.

Failure semantics: any collection failure yields telemetry_ok=false with a zero
state and a reason, never a panic. Engine walkers have no recover, so a nil
dereference here would take the whole EFO down rather than the one policy.
*/
package CALM_Telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"time"

	i "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/nodes/Functions/calm"
	u "1935/efo/Rule_Framework/nodes/utils"
)

// Property is the node's design-time configuration.
type Property struct {
	// Target_cluster is the decision domain: the one cluster this policy reads
	// and later scales. It is required; an empty value produces
	// telemetry_ok=false rather than a guess at which cluster was meant.
	Target_cluster string `yaml:"target_cluster"`
	// Sample_window_seconds is how far back the Jaeger scan reaches. It is the
	// denominator of every request rate.
	Sample_window_seconds int `yaml:"sample_window_seconds"`
	// Jaeger_limit caps the number of trace pages read per service. The proxy
	// truncates a page at about 4KB, so a page holds a single trace and this is
	// effectively the sample count per service. Reaching it sets
	// sampling_truncated, and the rates are then a lower bound.
	Jaeger_limit int `yaml:"jaeger_limit"`
	// Inject_file points at an optional JSON file whose state-vector fields
	// overwrite the measured ones. It is the demo scenario hook: a driver script
	// rewrites the file between slots. Empty disables injection, and a missing
	// file is not an error.
	Inject_file string `yaml:"inject_file"`
}

// OutputFormat is the fixed part of the payload. The per-service keys are added
// on top, so the packet is assembled as a map; this struct names the fields the
// schema and the downstream nodes rely on.
type OutputFormat struct {
	// TelemetryOk reports that every collection succeeded. When false the state
	// fields are zero and the selector holds the previous mode for this slot.
	TelemetryOk bool `mapstructure:"telemetry_ok"`
	// TelemetryReason explains a false TelemetryOk, and is empty otherwise.
	TelemetryReason string `mapstructure:"telemetry_reason"`
	// TargetCluster echoes the decision domain into the trace.
	TargetCluster string `mapstructure:"target_cluster"`
	// D99Source records how mean_d99_ms was derived: "p99" from a real
	// percentile, "max" from too few samples to rank one, "no-samples" when no
	// service produced a latency sample.
	D99Source string `mapstructure:"d99_source"`
	// InjectedFields lists the state fields the inject file overwrote, sorted.
	InjectedFields []string `mapstructure:"injected_fields"`
	// Services lists the services of the decision domain, sorted. It saves a
	// consumer from having to recover the names by scanning key prefixes.
	Services []string `mapstructure:"services"`
	// SamplingTruncated reports that at least one service hit Jaeger_limit
	// before the sampling window was exhausted, making its rate a lower bound.
	SamplingTruncated bool `mapstructure:"sampling_truncated"`
	// EnergyAvailable reports whether the energy reading is measured. It is
	// false for the whole of v1; the reward uses its replica-count proxy.
	EnergyAvailable bool `mapstructure:"energy_available"`
	// TotalPowerW and TransitionCostJ carry the energy sample when one exists.
	TotalPowerW     float64 `mapstructure:"total_power_w"`
	TransitionCostJ float64 `mapstructure:"transition_cost_j"`
}

var Description = i.NodeDescription{
	NodeType:      "CALM_Telemetry",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type CALM_Telemetry struct {
	i.FunctionNode
}

// Property defaults, applied when the design-time value is absent or not
// positive.
const (
	defaultSampleWindowSeconds = 60
	defaultJaegerLimit         = 20
)

// collectTimeout bounds one control-plane or Jaeger request. The scan issues one
// request per trace, so this is a per-request bound, not a budget for the slot;
// the run context ends the whole scan when the slot is superseded.
const collectTimeout = 10 * time.Second

// Values of the d99_source output field.
const (
	d99SourceP99       = "p99"
	d99SourceMax       = "max"
	d99SourceNoSamples = "no-samples"
)

// minPercentileSamples is the sample count below which the nearest-rank 99th
// percentile is just the maximum. Reporting the maximum and saying so is more
// honest than dressing it up as a percentile.
const minPercentileSamples = 100

// Radio and energy readings come through the calm collector seams. They are
// package-level because the collector is a property of the deployment, not of
// one node instance, and they are assigned once here: nothing reassigns them at
// runtime, so concurrent walkers only ever read them.
var (
	radioCollector  calm.RadioCollector  = calm.NewStubRadioCollector()
	energyCollector calm.EnergyCollector = calm.NewStubEnergyCollector()
)

// Output keys that are not part of OutputFormat.
const (
	keyServicePrefix  = "svc_"
	keyRateSuffix     = "_rate"
	keyReplicasSuffix = "_replicas"
	keyD99Suffix      = "_d99_ms"
)

// serviceSample is one service's measured detail for the slot.
type serviceSample struct {
	Name     string
	Rate     float64
	Replicas int
	D99Ms    float64
}

// reading is everything one collection produced.
type reading struct {
	State     calm.StateVector
	Services  []serviceSample
	D99Source string
	Truncated bool
}

func (n *CALM_Telemetry) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	if property.Target_cluster == "" {
		return failed("", "target_cluster property is empty")
	}

	cfg, err := calm.Shared()
	if err != nil || cfg == nil {
		return failed(property.Target_cluster, fmt.Sprintf("calm configuration unavailable: %v", err))
	}

	window := time.Duration(defaultSampleWindowSeconds) * time.Second
	if property.Sample_window_seconds > 0 {
		window = time.Duration(property.Sample_window_seconds) * time.Second
	}
	maxPages := defaultJaegerLimit
	if property.Jaeger_limit > 0 {
		maxPages = property.Jaeger_limit
	}

	ctx, cancel := u.RunContext(info)
	defer cancel()
	client := &http.Client{Timeout: collectTimeout}

	collected, err := collect(ctx, client, info.ControlPlaneBaseURL, property.Target_cluster, window, maxPages, cfg, time.Now())
	if err != nil {
		fmt.Println(Description.NodeType+": collection failed,", err)
		return failed(property.Target_cluster, err.Error())
	}

	injected, err := applyInjection(property.Inject_file, &collected.State)
	if err != nil {
		fmt.Println(Description.NodeType+": injection failed,", err)
		return failed(property.Target_cluster, err.Error())
	}

	energy, energyErr := collectEnergy()
	if energyErr != nil {
		fmt.Println(Description.NodeType+": energy collection failed,", energyErr)
		return failed(property.Target_cluster, energyErr.Error())
	}

	names := make([]string, 0, len(collected.Services))
	for _, svc := range collected.Services {
		names = append(names, svc.Name)
	}

	output := OutputFormat{
		TelemetryOk:       true,
		TargetCluster:     property.Target_cluster,
		D99Source:         collected.D99Source,
		InjectedFields:    injected,
		Services:          names,
		SamplingTruncated: collected.Truncated,
		EnergyAvailable:   energy.Available,
		TotalPowerW:       energy.TotalPowerW,
		TransitionCostJ:   energy.TransitionCostJ,
	}
	return emit(output, collected.State, collected.Services)
}

// emit assembles the packet: the fixed fields, the flattened state vector and
// the per-service detail.
func emit(output OutputFormat, state calm.StateVector, services []serviceSample) i.NodeExecResult {
	if output.InjectedFields == nil {
		output.InjectedFields = []string{}
	}
	if output.Services == nil {
		output.Services = []string{}
	}

	encoded, err := u.EncodeOutput(output, Description)
	if err != nil {
		// EncodeOutput only fails on a malformed struct, which would be a
		// programming error here; the packet still has to be well formed.
		fmt.Println(Description.NodeType+": Encode output error,", err)
		return i.NodeExecResult{Output: u.EmptyOutput(Description.NumOutputPort)}
	}

	packet := encoded[0]
	for key, value := range state.ToMap() {
		packet[key] = value
	}
	for _, svc := range services {
		packet[keyServicePrefix+svc.Name+keyRateSuffix] = svc.Rate
		packet[keyServicePrefix+svc.Name+keyReplicasSuffix] = svc.Replicas
		packet[keyServicePrefix+svc.Name+keyD99Suffix] = svc.D99Ms
	}
	return i.NodeExecResult{Output: encoded}
}

// failed builds the packet for a slot with no usable telemetry: a zero state,
// no services, and the reason.
//
// The zero state keeps radio_masked set. Nothing was measured, so a consumer
// that ignores telemetry_ok must still see the radio block as absent rather than
// learn from four zeros.
func failed(cluster, reason string) i.NodeExecResult {
	state := calm.StateVector{RadioMasked: true}
	output := OutputFormat{
		TelemetryOk:     false,
		TelemetryReason: reason,
		TargetCluster:   cluster,
		D99Source:       d99SourceNoSamples,
	}
	return emit(output, state, nil)
}

// collect reads the decision domain once.
//
// Order: node capacity, the application-id to name map, the deployments, then
// one Jaeger scan per service in sorted name order. Every step is fatal to the
// slot -- a partial state vector would be indistinguishable from a quiet system.
func collect(
	ctx context.Context,
	client *http.Client,
	baseURL, cluster string,
	window time.Duration,
	maxPages int,
	cfg *calm.Config,
	now time.Time,
) (reading, error) {
	capacity, err := calm.FetchClusterNodeTotals(ctx, client, baseURL, cluster)
	if err != nil {
		return reading{}, err
	}
	appNames, err := calm.FetchAppNames(ctx, client, baseURL)
	if err != nil {
		return reading{}, err
	}
	deployments, err := calm.FetchDeployments(ctx, client, baseURL, cluster)
	if err != nil {
		return reading{}, err
	}

	// Reservation totals cover every deployment, including one whose
	// application has gone missing from the inventory: it still holds capacity.
	var reservedCPU, reservedMemory float64
	replicaTotal := 0
	replicas := make(map[string]int, len(deployments))
	for _, deployment := range deployments {
		reservedCPU += deployment.AllocatedCPU * float64(deployment.Replicas)
		reservedMemory += deployment.AllocatedMemory * float64(deployment.Replicas)
		replicaTotal += deployment.Replicas

		name, known := appNames[deployment.ApplicationID]
		if !known || name == "" {
			fmt.Printf("%s: deployment of application %s on cluster %s has no name, per-service detail skipped\n",
				Description.NodeType, deployment.ApplicationID, cluster)
			continue
		}
		replicas[name] = deployment.Replicas
	}

	windowSeconds := window.Seconds()
	if windowSeconds <= 0 {
		return reading{}, fmt.Errorf("sampling window must be positive, got %s", window)
	}
	sinceMicros := now.Add(-window).UnixMicro()
	endMicros := now.UnixMicro()

	out := reading{Services: make([]serviceSample, 0, len(replicas))}
	var rateTotal, d99Total, maxDmr float64
	sampled := 0
	degraded := false
	latencySlo := math.Inf(1)

	for _, name := range sortedKeys(replicas) {
		stats, err := calm.FetchJaegerTraces(ctx, client, calm.JaegerScan{
			BaseURL:      baseURL,
			ClusterName:  cluster,
			ServiceName:  name,
			CollectDelay: true,
			SinceMicros:  sinceMicros,
			EndMicros:    endMicros,
			MaxPages:     maxPages,
		})
		if err != nil {
			return reading{}, err
		}
		if stats.Truncated {
			out.Truncated = true
		}

		slo := cfg.ServiceSLO(name)
		if slo.LatencySloMs > 0 && slo.LatencySloMs < latencySlo {
			latencySlo = slo.LatencySloMs
		}

		d99, source := percentile99(stats.Latencies)
		if source != d99SourceNoSamples {
			sampled++
			d99Total += d99
			degraded = degraded || source == d99SourceMax
		}

		if dmr := deadlineMissRatio(stats.Latencies, slo.LatencySloMs); dmr > maxDmr {
			maxDmr = dmr
		}

		rate := float64(stats.Number) / windowSeconds
		rateTotal += rate
		out.Services = append(out.Services, serviceSample{
			Name:     name,
			Rate:     rate,
			Replicas: replicas[name],
			D99Ms:    d99,
		})
	}

	switch {
	case sampled == 0:
		out.D99Source = d99SourceNoSamples
	case degraded:
		out.D99Source = d99SourceMax
	default:
		out.D99Source = d99SourceP99
	}

	meanD99 := 0.0
	if sampled > 0 {
		meanD99 = d99Total / float64(sampled)
	}
	if math.IsInf(latencySlo, 1) {
		latencySlo = cfg.DefaultService.LatencySloMs
	}

	// Utilisation is a ratio of reservations to capacity, so a cluster that
	// reports no capacity reads as zero rather than as an infinity that would
	// poison the learner.
	meanCPU := ratio(reservedCPU, capacity.CPU)
	out.State = calm.StateVector{
		MeanCpu: meanCPU,
		// The inventory attributes a deployment to a cluster, not to a node, so
		// there is no per-node breakdown to take a maximum over. With a single
		// decision domain the hottest node is not observable and the aggregate
		// stands in for it.
		MaxCpu:  meanCPU,
		MeanMem: ratio(reservedMemory, capacity.Memory),
		// No GPU inventory exists on the testbed.
		MeanGpu:      0,
		TotalReqRate: rateTotal,
		MeanD99Ms:    meanD99,
		MaxDmr:       maxDmr,
		// The domain-wide bound is the tightest service SLO: the context
		// normalises one latency figure, and the strictest service is the one
		// that decides whether the domain is in trouble.
		LatencySloMs: latencySlo,
		ReplicaTotal: replicaTotal,
	}

	radio, err := collectRadio()
	if err != nil {
		return reading{}, err
	}
	out.State.MeanSinr = radio.MeanSinr
	out.State.MeanPrbUtil = radio.MeanPrbUtil
	out.State.HoRate = radio.HoRate
	out.State.MeanIntf = radio.MeanIntf
	out.State.RadioMasked = radio.Masked

	return out, nil
}

// collectRadio reads the radio seam, treating an unwired collector as a masked
// sample rather than as a failure.
func collectRadio() (calm.RadioSample, error) {
	if radioCollector == nil {
		return calm.RadioSample{Masked: true}, nil
	}
	sample, err := radioCollector.CollectRadio()
	if err != nil {
		return calm.RadioSample{}, fmt.Errorf("radio collection failed: %w", err)
	}
	return sample, nil
}

// collectEnergy reads the energy seam, treating an unwired collector as "no
// measurement available", which is what the v1 stub reports anyway.
func collectEnergy() (calm.EnergySample, error) {
	if energyCollector == nil {
		return calm.EnergySample{}, nil
	}
	sample, err := energyCollector.CollectEnergy()
	if err != nil {
		return calm.EnergySample{}, fmt.Errorf("energy collection failed: %w", err)
	}
	return sample, nil
}

// applyInjection overlays the demo inject file onto state and returns the state
// fields it overwrote, sorted.
//
// An empty path or a missing file means no injection, which is the normal state
// between demo scenarios. A file that exists but cannot be read, cannot be
// parsed, or gives a known field the wrong type is an error: the scenario the
// operator asked for did not take effect, and pretending otherwise would make
// the demo lie. A key the state vector does not have is logged and ignored.
//
// Radio masking: StateVector.FromMap already honours the "radio_masked" key, so
// a file can set it explicitly and that always wins. When a file injects one of
// the four radio fields without saying anything about the mask, the mask is
// cleared -- otherwise the injection would be inert, because the context builder
// substitutes its neutral value for every radio dimension while the mask is on.
// The implied clear is reported in injected_fields like any other override.
func applyInjection(path string, state *calm.StateVector) ([]string, error) {
	if path == "" || state == nil {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read inject file %q: %w", path, err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("cannot parse inject file %q: %w", path, err)
	}

	known := calm.StateVector{}.ToMap()
	applied := make([]string, 0, len(fields))
	radioInjected := false
	for _, key := range sortedKeys(fields) {
		if _, ok := known[key]; !ok {
			fmt.Printf("%s: inject file %s has unknown field %q, ignored\n", Description.NodeType, path, key)
			continue
		}
		applied = append(applied, key)
		if radioFields[key] {
			radioInjected = true
		}
	}

	if err := state.FromMap(fields); err != nil {
		return nil, fmt.Errorf("inject file %q: %w", path, err)
	}

	if _, explicit := fields[calm.KeyRadioMasked]; radioInjected && !explicit {
		state.RadioMasked = false
		applied = append(applied, calm.KeyRadioMasked)
		sort.Strings(applied)
	}
	return applied, nil
}

// radioFields are the state fields whose injection implies the radio sample is
// no longer masked.
var radioFields = map[string]bool{
	calm.KeyMeanSinr:    true,
	calm.KeyMeanPrbUtil: true,
	calm.KeyHoRate:      true,
	calm.KeyMeanIntf:    true,
}

// percentile99 returns the nearest-rank 99th percentile of the latency samples
// and how it was obtained.
func percentile99(latencies []int64) (float64, string) {
	if len(latencies) == 0 {
		return 0, d99SourceNoSamples
	}
	sorted := append([]int64(nil), latencies...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
	if len(sorted) < minPercentileSamples {
		return float64(sorted[len(sorted)-1]), d99SourceMax
	}
	index := int(math.Ceil(0.99*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return float64(sorted[index]), d99SourceP99
}

// deadlineMissRatio approximates DMR as the share of sampled requests that
// exceeded the service's latency bound. It is an approximation because Jaeger
// samples traces rather than recording every request; post-verification uses the
// same one, so the two stages agree.
func deadlineMissRatio(latencies []int64, sloMs float64) float64 {
	if len(latencies) == 0 || sloMs <= 0 {
		return 0
	}
	missed := 0
	for _, latency := range latencies {
		if float64(latency) > sloMs {
			missed++
		}
	}
	return float64(missed) / float64(len(latencies))
}

// ratio divides while treating a non-positive or non-finite denominator as
// "unknown", which reads as zero.
func ratio(value, total float64) float64 {
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return 0
	}
	result := value / total
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0
	}
	return result
}

// sortedKeys returns the keys of m in ascending order. Go randomises map
// iteration, so every ordered output in this package is built from it.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
