package models

import "math"

// TriggerMetrics holds classification metrics for trigger accuracy.
type TriggerMetrics struct {
	TP        int     `json:"true_positives"`
	FP        int     `json:"false_positives"`
	TN        int     `json:"true_negatives"`
	FN        int     `json:"false_negatives"`
	Errors    int     `json:"errors,omitempty"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Accuracy  float64 `json:"accuracy"`
}

// TriggerResult pairs an expected trigger label with the actual outcome.
type TriggerResult struct {
	Prompt        string            `json:"prompt"`
	Confidence    string            `json:"confidence,omitempty"`
	ShouldTrigger bool              `json:"should_trigger"`
	DidTrigger    bool              `json:"did_trigger"`
	ErrorMsg      string            `json:"error_msg,omitempty"`
	FinalOutput   string            `json:"final_output,omitempty"`
	Transcript    []TranscriptEvent `json:"transcript,omitempty"`
	ToolCalls     []ToolCall        `json:"tool_calls,omitempty"`
}

// ComputeTriggerMetrics calculates precision, recall, F1, and accuracy
// from a set of trigger classification results. Results are weighted by
// confidence: "high" (or empty) counts as 1.0, "medium" as 0.5.
// Returns nil when results is empty.
func ComputeTriggerMetrics(results []TriggerResult) *TriggerMetrics {
	if len(results) == 0 {
		return nil
	}

	var tp, fp, tn, fn float64
	for _, r := range results {
		w := triggerConfidenceWeight(r.Confidence)
		switch {
		case r.ShouldTrigger && r.DidTrigger:
			tp += w
		case !r.ShouldTrigger && r.DidTrigger:
			fp += w
		case !r.ShouldTrigger && !r.DidTrigger:
			tn += w
		case r.ShouldTrigger && !r.DidTrigger:
			fn += w
		}
	}

	total := tp + fp + tn + fn

	precision := triggerSafeDivide(tp, tp+fp)
	recall := triggerSafeDivide(tp, tp+fn)

	var f1 float64
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}

	accuracy := triggerSafeDivide(tp+tn, total)

	return &TriggerMetrics{
		TP:        int(math.Round(tp)),
		FP:        int(math.Round(fp)),
		TN:        int(math.Round(tn)),
		FN:        int(math.Round(fn)),
		Precision: triggerRoundTo4(precision),
		Recall:    triggerRoundTo4(recall),
		F1:        triggerRoundTo4(f1),
		Accuracy:  triggerRoundTo4(accuracy),
	}
}

func triggerConfidenceWeight(c string) float64 {
	switch c {
	case "medium":
		return 0.5
	default:
		return 1.0
	}
}

func triggerSafeDivide(num, den float64) float64 {
	if den == 0 {
		return 0.0
	}
	return num / den
}

func triggerRoundTo4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
