package calm

/*
ContextBuilder — turns a StateVector into the bandit feature vector phi.

The dimension layout below is the checkpoint layout: an existing LinUCB snapshot
is only meaningful under exactly this ordering, so dimensions may be appended but
never reordered or reused.

Two groups from the simulator's context are dropped here:

  - the cluster one-hot, because v1 runs a single decision domain (one target
    cluster per deployed policy), so the one-hot would be a constant;
  - the two energy dimensions (normalised power, budget margin), because the
    testbed has no power telemetry and the replica-count proxy that stands in for
    it carries no information the compute dimensions do not already hold.

That leaves d = 19 against the simulator's 4 + 4 + 3 + 2 + 5 + (K+1) + 2.
*/

import "math"

// Feature-vector layout. The comment on each constant is the normalisation the
// dimension carries; see build for the formulas.
const (
	dimSinr          = 0  // SINR, dB mapped to [0,1]
	dimPrbUtil       = 1  // PRB utilisation, clipped to [0,1]
	dimHoRate        = 2  // handover rate, clipped to [0,1]
	dimIntf          = 3  // interference, dBm mapped to [0,1]
	dimMeanCPU       = 4  // mean CPU utilisation, clipped to [0,1]
	dimMaxCPU        = 5  // max CPU utilisation, clipped to [0,1]
	dimMeanGPU       = 6  // mean GPU utilisation, clipped to [0,1]
	dimMeanMem       = 7  // mean memory utilisation, clipped to [0,1]
	dimReqRate       = 8  // total request rate / RefReqRate, clipped to [0,1]
	dimD99Ratio      = 9  // clip(d99/L, 0, 5) / 5
	dimMaxDmr        = 10 // max DMR, clipped to [0,1]
	dimViolationRate = 11 // sliding-window violation rate, clipped to [0,1]
	dimPrevMode      = 12 // previous-mode one-hot, occupies 12..15
	dimBias          = 16 // constant 1.0
	dimPrbSq         = 17 // PRB utilisation squared
	dimMaxCPUSq      = 18 // max CPU utilisation squared

	// FeatureDim is the context length. It follows from the layout above and is
	// the dimension every LinUCB in this package is built with.
	FeatureDim = 19
)

// radioMaskValue is the neutral constant substituted for every radio dimension
// when the sample is masked. It matches the simulator's radio-ablation mask, so a
// masked run reproduces the ablation's behaviour rather than learning from
// placeholder numbers.
const radioMaskValue = 0.5

// latencyRatioCap bounds the d99/L ratio before it is scaled into [0,1]. Without
// it a single overload slot would saturate the dimension and drown out the
// learnable range.
const latencyRatioCap = 5.0

// ContextBuilder builds bandit feature vectors from telemetry state.
type ContextBuilder struct {
	// refReqRate normalises the total request rate. It is the request rate at
	// which the dimension saturates, not a capacity limit.
	refReqRate float64
}

// NewContextBuilder returns a builder using the configured normalisation
// reference. A non-positive reference falls back to the built-in default so a
// misconfigured value cannot produce an infinite feature.
func NewContextBuilder(cfg *Config) *ContextBuilder {
	ref := defaultRefReqRate
	if cfg != nil && cfg.RefReqRate > 0 {
		ref = cfg.RefReqRate
	}
	return &ContextBuilder{refReqRate: ref}
}

// FeatureDim returns the context length. It is constant, but exposed as a method
// so callers do not hard-code the number.
func (b *ContextBuilder) FeatureDim() int { return FeatureDim }

// Build returns phi for one slot.
//
// prevMode is the mode active in the previous slot; ModeUnset (the first slot)
// encodes an all-zero one-hot. violationRate is the recovery manager's
// sliding-window violation frequency.
//
// When state.RadioMasked is set the four radio dimensions are replaced by the
// neutral mask value, which is the normal case on the testbed.
func (b *ContextBuilder) Build(state StateVector, prevMode Mode, violationRate float64) []float64 {
	phi := make([]float64, FeatureDim)

	if state.RadioMasked {
		phi[dimSinr] = radioMaskValue
		phi[dimPrbUtil] = radioMaskValue
		phi[dimHoRate] = radioMaskValue
		phi[dimIntf] = radioMaskValue
	} else {
		phi[dimSinr] = sinrToUnit(state.MeanSinr)
		phi[dimPrbUtil] = clip(state.MeanPrbUtil, 0, 1)
		phi[dimHoRate] = clip(state.HoRate, 0, 1)
		phi[dimIntf] = intfToUnit(state.MeanIntf)
	}

	phi[dimMeanCPU] = clip(state.MeanCpu, 0, 1)
	phi[dimMaxCPU] = clip(state.MaxCpu, 0, 1)
	phi[dimMeanGPU] = clip(state.MeanGpu, 0, 1)
	phi[dimMeanMem] = clip(state.MeanMem, 0, 1)

	phi[dimReqRate] = clip(state.TotalReqRate/b.refReqRate, 0, 1)
	slo := math.Max(state.LatencySloMs, 1e-6)
	phi[dimD99Ratio] = clip(state.MeanD99Ms/slo, 0, latencyRatioCap) / latencyRatioCap
	phi[dimMaxDmr] = clip(state.MaxDmr, 0, 1)

	phi[dimViolationRate] = clip(violationRate, 0, 1)
	if idx, ok := ModeIndex(prevMode); ok {
		phi[dimPrevMode+idx] = 1
	}

	phi[dimBias] = 1

	// The nonlinear terms square the already-normalised dimensions, including
	// the mask value, so a masked run keeps a consistent radio block.
	phi[dimPrbSq] = phi[dimPrbUtil] * phi[dimPrbUtil]
	phi[dimMaxCPUSq] = phi[dimMaxCPU] * phi[dimMaxCPU]

	return phi
}

// sinrToUnit maps SINR in dB onto [0,1]. 0 dB lands on 0.5 and +/-20 dB is the
// operating window of interest.
func sinrToUnit(sinrDB float64) float64 {
	return clip((sinrDB+20.0)/40.0, 0, 1)
}

// intfToUnit maps interference in dBm onto [0,1], higher meaning worse.
// Interference floors near -120 dBm and approaches -60 dBm under heavy
// inter-cell load, so that window is the scale.
func intfToUnit(intfDBm float64) float64 {
	return clip((intfDBm+120.0)/60.0, 0, 1)
}
