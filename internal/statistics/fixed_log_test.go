package statistics

import (
	"math"
	"math/big"
	"testing"
)

func exactDecimal(t *testing.T, text string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		t.Fatalf("invalid independent decimal reference %q", text)
	}
	return r
}

func eightClusterRadiusReference(t *testing.T) (*big.Rat, *big.Rat) {
	t.Helper()
	// Independent 90-digit calculation using the exact float64 alpha,
	// not decimal 0.05 or a hardware logarithm.
	return exactDecimal(t, "0.96032279131992075294"),
		exactDecimal(t, "0.96032279131992075295")
}

func TestMinimumPairedClustersIndependentRoundedBoundary(t *testing.T) {
	const width = 0.9603227913199207
	lower, upper := eightClusterRadiusReference(t)
	if new(big.Rat).SetFloat64(width).Cmp(lower) >= 0 {
		t.Fatal("reported boundary is not below the independent lower radius")
	}
	for _, tc := range []struct {
		name  string
		width float64
		want  int
	}{
		{"reported_underestimate", width, 9},
		{"observed_width_above_enclosure", math.Nextafter(width, math.Inf(1)), 8},
		{"one_float_below", math.Nextafter(width, math.Inf(-1)), 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, err := MinimumPairedClusters(0.05, 1, tc.width)
			if err != nil || count != tc.want {
				t.Fatalf("minimum=%d err=%v, want %d", count, err, tc.want)
			}
			if tc.want == 8 && new(big.Rat).SetFloat64(tc.width).Cmp(upper) < 0 {
				t.Fatal("accepted boundary is below independent upper radius")
			}
		})
	}
	factor, err := pairedFactorUpper(0.05, 1)
	if err != nil {
		t.Fatal(err)
	}
	squaredLower := new(big.Rat).Mul(lower, lower)
	squaredUpper := new(big.Rat).Mul(upper, upper)
	if factor.Cmp(new(big.Rat).Mul(squaredLower, big.NewRat(8, 1))) < 0 ||
		factor.Cmp(new(big.Rat).Mul(squaredUpper, big.NewRat(8, 1))) > 0 {
		t.Fatal("factor is outside the independent high-precision enclosure")
	}
}

func TestLogMantissaExactSeriesEnclosure(t *testing.T) {
	lower, upper := logMantissaInterval(big.NewRat(1, 1))
	if lower.Sign() != 0 || upper.Sign() != 0 {
		t.Fatal("log(1) must be exactly zero")
	}
	lower, upper = logMantissaInterval(big.NewRat(2, 1))
	referenceLower := exactDecimal(t, "0.693147180559945309417232121458176568075500134360255254120680009493393621969694715605863326")
	referenceUpper := exactDecimal(t, "0.693147180559945309417232121458176568075500134360255254120680009493393621969694715605863327")
	if lower.Cmp(referenceLower) > 0 || upper.Cmp(referenceUpper) < 0 {
		t.Fatal("analytic log(2) interval misses independent reference")
	}
	if new(big.Rat).Sub(upper, lower).Cmp(exactDecimal(t, "0.000000000000000000000000000000000000000063")) >= 0 {
		t.Fatal("fixed series interval is unexpectedly wide")
	}
}

func TestPairedFactorSubnormalAndIntegerFamily(t *testing.T) {
	lnTwoLower := exactDecimal(t, "0.693147180559945309417232121458176568075500134360255254120680009493393621969694715605863326")
	lnTwoUpper := exactDecimal(t, "0.693147180559945309417232121458176568075500134360255254120680009493393621969694715605863327")
	for _, tc := range []struct {
		name        string
		alpha       float64
		family      int
		coefficient int64
	}{
		{"smallest_subnormal", math.SmallestNonzeroFloat64, 1, 2150},
		{"normalized_smallest", math.Ldexp(1, -1022), 1, 2046},
		{"half_alpha_eight_family", 0.5, 8, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factor, err := pairedFactorUpper(tc.alpha, tc.family)
			if err != nil {
				t.Fatal(err)
			}
			independentLower := new(big.Rat).Mul(lnTwoLower, big.NewRat(tc.coefficient, 1))
			independentUpper := new(big.Rat).Mul(lnTwoUpper, big.NewRat(tc.coefficient, 1))
			if factor.Cmp(independentLower) < 0 || factor.Cmp(independentUpper) <= 0 {
				t.Fatal("upper factor is not above independent logarithm reference")
			}
			slack := new(big.Rat).Sub(factor, independentUpper)
			if slack.Cmp(exactDecimal(t, "0.000000000000000000000000000000000001")) > 0 {
				t.Fatal("normalized factor enclosure is unexpectedly wide")
			}
		})
	}
	factor, err := pairedFactorUpper(math.SmallestNonzeroFloat64, math.MaxInt)
	if err != nil || factor.Sign() <= 0 {
		t.Fatalf("extreme exact integer family factor unavailable: %v", err)
	}
	count, err := MinimumPairedClusters(math.SmallestNonzeroFloat64, math.MaxInt, 1)
	if err != nil || count < 1491 {
		t.Fatalf("subnormal precision understated: count=%d err=%v", count, err)
	}
}

func TestRationalUpperFloat(t *testing.T) {
	for _, value := range []*big.Rat{big.NewRat(1, 2), big.NewRat(1, 3), big.NewRat(2, 3)} {
		got := rationalUpperFloat(value)
		if new(big.Rat).SetFloat64(got).Cmp(value) < 0 {
			t.Fatalf("rounded %s downward: %v", value, got)
		}
	}
	if rationalUpperFloat(big.NewRat(1, 2)) != 0.5 {
		t.Fatal("exact representable value was needlessly widened")
	}
}
