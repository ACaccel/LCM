/*
Package calm holds the shared logic of the CALM (Context-Aware Lifecycle
Management) four-stage adaptive loop: telemetry state, contextual-bandit policy
selection, the parametrised autoscaler, the stability guard, the recovery state
machine and the reward function.

The five CALM_* nodes are thin shells over this package. Keeping the logic here
serves two purposes: the semantics ported from the lcm_sim simulator stay in one
reviewable place, and they are unit-testable without an engine, an HTTP server or
a live cluster.

Dependency rule: this package imports the standard library and the YAML parser
only. It must never import a node package -- the nodes import it, and a cycle
would break the build.

State lives in memory only, matching the existing LCM_Enabler semantics: an EFO
restart resets every learner. The optional state file (see LoopState.SnapshotJSON)
is the escape hatch, not a durability guarantee.
*/
package calm

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// NumArms is the number of policy modes the bandit chooses between. It is a
// compile-time constant because the LinUCB per-arm arrays and the previous-mode
// one-hot block of the context vector are both sized by it.
const NumArms = 4

// Mode names one of the four policy modes. The wire form is the string value, so
// it round-trips through the node payload and the execution trace unchanged.
type Mode string

// The four policy modes. Their order is canonical and load-bearing: it fixes the
// LinUCB arm indices and the previous-mode one-hot layout in the context vector,
// so a stored LinUCB snapshot is only meaningful under this exact ordering.
// Never reorder these.
const (
	ModeLatencyPriority Mode = "latency_priority" // arm 0
	ModeBalanced        Mode = "balanced"         // arm 1
	ModeEnergySaving    Mode = "energy_saving"    // arm 2
	ModeResilient       Mode = "resilient"        // arm 3

	// ModeUnset is the zero value, meaning "no mode has been selected yet".
	// The context builder encodes it as an all-zero one-hot block.
	ModeUnset Mode = ""
)

var canonicalModes = [NumArms]Mode{
	ModeLatencyPriority,
	ModeBalanced,
	ModeEnergySaving,
	ModeResilient,
}

// Modes returns the canonical mode ordering. It returns a copy so a caller
// cannot corrupt the ordering that arm indices and one-hot offsets depend on.
func Modes() [NumArms]Mode { return canonicalModes }

// ModeIndex returns the canonical arm index of m, and false when m is not one of
// the four modes (including ModeUnset).
func ModeIndex(m Mode) (int, bool) {
	for i, candidate := range canonicalModes {
		if candidate == m {
			return i, true
		}
	}
	return 0, false
}

// ModeAt returns the mode at canonical arm index i, and false when i is out of
// range.
func ModeAt(i int) (Mode, bool) {
	if i < 0 || i >= NumArms {
		return ModeUnset, false
	}
	return canonicalModes[i], true
}

// StateVector is the flat telemetry snapshot produced by CALM_Telemetry and
// consumed by every later stage. Every field is a scalar so the node payload
// stays flat and the GUI's If/Compare nodes can read individual fields.
//
// Units follow the simulator this was ported from: MeanSinr is in dB, MeanIntf
// in dBm, the utilisation and DMR fields are fractions in [0,1], TotalReqRate is
// requests per second and the latency fields are milliseconds.
type StateVector struct {
	// Radio sub-vector x_radio = {SINR, PRB, HO, INTF}. The testbed cannot
	// measure these, so RadioMasked is normally true and the context builder
	// substitutes the neutral mask value for all four dimensions; a demo can
	// inject synthetic values and clear the flag to exercise the radio path.
	MeanSinr    float64
	MeanPrbUtil float64
	HoRate      float64
	MeanIntf    float64
	RadioMasked bool

	// Compute sub-vector, all fractions in [0,1].
	MeanCpu float64
	MaxCpu  float64
	MeanMem float64
	MeanGpu float64

	// Service sub-vector.
	TotalReqRate float64
	MeanD99Ms    float64
	MaxDmr       float64
	LatencySloMs float64

	// ReplicaTotal is the total replica count across the decision domain. It is
	// the energy proxy the reward function normalises (see reward.go).
	ReplicaTotal int
}

// Payload keys for StateVector. They are the flat field names the nodes publish
// and the demo inject file overrides, so they are part of the node contract.
const (
	KeyMeanSinr     = "mean_sinr"
	KeyMeanPrbUtil  = "mean_prb_util"
	KeyHoRate       = "ho_rate"
	KeyMeanIntf     = "mean_intf"
	KeyRadioMasked  = "radio_masked"
	KeyMeanCpu      = "mean_cpu"
	KeyMaxCpu       = "max_cpu"
	KeyMeanMem      = "mean_mem"
	KeyMeanGpu      = "mean_gpu"
	KeyTotalReqRate = "total_req_rate"
	KeyMeanD99Ms    = "mean_d99_ms"
	KeyMaxDmr       = "max_dmr"
	KeyLatencySloMs = "latency_slo_ms"
	KeyReplicaTotal = "replica_total"
)

// ToMap renders the state as a flat payload map.
func (s StateVector) ToMap() map[string]any {
	return map[string]any{
		KeyMeanSinr:     s.MeanSinr,
		KeyMeanPrbUtil:  s.MeanPrbUtil,
		KeyHoRate:       s.HoRate,
		KeyMeanIntf:     s.MeanIntf,
		KeyRadioMasked:  s.RadioMasked,
		KeyMeanCpu:      s.MeanCpu,
		KeyMaxCpu:       s.MaxCpu,
		KeyMeanMem:      s.MeanMem,
		KeyMeanGpu:      s.MeanGpu,
		KeyTotalReqRate: s.TotalReqRate,
		KeyMeanD99Ms:    s.MeanD99Ms,
		KeyMaxDmr:       s.MaxDmr,
		KeyLatencySloMs: s.LatencySloMs,
		KeyReplicaTotal: s.ReplicaTotal,
	}
}

// FromMap overlays the fields present in m onto the receiver and leaves absent
// fields untouched. Overlay rather than replace is deliberate: the same method
// serves both "decode a full state from the payload" and "apply the demo inject
// file", which carries only the handful of fields a scenario overrides.
//
// A present key whose value cannot be read as the field's type is an error, so a
// malformed inject file is reported rather than silently ignored.
func (s *StateVector) FromMap(m map[string]any) error {
	floats := []struct {
		key string
		dst *float64
	}{
		{KeyMeanSinr, &s.MeanSinr},
		{KeyMeanPrbUtil, &s.MeanPrbUtil},
		{KeyHoRate, &s.HoRate},
		{KeyMeanIntf, &s.MeanIntf},
		{KeyMeanCpu, &s.MeanCpu},
		{KeyMaxCpu, &s.MaxCpu},
		{KeyMeanMem, &s.MeanMem},
		{KeyMeanGpu, &s.MeanGpu},
		{KeyTotalReqRate, &s.TotalReqRate},
		{KeyMeanD99Ms, &s.MeanD99Ms},
		{KeyMaxDmr, &s.MaxDmr},
		{KeyLatencySloMs, &s.LatencySloMs},
	}
	for _, f := range floats {
		v, ok := m[f.key]
		if !ok {
			continue
		}
		n, ok := asFloat(v)
		if !ok {
			return fmt.Errorf("calm: field %q is not a number: %v", f.key, v)
		}
		*f.dst = n
	}
	if v, ok := m[KeyReplicaTotal]; ok {
		n, ok := asInt(v)
		if !ok {
			return fmt.Errorf("calm: field %q is not an integer: %v", KeyReplicaTotal, v)
		}
		s.ReplicaTotal = n
	}
	if v, ok := m[KeyRadioMasked]; ok {
		b, ok := asBool(v)
		if !ok {
			return fmt.Errorf("calm: field %q is not a boolean: %v", KeyRadioMasked, v)
		}
		s.RadioMasked = b
	}
	return nil
}

// RadioSample is the raw radio sub-vector a RadioCollector produces.
type RadioSample struct {
	MeanSinr    float64 // dB
	MeanPrbUtil float64 // fraction in [0,1]
	HoRate      float64 // handovers per user per slot
	MeanIntf    float64 // dBm
	// Masked reports that the sample carries no measured information, so the
	// context builder should substitute the neutral value for the radio
	// dimensions instead of learning from placeholder numbers.
	Masked bool
}

// RadioCollector supplies the radio sub-vector of the state. The testbed exposes
// no RAN KPIs, so v1 ships only StubRadioCollector; wiring a real RAN feed later
// means implementing this interface, with no change to the context layout.
type RadioCollector interface {
	// CollectRadio returns the current radio sample.
	CollectRadio() (RadioSample, error)
}

// Neutral radio placeholders used when no RAN KPI source is wired. They are
// arbitrary: the sample is flagged Masked, so the context builder replaces all
// four dimensions with the neutral mask value and never sees these numbers.
const (
	stubMeanSinr    = 0.5
	stubMeanPrbUtil = 0.5
	stubHoRate      = 0.5
	stubMeanIntf    = 0.0
)

// StubRadioCollector returns a fixed, masked radio sample. Sample may be
// overwritten (and Sample.Masked cleared) to drive a synthetic demo scenario.
type StubRadioCollector struct {
	Sample RadioSample
}

// NewStubRadioCollector returns a collector emitting the masked neutral sample.
func NewStubRadioCollector() *StubRadioCollector {
	return &StubRadioCollector{Sample: RadioSample{
		MeanSinr:    stubMeanSinr,
		MeanPrbUtil: stubMeanPrbUtil,
		HoRate:      stubHoRate,
		MeanIntf:    stubMeanIntf,
		Masked:      true,
	}}
}

// CollectRadio implements RadioCollector.
func (c *StubRadioCollector) CollectRadio() (RadioSample, error) { return c.Sample, nil }

// EnergySample is the raw energy reading an EnergyCollector produces.
type EnergySample struct {
	TotalPowerW     float64 // instantaneous draw across the decision domain
	TransitionCostJ float64 // energy attributed to this slot's reconfiguration
	// Available reports that the reading is measured. When false the reward
	// falls back to the replica-count energy proxy (see reward.go).
	Available bool
}

// EnergyCollector supplies power and transition-energy readings. No power
// telemetry exists on the testbed, so v1 ships only StubEnergyCollector and the
// reward uses a replica-count proxy; an IPMI/RAPL/PDU feed later implements this
// interface and the reward switches to the measured term.
type EnergyCollector interface {
	// CollectEnergy returns the current energy sample.
	CollectEnergy() (EnergySample, error)
}

// StubEnergyCollector reports that no energy measurement is available.
type StubEnergyCollector struct{}

// NewStubEnergyCollector returns the no-measurement energy collector.
func NewStubEnergyCollector() *StubEnergyCollector { return &StubEnergyCollector{} }

// CollectEnergy implements EnergyCollector.
func (c *StubEnergyCollector) CollectEnergy() (EnergySample, error) {
	return EnergySample{Available: false}, nil
}

// Valid values for the enumerated PolicyParams knobs.
const (
	OffloadingLatencyPref = "latency_pref"
	OffloadingEnergyPref  = "energy_pref"

	MigrationTrue       = "true"
	MigrationPenalized  = "penalized"
	MigrationRestricted = "restricted"

	RecoveryBehaviorPersistentOnly = "persistent_only"
	RecoveryBehaviorNormal         = "normal"
	RecoveryBehaviorFallback       = "fallback"

	NodeSelectionPack   = "pack"
	NodeSelectionSpread = "spread"
)

// PolicyParams is the knob set that makes one policy mode behave differently
// from another. The thirteen knobs are the simulator's, carried over unchanged
// so the calibrated defaults keep their meaning; Mode is a label, not a knob.
//
// The default values are calibrated at simulator scale and will need
// recalibrating against the real testbed.
type PolicyParams struct {
	// Mode labels which policy mode this knob set belongs to. It is pinned to
	// the config section name, so a file cannot detach a knob set from its arm.
	Mode Mode `yaml:"-"`

	// ScaleOutThreshold is the risk level above which a service scales out.
	ScaleOutThreshold float64 `yaml:"scale_out_threshold"`
	// ScaleInThreshold is the load ratio below which a service may scale in.
	ScaleInThreshold float64 `yaml:"scale_in_threshold"`
	// ScaleStep is how many replicas one scale action adds or removes.
	ScaleStep int `yaml:"scale_step"`
	// ScaleInDmrTolerance scales the DMR bound in the scale-in gate: higher
	// means more willing to shed replicas while DMR is elevated.
	ScaleInDmrTolerance float64 `yaml:"scale_in_dmr_tolerance"`
	// RedundancyTarget is the mode's absolute replica floor per service.
	RedundancyTarget int `yaml:"redundancy_target"`
	// RedundancyFactor multiplies the demand-derived part of the replica floor.
	RedundancyFactor float64 `yaml:"redundancy_factor"`
	// OffloadingMode picks the offload destination preference. Not acted on in
	// v1 (offloads are always empty); kept so the knob set stays complete.
	OffloadingMode string `yaml:"offloading_mode"`
	// OffloadFraction is the share of traffic an offload would divert. Not
	// acted on in v1.
	OffloadFraction float64 `yaml:"offload_fraction"`
	// MigrationAllowed gates migration. Not acted on in v1.
	MigrationAllowed string `yaml:"migration_allowed"`
	// MigrationCostWeight scales the transition energy a migration is charged.
	// Not acted on in v1.
	MigrationCostWeight float64 `yaml:"migration_cost_weight"`
	// RecoveryBehavior selects the mode's recovery strategy (see recovery.go).
	RecoveryBehavior string `yaml:"recovery_behavior"`
	// RadioCompressionFactor is the mode's radio payload relief factor. Emitted
	// on the plan but not acted on in v1.
	RadioCompressionFactor float64 `yaml:"radio_compression_factor"`
	// NodeSelection is pack or spread. Not acted on in v1: placement targets a
	// service's replica count, not individual nodes.
	NodeSelection string `yaml:"node_selection"`
}

// defaultPolicyParams holds the simulator's calibrated knob values.
var defaultPolicyParams = map[Mode]PolicyParams{
	ModeLatencyPriority: {
		Mode:                   ModeLatencyPriority,
		ScaleOutThreshold:      0.55,
		ScaleInThreshold:       0.35,
		ScaleStep:              2,
		ScaleInDmrTolerance:    0.3,
		RedundancyTarget:       2,
		RedundancyFactor:       1.5,
		OffloadingMode:         OffloadingLatencyPref,
		OffloadFraction:        0.30,
		MigrationAllowed:       MigrationTrue,
		MigrationCostWeight:    1.0,
		RecoveryBehavior:       RecoveryBehaviorPersistentOnly,
		RadioCompressionFactor: 0.5,
		NodeSelection:          NodeSelectionSpread,
	},
	ModeBalanced: {
		Mode:                   ModeBalanced,
		ScaleOutThreshold:      0.70,
		ScaleInThreshold:       0.35,
		ScaleStep:              1,
		ScaleInDmrTolerance:    0.5,
		RedundancyTarget:       2,
		RedundancyFactor:       1.2,
		OffloadingMode:         OffloadingLatencyPref,
		OffloadFraction:        0.15,
		MigrationAllowed:       MigrationPenalized,
		MigrationCostWeight:    1.5,
		RecoveryBehavior:       RecoveryBehaviorNormal,
		RadioCompressionFactor: 0.8,
		NodeSelection:          NodeSelectionPack,
	},
	ModeEnergySaving: {
		Mode:                   ModeEnergySaving,
		ScaleOutThreshold:      0.70,
		ScaleInThreshold:       0.30,
		ScaleStep:              1,
		ScaleInDmrTolerance:    1.0,
		RedundancyTarget:       1,
		RedundancyFactor:       1.0,
		OffloadingMode:         OffloadingEnergyPref,
		OffloadFraction:        0.20,
		MigrationAllowed:       MigrationRestricted,
		MigrationCostWeight:    1.0,
		RecoveryBehavior:       RecoveryBehaviorPersistentOnly,
		RadioCompressionFactor: 0.5,
		NodeSelection:          NodeSelectionPack,
	},
	ModeResilient: {
		Mode:                   ModeResilient,
		ScaleOutThreshold:      0.65,
		ScaleInThreshold:       0.25,
		ScaleStep:              2,
		ScaleInDmrTolerance:    0.2,
		RedundancyTarget:       3,
		RedundancyFactor:       2.0,
		OffloadingMode:         OffloadingLatencyPref,
		OffloadFraction:        0.25,
		MigrationAllowed:       MigrationRestricted,
		MigrationCostWeight:    1.0,
		RecoveryBehavior:       RecoveryBehaviorFallback,
		RadioCompressionFactor: 0.6,
		NodeSelection:          NodeSelectionSpread,
	},
}

// DefaultPolicyParams returns the calibrated knob set for m.
func DefaultPolicyParams(m Mode) (PolicyParams, error) {
	p, ok := defaultPolicyParams[m]
	if !ok {
		return PolicyParams{}, fmt.Errorf("calm: unknown policy mode %q", m)
	}
	return p, nil
}

// Validate reports whether the knob set is usable. It is called at config load
// so a bad GUI value or YAML override fails fast instead of producing a silently
// degenerate plan.
func (p PolicyParams) Validate() error {
	if _, ok := ModeIndex(p.Mode); !ok {
		return fmt.Errorf("calm: policy params carry unknown mode %q", p.Mode)
	}
	if p.ScaleOutThreshold <= 0 {
		return fmt.Errorf("calm: %s scale_out_threshold must be positive, got %g", p.Mode, p.ScaleOutThreshold)
	}
	if p.ScaleInThreshold < 0 {
		return fmt.Errorf("calm: %s scale_in_threshold must not be negative, got %g", p.Mode, p.ScaleInThreshold)
	}
	if p.ScaleInThreshold >= p.ScaleOutThreshold {
		return fmt.Errorf("calm: %s scale_in_threshold (%g) must be below scale_out_threshold (%g)", p.Mode, p.ScaleInThreshold, p.ScaleOutThreshold)
	}
	if p.ScaleStep < 1 {
		return fmt.Errorf("calm: %s scale_step must be at least 1, got %d", p.Mode, p.ScaleStep)
	}
	if p.ScaleInDmrTolerance < 0 {
		return fmt.Errorf("calm: %s scale_in_dmr_tolerance must not be negative, got %g", p.Mode, p.ScaleInDmrTolerance)
	}
	if p.RedundancyTarget < 0 {
		return fmt.Errorf("calm: %s redundancy_target must not be negative, got %d", p.Mode, p.RedundancyTarget)
	}
	if p.RedundancyFactor <= 0 {
		return fmt.Errorf("calm: %s redundancy_factor must be positive, got %g", p.Mode, p.RedundancyFactor)
	}
	switch p.OffloadingMode {
	case OffloadingLatencyPref, OffloadingEnergyPref:
	default:
		return fmt.Errorf("calm: %s offloading_mode %q must be %q or %q", p.Mode, p.OffloadingMode, OffloadingLatencyPref, OffloadingEnergyPref)
	}
	if p.OffloadFraction < 0 || p.OffloadFraction > 1 {
		return fmt.Errorf("calm: %s offload_fraction must be in [0,1], got %g", p.Mode, p.OffloadFraction)
	}
	switch p.MigrationAllowed {
	case MigrationTrue, MigrationPenalized, MigrationRestricted:
	default:
		return fmt.Errorf("calm: %s migration_allowed %q must be one of %q, %q, %q", p.Mode, p.MigrationAllowed, MigrationTrue, MigrationPenalized, MigrationRestricted)
	}
	if p.MigrationCostWeight <= 0 {
		return fmt.Errorf("calm: %s migration_cost_weight must be positive, got %g", p.Mode, p.MigrationCostWeight)
	}
	switch p.RecoveryBehavior {
	case RecoveryBehaviorPersistentOnly, RecoveryBehaviorNormal, RecoveryBehaviorFallback:
	default:
		return fmt.Errorf("calm: %s recovery_behavior %q must be one of %q, %q, %q", p.Mode, p.RecoveryBehavior, RecoveryBehaviorPersistentOnly, RecoveryBehaviorNormal, RecoveryBehaviorFallback)
	}
	if p.RadioCompressionFactor <= 0 || p.RadioCompressionFactor > 1 {
		return fmt.Errorf("calm: %s radio_compression_factor must be in (0,1], got %g", p.Mode, p.RadioCompressionFactor)
	}
	switch p.NodeSelection {
	case NodeSelectionPack, NodeSelectionSpread:
	default:
		return fmt.Errorf("calm: %s node_selection %q must be %q or %q", p.Mode, p.NodeSelection, NodeSelectionPack, NodeSelectionSpread)
	}
	return nil
}

// PlacementPlan is what a policy mode decides for one slot.
//
// ReplicaTarget is the only field acted on in v1: it maps a service name to its
// absolute target replica count, which CALM_PlacementApply writes through
// PATCH. Migrations and Offloads always come back empty; they exist so the plan
// structure is stable, and CALM_PlacementApply reports "not implemented" rather
// than silently dropping a non-empty set.
type PlacementPlan struct {
	ReplicaTarget    map[string]int
	Migrations       []map[string]any
	Offloads         []map[string]any
	RadioCompression float64
	Mode             string
	Reason           string
}

// Payload keys for PlacementPlan.
const (
	KeyReplicaTarget    = "replica_target"
	KeyMigrations       = "migrations"
	KeyOffloads         = "offloads"
	KeyRadioCompression = "radio_compression"
	KeyMode             = "mode"
	KeyReason           = "reason"
)

// ToMap renders the plan as a flat payload map. ReplicaTarget becomes a nested
// object because a service-keyed map cannot be flattened without colliding with
// the state fields.
func (p PlacementPlan) ToMap() map[string]any {
	targets := make(map[string]any, len(p.ReplicaTarget))
	for svc, n := range p.ReplicaTarget {
		targets[svc] = n
	}
	return map[string]any{
		KeyReplicaTarget:    targets,
		KeyMigrations:       p.Migrations,
		KeyOffloads:         p.Offloads,
		KeyRadioCompression: p.RadioCompression,
		KeyMode:             p.Mode,
		KeyReason:           p.Reason,
	}
}

// FromMap decodes a plan previously rendered by ToMap. Absent keys leave the
// corresponding field at its zero value.
func (p *PlacementPlan) FromMap(m map[string]any) error {
	if v, ok := m[KeyReplicaTarget]; ok {
		raw, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("calm: field %q is not an object: %v", KeyReplicaTarget, v)
		}
		targets := make(map[string]int, len(raw))
		for svc, val := range raw {
			n, ok := asInt(val)
			if !ok {
				return fmt.Errorf("calm: replica target for service %q is not an integer: %v", svc, val)
			}
			targets[svc] = n
		}
		p.ReplicaTarget = targets
	}
	var err error
	if p.Migrations, err = decodeOpList(m, KeyMigrations); err != nil {
		return err
	}
	if p.Offloads, err = decodeOpList(m, KeyOffloads); err != nil {
		return err
	}
	if v, ok := m[KeyRadioCompression]; ok {
		n, ok := asFloat(v)
		if !ok {
			return fmt.Errorf("calm: field %q is not a number: %v", KeyRadioCompression, v)
		}
		p.RadioCompression = n
	}
	if v, ok := m[KeyMode]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("calm: field %q is not a string: %v", KeyMode, v)
		}
		p.Mode = s
	}
	if v, ok := m[KeyReason]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("calm: field %q is not a string: %v", KeyReason, v)
		}
		p.Reason = s
	}
	return nil
}

// decodeOpList reads a migration or offload list, tolerating both the in-process
// form ([]map[string]any) and the JSON round-tripped form ([]any).
func decodeOpList(m map[string]any, key string) ([]map[string]any, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, nil
	}
	switch list := v.(type) {
	case []map[string]any:
		return list, nil
	case []any:
		out := make([]map[string]any, 0, len(list))
		for i, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("calm: %s[%d] is not an object: %v", key, i, item)
			}
			out = append(out, entry)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("calm: field %q is not a list: %v", key, v)
	}
}

// sortedKeys returns the keys of m in ascending order. Every routine that walks
// a service-keyed map uses it: Go randomises map iteration, so ranging directly
// would make the same inputs produce different plans, reasons and traces between
// runs.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// asFloat reads a JSON- or YAML-decoded value as a float64.
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// asInt reads a JSON- or YAML-decoded value as an int. A fractional float is
// rejected rather than truncated: a replica count of 1.5 is a caller bug.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	case float32:
		return asInt(float64(n))
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	default:
		return 0, false
	}
}

// asBool reads a JSON- or YAML-decoded value as a bool.
func asBool(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		parsed, err := strconv.ParseBool(b)
		return parsed, err == nil
	default:
		return false, false
	}
}

// clip constrains v to [lo, hi]. NaN maps to lo so a bad telemetry reading
// cannot poison the learner with a NaN feature.
func clip(v, lo, hi float64) float64 {
	if math.IsNaN(v) || v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
