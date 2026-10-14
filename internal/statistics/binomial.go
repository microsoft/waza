package statistics

import "math"

// WilsonCI returns the Wilson score interval for a pass rate of successes out
// of trials at the given confidence level (e.g. 0.95). Unlike a bootstrap or the
// normal approximation, it stays inside [0, 1] and keeps its coverage at the
// 3–10 trials typical of agent evals (Bowyer et al., ICML 2025).
// Mean holds the observed rate. Returns a zero interval when trials < 1.
func WilsonCI(successes, trials int, confidenceLevel float64) ConfidenceInterval {
	if trials < 1 {
		return ConfidenceInterval{}
	}
	n := float64(trials)
	p := float64(successes) / n
	z := math.Sqrt2 * math.Erfinv(confidenceLevel)
	z2 := z * z
	denom := 1 + z2/n
	center := (p + z2/(2*n)) / denom
	half := z / denom * math.Sqrt(p*(1-p)/n+z2/(4*n*n))
	return ConfidenceInterval{
		Lower:           math.Max(0, center-half),
		Upper:           math.Min(1, center+half),
		Mean:            p,
		ConfidenceLevel: confidenceLevel,
	}
}

// PassHatK is the unbiased pass^k estimate from τ-bench (Yao et al., 2024): the
// probability that k fresh trials of a task all pass. For a task that passed
// `successes` of its `trials` runs, it is C(successes, k) / C(trials, k).
// Returns 0 when k is outside [1, trials].
func PassHatK(successes, trials, k int) float64 {
	if k < 1 || k > trials || successes < k {
		return 0
	}
	p := 1.0
	for i := range k {
		p *= float64(successes-i) / float64(trials-i)
	}
	return p
}
