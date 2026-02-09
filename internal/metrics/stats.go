package metrics

import "math"

// Mean computes the arithmetic mean of a float64 slice.
// Returns 0 for empty input.
func Mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// Variance computes the population variance of a float64 slice.
// Returns 0 for empty input.
func Variance(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := Mean(values)
	sumSq := 0.0
	for _, v := range values {
		d := v - m
		sumSq += d * d
	}
	return sumSq / float64(len(values))
}

// StdDev computes the population standard deviation.
func StdDev(values []float64) float64 {
	return math.Sqrt(Variance(values))
}

// ConfidenceInterval95 returns the 95% confidence interval (low, high)
// using the normal approximation (z=1.96). Returns (mean, mean) when
// fewer than 2 data points are available.
func ConfidenceInterval95(values []float64) (float64, float64) {
	n := len(values)
	if n < 2 {
		m := Mean(values)
		return m, m
	}
	m := Mean(values)
	// sample standard deviation (Bessel's correction)
	sumSq := 0.0
	for _, v := range values {
		d := v - m
		sumSq += d * d
	}
	sampleSD := math.Sqrt(sumSq / float64(n-1))
	margin := 1.96 * sampleSD / math.Sqrt(float64(n))
	return m - margin, m + margin
}

// IsFlaky returns true when the pass rate is strictly between 0 and 1,
// meaning the task sometimes passes and sometimes fails.
func IsFlaky(passRate float64) bool {
	return passRate > 0 && passRate < 1
}
