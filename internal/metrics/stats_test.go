package metrics

import (
	"math"
	"testing"
)

const epsilon = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestMean(t *testing.T) {
	tests := []struct {
		name   string
		input  []float64
		expect float64
	}{
		{"empty", nil, 0},
		{"single", []float64{5.0}, 5.0},
		{"multiple", []float64{1, 2, 3, 4, 5}, 3.0},
		{"all_same", []float64{7, 7, 7}, 7.0},
		{"negative", []float64{-2, 0, 2}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Mean(tt.input)
			if !approxEqual(got, tt.expect) {
				t.Errorf("Mean(%v) = %f, want %f", tt.input, got, tt.expect)
			}
		})
	}
}

func TestVariance(t *testing.T) {
	tests := []struct {
		name   string
		input  []float64
		expect float64
	}{
		{"empty", nil, 0},
		{"single", []float64{5.0}, 0},
		{"uniform", []float64{3, 3, 3}, 0},
		{"simple", []float64{2, 4, 4, 4, 5, 5, 7, 9}, 4.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Variance(tt.input)
			if !approxEqual(got, tt.expect) {
				t.Errorf("Variance(%v) = %f, want %f", tt.input, got, tt.expect)
			}
		})
	}
}

func TestStdDev(t *testing.T) {
	tests := []struct {
		name   string
		input  []float64
		expect float64
	}{
		{"empty", nil, 0},
		{"single", []float64{5.0}, 0},
		{"simple", []float64{2, 4, 4, 4, 5, 5, 7, 9}, 2.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StdDev(tt.input)
			if !approxEqual(got, tt.expect) {
				t.Errorf("StdDev(%v) = %f, want %f", tt.input, got, tt.expect)
			}
		})
	}
}

func TestConfidenceInterval95(t *testing.T) {
	tests := []struct {
		name   string
		input  []float64
		wantLo float64
		wantHi float64
	}{
		{"empty", nil, 0, 0},
		{"single", []float64{5.0}, 5.0, 5.0},
		{"two_values", []float64{4, 6}, 3.04, 6.96},
		{"five_equal", []float64{3, 3, 3, 3, 3}, 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lo, hi := ConfidenceInterval95(tt.input)
			if !approxEqual(lo, tt.wantLo) || !approxEqual(hi, tt.wantHi) {
				t.Errorf("ConfidenceInterval95(%v) = (%f, %f), want (%f, %f)",
					tt.input, lo, hi, tt.wantLo, tt.wantHi)
			}
		})
	}
}

func TestConfidenceInterval95_TwoValues(t *testing.T) {
	// mean=5, sampleSD=sqrt((1+1)/1)=sqrt(2), margin=1.96*sqrt(2)/sqrt(2)=1.96
	lo, hi := ConfidenceInterval95([]float64{4, 6})
	wantLo := 5.0 - 1.96
	wantHi := 5.0 + 1.96
	if !approxEqual(lo, wantLo) || !approxEqual(hi, wantHi) {
		t.Errorf("got (%f, %f), want (%f, %f)", lo, hi, wantLo, wantHi)
	}
}

func TestIsFlaky(t *testing.T) {
	tests := []struct {
		name     string
		passRate float64
		want     bool
	}{
		{"all_pass", 1.0, false},
		{"all_fail", 0.0, false},
		{"half", 0.5, true},
		{"mostly_pass", 0.9, true},
		{"mostly_fail", 0.1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsFlaky(tt.passRate)
			if got != tt.want {
				t.Errorf("IsFlaky(%f) = %v, want %v", tt.passRate, got, tt.want)
			}
		})
	}
}
