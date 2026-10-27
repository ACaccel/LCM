package calm

/*
LinUCB — the shared linear-UCB contextual bandit over the four policy modes.

Per arm a the learner keeps A_a (d x d) and b_a (d). Selection and update are

	theta_a = A_a^-1 b_a
	p_a     = theta_a . phi + alpha * sqrt(phi^T A_a^-1 phi)
	A_a    <- A_a + phi phi^T
	b_a    <- b_a + reward * phi

The simulator this is ported from inverted A on every selection with numpy. Here
A^-1 is maintained directly and updated by the Sherman-Morrison rank-1 formula

	(A + phi phi^T)^-1 = A^-1 - (A^-1 phi)(A^-1 phi)^T / (1 + phi^T A^-1 phi)

which keeps the update O(d^2) and, more importantly, avoids pulling a linear
algebra library into the module for a d=19 problem. A starts at the identity, so
A^-1 starts at the identity too and stays symmetric positive definite under
rank-1 updates.

Selection is deterministic and draws no randomness: ties go to the lowest arm
index, so the same context always yields the same mode.
*/

import (
	"encoding/json"
	"fmt"
	"math"
)

// LinUCB is the four-armed linear UCB learner. It is not safe for concurrent
// use; LoopState owns the lock that serialises access (see store.go).
type LinUCB struct {
	d     int
	alpha float64
	// aInv[arm] is A_arm^-1, maintained incrementally.
	aInv [NumArms][][]float64
	b    [NumArms][]float64
}

// NewLinUCB returns a learner over d-dimensional contexts with exploration
// coefficient alpha. Both must be positive.
func NewLinUCB(d int, alpha float64) (*LinUCB, error) {
	if d <= 0 {
		return nil, fmt.Errorf("calm: LinUCB feature dimension must be positive, got %d", d)
	}
	if alpha < 0 || math.IsNaN(alpha) {
		return nil, fmt.Errorf("calm: LinUCB alpha must not be negative, got %g", alpha)
	}
	l := &LinUCB{d: d, alpha: alpha}
	for arm := 0; arm < NumArms; arm++ {
		l.aInv[arm] = identity(d)
		l.b[arm] = make([]float64, d)
	}
	return l, nil
}

// Dim returns the context dimension the learner was built for.
func (l *LinUCB) Dim() int { return l.d }

// Alpha returns the exploration coefficient.
func (l *LinUCB) Alpha() float64 { return l.alpha }

// Select returns the arm with the highest UCB score for ctx, together with every
// arm's score so the selection can be explained in the node output and the
// execution trace. Ties resolve to the lowest arm index.
func (l *LinUCB) Select(ctx []float64) (int, [NumArms]float64, error) {
	var scores [NumArms]float64
	if err := l.checkContext(ctx); err != nil {
		return 0, scores, err
	}
	best := 0
	for arm := 0; arm < NumArms; arm++ {
		theta := matVec(l.aInv[arm], l.b[arm])
		mean := dot(theta, ctx)
		scores[arm] = mean + l.alpha*l.uncertainty(arm, ctx)
		if arm > 0 && scores[arm] > scores[best] {
			best = arm
		}
	}
	return best, scores, nil
}

// Update folds one observed reward into the given arm.
func (l *LinUCB) Update(ctx []float64, arm int, reward float64) error {
	if err := l.checkContext(ctx); err != nil {
		return err
	}
	if arm < 0 || arm >= NumArms {
		return fmt.Errorf("calm: LinUCB arm %d out of range [0,%d)", arm, NumArms)
	}
	if math.IsNaN(reward) || math.IsInf(reward, 0) {
		return fmt.Errorf("calm: LinUCB reward must be finite, got %g", reward)
	}

	// Sherman-Morrison rank-1 update of A^-1. A is symmetric, so A^-1 phi is
	// both the column and the row factor of the correction term.
	u := matVec(l.aInv[arm], ctx)
	denom := 1.0 + dot(ctx, u)
	if denom <= 0 || math.IsNaN(denom) {
		// Unreachable while A^-1 stays positive definite, which rank-1 updates
		// of an identity-seeded matrix preserve. Guarded anyway: dividing by a
		// non-positive denominator would silently corrupt the learner.
		return fmt.Errorf("calm: LinUCB Sherman-Morrison denominator %g is not positive", denom)
	}
	for i := 0; i < l.d; i++ {
		ui := u[i] / denom
		row := l.aInv[arm][i]
		for j := 0; j < l.d; j++ {
			row[j] -= ui * u[j]
		}
	}
	for i := 0; i < l.d; i++ {
		l.b[arm][i] += reward * ctx[i]
	}
	return nil
}

// Uncertainty returns the exploration term sqrt(phi^T A_a^-1 phi) for one arm.
// It is the bandit's confidence width, surfaced for the demo readout.
func (l *LinUCB) Uncertainty(ctx []float64, arm int) (float64, error) {
	if err := l.checkContext(ctx); err != nil {
		return 0, err
	}
	if arm < 0 || arm >= NumArms {
		return 0, fmt.Errorf("calm: LinUCB arm %d out of range [0,%d)", arm, NumArms)
	}
	return l.uncertainty(arm, ctx), nil
}

// Theta returns the current ridge estimate A_a^-1 b_a for one arm. The slice is
// freshly allocated, so the caller cannot mutate learner state through it.
func (l *LinUCB) Theta(arm int) ([]float64, error) {
	if arm < 0 || arm >= NumArms {
		return nil, fmt.Errorf("calm: LinUCB arm %d out of range [0,%d)", arm, NumArms)
	}
	return matVec(l.aInv[arm], l.b[arm]), nil
}

func (l *LinUCB) uncertainty(arm int, ctx []float64) float64 {
	v := quadraticForm(l.aInv[arm], ctx)
	if v <= 0 {
		// A^-1 is positive definite, so this only happens from floating-point
		// cancellation on a near-zero context; clamping keeps sqrt real.
		return 0
	}
	return math.Sqrt(v)
}

func (l *LinUCB) checkContext(ctx []float64) error {
	if len(ctx) != l.d {
		return fmt.Errorf("calm: LinUCB context has length %d, want %d", len(ctx), l.d)
	}
	for i, v := range ctx {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("calm: LinUCB context element %d is not finite: %g", i, v)
		}
	}
	return nil
}

// linUCBSnapshot is the serialised learner. A^-1 rather than A is stored because
// that is the matrix the learner actually maintains; round-tripping it avoids a
// re-inversion that would not reproduce the same bits.
type linUCBSnapshot struct {
	Dim   int           `json:"dim"`
	Alpha float64       `json:"alpha"`
	AInv  [][][]float64 `json:"a_inv"`
	B     [][]float64   `json:"b"`
}

// SnapshotJSON serialises the learner for the optional selector state file.
func (l *LinUCB) SnapshotJSON() ([]byte, error) {
	snap := linUCBSnapshot{Dim: l.d, Alpha: l.alpha}
	snap.AInv = make([][][]float64, NumArms)
	snap.B = make([][]float64, NumArms)
	for arm := 0; arm < NumArms; arm++ {
		snap.AInv[arm] = cloneMatrix(l.aInv[arm])
		snap.B[arm] = append([]float64(nil), l.b[arm]...)
	}
	return json.Marshal(snap)
}

// RestoreJSON replaces the learner state from a SnapshotJSON payload. A snapshot
// whose dimension differs from the current context layout is rejected: the arm
// matrices would be meaningless under a changed feature layout.
func (l *LinUCB) RestoreJSON(data []byte) error {
	var snap linUCBSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("calm: cannot decode LinUCB snapshot: %w", err)
	}
	if snap.Dim != l.d {
		return fmt.Errorf("calm: LinUCB snapshot has dimension %d, want %d", snap.Dim, l.d)
	}
	if len(snap.AInv) != NumArms || len(snap.B) != NumArms {
		return fmt.Errorf("calm: LinUCB snapshot has %d/%d arms, want %d", len(snap.AInv), len(snap.B), NumArms)
	}
	for arm := 0; arm < NumArms; arm++ {
		if len(snap.AInv[arm]) != l.d || len(snap.B[arm]) != l.d {
			return fmt.Errorf("calm: LinUCB snapshot arm %d has inconsistent shapes", arm)
		}
		for _, row := range snap.AInv[arm] {
			if len(row) != l.d {
				return fmt.Errorf("calm: LinUCB snapshot arm %d has a row of length %d, want %d", arm, len(row), l.d)
			}
		}
	}
	for arm := 0; arm < NumArms; arm++ {
		l.aInv[arm] = cloneMatrix(snap.AInv[arm])
		l.b[arm] = append([]float64(nil), snap.B[arm]...)
	}
	if snap.Alpha >= 0 && !math.IsNaN(snap.Alpha) {
		l.alpha = snap.Alpha
	}
	return nil
}

func identity(d int) [][]float64 {
	m := make([][]float64, d)
	for i := range m {
		m[i] = make([]float64, d)
		m[i][i] = 1
	}
	return m
}

func cloneMatrix(m [][]float64) [][]float64 {
	out := make([][]float64, len(m))
	for i, row := range m {
		out[i] = append([]float64(nil), row...)
	}
	return out
}

func matVec(m [][]float64, v []float64) []float64 {
	out := make([]float64, len(m))
	for i, row := range m {
		var sum float64
		for j, x := range row {
			sum += x * v[j]
		}
		out[i] = sum
	}
	return out
}

func dot(a, b []float64) float64 {
	var sum float64
	for i, x := range a {
		sum += x * b[i]
	}
	return sum
}

// quadraticForm computes v^T M v.
func quadraticForm(m [][]float64, v []float64) float64 {
	return dot(v, matVec(m, v))
}
