package calm

/*
RewardCalculator — the scalar the bandit learns from.

	Score  = w1*R_SLO + w2*C_energy + w3*C_switch + w4*C_recovery
	reward = -Score / (w1 + w2 + w3 + w4)

Normalising by the weight sum keeps rewards comparable across weight settings.

R_SLO is a hinge, not a level:

	R_SLO = alpha*dmr_excess + beta*min(cap, latency_excess) + gamma*severe

with dmr_excess = max_s max(0, dmr_s - eps_s) and latency_excess =
max_s max(0, d99_s/L_s - 1). A domain that already meets every SLO contributes
zero SLO risk, so the energy term decides the mode -- "satisfy the SLO, then
minimise energy". Scoring the satisfied DMR margin instead biases the selector
towards high-redundancy modes even when nothing is wrong.

C_energy is a stub. The simulator normalises measured joules by a per-slot energy
ceiling; the testbed has no power telemetry, so this uses

	C_energy = ReplicaTotal / ReplicaCapacity

Replica count is a monotone proxy for energy because the simulator's power model
is close to linear in utilisation and replicas set the reserved utilisation. That
is enough to keep the "save energy once the SLO is met" gradient. When a real
feed (IPMI/RAPL/PDU) is wired through EnergyCollector, replace this term; the
Score structure does not change.
*/

import "math"

// latencyExcessCap bounds the tail-latency excess term. A rare overload slot can
// push d99/L to a large multiple, which would drown out the controllable part of
// the signal; the cap keeps that term comparable to the others.
const latencyExcessCap = 5.0

// RewardInput is one slot's observation, already reduced to the scalars the
// Score needs. Use Config.Verify to derive the hinges from per-service readings.
type RewardInput struct {
	// DmrExcess is max_s max(0, dmr_s - eps_s).
	DmrExcess float64
	// LatencyExcess is max_s max(0, d99_s/L_s - 1), before the cap.
	LatencyExcess float64
	// Severe reports whether any service crossed its critical threshold.
	Severe bool
	// ReplicaTotal drives the energy proxy.
	ReplicaTotal int
	// Switched reports whether the policy mode changed this slot.
	Switched bool
	// RecoveryState is the phase after this slot's violation was recorded, so
	// the reward and the reported state agree.
	RecoveryState RecoveryState
}

// RewardBreakdown is the reward plus the four terms it came from, so the demo
// and the execution trace can show why a slot scored the way it did.
type RewardBreakdown struct {
	SloRisk      float64 `json:"slo_risk"`
	EnergyCost   float64 `json:"energy_cost"`
	SwitchCost   float64 `json:"switch_cost"`
	RecoveryCost float64 `json:"recovery_cost"`
	Score        float64 `json:"score"`
	Reward       float64 `json:"reward"`
}

// RewardCalculator computes the Score and the reward.
type RewardCalculator struct {
	cfg             RewardConfig
	replicaCapacity float64
}

// NewRewardCalculator returns a calculator over the configured weights and the
// replica capacity used as the energy proxy denominator.
func NewRewardCalculator(cfg RewardConfig, replicaCapacity float64) *RewardCalculator {
	if replicaCapacity <= 0 {
		replicaCapacity = defaultReplicaCapacity
	}
	return &RewardCalculator{cfg: cfg, replicaCapacity: replicaCapacity}
}

// Compute returns the reward and its breakdown for one slot.
func (c *RewardCalculator) Compute(in RewardInput) RewardBreakdown {
	severe := 0.0
	if in.Severe {
		severe = 1.0
	}
	sloRisk := c.cfg.Alpha*math.Max(0, in.DmrExcess) +
		c.cfg.Beta*math.Min(latencyExcessCap, math.Max(0, in.LatencyExcess)) +
		c.cfg.Gamma*severe

	energy := math.Max(0, float64(in.ReplicaTotal)) / c.replicaCapacity

	switchCost := 0.0
	if in.Switched {
		switchCost = 1.0
	}

	recoveryCost := 0.0
	switch in.RecoveryState {
	case RecoveryHard:
		recoveryCost = c.cfg.CHard
	case RecoverySoft:
		recoveryCost = c.cfg.CSoft
	}

	score := c.cfg.W1*sloRisk + c.cfg.W2*energy + c.cfg.W3*switchCost + c.cfg.W4*recoveryCost
	norm := math.Max(c.cfg.W1+c.cfg.W2+c.cfg.W3+c.cfg.W4, 1e-9)

	return RewardBreakdown{
		SloRisk:      sloRisk,
		EnergyCost:   energy,
		SwitchCost:   switchCost,
		RecoveryCost: recoveryCost,
		Score:        score,
		Reward:       -score / norm,
	}
}

// ServiceObservation is one service's post-placement measurement.
type ServiceObservation struct {
	Service string
	// Dmr is the deadline-miss ratio. On the testbed it is approximated by the
	// share of Jaeger latency samples above the service's latency SLO.
	Dmr float64
	// D99Ms is the 99th-percentile latency in milliseconds.
	D99Ms float64
}

// ServiceVerdict is the per-service violation breakdown for one slot.
type ServiceVerdict struct {
	DmrViolated  bool `json:"dmr_violated"`
	TailViolated bool `json:"tail_violated"`
	Severe       bool `json:"severe"`
}

// VerificationResult is the post-placement verdict for the decision domain.
type VerificationResult struct {
	// Violated is V: any service missed its DMR or tail-latency bound.
	Violated bool `json:"violated"`
	// Severe is V_severe: any service crossed a critical threshold. It lets the
	// next slot bypass the stability guard.
	Severe bool `json:"severe"`
	// DmrExcess and LatencyExcess are the reward hinges.
	DmrExcess     float64 `json:"dmr_excess"`
	LatencyExcess float64 `json:"latency_excess"`
	// MaxDmr and MeanD99Ms feed the next slot's state vector.
	MaxDmr     float64                   `json:"max_dmr"`
	MeanD99Ms  float64                   `json:"mean_d99_ms"`
	PerService map[string]ServiceVerdict `json:"per_service"`
}

// Verify turns per-service measurements into the violation indicators and reward
// hinges, using each service's own SLO row so a service with a looser bound is
// never judged against a tighter one.
//
//	V_dmr    = dmr > eps
//	V_tail   = d99 > L
//	V_severe = d99 > severe_factor*L  or  dmr > eps_crit
func (c *Config) Verify(obs []ServiceObservation) VerificationResult {
	result := VerificationResult{PerService: make(map[string]ServiceVerdict, len(obs))}
	if len(obs) == 0 {
		return result
	}
	var d99Total float64
	for _, o := range obs {
		slo := c.ServiceSLO(o.Service)
		verdict := ServiceVerdict{
			DmrViolated:  o.Dmr > slo.DmrSlo,
			TailViolated: o.D99Ms > slo.LatencySloMs,
			Severe:       o.D99Ms > slo.LatencySevereFactor*slo.LatencySloMs || o.Dmr > slo.DmrCrit,
		}
		result.PerService[o.Service] = verdict
		result.Violated = result.Violated || verdict.DmrViolated || verdict.TailViolated
		result.Severe = result.Severe || verdict.Severe
		result.MaxDmr = math.Max(result.MaxDmr, o.Dmr)
		result.DmrExcess = math.Max(result.DmrExcess, math.Max(0, o.Dmr-slo.DmrSlo))
		result.LatencyExcess = math.Max(result.LatencyExcess, math.Max(0, o.D99Ms/math.Max(slo.LatencySloMs, 1e-9)-1.0))
		d99Total += o.D99Ms
	}
	result.MeanD99Ms = d99Total / float64(len(obs))
	return result
}
