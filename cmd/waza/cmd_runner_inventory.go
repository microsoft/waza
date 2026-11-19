package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/microsoft/waza/internal/inventory"
	"github.com/microsoft/waza/internal/rootedfile"
	"github.com/spf13/cobra"
)

const maxInventorySourceBytes = 64 << 20

func newRunnerInventoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Generate and verify runner image and plugin component inventories",
		Long: `Generate a CycloneDX component inventory (profile waza.component-inventory/1.0)
bound to an immutable runner image or plugin digest, and verify offline that an
inventory describes exactly the image or plugin you consumed. Bind a runner
image inventory into a provenance bundle with
'waza runner provenance generate --bind component-inventory=PATH'.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newInventoryGenerateCommand(), newInventoryVerifyCommand())
	return cmd
}

// readInventoryPrefix reads at most one byte past the inventory limit so an
// oversized inventory still reaches the verifier and fails as
// inventory_too_large rather than as a flag error.
func readInventoryPrefix(path string) ([]byte, error) {
	file, err := rootedfile.OpenPath(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("inventory must be a regular file")
	}
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(file, inventory.MaxInventoryBytes+1))
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return nil, err
	}
	return data, nil
}

// fileDigest streams a regular file into its sha256 digest.
func fileDigest(path string) (string, error) {
	file, err := rootedfile.OpenPath(path)
	if err != nil {
		return "", err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("plugin artifact must be a regular file")
	}
	hash := sha256.New()
	if err == nil {
		_, err = io.Copy(hash, file)
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func newInventoryGenerateCommand() *cobra.Command {
	var subjectKind, name, digest, subjectFile, out string
	var dpkgStatus, osRelease, embeddedDir string
	var goBinaries, cyclonedx, files []string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Write a component inventory bound to an image or plugin digest",
		Long: `Write a canonical component inventory for one runner image or plugin.
Components come from Go binaries (build information and file digest), a dpkg
status database, an os-release file, files hashed by sha256, and components
imported from other CycloneDX JSON documents. The subject must be identified by
an immutable sha256 digest; generation fails rather than emit an unbound
inventory.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fail := func(format string, args ...any) error {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf(format, args...)}
			}
			if subjectFile != "" {
				if digest != "" {
					return fail("use --digest or --subject-file, not both")
				}
				computed, err := fileDigest(subjectFile)
				if err != nil {
					return fail("hashing subject: %w", err)
				}
				digest = computed
			}
			in := inventory.GenerateInput{SubjectKind: subjectKind, SubjectName: name, SubjectDigest: digest}
			for _, path := range goBinaries {
				components, err := inventory.GoBinaryComponents(path)
				if err != nil {
					return fail("reading Go binary: %w", err)
				}
				in.Components = append(in.Components, components...)
			}
			distroID, distroVersion := "", ""
			if osRelease != "" {
				data, err := readArtifactFile(osRelease, 64<<10)
				if err != nil {
					return fail("reading os-release: %w", err)
				}
				components, err := inventory.OSComponents(data)
				if err != nil {
					return fail("%w", err)
				}
				distroID, distroVersion = inventory.OSRelease(data)
				in.Components = append(in.Components, components...)
			}
			if dpkgStatus != "" {
				data, err := readArtifactFile(dpkgStatus, maxInventorySourceBytes)
				if err != nil {
					return fail("reading dpkg status: %w", err)
				}
				components, err := inventory.DpkgComponents(data, distroID, distroVersion)
				if err != nil {
					return fail("%w", err)
				}
				in.Components = append(in.Components, components...)
			}
			for _, path := range cyclonedx {
				data, err := readArtifactFile(path, maxInventorySourceBytes)
				if err != nil {
					return fail("reading CycloneDX source: %w", err)
				}
				components, err := inventory.CycloneDXComponents(data)
				if err != nil {
					return fail("importing %s: %w", path, err)
				}
				in.Components = append(in.Components, components...)
			}
			if embeddedDir != "" {
				if len(goBinaries) == 0 {
					return fail("--embedded-dir requires --go-binary")
				}
				components, err := inventory.EmbeddedComponents(embeddedDir, goBinaries)
				if err != nil {
					return fail("inventorying embedded payloads: %w", err)
				}
				in.Components = append(in.Components, components...)
			}
			for _, path := range files {
				component, err := inventory.FileComponent(path)
				if err != nil {
					return fail("hashing file: %w", err)
				}
				in.Components = append(in.Components, component)
			}
			data, err := inventory.Generate(in)
			if err != nil {
				return fail("generating inventory: %w", err)
			}
			if err := writeNewFile(out, data, 0o644); err != nil {
				return fail("writing inventory: %w", err)
			}
			doc, err := inventory.Parse(data)
			if err != nil {
				return fail("%w", err)
			}
			subject := doc.Subject()
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "{\"status\":\"generated\",\"inventory\":%q,\"sha256\":%q,\"subject\":{\"kind\":%q,\"name\":%q,\"digest\":%q},\"components\":%d}\n",
				out, inventory.DigestOf(data), subject.Kind, subject.Name, subject.Digest, len(doc.Components))
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&subjectKind, "subject", inventory.SubjectImage, "Subject kind: image or plugin")
	f.StringVar(&name, "name", "", "Image reference/repository or plugin name")
	f.StringVar(&digest, "digest", "", "Immutable subject digest, sha256:<64 hex> (image manifest digest or plugin artifact digest)")
	f.StringVar(&subjectFile, "subject-file", "", "Plugin artifact whose sha256 becomes the subject digest")
	f.StringArrayVar(&goBinaries, "go-binary", nil, "Go binary to inventory (repeatable)")
	f.StringVar(&embeddedDir, "embedded-dir", "", "Directory of bundled Copilot CLI/runtime payloads (internal/embedded); records those embedded in a --go-binary")
	f.StringVar(&dpkgStatus, "dpkg-status", "", "dpkg status database (for example /var/lib/dpkg/status from the image)")
	f.StringVar(&osRelease, "os-release", "", "os-release file from the image (adds the OS component and distro qualifiers)")
	f.StringArrayVar(&cyclonedx, "from-cyclonedx", nil, "Import components from a CycloneDX JSON document (repeatable)")
	f.StringArrayVar(&files, "file", nil, "File to inventory by sha256 (repeatable)")
	f.StringVar(&out, "out", "", "Path for the new inventory (never overwritten)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func newInventoryVerifyCommand() *cobra.Command {
	var inventoryPath, imageDigest, imageReference, pluginPath, pluginDigest, pluginName string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify offline that an inventory is bound to the consumed image or plugin",
		Long: `Verify a component inventory offline against the image or plugin you
consumed. Prints a waza.inventory-verification report; exit 0 when verified, 1
when the inventory is missing, malformed, unbound, mismatched or altered, or
when no consumed digest was supplied, and 2 for invalid flags. Pass
--image-digest for runner images, or --plugin/--plugin-digest for plugins.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			isPlugin := pluginPath != "" || pluginDigest != "" || pluginName != ""
			if isPlugin && (imageDigest != "" || imageReference != "") {
				return &ExitCodeError{Code: 2, Err: errors.New("verify an image or a plugin, not both")}
			}
			expect := inventory.Expectation{Kind: inventory.SubjectImage, Digest: imageDigest}
			if imageReference != "" {
				expect.Name = inventory.ImageRepository(imageReference)
			}
			if isPlugin {
				expect = inventory.Expectation{Kind: inventory.SubjectPlugin, Digest: pluginDigest, Name: pluginName}
				if pluginPath != "" {
					if pluginDigest != "" {
						return &ExitCodeError{Code: 2, Err: errors.New("use --plugin or --plugin-digest, not both")}
					}
					computed, err := fileDigest(pluginPath)
					if err != nil {
						return &ExitCodeError{Code: 2, Err: fmt.Errorf("hashing plugin: %w", err)}
					}
					expect.Digest = computed
				}
			}
			data, err := readInventoryPrefix(inventoryPath)
			if errors.Is(err, fs.ErrNotExist) {
				data, err = nil, nil
			}
			if err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("reading inventory: %w", err)}
			}
			report := inventory.Verify(data, expect)
			encoded, err := inventory.EncodeReport(report)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			return emitRunnerOperationJSON(cmd, encoded, report.ExitCode, report.Status)
		},
	}
	f := cmd.Flags()
	f.StringVar(&inventoryPath, "inventory", "", "Component inventory JSON")
	f.StringVar(&imageDigest, "image-digest", "", "Consumed runner image manifest digest, sha256:<64 hex>")
	f.StringVar(&imageReference, "image-reference", "", "Optional consumed image reference; pins the inventory repository")
	f.StringVar(&pluginPath, "plugin", "", "Consumed plugin artifact; its sha256 is the expected digest")
	f.StringVar(&pluginDigest, "plugin-digest", "", "Consumed plugin artifact digest, sha256:<64 hex>")
	f.StringVar(&pluginName, "plugin-name", "", "Optional plugin name to pin")
	_ = cmd.MarkFlagRequired("inventory")
	return cmd
}
