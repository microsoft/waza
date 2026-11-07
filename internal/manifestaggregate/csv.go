package manifestaggregate

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
)

type boundedCSV struct{ buffer bytes.Buffer }

func (w *boundedCSV) Len() int { return w.buffer.Len() }

func (w *boundedCSV) Write(data []byte) (int, error) {
	if len(data) > MaxReportBytes-w.Len() {
		return 0, ErrLimit
	}
	return w.buffer.Write(data)
}

func EncodeCSV(report Report) ([]byte, error) {
	if _, err := Encode(report); err != nil {
		return nil, err
	}
	var output boundedCSV
	writer := csv.NewWriter(&output)
	header := []string{
		"manifest_sha256", "input_state", "recorded_outcome", "evidence_sha256", "evidence_state",
		"usage_state", "usage_verification", "declared_usage_source",
		"turns", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"premium_requests", "ai_credits", "model_metrics_json", "usage_totals", "new_operation_billing", "verified_source_manifests_json",
	}
	if report.Evidence != nil {
		header = append(header, "category_evidence_json", "declared_categories_json", "source_context_json")
	}
	if err := writer.Write(header); err != nil {
		return nil, fmt.Errorf("encoding aggregate CSV header: %w", err)
	}
	for index, member := range report.Members {
		candidates := []EvidenceCandidate{{State: "not_selected"}}
		if report.Evidence != nil && len(report.Evidence.Members[index].Candidates) > 0 {
			candidates = report.Evidence.Members[index].Candidates
		}
		for _, candidate := range candidates {
			row := make([]string, len(header))
			row[0], row[1], row[2] = member.ManifestSHA256, member.State, string(member.Outcome)
			row[3], row[4] = candidate.EvidenceSHA256, candidate.State
			row[5], row[6], row[16], row[17] = "unavailable", "unverified", "withheld", "unavailable"
			if len(candidate.Sources) > 0 {
				data, err := json.Marshal(candidate.Sources)
				if err != nil {
					return nil, fmt.Errorf("encoding aggregate CSV source scope: %w", err)
				}
				row[18] = string(data)
			}
			for _, declaration := range candidate.Declarations {
				if declaration.Category == "usage" {
					row[7] = string(declaration.Source)
				}
			}
			if report.Evidence != nil && candidate.EvidenceSHA256 != "" {
				for offset, value := range []any{candidate.Categories, candidate.Declarations} {
					data, err := json.Marshal(value)
					if err != nil {
						return nil, fmt.Errorf("encoding aggregate CSV category metadata: %w", err)
					}
					row[19+offset] = string(data)
				}
				if candidate.SourceContext != nil {
					data, err := json.Marshal(candidate.SourceContext)
					if err != nil {
						return nil, fmt.Errorf("encoding aggregate CSV original source context: %w", err)
					}
					row[21] = string(data)
				}
			}
			if candidate.Usage != nil {
				row[5], row[6] = string(candidate.Usage.State), candidate.Usage.Verification
				if usage := candidate.Usage.Recorded; usage != nil {
					row[8], row[9], row[10] = strconv.Itoa(usage.Turns), strconv.Itoa(usage.InputTokens), strconv.Itoa(usage.OutputTokens)
					row[11], row[12] = strconv.Itoa(usage.CacheReadTokens), strconv.Itoa(usage.CacheWriteTokens)
					row[13] = strconv.FormatFloat(usage.PremiumRequests, 'g', -1, 64)
					if usage.AICredits != nil {
						row[14] = strconv.FormatFloat(*usage.AICredits, 'g', -1, 64)
					}
					if len(usage.ModelMetrics) > 0 {
						data, err := json.Marshal(usage.ModelMetrics)
						if err != nil {
							return nil, fmt.Errorf("encoding aggregate CSV model usage: %w", err)
						}
						row[15] = string(data)
					}
				}
			}
			if err := writer.Write(row); err != nil {
				return nil, fmt.Errorf("encoding aggregate CSV row: %w", err)
			}
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flushing aggregate CSV: %w", err)
	}
	return output.buffer.Bytes(), nil
}
