package statistics

import (
	"math"
	"testing"
)

func TestWilsonCI(t *testing.T) {
	tests := []struct {
		name             string
		successes, n     int
		wantLo, wantHi   float64
		wantObservedRate float64
	}{
		// Reference values from the Wilson score formula at z = 1.959964.
		{"none of three", 0, 3, 0.0, 0.5615, 0.0},
		{"all of three", 3, 3, 0.4385, 1.0, 1.0},
		{"two of three", 2, 3, 0.2077, 0.9385, 2.0 / 3.0},
		{"half of ten", 5, 10, 0.2366, 0.7634, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ci := WilsonCI(tt.successes, tt.n, 0.95)
			if math.Abs(ci.Lower-tt.wantLo) > 1e-4 || math.Abs(ci.Upper-tt.wantHi) > 1e-4 {
				t.Errorf("WilsonCI(%d, %d) = [%.4f, %.4f], want [%.4f, %.4f]",
					tt.successes, tt.n, ci.Lower, ci.Upper, tt.wantLo, tt.wantHi)
			}
			if math.Abs(ci.Mean-tt.wantObservedRate) > 1e-9 {
				t.Errorf("Mean = %f, want observed rate %f", ci.Mean, tt.wantObservedRate)
			}
			if ci.ConfidenceLevel != 0.95 || ci.NumBootstraps != 0 {
				t.Errorf("unexpected metadata: %+v", ci)
			}
		})
	}
}

func TestWilsonCI_StaysInUnitIntervalAndNarrowsWithN(t *testing.T) {
	small := WilsonCI(2, 3, 0.95)
	large := WilsonCI(20, 30, 0.95)
	for _, ci := range []ConfidenceInterval{small, large} {
		if ci.Lower < 0 || ci.Upper > 1 || ci.Lower > ci.Mean || ci.Upper < ci.Mean {
			t.Errorf("interval must lie in [0, 1] and contain the observed rate: %+v", ci)
		}
	}
	if large.Upper-large.Lower >= small.Upper-small.Lower {
		t.Errorf("more trials should give a narrower interval: n=3 %+v, n=30 %+v", small, large)
	}
}

func TestWilsonCI_NoTrials(t *testing.T) {
	if ci := WilsonCI(0, 0, 0.95); ci != (ConfidenceInterval{}) {
		t.Errorf("expected zero interval for no trials, got %+v", ci)
	}
}

func TestPassHatK(t *testing.T) {
	tests := []struct {
		name            string
		successes, n, k int
		want            float64
	}{
		{"always passes", 3, 3, 3, 1.0},
		{"never passes", 0, 5, 1, 0.0},
		{"k=1 is the per-trial pass rate", 2, 3, 1, 2.0 / 3.0},
		{"two of three, k=2", 2, 3, 2, 1.0 / 3.0}, // C(2,2)/C(3,2)
		{"two of three, k=3", 2, 3, 3, 0.0},
		{"four of five, k=2", 4, 5, 2, 0.6}, // C(4,2)/C(5,2) = 6/10
		{"k above trials", 3, 3, 4, 0.0},
		{"k below one", 3, 3, 0, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PassHatK(tt.successes, tt.n, tt.k)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("PassHatK(%d, %d, %d) = %f, want %f", tt.successes, tt.n, tt.k, got, tt.want)
			}
		})
	}
}
