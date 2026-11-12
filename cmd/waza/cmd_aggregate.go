package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/spf13/cobra"
)

func newAggregateCommand() *cobra.Command {
	var directory, inputPath, outputPath string
	cmd := &cobra.Command{
		Use:          "aggregate [MANIFEST_SHA256...]",
		Short:        "Aggregate public completed manifests without agents or worker state",
		SilenceUsage: true,
		Long:         "Aggregate explicit references from an existing local immutable store. Optional evidence sidecars qualify recorded categories and execution records. Admission completeness is not a pass verdict or execution attestation. Recorded historical usage is not new SDK billing.",
		RunE: func(cmd *cobra.Command, args []string) error {
			inputs := make([]manifestaggregate.Input, 0, len(args))
			if inputPath != "" {
				if len(args) != 0 {
					return fmt.Errorf("%w: --inputs and positional digests are mutually exclusive", manifestaggregate.ErrInvalid)
				}
				data, err := readArtifactFile(inputPath, artifactmanifest.MaxManifestBytes)
				if err != nil {
					return err
				}
				inputs, err = manifestaggregate.ParseInputs(data)
				if err != nil {
					return err
				}
			} else {
				for _, digest := range args {
					inputs = append(inputs, manifestaggregate.Input{Manifest: digest})
				}
			}
			inputs, err := manifestaggregate.Normalize(inputs)
			if err != nil {
				return err
			}
			sources, closeSources, err := manifestaggregate.OpenLocal(directory)
			if err != nil {
				return err
			}
			report, buildErr := manifestaggregate.Build(cmd.Context(), sources, inputs, manifestaggregate.DefaultLimits())
			closeErr := closeSources()
			if closeErr != nil {
				// Do not emit a success-shaped report after a local close failure.
				return errors.Join(buildErr, fmt.Errorf("closing aggregate sources: %w", closeErr))
			}
			if report == nil {
				return buildErr
			}
			wire, err := manifestaggregate.Encode(*report)
			if err != nil {
				return errors.Join(buildErr, err)
			}
			if outputPath != "" {
				if err := writeAggregateOutput(outputPath, wire); err != nil {
					return errors.Join(buildErr, err)
				}
			}
			_, emitErr := cmd.OutOrStdout().Write(append(wire, '\n'))
			return errors.Join(buildErr, emitErr)
		},
	}
	cmd.Flags().StringVar(&directory, "store", ".waza-artifacts", "Existing local immutable store (never reads ambient configuration or starts credentials)")
	cmd.Flags().StringVar(&inputPath, "inputs", "", "JSON array of manifest_sha256 and optional evidence_sha256 reference pairs")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Create a new canonical aggregate JSON file; existing files are never replaced")
	return cmd
}

func writeAggregateOutput(path string, wire []byte) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("opening aggregate output parent: %w", err)
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Join(fmt.Errorf("creating aggregate output: %w", err), root.Close())
	}
	written, writeErr := file.Write(wire)
	if writeErr == nil && written != len(wire) {
		writeErr = io.ErrShortWrite
	}
	if err := errors.Join(writeErr, file.Close(), root.Close()); err != nil {
		return fmt.Errorf("aggregate output may be partial at %q: %w", path, err)
	}
	return nil
}
