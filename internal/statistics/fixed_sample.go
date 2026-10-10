package statistics

import (
	"fmt"
	"math"
)

// PairedHoeffdingBound describes a fixed-sample precision bound, not a release
// verdict or a power calculation. EffectiveClusters conservatively approximates
// 1 / sum(weights^2); arithmetic rounding must not overstate evidence.
type PairedHoeffdingBound struct {
	HalfWidth         float64
	EffectiveClusters float64
}

// FixedPairedHoeffdingBound bounds the weighted expected candidate-minus-baseline
// outcome for a declared fixed suite under its repeated-execution mechanism.
// Each independent cluster contributes a difference in [-1, 1]; dependence
// within a cluster is unrestricted. Trials and retries within the same cluster
// do not supply additional independent samples.
//
// Weights, allocation, scoring, family size and alpha must be fixed before
// collection. The two-sided intervals [estimate-HalfWidth, estimate+HalfWidth]
// have simultaneous coverage at least 1-alpha for the declared comparisons.
// Comparisons need not be independent, but execution clusters must be.
// The radius is sqrt(2*log(2*comparisons/alpha)*sum(weights^2)): signed
// differences have range length 2, not the range length 1 of each arm's score.
//
// This function validates numeric inputs only. It cannot establish independence,
// completeness, precollection binding, golden evidence or billing availability.
// Missing clusters must not be dropped or their weights renormalized. Adaptive
// stopping, selected successful attempts and post-hoc comparisons are unsupported.
func FixedPairedHoeffdingBound(weights []float64, alpha float64, comparisons int) (PairedHoeffdingBound, error) {
	logTail, err := pairedLogTail(alpha, comparisons)
	if err != nil {
		return PairedHoeffdingBound{}, err
	}
	if len(weights) == 0 {
		return PairedHoeffdingBound{}, fmt.Errorf("declare at least one independent cluster weight")
	}

	total := 0.0
	correction := 0.0
	roundingBudget := 0.0
	positiveWeights := 0
	sumSquares := 0.0
	for i, weight := range weights {
		if !isFinite(weight) || weight < 0 || weight > 1 {
			return PairedHoeffdingBound{}, fmt.Errorf("cluster weight %d must be finite and in [0, 1], got %v", i, weight)
		}
		if weight > 0 {
			positiveWeights++
			roundingBudget += (math.Nextafter(weight, math.Inf(1)) - weight) / 2
			// Round squared mass upward so effective evidence is not overstated.
			square := math.Nextafter(weight*weight, math.Inf(1))
			sumSquares = math.Nextafter(sumSquares+square, math.Inf(1))
		}
		next := total + weight
		// Compensate lost low-order bits before checking unit total mass.
		if total >= weight {
			correction += (total - next) + weight
		} else {
			correction += (weight - next) + total
		}
		total = next
	}
	total += correction
	// Only permit coefficient representation and final-sum rounding, not a
	// blanket tolerance. One positive coefficient has no summation ambiguity.
	roundingBudget += (math.Nextafter(total, math.Inf(1)) - total) / 2
	if (positiveWeights <= 1 && total != 1) || math.Abs(total-1) > roundingBudget {
		return PairedHoeffdingBound{}, fmt.Errorf("cluster weights must sum to 1 without reweighting missing evidence, got %v", total)
	}

	return PairedHoeffdingBound{
		HalfWidth:         math.Nextafter(math.Sqrt(2*logTail*sumSquares), math.Inf(1)),
		EffectiveClusters: math.Nextafter(1/sumSquares, math.Inf(-1)),
	}, nil
}

// MinimumPairedClusters returns the equal-weight independent cluster count
// needed for a declared maximum two-sided Hoeffding half-width. It is a
// conservative precision requirement, not power or a guarantee that any
// particular release threshold will be met. Always validate the actual frozen
// weights with FixedPairedHoeffdingBound: unequal weighting or conservative
// arithmetic rounding at an exact precision boundary can require more clusters.
func MinimumPairedClusters(alpha float64, comparisons int, maxHalfWidth float64) (int, error) {
	logTail, err := pairedLogTail(alpha, comparisons)
	if err != nil {
		return 0, err
	}
	if !isFinite(maxHalfWidth) || maxHalfWidth <= 0 || maxHalfWidth > 1 {
		return 0, fmt.Errorf("maximum half-width must be finite and in (0, 1], got %v", maxHalfWidth)
	}

	factor := 2 * logTail
	count := math.Ceil(factor / (maxHalfWidth * maxHalfWidth))
	// Beyond these limits an integer conversion or integer precision is unsafe.
	limit := math.Min(float64(math.MaxInt), 1<<53)
	if !isFinite(count) || count >= limit {
		return 0, fmt.Errorf("required cluster count exceeds supported integer precision; choose a wider half-width or smaller comparison family")
	}
	clusters := int(count)
	// Inverting a squared, rounded half-width can land just above an integer.
	// Check the original inequality to retain the inclusive precision boundary.
	for clusters > 1 && math.Sqrt(factor/float64(clusters-1)) <= maxHalfWidth {
		clusters--
	}
	for math.Sqrt(factor/float64(clusters)) > maxHalfWidth {
		if float64(clusters+1) >= limit {
			return 0, fmt.Errorf("required cluster count exceeds supported integer precision; choose a wider half-width or smaller comparison family")
		}
		clusters++
	}
	return clusters, nil
}

func pairedLogTail(alpha float64, comparisons int) (float64, error) {
	if !isFinite(alpha) || alpha <= 0 || alpha >= 1 {
		return 0, fmt.Errorf("family error probability alpha must be finite and in (0, 1), got %v", alpha)
	}
	if comparisons < 1 {
		return 0, fmt.Errorf("declare at least one comparison in the fixed family, got %d", comparisons)
	}
	// Log separately so a valid tiny alpha does not overflow 2*M/alpha.
	return math.Log(2) + math.Log(float64(comparisons)) - math.Log(alpha), nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
