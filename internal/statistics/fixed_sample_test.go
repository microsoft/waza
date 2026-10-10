package statistics

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"
)

func equalClusterWeights(count int) []float64 {
	weights := make([]float64, count)
	for i := range weights {
		weights[i] = 1 / float64(count)
	}
	return weights
}

func TestFixedPairedHoeffdingBoundRangeAndPrecision(t *testing.T) {
	for _, count := range []int{1, 3, 12, 49, 185, 737, 738} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			bound, err := FixedPairedHoeffdingBound(equalClusterWeights(count), 0.05, 1)
			if err != nil {
				t.Fatal(err)
			}
			want := math.Sqrt(2 * math.Log(40) / float64(count))
			if math.Abs(bound.HalfWidth-want) > 1e-12 {
				t.Fatalf("half-width = %v, want %v for signed differences in [-1, 1]", bound.HalfWidth, want)
			}
			if math.Abs(bound.EffectiveClusters-float64(count)) > 1e-8 {
				t.Fatalf("effective clusters = %v, want %d", bound.EffectiveClusters, count)
			}
			if got := bound.HalfWidth <= 0.1; got != (count >= 738) {
				t.Fatalf("%d clusters precision eligible = %v", count, got)
			}
			if count > 1 {
				if _, err := FixedPairedHoeffdingBound(equalClusterWeights(count)[1:], 0.05, 1); err == nil {
					t.Fatalf("omitting one of %d planned equal clusters was accepted", count)
				}
			}
			exactSquares := big.NewRat(1, int64(count))
			assertConservativePairedBound(t, bound, exactSquares, 0.05, 1)
		})
	}
}

func TestFixedPairedHoeffdingBoundConcentratedWeights(t *testing.T) {
	weights := equalClusterWeights(738)
	weights[0] = 0.5
	for i := 1; i < len(weights); i++ {
		weights[i] = 0.5 / 737
	}
	bound, err := FixedPairedHoeffdingBound(weights, 0.05, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bound.HalfWidth <= 0.1 || bound.EffectiveClusters >= 4 {
		t.Fatalf("concentrated weights must not count as 738 equal clusters: %+v", bound)
	}
}

func TestFixedPairedHoeffdingBoundZeroWeightDoesNotAddEvidence(t *testing.T) {
	single, err := FixedPairedHoeffdingBound([]float64{1}, 0.05, 1)
	if err != nil {
		t.Fatal(err)
	}
	padded, err := FixedPairedHoeffdingBound([]float64{1, 0, 0}, 0.05, 1)
	if err != nil {
		t.Fatal(err)
	}
	if padded != single {
		t.Fatalf("zero-weight entries increased evidence: %+v vs %+v", padded, single)
	}
}

func TestFixedPairedHoeffdingBoundMultiplicityAndAlpha(t *testing.T) {
	base, err := FixedPairedHoeffdingBound([]float64{0.5, 0.5}, 0.05, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		alpha       float64
		comparisons int
	}{
		{"larger family", 0.05, 2},
		{"lower alpha", 0.01, 1},
		{"smallest alpha", math.SmallestNonzeroFloat64, math.MaxInt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, err := FixedPairedHoeffdingBound([]float64{0.5, 0.5}, tc.alpha, tc.comparisons)
			if err != nil {
				t.Fatal(err)
			}
			if !isFinite(bound.HalfWidth) || bound.HalfWidth <= base.HalfWidth {
				t.Fatalf("family/confidence adjustment must widen finite bound: %+v", bound)
			}
		})
	}
}

func TestFixedPairedHoeffdingBoundInvalidWeights(t *testing.T) {
	tests := []struct {
		name    string
		weights []float64
		want    string
	}{
		{"nil", nil, "at least one"},
		{"empty", []float64{}, "at least one"},
		{"zero total", []float64{0}, "sum to 1"},
		{"missing mass", []float64{0.5}, "sum to 1"},
		{"extra mass", []float64{0.6, 0.6}, "sum to 1"},
		{"near-unit single", []float64{1 - 5e-13}, "sum to 1"},
		{"near-unit single padded", []float64{1 - 5e-13, 0}, "sum to 1"},
		{"small missing mass", []float64{0.5, 0.5 - 5e-13}, "sum to 1"},
		{"small extra mass", []float64{1, 5e-13}, "sum to 1"},
		{"negative", []float64{-0.1, 1}, "weight 0"},
		{"above one", []float64{1.1}, "weight 0"},
		{"nan", []float64{math.NaN()}, "weight 0"},
		{"positive infinity", []float64{math.Inf(1)}, "weight 0"},
		{"negative infinity", []float64{math.Inf(-1)}, "weight 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FixedPairedHoeffdingBound(tc.weights, 0.05, 1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %+v, %v; want error containing %q", got, err, tc.want)
			}
			if got != (PairedHoeffdingBound{}) {
				t.Fatalf("invalid input returned usable bound: %+v", got)
			}
		})
	}
}

func TestPairedHoeffdingInvalidFamily(t *testing.T) {
	for _, tc := range []struct {
		name        string
		alpha       float64
		comparisons int
		want        string
	}{
		{"alpha zero", 0, 1, "alpha"},
		{"alpha one", 1, 1, "alpha"},
		{"negative alpha", -0.1, 1, "alpha"},
		{"above one alpha", 1.1, 1, "alpha"},
		{"nan alpha", math.NaN(), 1, "alpha"},
		{"positive infinite alpha", math.Inf(1), 1, "alpha"},
		{"negative infinite alpha", math.Inf(-1), 1, "alpha"},
		{"zero comparisons", 0.05, 0, "comparison"},
		{"negative comparisons", 0.05, -1, "comparison"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, err := FixedPairedHoeffdingBound([]float64{1}, tc.alpha, tc.comparisons)
			if err == nil || !strings.Contains(err.Error(), tc.want) || bound != (PairedHoeffdingBound{}) {
				t.Fatalf("bound = %+v, error = %v; want %q", bound, err, tc.want)
			}
			count, err := MinimumPairedClusters(tc.alpha, tc.comparisons, 0.1)
			if err == nil || !strings.Contains(err.Error(), tc.want) || count != 0 {
				t.Fatalf("count = %d, error = %v; want %q", count, err, tc.want)
			}
		})
	}
}

func TestMinimumPairedClusters(t *testing.T) {
	count, err := MinimumPairedClusters(0.05, 1, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 738 {
		t.Fatalf("count = %d, want 738 independent clusters, not trials or attempts", count)
	}
	for _, width := range []float64{1, 0.5, 0.1, 0.01} {
		for _, family := range []int{1, 2, 10} {
			count, err := MinimumPairedClusters(0.05, family, width)
			if err != nil {
				t.Fatal(err)
			}
			logTail := math.Log(2*float64(family)) - math.Log(0.05)
			if math.Sqrt(2*logTail/float64(count)) > width {
				t.Fatalf("%d clusters do not meet width %v, family %d", count, width, family)
			}
			if math.Sqrt(2*logTail/float64(count-1)) <= width {
				t.Fatalf("%d clusters are not minimal for width %v, family %d", count, width, family)
			}
		}
	}
}

func TestMinimumPairedClustersInvalidPrecision(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width float64
		want  string
	}{
		{"zero", 0, "half-width"},
		{"negative", -0.1, "half-width"},
		{"above one", 1.1, "half-width"},
		{"nan", math.NaN(), "half-width"},
		{"positive infinity", math.Inf(1), "half-width"},
		{"negative infinity", math.Inf(-1), "half-width"},
		{"underflow", math.SmallestNonzeroFloat64, "integer precision"},
		{"integer overflow", 1e-12, "integer precision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, err := MinimumPairedClusters(0.05, 1, tc.width)
			if err == nil || !strings.Contains(err.Error(), tc.want) || count != 0 {
				t.Fatalf("count = %d, error = %v; want %q", count, err, tc.want)
			}
		})
	}
}

func TestMinimumPairedClustersExactBoundary(t *testing.T) {
	logTail := math.Log(2) - math.Log(0.05)
	for count := 8; count < 1000; count++ {
		width := math.Sqrt(2 * logTail / float64(count))
		got, err := MinimumPairedClusters(0.05, 1, width)
		if err != nil {
			t.Fatal(err)
		}
		if got != count {
			t.Fatalf("precision exactly at %d-cluster boundary requires %d clusters, want %d", count, got, count)
		}
	}
}

func TestPairedSignedRangeCounterexample(t *testing.T) {
	const count = 185
	// Under independent symmetric +/-1 differences, a radius of 0.1 with
	// 185 clusters does not have the claimed 95% coverage for paired outcomes.
	probability := math.Pow(2, -count)
	tail := 0.0
	for positives := 0; positives <= count; positives++ {
		if math.Abs(float64(2*positives-count))/count >= 0.1 {
			tail += probability
		}
		probability *= float64(count-positives) / float64(positives+1)
	}
	if tail <= 0.05 || math.Abs(tail-0.18555890492457622) > 1e-12 {
		t.Fatalf("incorrect signed-range counterexample tail: %v", tail)
	}
	bound, err := FixedPairedHoeffdingBound(equalClusterWeights(count), 0.05, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bound.HalfWidth <= 0.1 {
		t.Fatalf("bound used each arm's range instead of signed difference range: %+v", bound)
	}
}

func TestFixedPairedHoeffdingBoundConservativeRounding(t *testing.T) {
	weights := []float64{0x1.5970b687b589cp-2, 0x1.962cc5e323610p-3, 0x1.db78e686b8c5cp-2}
	bound, err := FixedPairedHoeffdingBound(weights, 0.05, 1)
	if err != nil {
		t.Fatal(err)
	}
	exactSquares := new(big.Rat)
	for _, weight := range weights {
		w := new(big.Rat).SetFloat64(weight)
		exactSquares.Add(exactSquares, new(big.Rat).Mul(w, w))
	}
	assertConservativePairedBound(t, bound, exactSquares, 0.05, 1)
}

func assertConservativePairedBound(t *testing.T, bound PairedHoeffdingBound, exactSquares *big.Rat, alpha float64, family int) {
	t.Helper()
	effective := new(big.Rat).SetFloat64(bound.EffectiveClusters)
	if new(big.Rat).Mul(effective, exactSquares).Cmp(big.NewRat(1, 1)) > 0 {
		t.Fatalf("rounded effective cluster count overstates evidence: %v", bound.EffectiveClusters)
	}
	factor := new(big.Rat).SetFloat64(2 * (math.Log(2) + math.Log(float64(family)) - math.Log(alpha)))
	requiredRadiusSquared := new(big.Rat).Mul(factor, exactSquares)
	radius := new(big.Rat).SetFloat64(bound.HalfWidth)
	if new(big.Rat).Mul(radius, radius).Cmp(requiredRadiusSquared) < 0 {
		t.Fatalf("rounded radius understates signed-range bound: %v", bound.HalfWidth)
	}
}

func TestFixedPairedHoeffdingBoundDeclaredRelativeWeights(t *testing.T) {
	for _, relative := range [][]float64{
		{1, 1, 1},
		{1, 2, 3},
		{1, 3, 7, 11},
		{0.1, 0.2, 0.3},
		{1, 0, 2, 0, 3},
	} {
		total := 0.0
		for _, weight := range relative {
			total += weight
		}
		weights := make([]float64, len(relative))
		exactTotal := new(big.Rat)
		for _, weight := range relative {
			exactTotal.Add(exactTotal, new(big.Rat).SetFloat64(weight))
		}
		exactSquares := new(big.Rat)
		for i, weight := range relative {
			weights[i] = weight / total
			exactWeight := new(big.Rat).Quo(new(big.Rat).SetFloat64(weight), exactTotal)
			exactSquares.Add(exactSquares, new(big.Rat).Mul(exactWeight, exactWeight))
		}
		bound, err := FixedPairedHoeffdingBound(weights, 0.05, 1)
		if err != nil {
			t.Fatalf("predeclared relative allocation %v generated unusable weights %v: %v", relative, weights, err)
		}
		if !isFinite(bound.HalfWidth) || bound.EffectiveClusters <= 0 {
			t.Fatalf("invalid bound for declared allocation %v: %+v", relative, bound)
		}
		assertConservativePairedBound(t, bound, exactSquares, 0.05, 1)
		// Removing a positive cluster must not silently normalize the survivors.
		for i, weight := range weights {
			if weight == 0 {
				continue
			}
			incomplete := append([]float64(nil), weights[:i]...)
			incomplete = append(incomplete, weights[i+1:]...)
			if _, err := FixedPairedHoeffdingBound(incomplete, 0.05, 1); err == nil {
				t.Fatalf("omitted cluster %d accepted for %v", i, weights)
			}
		}
	}
}

func TestMinimumPairedClustersGeneratedAllocation(t *testing.T) {
	for _, width := range []float64{1, 0.5, 0.1, 0.05} {
		for _, family := range []int{1, 2, 10} {
			count, err := MinimumPairedClusters(0.05, family, width)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := FixedPairedHoeffdingBound(equalClusterWeights(count), 0.05, family)
			if err != nil {
				t.Fatalf("minimum count %d produces unusable allocation: %v", count, err)
			}
			if bound.HalfWidth > width {
				t.Fatalf("minimum count %d produces radius %v above planned %v", count, bound.HalfWidth, width)
			}
		}
	}
}

func TestGeneratedAllocationAtExactPrecisionBoundary(t *testing.T) {
	for _, count := range []int{12, 738} {
		width := math.Sqrt(2 * (math.Log(2) - math.Log(0.05)) / float64(count))
		minimum, err := MinimumPairedClusters(0.05, 1, width)
		if err != nil {
			t.Fatal(err)
		}
		if minimum != count {
			t.Fatalf("mathematical minimum = %d, want %d", minimum, count)
		}
		bound, err := FixedPairedHoeffdingBound(equalClusterWeights(minimum), 0.05, 1)
		if err != nil {
			t.Fatal(err)
		}
		// Precollection builders must check actual weights, not bypass the
		// conservative bound when its radius exceeds an exact boundary.
		if bound.HalfWidth <= width {
			continue
		}
		bound, err = FixedPairedHoeffdingBound(equalClusterWeights(minimum+1), 0.05, 1)
		if err != nil {
			t.Fatal(err)
		}
		if bound.HalfWidth > width {
			t.Fatalf("one precollection rounding-reserve cluster did not meet %v: %+v", width, bound)
		}
	}
}
