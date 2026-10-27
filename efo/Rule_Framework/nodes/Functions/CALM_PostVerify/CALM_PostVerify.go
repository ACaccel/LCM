/*
Package CALM_PostVerify is stage 4 of the CALM loop: it re-reads the decision
domain after the placement has been applied, judges the slot against the SLO,
and closes the two feedback edges the graph cannot carry.

Where it sits: the graph feeds it from a Wait node placed after
CALM_PlacementApply, so the replica changes have had time to take effect before
anything is measured. The flat payload it receives has accumulated the keys of
every upstream stage; this node reads target_cluster and services from the
telemetry stage and mode from the stage that decided it.

What it writes back, in the order the simulator's post-verification stage uses:

 1. RecordViolation advances the recovery state machine.
 2. The reward is computed from the state that advance produced, so the reward
    and the reported recovery_state describe the same slot. A violation streak
    that tips the domain into soft recovery is paid for in the very slot it
    tips, not the next one.
 3. Omega^stable is refreshed only when the slot both sits in NORMAL and
    recorded no violation.
 4. The pending decision receives its reward, which the next slot's selector
    folds into the bandit.
 5. PrevSevere carries this slot's severe flag into the next slot's guard
    bypass.

Failure semantics: any collection failure yields verify_ok=false with a reason
and no state mutation at all. A slot that could not be verified must not append
a fabricated "clean" flag to the recovery window, and must not hand the bandit a
reward derived from measurements that were never taken. Nothing here panics: the
engine's walkers have no recover, so a nil dereference would take the whole EFO
process down rather than the one policy.

All HTTP happens before the loop state is locked. The critical section is a few
arithmetic operations, so a slow trace backend never holds up another policy
working on the same service.
*/
package CALM_PostVerify

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	lcm "1935/efo/LCM_Enabler/interfaces"
	"1935/efo/Rule_Framework/config"
	i "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/nodes/Functions/calm"
	u "1935/efo/Rule_Framework/nodes/utils"
)

// Property is the node's design-time configuration.
type Property struct {
	// Verify_window_seconds is how far back the post-placement Jaeger scan
	// reaches. It should be shorter than the slot period and longer than the
	// Wait node in front of this node, so the samples belong to the placement
	// that was just applied.
	Verify_window_seconds int `yaml:"verify_window_seconds"`
}

// OutputFormat is the fixed part of the slot's report card. The per-service keys
// are added on top, because their names depend on the services of the domain and
// cannot be struct fields.
//
// The tags are mapstructure tags rather than yaml ones because that is what the
// port encoder and the design-time schema both read for a port format.
type OutputFormat struct {
	// VerifyOk reports that the domain was measured and judged. When false every
	// verdict field below is zero, no state was advanced, and VerifyReason says
	// why.
	VerifyOk bool `mapstructure:"verify_ok"`
	// VerifyReason explains a false VerifyOk, and is empty otherwise.
	VerifyReason string `mapstructure:"verify_reason"`
	// Mode echoes the policy mode whose placement was verified.
	Mode string `mapstructure:"mode"`
	// Violated is V: some service missed its DMR or tail-latency bound.
	Violated bool `mapstructure:"violated"`
	// Severe is V_severe: some service crossed a critical threshold. It lets the
	// next slot bypass the stability guard.
	Severe bool `mapstructure:"severe"`
	// RecoveryState is the phase after this slot's violation was recorded.
	RecoveryState string `mapstructure:"recovery_state"`
	// Reward is the scalar the bandit learns from, and Score is the penalty it
	// negates and normalises.
	Reward float64 `mapstructure:"reward"`
	Score  float64 `mapstructure:"score"`
	// The four Score components, so a demo can show why a slot scored the way it
	// did without recomputing the formula.
	SloRisk      float64 `mapstructure:"slo_risk"`
	EnergyCost   float64 `mapstructure:"energy_cost"`
	SwitchCost   float64 `mapstructure:"switch_cost"`
	RecoveryCost float64 `mapstructure:"recovery_cost"`
	// DmrSource records how the deadline-miss ratios were derived:
	// "latency-samples" from the trace scan, "no-samples" when no service
	// produced one and every ratio is therefore zero by definition rather than by
	// measurement.
	DmrSource string `mapstructure:"dmr_source"`
}

// Input payload keys. They are the contract with the upstream stages:
// target_cluster and services come from CALM_Telemetry, mode from whichever
// stage last published it (selector, policy mode, or placement apply -- all
// three carry the same value in a slot that reached a decision).
const (
	KeyTargetCluster = "target_cluster"
	KeyServices      = "services"
)

// Output key prefixes for the per-service detail.
const (
	keyD99Prefix      = "d99_"
	keyDmrPrefix      = "dmr_"
	keyViolatedPrefix = "v_"
	keyReplicasPrefix = "replicas_"
)

// Values of the dmr_source output field.
const (
	dmrSourceLatencySamples = "latency-samples"
	dmrSourceNoSamples      = "no-samples"
)

const (
	// defaultVerifyWindowSeconds is the sampling window when the property is
	// unset. It is shorter than the telemetry stage's window on purpose: this
	// scan must not reach back past the placement it is judging.
	defaultVerifyWindowSeconds = 30
	// verifyJaegerPages bounds the trace walk per service. The Jaeger proxy
	// truncates a page at about 4KB, so a page holds a single trace and this is
	// effectively the sample count per service. It matches the telemetry stage's
	// default so both stages judge a service on a comparable number of samples.
	verifyJaegerPages = 20
)

var Description = i.NodeDescription{
	NodeType:      "CALM_PostVerify",
	NumInputPort:  1,
	NumOutputPort: 1,
}

// CALM_PostVerify holds no state of its own: everything that survives a slot
// lives in the shared calm loop state, which outlives the node instance a
// redeploy replaces.
type CALM_PostVerify struct {
	i.FunctionNode
}

// reading is what one post-placement collection produced.
type reading struct {
	// Observations are the per-service measurements, in sorted service order.
	Observations []calm.ServiceObservation
	// Replicas is the live replica count of each verified service, and the
	// rollback target Omega^stable is refreshed from.
	Replicas map[string]int
	// ReplicaTotal is the energy proxy's numerator: the replica footprint of the
	// verified services, so it agrees with the replicas_<svc> keys on the wire.
	ReplicaTotal int
	// Sampled reports that at least one service produced a latency sample.
	Sampled bool
}

func (n *CALM_PostVerify) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	window := time.Duration(defaultVerifyWindowSeconds) * time.Second
	if property.Verify_window_seconds > 0 {
		window = time.Duration(property.Verify_window_seconds) * time.Second
	}

	flat := u.ContentOf(u.FirstInputPacket(input))
	mode := stringAt(flat, calm.KeyMode)

	cluster := stringAt(flat, KeyTargetCluster)
	if cluster == "" {
		return failed(mode, fmt.Sprintf("payload carries no %q: nothing to verify", KeyTargetCluster))
	}
	services := serviceNames(flat)
	if len(services) == 0 {
		return failed(mode, fmt.Sprintf("payload carries no %q: nothing to verify", KeyServices))
	}

	cfg, err := calm.Shared()
	if err != nil || cfg == nil {
		return failed(mode, fmt.Sprintf("calm configuration unavailable: %v", err))
	}

	ctx, cancel := u.RunContext(info)
	defer cancel()

	baseURL := info.ControlPlaneBaseURL
	if baseURL == "" {
		baseURL = config.ControlPlaneBaseURL()
	}
	client := &http.Client{Timeout: time.Duration(config.HTTPTimeoutSeconds() * float64(time.Second))}

	collected, err := collect(ctx, client, baseURL, cluster, services, window, cfg, time.Now())
	if err != nil {
		fmt.Println(Description.NodeType+": collection failed,", err)
		return failed(mode, err.Error())
	}

	result := cfg.Verify(collected.Observations)

	loop, err := calm.StateFor(info.Service_Name, cfg)
	if err != nil {
		return failed(mode, fmt.Sprintf("loop state unavailable: %v", err))
	}

	var (
		breakdown     calm.RewardBreakdown
		recoveryState calm.RecoveryState
		stableMode    calm.Mode
		incomplete    bool
	)
	// One critical section for the whole slot: the state machine advance, the
	// reward computed from the state it produced, and the two write-backs must
	// not be interleaved with another policy verifying the same service.
	loop.Do(func(l *calm.Loop) {
		if l == nil || l.Recovery == nil || l.Reward == nil {
			incomplete = true
			return
		}

		// The state machine advances before the reward, so the recovery cost the
		// bandit is charged is the cost of the state this slot's violation caused.
		l.Recovery.RecordViolation(result.Violated)
		recoveryState = l.Recovery.State()

		// The switch cost belongs to the decision this reward pays for, which is
		// the pending one recorded by the selector earlier in this same slot.
		switched := false
		if l.Pending != nil {
			switched = l.Pending.Switched
		}
		breakdown = l.Reward.Compute(calm.RewardInput{
			DmrExcess:     result.DmrExcess,
			LatencyExcess: result.LatencyExcess,
			Severe:        result.Severe,
			ReplicaTotal:  collected.ReplicaTotal,
			Switched:      switched,
			RecoveryState: recoveryState,
		})

		if recoveryState == calm.RecoveryNormal && !result.Violated {
			stableMode = verifiedMode(mode, l.PrevMode)
			l.Recovery.UpdateStable(stableMode, collected.Replicas)
		}

		if l.Pending != nil {
			l.Pending.Reward = breakdown.Reward
			l.Pending.RewardReady = true
		}
		l.PrevSevere = result.Severe
	})
	if incomplete {
		return failed(mode, "loop state is incomplete")
	}

	out := OutputFormat{
		VerifyOk:      true,
		Mode:          mode,
		Violated:      result.Violated,
		Severe:        result.Severe,
		RecoveryState: string(recoveryState),
		Reward:        breakdown.Reward,
		Score:         breakdown.Score,
		SloRisk:       breakdown.SloRisk,
		EnergyCost:    breakdown.EnergyCost,
		SwitchCost:    breakdown.SwitchCost,
		RecoveryCost:  breakdown.RecoveryCost,
		DmrSource:     dmrSource(collected.Sampled),
	}
	return emit(out, collected, result)
}

// collect re-reads the decision domain after the placement was applied: the
// live replica counts from the control plane, and one Jaeger scan per service
// over the verification window.
//
// Every step is fatal to the slot. A partial reading would be indistinguishable
// from a domain that is quiet and healthy, and the reward built from it would
// train the bandit on a fiction.
func collect(
	ctx context.Context,
	client *http.Client,
	baseURL, cluster string,
	services []string,
	window time.Duration,
	cfg *calm.Config,
	now time.Time,
) (reading, error) {
	if window <= 0 {
		return reading{}, fmt.Errorf("verification window must be positive, got %s", window)
	}

	appNames, err := calm.FetchAppNames(ctx, client, baseURL)
	if err != nil {
		return reading{}, err
	}
	deployments, err := calm.FetchDeployments(ctx, client, baseURL, cluster)
	if err != nil {
		return reading{}, err
	}
	// The control plane identifies a deployment by application id; the services
	// list, Jaeger and the SLO table all address it by name.
	live := make(map[string]int, len(deployments))
	for _, deployment := range deployments {
		name, known := appNames[deployment.ApplicationID]
		if !known || name == "" {
			fmt.Printf("%s: deployment of application %s on cluster %s has no name, replicas not attributed\n",
				Description.NodeType, deployment.ApplicationID, cluster)
			continue
		}
		live[name] = deployment.Replicas
	}

	sinceMicros := now.Add(-window).UnixMicro()
	endMicros := now.UnixMicro()

	out := reading{
		Observations: make([]calm.ServiceObservation, 0, len(services)),
		Replicas:     make(map[string]int, len(services)),
	}
	for _, name := range services {
		stats, err := calm.FetchJaegerTraces(ctx, client, calm.JaegerScan{
			BaseURL:      baseURL,
			ClusterName:  cluster,
			ServiceName:  name,
			CollectDelay: true,
			SinceMicros:  sinceMicros,
			EndMicros:    endMicros,
			MaxPages:     verifyJaegerPages,
		})
		if err != nil {
			return reading{}, err
		}
		if len(stats.Latencies) > 0 {
			out.Sampled = true
		}

		slo := cfg.ServiceSLO(name)
		out.Observations = append(out.Observations, calm.ServiceObservation{
			Service: name,
			Dmr:     deadlineMissRatio(stats.Latencies, slo.LatencySloMs),
			D99Ms:   maxLatency(stats.Latencies),
		})
		// A service the control plane does not report is recorded at zero rather
		// than dropped: the report card should show that it is not running.
		out.Replicas[name] = live[name]
		out.ReplicaTotal += live[name]
	}
	return out, nil
}

// deadlineMissRatio approximates the deadline-miss ratio as the share of sampled
// requests that exceeded the service's latency bound.
//
// It is an approximation because Jaeger samples traces rather than recording
// every request. The telemetry stage uses the identical approximation, so the
// ratio this stage verifies against the SLO is the same quantity the autoscaler
// planned from. With no sample at all the ratio is zero -- nothing was observed
// to miss -- and dmr_source says so, because a zero that means "no evidence" is
// not the same as a zero that means "no misses".
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

// maxLatency returns the tail latency of the verification window.
//
// The window is a fraction of a slot and the scan is capped at a few dozen
// traces, which is far too few to rank a 99th percentile: the nearest-rank
// percentile of such a sample is its maximum anyway. Reporting the maximum keeps
// the verdict conservative -- a service is judged on its worst observed request
// -- and matches the telemetry stage's own small-sample path.
func maxLatency(latencies []int64) float64 {
	worst := int64(0)
	for _, latency := range latencies {
		if latency > worst {
			worst = latency
		}
	}
	return float64(worst)
}

// verifiedMode resolves the mode whose placement this slot verified. The payload
// value wins when it names one of the four canonical modes; otherwise the mode
// the selector committed is used, since that is what the slot actually ran.
func verifiedMode(payloadMode string, prev calm.Mode) calm.Mode {
	mode := calm.Mode(payloadMode)
	if _, ok := calm.ModeIndex(mode); ok {
		return mode
	}
	return prev
}

// dmrSource names how the deadline-miss ratios were obtained.
func dmrSource(sampled bool) string {
	if sampled {
		return dmrSourceLatencySamples
	}
	return dmrSourceNoSamples
}

// serviceNames reads the services list the telemetry stage published, sorted and
// deduplicated. Both the in-process []string and the JSON round-tripped []any
// forms are accepted, and an entry that is not a non-empty string is skipped.
func serviceNames(flat lcm.DataPacketContent) []string {
	raw, found := flat[KeyServices]
	if !found {
		return nil
	}

	var listed []string
	switch typed := raw.(type) {
	case []string:
		listed = typed
	case []any:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				listed = append(listed, name)
			}
		}
	default:
		return nil
	}

	seen := make(map[string]bool, len(listed))
	names := make([]string, 0, len(listed))
	for _, name := range listed {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// stringAt reads one payload field as a string, yielding "" for an absent field
// or one holding something else.
func stringAt(flat lcm.DataPacketContent, key string) string {
	value, found := flat[key]
	if !found {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// failed builds the packet for a slot that could not be verified: the reason,
// and no per-service detail because nothing was measured.
//
// No loop state was touched on this path. Appending a violation flag for a slot
// nobody measured would shift the recovery window on evidence that does not
// exist, and marking the pending decision ready would train the bandit on a
// reward of zero components. Both are left for the next slot that can actually
// see the domain; the pending decision simply waits.
func failed(mode, reason string) i.NodeExecResult {
	fmt.Println(Description.NodeType+":", reason)
	return emit(OutputFormat{
		VerifyOk:     false,
		VerifyReason: reason,
		Mode:         mode,
		DmrSource:    dmrSourceNoSamples,
	}, reading{}, calm.VerificationResult{})
}

// emit encodes the fixed report card and adds the per-service keys on top, in
// sorted service order: Go randomises map iteration, so every ordered part of
// the output is built from the observation slice the collection filled in order.
func emit(out OutputFormat, collected reading, result calm.VerificationResult) i.NodeExecResult {
	encoded, err := u.EncodeOutput(out, Description)
	if err != nil {
		// EncodeOutput only fails on a malformed struct, which would be a
		// programming error here; the packet still has to be well formed.
		fmt.Println(Description.NodeType+": Encode output error,", err)
		encoded = u.EmptyOutput(Description.NumOutputPort)
	}
	packet := u.ContentOf(encoded[0])
	if packet == nil {
		packet = lcm.DataPacketContent{}
		encoded[0] = packet
	}

	for _, observation := range collected.Observations {
		name := observation.Service
		verdict := result.PerService[name]
		packet[keyD99Prefix+name] = observation.D99Ms
		packet[keyDmrPrefix+name] = observation.Dmr
		packet[keyViolatedPrefix+name] = verdict.DmrViolated || verdict.TailViolated
		packet[keyReplicasPrefix+name] = collected.Replicas[name]
	}
	return i.NodeExecResult{Output: encoded, Ports: []int{0}}
}
