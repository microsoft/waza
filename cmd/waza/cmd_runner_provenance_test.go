package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runProvenanceCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := newRunnerCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"provenance"}, args...))
	err := cmd.Execute()
	var exit *ExitCodeError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exit):
		return out.String(), exit.Code
	default:
		t.Fatalf("unexpected error: %v", err)
		return "", -1
	}
}

func TestRunnerProvenanceGenerateVerifyAndTamper(t *testing.T) {
	dir := t.TempDir()
	private, public := filepath.Join(dir, "signing.pem"), filepath.Join(dir, "signing.pub.pem")
	if out, code := runProvenanceCLI(t, "keygen", "--private-key", private, "--public-key", public); code != 0 || !strings.Contains(out, `"key_id":"sha256:`) {
		t.Fatalf("keygen failed: %d %s", code, out)
	}
	if _, code := runProvenanceCLI(t, "keygen", "--private-key", private, "--public-key", public); code != 2 {
		t.Fatalf("keygen overwrote existing keys: %d", code)
	}
	artifacts := filepath.Join(dir, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	terminal, err := os.ReadFile("../../internal/artifactmanifest/testdata/publication-terminal.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "terminal.json"), terminal, 0o644); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policy, []byte("{\"policy\":\"release\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := os.Getenv("WAZA_RELEASE_IMAGE_DIGEST")
	if digest == "" {
		digest = "sha256:" + strings.Repeat("a", 64)
	}
	imageReference := os.Getenv("WAZA_RELEASE_IMAGE_REFERENCE")
	if imageReference == "" {
		imageReference = "ghcr.io/microsoft/waza-runner:test"
	}
	generate := func(signer, out string) (string, int) {
		return runProvenanceCLI(t, "generate", "--request", "../../examples/runner/unit.json",
			"--manifest", "../../internal/artifactmanifest/testdata/publication-manifest.json",
			"--artifacts", artifacts, "--policy", policy, "--image-reference", imageReference,
			"--image-digest", digest, "--waza-version", "0.42.0", "--signer", signer, "--signing-key", private, "--out", out)
	}
	bundle := filepath.Join(dir, "bundle")
	if out, code := generate("local-ed25519", bundle); code != 0 || !strings.Contains(out, `"status":"signed"`) {
		t.Fatalf("generate failed: %d %s", code, out)
	}
	if _, code := generate("sigstore-keyless", filepath.Join(dir, "keyless")); code != 5 {
		t.Fatalf("keyless signer should be unavailable (exit 5), got %d", code)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "terminal.json"), append(bytes.Clone(terminal), make([]byte, 1<<20)...), 0o644); err != nil {
		t.Fatal(err)
	}
	oversized := newRunnerCommand()
	oversized.SetOut(&bytes.Buffer{})
	oversized.SetErr(&bytes.Buffer{})
	oversized.SetArgs([]string{"provenance", "generate", "--request", "../../examples/runner/unit.json",
		"--manifest", "../../internal/artifactmanifest/testdata/publication-manifest.json",
		"--artifacts", artifacts, "--policy", policy, "--image-reference", "ghcr.io/microsoft/waza-runner:test",
		"--image-digest", digest, "--waza-version", "0.42.0", "--signing-key", private, "--out", filepath.Join(dir, "oversized")})
	if err := oversized.Execute(); err == nil || !strings.Contains(err.Error(), "reading artifact terminal.json") {
		t.Fatalf("artifact larger than its declared size was read: %v", err)
	}

	verify := func(extra ...string) map[string]any {
		out, code := runProvenanceCLI(t, append([]string{"verify", "--bundle", bundle, "--public-key", public}, extra...)...)
		var report map[string]any
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatalf("verify output is not JSON (exit %d): %s", code, out)
		}
		if exitCode, ok := report["exit_code"].(float64); !ok || int(exitCode) != code {
			t.Fatalf("report exit_code %v differs from process exit %d", report["exit_code"], code)
		}
		return report
	}
	if r := verify("--expect-image-digest", digest); r["status"] != "verified" {
		t.Fatalf("expected verified: %v", r["checks"])
	}
	if r := verify("--expect-image-digest", "sha256:"+strings.Repeat("b", 64)); r["status"] != "failed" {
		t.Fatal("image digest pin was not enforced")
	}
	if r := verify("--require-inventory"); r["status"] != "failed" || !strings.Contains(string(mustJSON(t, r["checks"])), "binding_required_missing") {
		t.Fatalf("bundle without an inventory satisfied --require-inventory: %v", r["checks"])
	}
	inventoryPath := os.Getenv("WAZA_RELEASE_INVENTORY_PATH")
	if inventoryPath == "" {
		inventoryPath = filepath.Join(dir, "runner.cdx.json")
		if out, code := runRunnerCLI(t, "inventory", "generate", "--name", imageReference, "--digest", digest,
			"--dpkg-status", "../../internal/inventory/testdata/dpkg-status", "--out", inventoryPath); code != 0 {
			t.Fatalf("inventory generate: %d %s", code, out)
		}
	}
	bound := filepath.Join(dir, "bound")
	if err := os.WriteFile(filepath.Join(artifacts, "terminal.json"), terminal, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runProvenanceCLI(t, "generate", "--request", "../../examples/runner/unit.json",
		"--manifest", "../../internal/artifactmanifest/testdata/publication-manifest.json",
		"--artifacts", artifacts, "--policy", policy, "--image-reference", imageReference,
		"--image-digest", digest, "--waza-version", "0.42.0", "--signing-key", private, "--out", bound,
		"--bind", "component-inventory="+inventoryPath); code != 0 {
		t.Fatalf("generate with inventory binding: %d %s", code, out)
	}
	verification, code := runProvenanceCLI(t, "verify", "--bundle", bound, "--public-key", public, "--require-inventory", "--expect-image-digest", digest)
	if code != 0 {
		t.Fatalf("bound inventory did not verify: %d %s", code, verification)
	}
	evidenceDir := os.Getenv("WAZA_RELEASE_PROVENANCE_EVIDENCE")
	if evidenceDir == "" {
		evidenceDir = filepath.Join(dir, "retained-evidence")
	}
	if err := copyProvenanceFixture(bound, public, evidenceDir); err != nil {
		t.Fatalf("retaining local provenance evidence: %v", err)
	}
	for _, name := range []string{"statement.json", "envelope.json", "public-key.pem", filepath.Join("bundle", "bundle.json")} {
		if _, err := os.Stat(filepath.Join(evidenceDir, name)); err != nil {
			t.Fatalf("retained provenance evidence is missing %s: %v", name, err)
		}
	}
	object := filepath.Join(bundle, "artifacts", "objects", "terminal.json")
	if err := os.WriteFile(object, append(terminal, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := verify(); r["status"] != "failed" || !strings.Contains(string(mustJSON(t, r["checks"])), "artifact_digest_mismatch") {
		t.Fatalf("tampered artifact verified: %v", r["checks"])
	}
}

func copyProvenanceFixture(bundleDir, publicKeyPath, evidenceDir string) error {
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		return err
	}
	if err := filepath.WalkDir(bundleDir, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("provenance fixture contains a symlink")
		}
		relative, err := filepath.Rel(bundleDir, source)
		if err != nil {
			return err
		}
		target := filepath.Join(evidenceDir, "bundle", relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return errors.Join(err, input.Close())
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return errors.Join(err, input.Close())
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, input.Close(), output.Close())
	}); err != nil {
		return err
	}
	envelopePath := filepath.Join(bundleDir, "statement.dsse.json")
	envelopeBytes, err := os.ReadFile(envelopePath)
	if err != nil {
		return err
	}
	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(envelopeBytes, &envelope); err != nil {
		return err
	}
	statement, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "envelope.json"), envelopeBytes, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "statement.json"), statement, 0o644); err != nil {
		return err
	}
	publicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "public-key.pem"), publicKey, 0o644); err != nil {
		return err
	}
	return nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
