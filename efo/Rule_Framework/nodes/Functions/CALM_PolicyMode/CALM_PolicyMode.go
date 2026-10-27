/*
Package CALM_PolicyMode is stage 3 of the CALM loop: it turns one policy mode's
knob set into a placement plan.

The same node type is instantiated once per mode in the demo graph, each instance
carrying its own knob values, so the difference between latency_priority,
balanced, energy_saving and resilient is visible in the GUI inspector as
configuration rather than hidden in code.

Everything here is pure computation over the telemetry payload and the shared
calm state: no HTTP and nothing that blocks, so the node never delays the run it
belongs to. The autoscaler and the recovery state machine live in the calm
package; this node decodes properties, rebuilds the autoscaler's inputs from the
flat payload, and flattens the resulting plan back onto it.
*/
package CALM_PolicyMode

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	lcm "1935/efo/LCM_Enabler/interfaces"
	i "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/nodes/Functions/calm"
	u "1935/efo/Rule_Framework/nodes/utils"
)

// Property is the mode's identity plus the thirteen knobs of calm.PolicyParams.
// The knob defaults are the balanced mode's calibrated values, so an instance
// overrides only what its own mode changes.
//
// The calibration comes from the simulator this was ported from and is at
// simulator scale; it is a starting point on the testbed, not a tuning.
type Property struct {
	// Mode names the policy mode this instance represents. It is required and
	// must be one of the four canonical modes; anything else is reported through
	// plan_ok rather than guessed at.
	Mode                     string  `yaml:"mode"`
	Scale_out_threshold      float64 `yaml:"scale_out_threshold"`
	Scale_in_threshold       float64 `yaml:"scale_in_threshold"`
	Scale_step               int     `yaml:"scale_step"`
	Scale_in_dmr_tolerance   float64 `yaml:"scale_in_dmr_tolerance"`
	Redundancy_target        int     `yaml:"redundancy_target"`
	Redundancy_factor        float64 `yaml:"redundancy_factor"`
	Offloading_mode          string  `yaml:"offloading_mode"`
	Offload_fraction         float64 `yaml:"offload_fraction"`
	Migration_allowed        string  `yaml:"migration_allowed"`
	Migration_cost_weight    float64 `yaml:"migration_cost_weight"`
	Recovery_behavior        string  `yaml:"recovery_behavior"`
	Radio_compression_factor float64 `yaml:"radio_compression_factor"`
	Node_selection           string  `yaml:"node_selection"`
}

// OutputFormat is the node's fixed output surface. The plan's own fields
// (replica_target, migrations, offloads, radio_compression) and the per-service
// keys plan_<svc>_replicas and risk_<svc> are added on top of the encoded struct,
// because neither a service-keyed map nor a nested plan can be described by a
// fixed set of struct fields.
type OutputFormat struct {
	Mode            string `mapstructure:"mode"`
	Recovery_action string `mapstructure:"recovery_action"`
	Reason          string `mapstructure:"reason"`
	Plan_ok         bool   `mapstructure:"plan_ok"`
}

// Flat payload keys. The input pair is the contract with CALM_Telemetry; the
// output pair is what CALM_PlacementApply and the GUI's If/Compare nodes read.
const (
	inputServicePrefix  = "svc_"
	inputRateSuffix     = "_rate"
	inputReplicasSuffix = "_replicas"

	outputPlanPrefix       = "plan_"
	outputPlanReplicasSuff = "_replicas"
	outputRiskPrefix       = "risk_"
)

var Description = i.NodeDescription{
	NodeType:      "CALM_PolicyMode",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type CALM_PolicyMode struct {
	i.FunctionNode
}

func (n *CALM_PolicyMode) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	property := defaultProperty()
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	params := policyParams(property)
	if err := params.Validate(); err != nil {
		return softFail(property.Mode, err.Error())
	}

	flat := u.ContentOf(u.FirstInputPacket(input))
	var state calm.StateVector
	if err := state.FromMap(flat); err != nil {
		return softFail(property.Mode, fmt.Sprintf("unusable telemetry state: %v", err))
	}
	services, names, current := parseServices(flat)
	if len(names) == 0 {
		// No service metrics means there is nothing to plan for. Saying so is
		// better than emitting an empty plan the apply stage would treat as a
		// deliberate "scale everything to nothing".
		return softFail(property.Mode, fmt.Sprintf("no per-service metrics in input (expected %s<name>%s and %s<name>%s)",
			inputServicePrefix, inputRateSuffix, inputServicePrefix, inputReplicasSuffix))
	}

	cfg, err := calm.Shared()
	if err != nil {
		return softFail(property.Mode, fmt.Sprintf("configuration error: %v", err))
	}
	loopState, err := calm.StateFor(info.Service_Name, cfg)
	if err != nil {
		return softFail(property.Mode, fmt.Sprintf("loop state error: %v", err))
	}

	var (
		plan   calm.PlacementPlan
		risks  map[string]float64
		action = calm.RecoveryActionNone
		failed string
	)
	// One critical section for the whole slot: the recovery phase read here and
	// the plan built from it must belong to the same state, and another policy
	// touching the same service must not interleave between them.
	loopState.Do(func(l *calm.Loop) {
		if l == nil || l.Autoscaler == nil || l.Recovery == nil {
			failed = "loop state is incomplete"
			return
		}
		plan = l.Autoscaler.ComputePlan(state, services, params)
		risks = risksFromReason(plan.Reason, names)

		var actionErr error
		action, actionErr = calm.RecoveryActionFor(params.RecoveryBehavior, l.Recovery.State())
		if actionErr != nil {
			// Validate already rejects an unknown behaviour, so this is
			// unreachable; holding the autoscaler's own output is the safe
			// reading of "no recovery strategy applies".
			fmt.Println(Description.NodeType+": recovery action error,", actionErr)
			action = calm.RecoveryActionNone
		}
		// A missing Omega^stable must be passed as nil, not as the zero value:
		// the zero OmegaStable carries an empty replica map, and restoring that
		// would wipe the plan instead of skipping the restore.
		var stable *calm.OmegaStable
		if omega, ok := l.Recovery.StableConfig(); ok {
			stable = &omega
		}
		calm.ApplyRecoveryAction(&plan, action, params, current, stable)
	})
	if failed != "" {
		return softFail(property.Mode, failed)
	}

	return emit(plan, action, names, risks, true)
}

// defaultProperty is the balanced mode's calibrated knob set, taken from calm so
// the node cannot drift from the configuration the rest of the loop uses. Mode
// has no default: it is required, and guessing one would attach a knob set to
// the wrong arm.
func defaultProperty() Property {
	params, err := calm.DefaultPolicyParams(calm.ModeBalanced)
	if err != nil {
		// Unreachable: balanced is one of the four canonical modes. Leaving the
		// knobs at zero makes Validate refuse the run with a reason instead of
		// planning from degenerate values.
		fmt.Println(Description.NodeType+": default policy params error,", err)
		return Property{}
	}
	return Property{
		Scale_out_threshold:      params.ScaleOutThreshold,
		Scale_in_threshold:       params.ScaleInThreshold,
		Scale_step:               params.ScaleStep,
		Scale_in_dmr_tolerance:   params.ScaleInDmrTolerance,
		Redundancy_target:        params.RedundancyTarget,
		Redundancy_factor:        params.RedundancyFactor,
		Offloading_mode:          params.OffloadingMode,
		Offload_fraction:         params.OffloadFraction,
		Migration_allowed:        params.MigrationAllowed,
		Migration_cost_weight:    params.MigrationCostWeight,
		Recovery_behavior:        params.RecoveryBehavior,
		Radio_compression_factor: params.RadioCompressionFactor,
		Node_selection:           params.NodeSelection,
	}
}

// policyParams maps the node properties onto the knob set the calm package
// works with.
func policyParams(p Property) calm.PolicyParams {
	return calm.PolicyParams{
		Mode:                   calm.Mode(p.Mode),
		ScaleOutThreshold:      p.Scale_out_threshold,
		ScaleInThreshold:       p.Scale_in_threshold,
		ScaleStep:              p.Scale_step,
		ScaleInDmrTolerance:    p.Scale_in_dmr_tolerance,
		RedundancyTarget:       p.Redundancy_target,
		RedundancyFactor:       p.Redundancy_factor,
		OffloadingMode:         p.Offloading_mode,
		OffloadFraction:        p.Offload_fraction,
		MigrationAllowed:       p.Migration_allowed,
		MigrationCostWeight:    p.Migration_cost_weight,
		RecoveryBehavior:       p.Recovery_behavior,
		RadioCompressionFactor: p.Radio_compression_factor,
		NodeSelection:          p.Node_selection,
	}
}

// parseServices rebuilds the autoscaler's per-service inputs from the flat
// telemetry keys svc_<name>_rate and svc_<name>_replicas. It returns the metrics,
// the service names in sorted order, and the live replica counts the recovery
// no-op guard compares against.
//
// A service named by only one of the two keys is still planned for, with the
// missing quantity at zero: dropping it would silently leave a running service
// unmanaged.
func parseServices(flat lcm.DataPacketContent) (map[string]calm.ServiceMetrics, []string, map[string]int) {
	services := make(map[string]calm.ServiceMetrics)
	// The map is only walked to collect names; every ordered result below is
	// built from the sorted slice, because Go randomises map iteration and the
	// plan, its reason and the trace must be reproducible.
	for key := range flat {
		name, suffix, ok := splitServiceKey(key)
		if !ok {
			continue
		}
		value, ok := u.NumberAt(flat, nil, key)
		if !ok {
			fmt.Println(Description.NodeType+": ignoring non-numeric telemetry key", key)
			continue
		}
		metrics := services[name]
		switch suffix {
		case inputRateSuffix:
			metrics.ReqRate = value
		case inputReplicasSuffix:
			// A fractional count is a telemetry bug; rounding keeps a value that
			// survived a JSON round trip as 1.9999999 reading as 2.
			metrics.CurrentReplicas = int(math.Round(value))
		}
		services[name] = metrics
	}

	names := sortedNames(services)
	current := make(map[string]int, len(services))
	for _, name := range names {
		current[name] = services[name].CurrentReplicas
	}
	return services, names, current
}

// splitServiceKey reads a per-service telemetry key, returning the service name
// and which quantity the key carries.
func splitServiceKey(key string) (name string, suffix string, ok bool) {
	rest, found := strings.CutPrefix(key, inputServicePrefix)
	if !found {
		return "", "", false
	}
	for _, candidate := range []string{inputRateSuffix, inputReplicasSuffix} {
		trimmed, found := strings.CutSuffix(rest, candidate)
		if found && trimmed != "" {
			return trimmed, candidate, true
		}
	}
	return "", "", false
}

// risksFromReason recovers the per-service risk the autoscaler computed.
//
// ComputePlan does not return the risk values; it renders them into the plan
// reason, one "; "-separated segment per service in the same sorted order this
// node walks. Reading them back is preferred over recomputing the risk formula
// here: a second copy of the formula would silently disagree with the autoscaler
// the day the autoscaler changes, whereas a parse that stops matching just omits
// a reporting key while the numbers stay readable in the reason itself.
//
// Call it before the recovery action appends its own segment to the reason.
func risksFromReason(reason string, names []string) map[string]float64 {
	risks := make(map[string]float64, len(names))
	segments := strings.Split(reason, "; ")
	for index, name := range names {
		if index >= len(segments) {
			break
		}
		fields := strings.Fields(segments[index])
		if len(fields) == 0 || fields[0] != name {
			continue
		}
		for _, field := range fields[1:] {
			value, found := strings.CutPrefix(field, "risk=")
			if !found {
				continue
			}
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				risks[name] = parsed
			}
			break
		}
	}
	return risks
}

// softFail emits a reasoned empty plan. A bad property or an unusable payload
// must not stop the run: the engine has no recover, so a panic here would take
// the whole EFO down, and CALM_PlacementApply reads plan_ok to decide whether
// there is anything to apply.
func softFail(mode string, reason string) i.NodeExecResult {
	fmt.Println(Description.NodeType+":", reason)
	return emit(calm.PlacementPlan{Mode: mode, Reason: reason}, calm.RecoveryActionNone, nil, nil, false)
}

// emit flattens the plan onto the payload: the fixed output surface first, then
// the plan's own fields, then the per-service keys.
func emit(plan calm.PlacementPlan, action calm.RecoveryAction, names []string, risks map[string]float64, ok bool) i.NodeExecResult {
	output := OutputFormat{
		Mode:            plan.Mode,
		Recovery_action: string(action),
		Reason:          plan.Reason,
		Plan_ok:         ok,
	}
	encoded, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println(Description.NodeType+": Encode output error,", err)
		encoded = u.EmptyOutput(Description.NumOutputPort)
	}
	packet := encoded[0]
	if packet == nil {
		packet = lcm.DataPacketContent{}
		encoded[0] = packet
	}

	// The plan supplies its own key names, so the wire form of a plan stays the
	// calm package's to define. Keys the fixed surface already carries (mode,
	// reason) hold the same values and are left alone.
	for key, value := range plan.ToMap() {
		if _, exists := packet[key]; !exists {
			packet[key] = value
		}
	}

	for _, name := range sortedNames(plan.ReplicaTarget) {
		packet[outputPlanPrefix+name+outputPlanReplicasSuff] = plan.ReplicaTarget[name]
	}
	for _, name := range names {
		if risk, found := risks[name]; found {
			packet[outputRiskPrefix+name] = risk
		}
	}

	return i.NodeExecResult{Output: encoded}
}

// sortedNames returns the keys of m in ascending order, so every ordered result
// this node produces is reproducible between runs.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
