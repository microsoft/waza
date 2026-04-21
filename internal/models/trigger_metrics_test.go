package models

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeTriggerMetrics(t *testing.T) {
	tests := []struct {
		name    string
		results []TriggerResult
		want    *TriggerMetrics
	}{
		{
			name:    "empty input returns nil",
			results: []TriggerResult{},
			want:    nil,
		},
		{
			name: "all true positives",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: true},
				{ShouldTrigger: true, DidTrigger: true},
			},
			want: &TriggerMetrics{
				TP: 2, FP: 0, TN: 0, FN: 0,
				Precision: 1.0, Recall: 1.0, F1: 1.0, Accuracy: 1.0,
			},
		},
		{
			name: "all true negatives",
			results: []TriggerResult{
				{ShouldTrigger: false, DidTrigger: false},
				{ShouldTrigger: false, DidTrigger: false},
			},
			want: &TriggerMetrics{
				TP: 0, FP: 0, TN: 2, FN: 0,
				Precision: 0.0, Recall: 0.0, F1: 0.0, Accuracy: 1.0,
			},
		},
		{
			name: "mixed results",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: true},   // TP
				{ShouldTrigger: true, DidTrigger: false},  // FN
				{ShouldTrigger: false, DidTrigger: true},  // FP
				{ShouldTrigger: false, DidTrigger: false}, // TN
			},
			want: &TriggerMetrics{
				TP: 1, FP: 1, TN: 1, FN: 1,
				Precision: 0.5, Recall: 0.5, F1: 0.5, Accuracy: 0.5,
			},
		},
		{
			name: "all false positives",
			results: []TriggerResult{
				{ShouldTrigger: false, DidTrigger: true},
				{ShouldTrigger: false, DidTrigger: true},
			},
			want: &TriggerMetrics{
				TP: 0, FP: 2, TN: 0, FN: 0,
				Precision: 0.0, Recall: 0.0, F1: 0.0, Accuracy: 0.0,
			},
		},
		{
			name: "all false negatives",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: false},
				{ShouldTrigger: true, DidTrigger: false},
			},
			want: &TriggerMetrics{
				TP: 0, FP: 0, TN: 0, FN: 2,
				Precision: 0.0, Recall: 0.0, F1: 0.0, Accuracy: 0.0,
			},
		},
		{
			name: "high precision low recall",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: true},   // TP
				{ShouldTrigger: true, DidTrigger: false},  // FN
				{ShouldTrigger: true, DidTrigger: false},  // FN
				{ShouldTrigger: false, DidTrigger: false}, // TN
			},
			// precision = 1/(1+0) = 1.0, recall = 1/(1+2) = 0.3333
			// f1 = 2*1*0.3333/(1+0.3333) = 0.5
			want: &TriggerMetrics{
				TP: 1, FP: 0, TN: 1, FN: 2,
				Precision: 1.0, Recall: 0.3333, F1: 0.5, Accuracy: 0.5,
			},
		},
		{
			name: "realistic scenario 8 samples",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: true},   // TP
				{ShouldTrigger: true, DidTrigger: true},   // TP
				{ShouldTrigger: true, DidTrigger: true},   // TP
				{ShouldTrigger: true, DidTrigger: false},  // FN
				{ShouldTrigger: false, DidTrigger: false}, // TN
				{ShouldTrigger: false, DidTrigger: false}, // TN
				{ShouldTrigger: false, DidTrigger: true},  // FP
				{ShouldTrigger: false, DidTrigger: false}, // TN
			},
			// TP=3, FP=1, TN=3, FN=1
			// precision = 3/4 = 0.75, recall = 3/4 = 0.75
			// f1 = 2*0.75*0.75/1.5 = 0.75
			// accuracy = 6/8 = 0.75
			want: &TriggerMetrics{
				TP: 3, FP: 1, TN: 3, FN: 1,
				Precision: 0.75, Recall: 0.75, F1: 0.75, Accuracy: 0.75,
			},
		},
		{
			name: "medium confidence weights half",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: true, Confidence: "high"},     // TP weight 1.0
				{ShouldTrigger: false, DidTrigger: true, Confidence: "medium"},  // FP weight 0.5
				{ShouldTrigger: false, DidTrigger: false, Confidence: "medium"}, // TN weight 0.5
			},
			// tp=1.0, fp=0.5, tn=0.5, fn=0
			// precision = 1.0/1.5 = 0.6667, recall = 1.0/1.0 = 1.0
			// f1 = 2*0.6667*1.0/1.6667 = 0.8
			// accuracy = 1.5/2.0 = 0.75
			want: &TriggerMetrics{
				TP: 1, FP: 1, TN: 1, FN: 0,
				Precision: 0.6667, Recall: 1.0, F1: 0.8, Accuracy: 0.75,
			},
		},
		{
			name: "empty confidence defaults to high",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: true, Confidence: ""},
				{ShouldTrigger: false, DidTrigger: false, Confidence: ""},
			},
			want: &TriggerMetrics{
				TP: 1, FP: 0, TN: 1, FN: 0,
				Precision: 1.0, Recall: 1.0, F1: 1.0, Accuracy: 1.0,
			},
		},
		{
			name: "all medium confidence",
			results: []TriggerResult{
				{ShouldTrigger: true, DidTrigger: true, Confidence: "medium"},  // TP 0.5
				{ShouldTrigger: true, DidTrigger: false, Confidence: "medium"}, // FN 0.5
			},
			// tp_w=0.5, fn_w=0.5; precision=1.0, recall=0.5, f1=0.6667, accuracy=0.5
			// Integer counts: TP=1, FN=1 (actual result counts, not weighted)
			want: &TriggerMetrics{
				TP: 1, FP: 0, TN: 0, FN: 1,
				Precision: 1.0, Recall: 0.5, F1: 0.6667, Accuracy: 0.5,
			},
		},
		{
			name: "medium confidence does not reduce integer counts (issue #184)",
			results: func() []TriggerResult {
				// 8 should-trigger (6 high + 2 medium), 8 should-not-trigger (6 high + 2 medium)
				// None triggered → all should-trigger are FN, all should-not-trigger are TN.
				var r []TriggerResult
				for i := 0; i < 6; i++ {
					r = append(r, TriggerResult{ShouldTrigger: true, DidTrigger: false, Confidence: "high"})
				}
				for i := 0; i < 2; i++ {
					r = append(r, TriggerResult{ShouldTrigger: true, DidTrigger: false, Confidence: "medium"})
				}
				for i := 0; i < 6; i++ {
					r = append(r, TriggerResult{ShouldTrigger: false, DidTrigger: false, Confidence: "high"})
				}
				for i := 0; i < 2; i++ {
					r = append(r, TriggerResult{ShouldTrigger: false, DidTrigger: false, Confidence: "medium"})
				}
				return r
			}(),
			// FN count = 8 (not 7), TN count = 8 (not 7)
			// Weighted: fn_w=7.0, tn_w=7.0 → accuracy = 7.0/14.0 = 0.5
			want: &TriggerMetrics{
				TP: 0, FP: 0, TN: 8, FN: 8,
				Precision: 0.0, Recall: 0.0, F1: 0.0, Accuracy: 0.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeTriggerMetrics(tt.results)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			require.NotNil(t, got)
			if got.TP != tt.want.TP || got.FP != tt.want.FP ||
				got.TN != tt.want.TN || got.FN != tt.want.FN {
				t.Errorf("confusion matrix: got TP=%d FP=%d TN=%d FN=%d, want TP=%d FP=%d TN=%d FN=%d",
					got.TP, got.FP, got.TN, got.FN,
					tt.want.TP, tt.want.FP, tt.want.TN, tt.want.FN)
			}
			const eps = 0.001
			if math.Abs(got.Precision-tt.want.Precision) > eps {
				t.Errorf("Precision: got %f, want %f", got.Precision, tt.want.Precision)
			}
			if math.Abs(got.Recall-tt.want.Recall) > eps {
				t.Errorf("Recall: got %f, want %f", got.Recall, tt.want.Recall)
			}
			if math.Abs(got.F1-tt.want.F1) > eps {
				t.Errorf("F1: got %f, want %f", got.F1, tt.want.F1)
			}
			if math.Abs(got.Accuracy-tt.want.Accuracy) > eps {
				t.Errorf("Accuracy: got %f, want %f", got.Accuracy, tt.want.Accuracy)
			}
		})
	}
}
