package webapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
)

type evidenceCategoryReader func(context.Context, string) (evidencestate.Inspection, []evidencestate.CategoryEvidence, error)

type evidenceCategoryReceipt struct {
	evidencestate.Inspection
	Categories []evidencestate.CategoryEvidence `json:"categories"`
}

func registerEvidenceCategoryRoute(mux *http.ServeMux, read evidenceCategoryReader) {
	for _, format := range []string{"", "document", "csv"} {
		path := "GET /api/v1/evidence/{digest}/categories"
		if format != "" {
			path += "/" + format
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			inspection, categories, err := read(ctx, r.PathValue("digest"))
			if err == nil && (inspection.Integrity != "verified" || inspection.Document == nil || len(categories) != 6) {
				err = artifactmanifest.ErrCorrupt
				inspection.Integrity, inspection.Document = "failed", nil
				_, missing, missingErr := evidencestate.InspectCategories(ctx, nil, r.PathValue("digest"))
				categories = missing
				err = errors.Join(err, missingErr)
				inspection.Diagnostics = append(inspection.Diagnostics, artifactmanifest.Diagnostic{
					Code: "invalid_evidence_receipt", Message: "The category receipt did not establish current source integrity.",
				})
			}
			status := http.StatusOK
			switch {
			case err == nil:
			case errors.Is(err, artifactmanifest.ErrCorrupt), errors.Is(err, artifactmanifest.ErrConflict):
				status = http.StatusConflict
			case errors.Is(err, evidencestate.ErrInvalid), errors.Is(err, artifactmanifest.ErrInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, artifactmanifest.ErrMissing):
				status = http.StatusNotFound
			default:
				status = http.StatusServiceUnavailable
				slog.Error("recorded evidence category inspection failed", "error", err)
			}
			if err != nil && inspection.Document != nil {
				inspection.Diagnostics = append(inspection.Diagnostics, artifactmanifest.Diagnostic{
					Code:    "category_admission_failed",
					Message: "Some category claims were rejected; inspect their individual states. No assurance acceptance was established.",
				})
			}
			receipt := evidenceCategoryReceipt{inspection, categories}
			if err != nil || format == "" {
				writeJSON(w, status, receipt)
				return
			}
			var data []byte
			var encodeErr error
			extension, contentType := "json", "application/json"
			if format == "document" {
				data, encodeErr = json.Marshal(receipt)
			} else {
				extension, contentType = "csv", "text/csv; charset=utf-8"
				data, encodeErr = encodeCategoryCSV(receipt)
			}
			if encodeErr != nil {
				slog.Error("encoding qualified category download", "error", encodeErr)
				http.Error(w, "Category download could not be encoded.", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Disposition", `attachment; filename="execution-evidence-categories-`+r.PathValue("digest")+`.`+extension+`"`)
			if _, err := w.Write(data); err != nil {
				slog.Error("writing qualified category download", "error", err)
			}
		})
	}
}

func encodeCategoryCSV(receipt evidenceCategoryReceipt) ([]byte, error) {
	var data bytes.Buffer
	rows := [][]string{{"category", "declared_state", "admitted_state", "verification", "reason", "observed_count",
		"turns", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "premium_requests", "ai_credits",
		"declared_source", "declared_context", "origin_manifest", "evidence_sha256", "execution_id"}}
	records := make(map[evidencestate.Category]evidencestate.Record, len(receipt.Document.Categories))
	for _, record := range receipt.Document.Categories {
		records[record.Category] = record
	}
	executionID := ""
	if receipt.Document.ExecutionSource != nil {
		executionID = receipt.Document.ExecutionSource.ExecutionID
	}
	for _, category := range receipt.Categories {
		count := ""
		if category.ObservedCount != nil {
			count = strconv.FormatInt(*category.ObservedCount, 10)
		}
		row := []string{string(category.Category), string(category.DeclaredState), string(category.State),
			category.Verification, category.Reason, count, "", "", "", "", "", "", ""}
		if usage := category.Usage; usage != nil {
			row[6], row[7], row[8] = strconv.Itoa(usage.Turns), strconv.Itoa(usage.InputTokens), strconv.Itoa(usage.OutputTokens)
			row[9], row[10] = strconv.Itoa(usage.CacheReadTokens), strconv.Itoa(usage.CacheWriteTokens)
			row[11] = strconv.FormatFloat(usage.PremiumRequests, 'g', -1, 64)
			if usage.AICredits != nil {
				row[12] = strconv.FormatFloat(*usage.AICredits, 'g', -1, 64)
			}
		}
		record := records[category.Category]
		row = append(row, string(record.Source), string(record.Context), receipt.Document.OriginManifest, receipt.EvidenceSHA256, executionID)
		rows = append(rows, row)
	}
	err := csv.NewWriter(&data).WriteAll(rows)
	return data.Bytes(), err
}
