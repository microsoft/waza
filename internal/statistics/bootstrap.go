package statistics

import (
	"math"
	"math/rand"
	"sort"
)

// ConfidenceInterval holds a confidence interval and its point estimate.
// NumBootstraps is set only for bootstrap intervals.
type ConfidenceInterval struct {
	Lower           float64 `json:"lower"`
	Upper           float64 `json:"upper"`
	Mean            float64 `json:"mean"`
	ConfidenceLevel float64 `json:"confidence_level"`
	NumBootstraps   int     `json:"num_bootstraps,omitempty"`
}

// DefaultBootstrapIterations is the number of bootstrap resamples.
const DefaultBootstrapIterations = 10000

// BootstrapCI computes a bootstrap confidence interval over the given scores
// using the percentile method. confidenceLevel should be in (0, 1), e.g. 0.95.
// Returns a zero-value ConfidenceInterval when fewer than 2 data points exist.
func BootstrapCI(scores []float64, confidenceLevel float64) ConfidenceInterval {
	return BootstrapCIWithSeed(scores, confidenceLevel, -1)
}

// BootstrapCIWithSeed is like BootstrapCI but accepts a seed for reproducibility.
// A negative seed uses a non-deterministic source.
func BootstrapCIWithSeed(scores []float64, confidenceLevel float64, seed int64) ConfidenceInterval {
	n := len(scores)
	if n < 2 {
		m := mean(scores)
		return ConfidenceInterval{
			Lower:           m,
			Upper:           m,
			Mean:            m,
			ConfidenceLevel: confidenceLevel,
			NumBootstraps:   0,
		}
	}

	var rng *rand.Rand
	if seed >= 0 {
		rng = rand.New(rand.NewSource(seed))
	} else {
		rng = rand.New(rand.NewSource(rand.Int63()))
	}

	m := mean(scores)
	iters := DefaultBootstrapIterations

	// Bootstrap: resample with replacement, compute mean of each resample
	bootMeans := make([]float64, iters)
	sample := make([]float64, n)
	for i := 0; i < iters; i++ {
		for j := 0; j < n; j++ {
			sample[j] = scores[rng.Intn(n)]
		}
		bootMeans[i] = mean(sample)
	}

	sort.Float64s(bootMeans)

	// Percentile method
	alpha := 1.0 - confidenceLevel
	loIdx := int(math.Floor(alpha / 2.0 * float64(iters)))
	hiIdx := int(math.Floor((1.0 - alpha/2.0) * float64(iters)))
	if hiIdx >= iters {
		hiIdx = iters - 1
	}

	return ConfidenceInterval{
		Lower:           bootMeans[loIdx],
		Upper:           bootMeans[hiIdx],
		Mean:            m,
		ConfidenceLevel: confidenceLevel,
		NumBootstraps:   iters,
	}
}

// IsSignificant returns true if the confidence interval does not contain zero,
// indicating statistical significance at the given confidence level. It is only
// meaningful for an interval on a difference (e.g. treatment minus baseline);
// for an interval on an absolute, non-negative score it only says the score is
// above zero.
func IsSignificant(ci ConfidenceInterval) bool {
	return ci.Lower > 0 || ci.Upper < 0
}

// NormalizedGain computes Hake's normalized gain (1998):
//
//	g = (post - pre) / (1 - pre)
//
// This controls for ceiling effects — a gain from 0.9→0.95 is harder than 0.1→0.15.
// Returns 0 if pre >= 1.0 (already at ceiling) or pre == post (no change).
// Returns 1.0 if post >= 1.0 (reached maximum).
func NormalizedGain(pre, post float64) float64 {
	if pre >= 1.0 {
		return 0.0
	}
	if post >= 1.0 {
		return 1.0
	}
	if math.Abs(post-pre) < 1e-12 {
		return 0.0
	}
	return (post - pre) / (1.0 - pre)
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0.0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}
