package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/rootedfile"
	"github.com/spf13/cobra"
)

func readArtifactFile(path string, limit int64) ([]byte, error) {
	file, err := rootedfile.OpenPath(path)
	if err != nil {
		return nil, fmt.Errorf("opening artifact input: %w", err)
	}
	info, statErr := file.Stat()
	if statErr == nil && (!info.Mode().IsRegular() || info.Size() > limit) {
		statErr = errors.New("artifact input must be a bounded regular file")
	}
	var data []byte
	var readErr error
	if statErr == nil {
		data, readErr = io.ReadAll(io.LimitReader(file, limit+1))
		if int64(len(data)) > limit {
			readErr = errors.New("artifact input exceeds declared bounds")
		}
	}
	if err := errors.Join(statErr, readErr, file.Close()); err != nil {
		return nil, err
	}
	return data, nil
}

func newArtifactCommand() *cobra.Command {
	var directory, azureURL string
	cmd := &cobra.Command{
		Use: "artifact", Short: "Publish and verify immutable manifest artifacts without executing agents",
		Long: "Opt-in immutable publication. Verification checks content and declared context, not execution attestation. Legacy result storage is unchanged.",
	}
	cmd.PersistentFlags().StringVar(&directory, "store", ".waza-artifacts", "Existing private local immutable store directory")
	cmd.PersistentFlags().StringVar(&azureURL, "azure-container-url", "", "Explicit HTTPS Azure container URL; uses existing credentials without initiating interactive login")
	cmd.MarkFlagsMutuallyExclusive("store", "azure-container-url")

	open := func() (*artifactmanifest.Store, func() error, error) {
		backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
		if err != nil {
			return nil, nil, err
		}
		return artifactmanifest.NewStore(backend), closeBackend, nil
	}
	run := func(action string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) (resultErr error) {
			store, closeStore, err := open()
			if err != nil {
				return err
			}
			defer func() { resultErr = errors.Join(resultErr, closeStore()) }()
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			encode := func(value any) error { return json.NewEncoder(cmd.OutOrStdout()).Encode(value) }
			switch action {
			case "create", "upload":
				data, err := readArtifactFile(args[0], artifactmanifest.MaxManifestBytes)
				if err != nil {
					return err
				}
				m, err := artifactmanifest.Parse(data)
				if err != nil {
					return err
				}
				if action == "create" {
					digest, err := store.Create(ctx, data)
					if err != nil {
						return err
					}
					return encode(struct {
						ManifestSHA256 string `json:"manifest_sha256"`
						Publication    string `json:"publication"`
					}{digest, "declared"})
				}
				for _, a := range m.Artifacts {
					if a.Name != args[1] {
						continue
					}
					content, err := readArtifactFile(args[2], a.SizeBytes)
					if err != nil {
						return err
					}
					if err := store.Put(ctx, a, content); err != nil {
						return err
					}
					return encode(a)
				}
				return fmt.Errorf("artifact %q is not declared in the manifest", args[1])
			case "finalize", "inspect":
				var r artifactmanifest.Receipt
				if action == "finalize" {
					r, err = store.Finalize(ctx, args[0])
				} else {
					r, err = store.Inspect(ctx, args[0])
				}
				return errors.Join(err, encode(r))
			default:
				return errors.New("unknown artifact action")
			}
		}
	}
	for _, action := range []struct {
		name, use, help string
		args            int
	}{
		{"create", "create MANIFEST.json", "Create an immutable declaration; uploads may follow", 1},
		{"upload", "upload MANIFEST.json NAME FILE", "Upload exactly the named artifact bytes without replacing content", 3},
		{"finalize", "finalize DIGEST", "Verify all content and conditionally finalize this manifest", 1},
		{"inspect", "inspect DIGEST", "Reread publication and integrity state; invalid content exits nonzero", 1},
	} {
		cmd.AddCommand(&cobra.Command{Use: action.use, Short: action.help, Args: cobra.ExactArgs(action.args), RunE: run(action.name)})
	}
	return cmd
}
