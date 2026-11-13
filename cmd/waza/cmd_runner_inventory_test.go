package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runRunnerCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := newRunnerCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
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

func inventoryCode(t *testing.T, out string) string {
	t.Helper()
	var report struct {
		Checks []struct{ Status, Code string }
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not a report: %s", out)
	}
	for _, c := range report.Checks {
		if c.Status == "failed" {
			return c.Code
		}
	}
	return ""
}

func TestRunnerInventoryImageAndPlugin(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	image := filepath.Join(dir, "image.cdx.json")
	generate := []string{"inventory", "generate", "--name", "ghcr.io/microsoft/waza-runner:0.42.0", "--digest", digest,
		"--go-binary", self, "--dpkg-status", "../../internal/inventory/testdata/dpkg-status",
		"--os-release", "../../internal/inventory/testdata/os-release", "--out", image}
	if out, code := runRunnerCLI(t, generate...); code != 0 || !strings.Contains(out, `"status":"generated"`) {
		t.Fatalf("generate: %d %s", code, out)
	}
	if _, code := runRunnerCLI(t, generate...); code != 2 {
		t.Fatalf("generate overwrote an existing inventory: %d", code)
	}
	if out, code := runRunnerCLI(t, "inventory", "generate", "--name", "x", "--digest", "latest", "--file", self, "--out", filepath.Join(dir, "tag.json")); code != 2 {
		t.Fatalf("generated an inventory bound to a tag: %d %s", code, out)
	}

	for _, tc := range []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"--image-digest", digest, "--image-reference", "ghcr.io/microsoft/waza-runner@" + digest}, 0, ""},
		{[]string{"--image-digest", "sha256:" + strings.Repeat("c", 64)}, 1, "subject_digest_mismatch"},
		{[]string{"--image-digest", digest, "--image-reference", "ghcr.io/other/runner:0.42.0"}, 1, "subject_name_mismatch"},
		{nil, 1, "expected_digest_missing"},
	} {
		out, exit := runRunnerCLI(t, append([]string{"inventory", "verify", "--inventory", image}, tc.args...)...)
		if exit != tc.exit || inventoryCode(t, out) != tc.code {
			t.Errorf("verify %v = exit %d code %q; want %d %q", tc.args, exit, inventoryCode(t, out), tc.exit, tc.code)
		}
	}
	out, exit := runRunnerCLI(t, "inventory", "verify", "--inventory", filepath.Join(dir, "absent.json"), "--image-digest", digest)
	if exit != 1 || inventoryCode(t, out) != "inventory_missing" {
		t.Fatalf("missing inventory = %d %s", exit, out)
	}

	plugin := filepath.Join(dir, "plugin.tgz")
	if err := os.WriteFile(plugin, []byte("plugin v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginInventory := filepath.Join(dir, "plugin.cdx.json")
	if out, code := runRunnerCLI(t, "inventory", "generate", "--subject", "plugin", "--name", "example", "--subject-file", plugin,
		"--file", plugin, "--out", pluginInventory); code != 0 {
		t.Fatalf("plugin generate: %d %s", code, out)
	}
	if out, code := runRunnerCLI(t, "inventory", "verify", "--inventory", pluginInventory, "--plugin", plugin, "--plugin-name", "example"); code != 0 {
		t.Fatalf("plugin verify: %d %s", code, out)
	}
	if err := os.WriteFile(plugin, []byte("plugin v1, tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := runRunnerCLI(t, "inventory", "verify", "--inventory", pluginInventory, "--plugin", plugin); code != 1 || inventoryCode(t, out) != "subject_digest_mismatch" {
		t.Fatalf("tampered plugin verified: %d %s", code, out)
	}
	if _, code := runRunnerCLI(t, "inventory", "verify", "--inventory", pluginInventory, "--plugin", plugin, "--image-digest", digest); code != 2 {
		t.Fatalf("image and plugin flags together = %d", code)
	}
}
