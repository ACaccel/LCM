package calm

/*
Configuration for the CALM loop.

Every value has a built-in default equal to the simulator's calibrated setting,
so the nodes run with no external file and behave identically whether or not one
is present. CALM_CONFIG_FILE points at an optional YAML file that overrides
individual values; anything it omits keeps its default, including a single knob
inside a mode or a single field of a service row.

The defaults are calibrated at simulator scale. They are a starting point on real
hardware, not a tuning.

The whole config is validated at load. A node validates once when it is
instantiated and fails there, rather than producing a degenerate plan every slot.
*/

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ConfigFileEnv names the environment variable holding the path to the optional
// YAML override file.
const ConfigFileEnv = "CALM_CONFIG_FILE"

// Built-in defaults that are not part of a nested section.
const (
	// defaultRefReqRate is the request rate at which the normalised request-rate
	// feature saturates. It is a normalisation scale, not a capacity limit.
	defaultRefReqRate = 200.0
	// defaultReplicaCapacity is the denominator of the replica-count energy
	// proxy: the replica total treated as "full power" for the domain.
	defaultReplicaCapacity = 20.0
	// defaultRedundancyTargetUtil is u_target, the utilisation the redundancy
	// floor sizes replicas for.
	defaultRedundancyTargetUtil = 0.7
	// defaultSelectorAlpha is the LinUCB exploration coefficient.
	defaultSelectorAlpha = 1.0
)

// GuardConfig holds the stability guard's limits.
type GuardConfig struct {
	// WindowW is the sliding window width in slots, shared with the recovery
	// manager's violation window.
	WindowW int `yaml:"window_W"`
	// MaxSwitchesSW caps mode switches inside the window.
	MaxSwitchesSW int `yaml:"max_switches_SW"`
	// MinDwellT is the minimum number of slots between two switches.
	MinDwellT int `yaml:"min_dwell_T"`
}

// RecoveryConfig holds the recovery state machine's thresholds.
type RecoveryConfig struct {
	// Eta is the violation rate above which soft recovery triggers.
	Eta float64 `yaml:"eta"`
	// SoftWindowWr is how many slots soft recovery runs before escalating.
	SoftWindowWr int `yaml:"soft_window_Wr"`
}

// RewardConfig holds the Score weights and the component coefficients.
type RewardConfig struct {
	W1 float64 `yaml:"w1"` // SLO risk
	W2 float64 `yaml:"w2"` // energy cost
	W3 float64 `yaml:"w3"` // switch cost
	W4 float64 `yaml:"w4"` // recovery cost

	Alpha float64 `yaml:"alpha"` // DMR excess coefficient in R_SLO
	Beta  float64 `yaml:"beta"`  // latency excess coefficient in R_SLO
	Gamma float64 `yaml:"gamma"` // severe-violation coefficient in R_SLO

	CSoft float64 `yaml:"c_soft"` // recovery cost while in soft recovery
	CHard float64 `yaml:"c_hard"` // recovery cost while in hard recovery
}

// ServiceSLO is one service's SLO row and queueing model.
type ServiceSLO struct {
	// ServiceRatePerReplica is mu: requests per second one replica serves.
	ServiceRatePerReplica float64 `yaml:"service_rate_per_replica"`
	// LatencySloMs is L: the tail-latency bound.
	LatencySloMs float64 `yaml:"latency_slo_ms"`
	// DmrSlo is eps: the deadline-miss-ratio bound.
	DmrSlo float64 `yaml:"dmr_slo"`
	// DmrCrit is eps_crit: the DMR level counted as a severe violation.
	DmrCrit float64 `yaml:"dmr_crit"`
	// LatencySevereFactor multiplies L to give the severe latency threshold.
	LatencySevereFactor float64 `yaml:"latency_severe_factor"`
	// QueueingHeadroomMs is L - 1/mu - W_radio - W_ho: the latency budget left
	// for queueing. At or below zero the service is radio-bound and adding
	// replicas cannot reduce its DMR.
	//
	// Computing it needs a per-service latency decomposition the testbed cannot
	// measure, so it defaults to +Inf, which leaves the guards it gates inactive
	// exactly as in the simulator's headroom-unaware path. Set it here once mu
	// has been calibrated on real hardware.
	QueueingHeadroomMs float64 `yaml:"queueing_headroom_ms"`
}

// Config is the fully resolved CALM configuration.
type Config struct {
	// RefReqRate normalises the request-rate context feature.
	RefReqRate float64
	// ReplicaCapacity is the energy proxy denominator.
	ReplicaCapacity float64
	// RedundancyTargetUtil is u_target in the redundancy floor.
	RedundancyTargetUtil float64
	// SelectorAlpha is the LinUCB exploration coefficient.
	SelectorAlpha float64

	Guard    GuardConfig
	Recovery RecoveryConfig
	Reward   RewardConfig

	// DefaultService is the SLO row used for any service without its own entry.
	DefaultService ServiceSLO
	// Services holds per-service SLO rows, keyed by the service name used in the
	// placement plan.
	Services map[string]ServiceSLO

	// Modes holds the per-mode knob sets. All four modes are always present.
	Modes map[Mode]PolicyParams
}

// DefaultConfig returns the built-in configuration. The service row mirrors the
// simulator's compute-heavy, tight-latency service; a deployment with different
// services overrides it through the config file.
func DefaultConfig() *Config {
	cfg := &Config{
		RefReqRate:           defaultRefReqRate,
		ReplicaCapacity:      defaultReplicaCapacity,
		RedundancyTargetUtil: defaultRedundancyTargetUtil,
		SelectorAlpha:        defaultSelectorAlpha,
		Guard: GuardConfig{
			WindowW:       100,
			MaxSwitchesSW: 5,
			MinDwellT:     10,
		},
		Recovery: RecoveryConfig{
			Eta:          0.3,
			SoftWindowWr: 8,
		},
		Reward: RewardConfig{
			W1:    1.0,
			W2:    0.4,
			W3:    0.3,
			W4:    0.5,
			Alpha: 1.0,
			Beta:  1.0,
			Gamma: 2.0,
			CSoft: 0.3,
			CHard: 1.0,
		},
		DefaultService: ServiceSLO{
			ServiceRatePerReplica: 200.0,
			LatencySloMs:          30.0,
			DmrSlo:                0.10,
			DmrCrit:               0.28,
			LatencySevereFactor:   2.0,
			QueueingHeadroomMs:    math.Inf(1),
		},
		Services: map[string]ServiceSLO{},
		Modes:    make(map[Mode]PolicyParams, NumArms),
	}
	for _, m := range canonicalModes {
		cfg.Modes[m] = defaultPolicyParams[m]
	}
	return cfg
}

// ServiceSLO returns the SLO row for name, falling back to DefaultService.
func (c *Config) ServiceSLO(name string) ServiceSLO {
	if slo, ok := c.Services[name]; ok {
		return slo
	}
	return c.DefaultService
}

// PolicyParams returns the knob set for m.
func (c *Config) PolicyParams(m Mode) (PolicyParams, error) {
	p, ok := c.Modes[m]
	if !ok {
		return PolicyParams{}, fmt.Errorf("calm: no policy params configured for mode %q", m)
	}
	return p, nil
}

// configFile is the YAML document schema. Sections are yaml.Node (a value, not a
// pointer: yaml.v3 does not populate a *yaml.Node field) so each one can be
// decoded onto a pre-populated default, which is what makes a partial override
// keep the untouched fields.
type configFile struct {
	RefReqRate           *float64             `yaml:"ref_req_rate"`
	ReplicaCapacity      *float64             `yaml:"replica_capacity"`
	RedundancyTargetUtil *float64             `yaml:"redundancy_target_util"`
	SelectorAlpha        *float64             `yaml:"selector_alpha"`
	Guard                yaml.Node            `yaml:"guard"`
	Recovery             yaml.Node            `yaml:"recovery"`
	Reward               yaml.Node            `yaml:"reward"`
	DefaultService       yaml.Node            `yaml:"default_service"`
	Services             map[string]yaml.Node `yaml:"services"`
	Modes                map[string]yaml.Node `yaml:"modes"`
}

// Load resolves the configuration: built-in defaults, overlaid with the YAML file
// named by CALM_CONFIG_FILE when that variable is set, then validated.
func Load() (*Config, error) {
	path := os.Getenv(ConfigFileEnv)
	if path == "" {
		cfg := DefaultConfig()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	return LoadFile(path)
}

// LoadFile resolves the configuration from an explicit YAML path. An unreadable
// or malformed file is an error: silently falling back to defaults would hide a
// deployment mistake behind plausible-looking behaviour.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("calm: cannot read config file %q: %w", path, err)
	}
	var doc configFile
	// KnownFields makes an unrecognised top-level key an error. A silently
	// ignored knob is worse than a refused start: the operator believes a value
	// took effect when it did not.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("calm: cannot parse config file %q: %w", path, err)
	}

	cfg := DefaultConfig()
	if doc.RefReqRate != nil {
		cfg.RefReqRate = *doc.RefReqRate
	}
	if doc.ReplicaCapacity != nil {
		cfg.ReplicaCapacity = *doc.ReplicaCapacity
	}
	if doc.RedundancyTargetUtil != nil {
		cfg.RedundancyTargetUtil = *doc.RedundancyTargetUtil
	}
	if doc.SelectorAlpha != nil {
		cfg.SelectorAlpha = *doc.SelectorAlpha
	}
	if err := decodeSection(doc.Guard, &cfg.Guard, "guard"); err != nil {
		return nil, err
	}
	if err := decodeSection(doc.Recovery, &cfg.Recovery, "recovery"); err != nil {
		return nil, err
	}
	if err := decodeSection(doc.Reward, &cfg.Reward, "reward"); err != nil {
		return nil, err
	}
	// default_service is applied before services so a per-service row inherits
	// the overridden default rather than the built-in one.
	if err := decodeSection(doc.DefaultService, &cfg.DefaultService, "default_service"); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(doc.Services) {
		node := doc.Services[name]
		slo := cfg.DefaultService
		if err := decodeSection(node, &slo, "services."+name); err != nil {
			return nil, err
		}
		cfg.Services[name] = slo
	}
	for _, name := range sortedKeys(doc.Modes) {
		node := doc.Modes[name]
		mode := Mode(name)
		params, ok := cfg.Modes[mode]
		if !ok {
			return nil, fmt.Errorf("calm: config file %q defines unknown mode %q", path, name)
		}
		if err := decodeSection(node, &params, "modes."+name); err != nil {
			return nil, err
		}
		// Mode is a label, not a knob: keep it pinned to the section name so a
		// file cannot detach a knob set from the arm it belongs to.
		params.Mode = mode
		cfg.Modes[mode] = params
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("calm: config file %q is invalid: %w", path, err)
	}
	return cfg, nil
}

// decodeSection overlays one YAML mapping onto a pre-populated destination, so
// keys the document omits keep their default value. An absent section (zero Kind)
// is a no-op.
//
// yaml.Node.Decode has no KnownFields switch, so unrecognised keys are checked
// here instead: a mistyped knob must fail the load rather than be ignored.
func decodeSection(node yaml.Node, dst any, name string) error {
	if node.Kind == 0 {
		return nil
	}
	if err := checkKnownFields(node, dst, name); err != nil {
		return err
	}
	if err := node.Decode(dst); err != nil {
		return fmt.Errorf("calm: cannot parse %s section: %w", name, err)
	}
	return nil
}

// checkKnownFields reports a mapping key that dst has no field for.
func checkKnownFields(node yaml.Node, dst any, name string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("calm: %s must be a mapping", name)
	}
	known := yamlFieldNames(reflect.TypeOf(dst).Elem())
	// A mapping node's content alternates key, value.
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !known[key] {
			return fmt.Errorf("calm: %s has no field %q", name, key)
		}
	}
	return nil
}

// yamlFieldNames returns the YAML key of every settable field of a struct type.
func yamlFieldNames(t reflect.Type) map[string]bool {
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		if tag == "" {
			// yaml.v3's default for an untagged field.
			tag = strings.ToLower(field.Name)
		}
		names[tag] = true
	}
	return names
}

// Validate checks every value the loop depends on and reports the first problem.
func (c *Config) Validate() error {
	if c.RefReqRate <= 0 {
		return fmt.Errorf("calm: ref_req_rate must be positive, got %g", c.RefReqRate)
	}
	if c.ReplicaCapacity <= 0 {
		return fmt.Errorf("calm: replica_capacity must be positive, got %g", c.ReplicaCapacity)
	}
	if c.RedundancyTargetUtil <= 0 || c.RedundancyTargetUtil > 1 {
		return fmt.Errorf("calm: redundancy_target_util must be in (0,1], got %g", c.RedundancyTargetUtil)
	}
	if c.SelectorAlpha < 0 || math.IsNaN(c.SelectorAlpha) {
		return fmt.Errorf("calm: selector_alpha must not be negative, got %g", c.SelectorAlpha)
	}

	if c.Guard.WindowW < 1 {
		return fmt.Errorf("calm: guard.window_W must be at least 1, got %d", c.Guard.WindowW)
	}
	if c.Guard.MaxSwitchesSW < 1 {
		return fmt.Errorf("calm: guard.max_switches_SW must be at least 1, got %d", c.Guard.MaxSwitchesSW)
	}
	if c.Guard.MinDwellT < 0 {
		return fmt.Errorf("calm: guard.min_dwell_T must not be negative, got %d", c.Guard.MinDwellT)
	}

	if c.Recovery.Eta < 0 || c.Recovery.Eta > 1 {
		return fmt.Errorf("calm: recovery.eta must be in [0,1], got %g", c.Recovery.Eta)
	}
	if c.Recovery.SoftWindowWr < 1 {
		return fmt.Errorf("calm: recovery.soft_window_Wr must be at least 1, got %d", c.Recovery.SoftWindowWr)
	}

	weights := []struct {
		name  string
		value float64
	}{
		{"w1", c.Reward.W1}, {"w2", c.Reward.W2}, {"w3", c.Reward.W3}, {"w4", c.Reward.W4},
		{"alpha", c.Reward.Alpha}, {"beta", c.Reward.Beta}, {"gamma", c.Reward.Gamma},
		{"c_soft", c.Reward.CSoft}, {"c_hard", c.Reward.CHard},
	}
	for _, w := range weights {
		if w.value < 0 || math.IsNaN(w.value) {
			return fmt.Errorf("calm: reward.%s must not be negative, got %g", w.name, w.value)
		}
	}
	if c.Reward.W1+c.Reward.W2+c.Reward.W3+c.Reward.W4 <= 0 {
		return fmt.Errorf("calm: reward weights w1..w4 must not all be zero")
	}

	if err := c.DefaultService.validate("default_service"); err != nil {
		return err
	}
	for _, name := range sortedKeys(c.Services) {
		slo := c.Services[name]
		if err := slo.validate("services." + name); err != nil {
			return err
		}
	}

	for _, m := range canonicalModes {
		params, ok := c.Modes[m]
		if !ok {
			return fmt.Errorf("calm: no policy params configured for mode %q", m)
		}
		if err := params.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s ServiceSLO) validate(label string) error {
	if s.ServiceRatePerReplica <= 0 {
		return fmt.Errorf("calm: %s.service_rate_per_replica must be positive, got %g", label, s.ServiceRatePerReplica)
	}
	if s.LatencySloMs <= 0 {
		return fmt.Errorf("calm: %s.latency_slo_ms must be positive, got %g", label, s.LatencySloMs)
	}
	if s.DmrSlo <= 0 || s.DmrSlo >= 1 {
		return fmt.Errorf("calm: %s.dmr_slo must be in (0,1), got %g", label, s.DmrSlo)
	}
	if s.DmrCrit <= s.DmrSlo || s.DmrCrit > 1 {
		return fmt.Errorf("calm: %s.dmr_crit must be in (dmr_slo,1], got %g", label, s.DmrCrit)
	}
	if s.LatencySevereFactor <= 1 {
		return fmt.Errorf("calm: %s.latency_severe_factor must be greater than 1, got %g", label, s.LatencySevereFactor)
	}
	if math.IsNaN(s.QueueingHeadroomMs) {
		return fmt.Errorf("calm: %s.queueing_headroom_ms must be a number", label)
	}
	return nil
}

var (
	sharedOnce sync.Once
	sharedCfg  *Config
	sharedErr  error
)

// Shared returns the process-wide configuration, resolved once on first use so
// every node in every policy sees the same values. Changing CALM_CONFIG_FILE
// after the first call has no effect; restart the EFO instead.
func Shared() (*Config, error) {
	sharedOnce.Do(func() {
		sharedCfg, sharedErr = Load()
	})
	return sharedCfg, sharedErr
}
