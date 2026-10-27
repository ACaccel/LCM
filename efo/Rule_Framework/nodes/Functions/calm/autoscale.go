package calm

/*
Autoscaler — the parametrised scaler that turns a policy mode into a placement
plan. It is steps 1 to 4 of the simulator's seven-step placement stage; the
remaining steps (node assignment, offload, migration, consolidation) need
node-level placement control the testbed does not expose through the LCM
surface, so they are deliberately absent and the corresponding plan fields stay
empty.

Per service:

 1. risk = max(dmr_risk, load_ratio), where load_ratio = lambda / (max(c,1)*mu)
    is the real arrival-to-capacity ratio and dmr_risk = max_dmr/eps counts only
    while the service is compute-bound. Using observed CPU as the load signal
    instead would make scale-out self-reinforcing (more replicas raise the
    reservation, which reads as more load) and would block scale-in for good.
 2. risk > scale_out_threshold -> +scale_step.
 3. otherwise scale in by scale_step only when all three hold: load below
    scale_in_threshold, the DMR gate passes, and the count is above the floor.
 4. floor = max(ceil(lambda/(mu*u_target) * redundancy_factor),
    redundancy_target, 1), maintained proactively. This is what gives each mode
    a different valley provisioning level.

Queueing headroom (L - 1/mu - W_radio - W_ho) decides whether a DMR violation is
compute-fixable. The testbed cannot measure the latency decomposition it needs,
so it defaults to +Inf: dmr_risk always counts, which is the behaviour the
simulator's headroom-unaware callers get. Config carries a per-service override
for when mu has been calibrated on real hardware.
*/

import (
	"fmt"
	"math"
	"strings"
)

// ServiceMetrics is the per-service input the autoscaler needs beyond the
// domain-wide StateVector.
type ServiceMetrics struct {
	// ReqRate is lambda: the service's current arrival rate in requests/second.
	ReqRate float64
	// CurrentReplicas is the live replica count read before planning.
	CurrentReplicas int
}

// Autoscaler computes placement plans. It is stateless and safe to share.
type Autoscaler struct {
	cfg *Config
}

// NewAutoscaler returns an autoscaler bound to cfg, which supplies the
// per-service mu/eps/headroom rows and the redundancy target utilisation.
func NewAutoscaler(cfg *Config) *Autoscaler { return &Autoscaler{cfg: cfg} }

// ComputePlan returns the placement plan for one slot under params.
//
// Services are walked in sorted name order so the plan, its reason string and
// the resulting trace are reproducible; ranging the map directly would reorder
// the reason text between runs.
func (a *Autoscaler) ComputePlan(state StateVector, services map[string]ServiceMetrics, params PolicyParams) PlacementPlan {
	plan := PlacementPlan{
		ReplicaTarget:    make(map[string]int, len(services)),
		RadioCompression: params.RadioCompressionFactor,
		Mode:             string(params.Mode),
	}

	reasons := make([]string, 0, len(services))
	for _, name := range sortedKeys(services) {
		m := services[name]
		target, reason := a.scaleService(name, m, state, params)
		plan.ReplicaTarget[name] = target
		reasons = append(reasons, reason)
	}
	plan.Reason = strings.Join(reasons, "; ")
	return plan
}

// scaleService runs steps 1 to 4 for one service and returns its absolute target
// replica count plus a one-line explanation.
func (a *Autoscaler) scaleService(name string, m ServiceMetrics, state StateVector, params PolicyParams) (int, string) {
	slo := a.cfg.ServiceSLO(name)
	eps := math.Max(slo.DmrSlo, 1e-9)
	mu := math.Max(slo.ServiceRatePerReplica, 1e-9)
	current := m.CurrentReplicas
	if current < 0 {
		current = 0
	}

	// Step 1: risk.
	loadRatio := m.ReqRate / (math.Max(float64(current), 1) * mu)
	headroom := slo.QueueingHeadroomMs
	dmrRisk := 0.0
	if headroom > 0 {
		dmrRisk = state.MaxDmr / eps
	}
	risk := math.Max(dmrRisk, loadRatio)

	floor := redundancyFloor(m.ReqRate, mu, a.cfg.RedundancyTargetUtil, params)

	// Step 2: scale out.
	delta := 0
	action := "hold"
	if risk > params.ScaleOutThreshold {
		delta = params.ScaleStep
		action = "scale_out"
	}

	// Step 3: scale in. The DMR gate is bypassed for a radio-bound service:
	// its DMR is not caused by compute, so shedding back towards the
	// demand-safe floor cannot make it worse.
	if delta == 0 {
		dmrSafe := headroom <= 0 || state.MaxDmr < eps*params.ScaleInDmrTolerance
		if loadRatio < params.ScaleInThreshold && dmrSafe && current > floor {
			delta = -params.ScaleStep
			action = "scale_in"
		}
	}

	// Step 4: the floor is maintained proactively, so it can both cancel a
	// scale-in and lift a count that demand has outgrown.
	if current+delta < floor {
		delta = floor - current
		action = "floor"
	}

	target := current + delta
	reason := fmt.Sprintf("%s %s risk=%.3f load=%.3f dmr_risk=%.3f floor=%d %d->%d",
		name, action, risk, loadRatio, dmrRisk, floor, current, target)
	return target, reason
}

// redundancyFloor is the workload-adaptive minimum replica count:
//
//	max(ceil(lambda/(mu*u_target) * redundancy_factor), redundancy_target, 1)
//
// The demand-derived base makes the floor track instantaneous load, so the same
// mode reserves more for a busy service than for an idle one, while
// redundancy_target holds the mode's absolute resilience baseline.
func redundancyFloor(reqRate, mu, uTarget float64, params PolicyParams) int {
	base := 0.0
	if mu > 0 && reqRate > 0 {
		base = reqRate / (mu * math.Max(uTarget, 1e-9))
	}
	workloadBase := int(math.Ceil(base * params.RedundancyFactor))
	floor := workloadBase
	if params.RedundancyTarget > floor {
		floor = params.RedundancyTarget
	}
	if floor < minReplicas {
		floor = minReplicas
	}
	return floor
}

// minReplicas is the hard lower bound applied even when a mode's redundancy
// target is lower: a service at zero replicas is undeployed, not scaled in.
const minReplicas = 1
