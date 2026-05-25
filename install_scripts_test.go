// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package waza_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallShSelectsFirstSemverReleaseTag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash installer test uses POSIX shell helpers")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	curlLog := filepath.Join(tempDir, "curl.log")
	installLog := filepath.Join(tempDir, "install.log")

	writeExecutable(t, filepath.Join(binDir, "uname"), `#!/bin/sh
case "$1" in
  -s) echo Darwin ;;
  -m) echo arm64 ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
out=""
last=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  last="$1"
  shift
done
echo "$last" >> "$CURL_LOG"
case "$last" in
  *'/releases?per_page=100&page=1')
    printf '[{"tag_name":"azd-ext-microsoft-azd-waza_0.33.0"},{"tag_name":"v0.35.0-rc.1"}]'
    ;;
  *'/releases?per_page=100&page=2')
    printf '[{"tag_name":"v0.34.0"},{"tag_name":"v0.33.0"}]'
    ;;
  */releases/download/v0.34.0/waza-darwin-arm64)
    printf 'binary' > "$out"
    ;;
  */releases/download/v0.34.0/checksums.txt)
    printf 'abc  waza-darwin-arm64\n' > "$out"
    ;;
  *)
    echo "unexpected curl URL: $last" >&2
    exit 9
    ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "shasum"), `#!/bin/sh
cat >/dev/null
exit 0
`)
	writeExecutable(t, filepath.Join(binDir, "cp"), `#!/bin/sh
echo "$@" >> "$INSTALL_LOG"
`)
	writeExecutable(t, filepath.Join(binDir, "chmod"), `#!/bin/sh
exit 0
`)

	cmd := exec.Command(bashPath, "install.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+tempDir,
		"CURL_LOG="+curlLog,
		"INSTALL_LOG="+installLog,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, output)
	}

	out := string(output)
	if !strings.Contains(out, "Latest version: 0.34.0 (v0.34.0)") {
		t.Fatalf("install.sh did not select CLI release tag; output:\n%s", out)
	}
	urls := readFile(t, curlLog)
	if strings.Contains(urls, "/releases/latest") {
		t.Fatalf("install.sh should not use /releases/latest; curl URLs:\n%s", urls)
	}
	if !strings.Contains(urls, "/releases/download/v0.34.0/waza-darwin-arm64") {
		t.Fatalf("install.sh downloaded from wrong release; curl URLs:\n%s", urls)
	}
}

func TestInstallPs1SelectsFirstSemverReleaseTag(t *testing.T) {
	pwshPath, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not available")
	}

	script := readFile(t, "install.ps1")
	start := strings.Index(script, "function Get-LatestReleaseTag {")
	if start < 0 {
		t.Fatal("Get-LatestReleaseTag function not found")
	}
	end := strings.Index(script[start:], "\nfunction ConvertTo-SingleQuotedPowerShellLiteral")
	if end < 0 {
		t.Fatal("could not find end of Get-LatestReleaseTag function")
	}
	functionBlock := script[start : start+end]

	harness := `$ErrorActionPreference = 'Stop'
$Repo = 'microsoft/waza'
function Invoke-RestMethod {
    param([string] $Uri, [hashtable] $Headers)
    if ($Uri -eq 'https://api.github.com/repos/microsoft/waza/releases?per_page=100&page=1') {
        return @(
            [pscustomobject]@{ tag_name = 'azd-ext-microsoft-azd-waza_0.33.0' },
            [pscustomobject]@{ tag_name = 'v0.35.0-rc.1' }
        )
    }
    if ($Uri -eq 'https://api.github.com/repos/microsoft/waza/releases?per_page=100&page=2') {
        return @(
            [pscustomobject]@{ tag_name = 'v0.34.0' },
            [pscustomobject]@{ tag_name = 'v0.33.0' }
        )
    }
    throw "Unexpected releases URI: $Uri"
}
` + functionBlock + `
$tag = Get-LatestReleaseTag
if ($tag -ne 'v0.34.0') {
    throw "Expected v0.34.0, got $tag"
}
`
	harnessPath := filepath.Join(t.TempDir(), "test-install.ps1")
	if err := os.WriteFile(harnessPath, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(pwshPath, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", harnessPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.ps1 release selection failed: %v\n%s", err, output)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
