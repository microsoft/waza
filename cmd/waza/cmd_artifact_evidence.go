package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/evidenceview"
	"github.com/spf13/cobra"
)

func newArtifactEvidenceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "evidence", Short: "Publish or inspect separately digested evidence declarations",
		Long: "Source manifests must be finalized and currently verified. Evidence states remain declarations, not execution attestation or assurance acceptance.",
	}
	for _, action := range []string{"publish", "inspect", "categories"} {
		use, help := action+" DIGEST", "Reread persisted evidence and its actual source publications"
		switch action {
		case "publish":
			use, help = action+" EVIDENCE.json", "Verify finalized origins and publish canonical evidence as a separate immutable blob"
		case "categories":
			help = "Admit recorded category shapes in one source pass without exposing captured payloads"
		}
		cmd.AddCommand(&cobra.Command{
			Use: use, Short: help, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				var d *evidencestate.Document
				if action == "publish" {
					data, err := readArtifactFile(args[0], evidencestate.MaxDocumentBytes)
					if err != nil {
						return err
					}
					d, err = evidencestate.Parse(data)
					if err != nil {
						return err
					}
				} else if err := evidencestate.ValidateDigest(args[0]); err != nil {
					return err
				}
				directory, err := cmd.Flags().GetString("store")
				if err != nil {
					return err
				}
				azureURL, err := cmd.Flags().GetString("azure-container-url")
				if err != nil {
					return err
				}
				if azureURL != "" {
					directory = ""
				}
				backend, closeStore, err := artifactmanifest.OpenBackend(directory, azureURL)
				if err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
				defer cancel()
				var receipt evidencestate.Inspection
				var categories []evidencestate.CategoryEvidence
				var categoryReceipt evidenceview.Categories
				switch action {
				case "publish":
					receipt, err = evidencestate.Publish(ctx, backend, *d)
				case "categories":
					categoryReceipt, err = evidenceview.InspectCategories(ctx, backend, args[0])
					receipt, categories = categoryReceipt.Inspection, categoryReceipt.Categories
				default:
					receipt, err = evidencestate.Inspect(ctx, backend, args[0])
				}
				if closeErr := closeStore(); closeErr != nil {
					err = errors.Join(err, closeErr)
					receipt.Document = nil
					receipt.Integrity = "failed"
					categoryReceipt.SourceContext = nil
					receipt.Diagnostics = append(receipt.Diagnostics, artifactmanifest.Diagnostic{
						Code: "storage_error", Message: "Evidence operation did not close successfully; no acceptance was established.",
					})
					for i := range categories {
						categories[i] = evidencestate.CategoryEvidence{
							Category: categories[i].Category, State: evidencestate.Unavailable,
							Verification: "unverified", Reason: "storage_error",
						}
					}
				}
				if action == "categories" {
					if err != nil && receipt.Document != nil && !errors.Is(err, evidenceview.ErrContextAdmission) {
						receipt.Diagnostics = append(receipt.Diagnostics, artifactmanifest.Diagnostic{
							Code:    "category_admission_failed",
							Message: "Some category claims were rejected; inspect their individual states. No assurance acceptance was established.",
						})
					}
					categoryReceipt.Inspection, categoryReceipt.Categories = receipt, categories
					encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(categoryReceipt)
					if errors.Is(err, evidenceview.ErrContextAdmission) {
						return &ExitCodeError{Code: 2, Err: errors.Join(err, encodeErr)}
					}
					return errors.Join(err, encodeErr)
				}
				return errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(receipt))
			},
		})
	}
	return cmd
}
