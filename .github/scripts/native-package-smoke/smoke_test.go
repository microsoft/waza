package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	testSHA      = "0123456789abcdef0123456789abcdef01234567"
	testRevision = "89abcdef0123456789abcdef0123456789abcdef"
	testVersion  = "1.2.3"
	testRunID    = "123456789"
	testAttempt  = 2
)

const exitEarlyEnv = "NATIVE_SMOKE_TEST_EXIT_EARLY"

// TestMain lets the test binary stand in for a package that exits at once.
func TestMain(m *testing.M) {
	if os.Getenv(exitEarlyEnv) == "1" {
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func repoFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{"..", "..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type matrixEntry struct {
	Target   string `yaml:"target"`
	Artifact string `yaml:"artifact"`
	Runner   string `yaml:"runner"`
}

type workflowJob struct {
	RunsOn          string `yaml:"runs-on"`
	ContinueOnError string `yaml:"continue-on-error"`
	Strategy        struct {
		Matrix struct {
			Include []matrixEntry `yaml:"include"`
			Runner  []string      `yaml:"runner"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
}

func workflowJobs(t *testing.T, name string) map[string]workflowJob {
	t.Helper()
	var workflow struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(repoFile(t, ".github", "workflows", name), &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs
}

func TestWorkflowMatricesMatchNativePlatforms(t *testing.T) {
	jobs := workflowJobs(t, "native-package-smoke.yml")
	var cli, extension []matrixEntry
	darwinRunners := []string{}
	for _, p := range platforms {
		if p.Evidence != evidenceNative {
			continue
		}
		cli = append(cli, matrixEntry{
			Target: p.target(), Artifact: "cli-waza-" + p.OS + "-" + p.Arch + "-${{ github.run_id }}-${{ inputs.package_run_attempt }}",
			Runner: p.Runner,
		})
		if p.Extension {
			extension = append(extension, matrixEntry{Target: p.target(), Runner: p.Runner})
		}
		if p.OS == "darwin" {
			darwinRunners = append(darwinRunners, p.Runner)
		}
	}
	if got := jobs["cli"].Strategy.Matrix.Include; !slices.Equal(got, cli) {
		t.Errorf("cli matrix = %+v, want native platforms %+v", got, cli)
	}
	if got := jobs["extension"].Strategy.Matrix.Include; !slices.Equal(got, extension) {
		t.Errorf("extension matrix = %+v, want %+v", got, extension)
	}
	got := slices.Clone(workflowJobs(t, "native-platform.yml")["darwin-production-callers"].Strategy.Matrix.Runner)
	slices.Sort(got)
	slices.Sort(darwinRunners)
	if !slices.Equal(got, darwinRunners) {
		t.Errorf("Darwin production-caller runners = %v, want native Darwin runners %v", got, darwinRunners)
	}
	selected := map[string]bool{}
	for _, p := range platforms {
		if p.Extension {
			selected[p.OS] = true
		}
	}
	if len(selected) != 3 {
		t.Errorf("extension smoke must cover one representative target per OS, got %v", selected)
	}
	for name, job := range jobs {
		if job.ContinueOnError != "${{ inputs.report_only }}" {
			t.Errorf("%s job must honor report_only, got continue-on-error %q", name, job.ContinueOnError)
		}
	}
	workflow := string(repoFile(t, ".github", "workflows", "native-package-smoke.yml"))
	for _, required := range []string{
		"package_run_attempt:",
		"extension_package_run_attempt:",
		"name: extension-packages-${{ github.run_id }}-${{ inputs.extension_package_run_attempt }}",
		"pattern: native-smoke-*-${{ github.run_id }}-*",
		"OUTCOMES: ${{ steps.checkout.outcome }},${{ steps.source.outcome }},${{ steps.setup.outcome }},${{ steps.guard.outcome }},${{ steps.package_download.outcome }},${{ steps.smoke.outcome }},${{ steps.reject.outcome }}",
		"name: native-smoke-cli-${{ github.run_id }}-${{ github.run_attempt }}-${{ strategy.job-index }}",
		"name: native-smoke-extension-${{ github.run_id }}-${{ github.run_attempt }}-${{ strategy.job-index }}",
		"- name: Upload native receipt and status\n        if: always()",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("native package smoke workflow lacks attempt-scoped handoff %q", required)
		}
	}
	if strings.Contains(workflow, "merge-multiple: true") {
		t.Error("native receipt downloads must preserve artifact directories for attempt validation")
	}
}

func TestRequireNativeHost(t *testing.T) {
	darwin, _ := findPlatform("darwin/arm64")
	linux, _ := findPlatform("linux/amd64")
	env := map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_OS": "macOS", "RUNNER_ARCH": "ARM64"}
	native := host{OS: "darwin", Arch: "arm64", Getenv: func(k string) string { return env[k] }, Translated: func() (bool, error) { return false, nil }}
	if err := requireNativeHost(darwin, native); err != nil {
		t.Fatalf("native host rejected: %v", err)
	}
	cases := map[string]func(h *host){
		"cross architecture": func(h *host) { h.Arch = "amd64" },
		"runner disagrees": func(h *host) {
			h.Getenv = func(k string) string {
				return map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_OS": "macOS", "RUNNER_ARCH": "X64"}[k]
			}
		},
		"rosetta":    func(h *host) { h.Translated = func() (bool, error) { return true, nil } },
		"unprovable": func(h *host) { h.Translated = func() (bool, error) { return false, errors.New("no sysctl") } },
	}
	for name, mutate := range cases {
		h := native
		mutate(&h)
		if err := requireNativeHost(darwin, h); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := requireNativeHost(linux, native); err == nil {
		t.Error("darwin host accepted for linux target")
	}
}

func TestObjectTargetAndBuildInfoBinding(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	goos, arch, err := objectTarget(executable)
	if err != nil || goos != runtime.GOOS || arch != runtime.GOARCH {
		t.Fatalf("objectTarget(test binary) = %s/%s, %v; want %s/%s", goos, arch, err, runtime.GOOS, runtime.GOARCH)
	}
	p := platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	// Test binaries carry no VCS stamp, so binding to any revision must fail.
	if err := verifyBinary(executable, p, testSHA); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("unstamped binary accepted: %v", err)
	}
	other := platform{OS: runtime.GOOS, Arch: map[string]string{"amd64": "arm64", "arm64": "amd64"}[runtime.GOARCH]}
	if err := verifyBinary(executable, other, testSHA); err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("wrong architecture accepted: %v", err)
	}
	text := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(text, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := objectTarget(text); err == nil {
		t.Fatal("script accepted as an executable")
	}
}

func writeCLIArtifact(t *testing.T, p platform, binary []byte, source string) string {
	t.Helper()
	dir := t.TempDir()
	name := cliBinaryName(p)
	if err := os.WriteFile(filepath.Join(dir, name), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, name+".sha256"), digest+"  "+name+"\n")
	mustWrite(t, filepath.Join(dir, name+".source-sha.txt"), source+"\n")
	return dir
}

func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCLIArtifact(t *testing.T) {
	p, _ := findPlatform("windows/arm64")
	dir := writeCLIArtifact(t, p, []byte("binary"), testRevision)
	if _, digest, err := verifyCLIArtifact(dir, p, testRevision); err != nil || len(digest) != 64 {
		t.Fatalf("valid artifact rejected: %q %v", digest, err)
	}
	if _, _, err := verifyCLIArtifact(dir, p, testSHA); err == nil {
		t.Error("artifact accepted for another source commit")
	}
	mustWrite(t, filepath.Join(dir, cliBinaryName(p)), "tampered")
	if _, _, err := verifyCLIArtifact(dir, p, testRevision); err == nil {
		t.Error("tampered artifact accepted")
	}
	if _, _, err := verifyCLIArtifact(t.TempDir(), p, testRevision); err == nil {
		t.Error("missing artifact accepted")
	}
}

type entry struct {
	name string
	data string
	mode int64
	typ  byte
}

func writeTarGz(t *testing.T, path string, entries []entry) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.data)), Typeflag: typ}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, buf.String())
}

func writeZip(t *testing.T, path string, entries []entry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		header.SetMode(os.FileMode(e.mode))
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, buf.String())
}

const testManifest = "id: microsoft.azd.waza\nversion: " + testVersion + "\n"

func TestExtractExtension(t *testing.T) {
	linux, _ := findPlatform("linux/amd64")
	windows, _ := findPlatform("windows/amd64")
	good := []entry{{name: "extension.yaml", data: testManifest, mode: 0o644}, {name: extensionEntryPoint(linux), data: "elf", mode: 0o755}}
	archive := filepath.Join(t.TempDir(), extensionArchiveName(linux))
	writeTarGz(t, archive, good)
	extracted, err := extractExtension(archive, linux, []byte(strings.ReplaceAll(testManifest, "\n", "\r\n")), testVersion, t.TempDir())
	if err != nil {
		t.Fatalf("valid tar.gz rejected: %v", err)
	}
	if data, _ := os.ReadFile(extracted); string(data) != "elf" {
		t.Fatalf("extracted entry point = %q", data)
	}
	if _, err := extractExtension(archive, linux, []byte(testManifest), "9.9.9", t.TempDir()); err == nil {
		t.Error("manifest for another version accepted")
	}
	if _, err := extractExtension(archive, linux, []byte(testManifest+"extra: true\n"), testVersion, t.TempDir()); err == nil {
		t.Error("packed manifest differing from source accepted")
	}
	bad := map[string][]entry{
		"not executable": {good[0], {name: good[1].name, data: "elf", mode: 0o644}},
		"extra entry":    append(slices.Clone(good), entry{name: "README.md", data: "x", mode: 0o644}),
		"nested entry":   {good[0], {name: "bin/" + good[1].name, data: "elf", mode: 0o755}},
		"duplicate":      append(slices.Clone(good), good[1]),
		"symlink":        {good[0], {name: good[1].name, mode: 0o755, typ: tar.TypeSymlink}},
		"wrong platform": {good[0], {name: extensionEntryPoint(windows), data: "pe", mode: 0o755}},
	}
	for name, entries := range bad {
		path := filepath.Join(t.TempDir(), extensionArchiveName(linux))
		writeTarGz(t, path, entries)
		if _, err := extractExtension(path, linux, []byte(testManifest), testVersion, t.TempDir()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	zipArchive := filepath.Join(t.TempDir(), extensionArchiveName(windows))
	writeZip(t, zipArchive, []entry{{name: "extension.yaml", data: testManifest, mode: 0o644}, {name: extensionEntryPoint(windows), data: "pe", mode: 0o644}})
	if _, err := extractExtension(zipArchive, windows, []byte(testManifest), testVersion, t.TempDir()); err != nil {
		t.Fatalf("valid zip rejected: %v", err)
	}
}

func TestVerifyExtensionProvenance(t *testing.T) {
	p, _ := findPlatform("darwin/arm64")
	dir := t.TempDir()
	archive := filepath.Join(dir, extensionArchiveName(p))
	mustWrite(t, archive, "zip bytes")
	digest, _ := fileSHA256(archive)
	other := strings.Repeat("a", 64)
	write := func(lines ...string) {
		mustWrite(t, filepath.Join(dir, "release-provenance.txt"), strings.Join(lines, "\n")+"\n")
	}
	listed := digest + "  artifacts/" + extensionArchiveName(p)
	write("source_sha="+testRevision, other+"  artifacts/microsoft-azd-waza-linux-amd64.tar.gz", listed)
	if _, got, err := verifyExtensionProvenance(dir, p, testRevision); err != nil || got != digest {
		t.Fatalf("valid provenance rejected: %q %v", got, err)
	}
	cases := map[string][]string{
		"other source": {"source_sha=" + testSHA, listed},
		"unlisted":     {"source_sha=" + testRevision},
		"wrong digest": {"source_sha=" + testRevision, other + "  artifacts/" + extensionArchiveName(p)},
		"duplicate":    {"source_sha=" + testRevision, listed, listed},
		"malformed":    {"source_sha=" + testRevision, "garbage"},
	}
	for name, lines := range cases {
		write(lines...)
		if _, _, err := verifyExtensionProvenance(dir, p, testRevision); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRequireGoTests(t *testing.T) {
	stream := func(events ...string) *strings.Reader { return strings.NewReader(strings.Join(events, "\n")) }
	pass := `{"Action":"pass","Package":"p","Test":"TestA"}`
	required := []string{"TestA"}
	var out bytes.Buffer
	if err := requireGoTests(stream(`{"Action":"run","Package":"p","Test":"TestA"}`, pass, `{"Action":"pass","Package":"p"}`), required, &out); err != nil {
		t.Fatalf("passing stream rejected: %v", err)
	}
	if out.String() != "PASS TestA\n" {
		t.Errorf("output = %q", out.String())
	}
	cases := map[string]*strings.Reader{
		"skipped":      stream(`{"Action":"skip","Package":"p","Test":"TestA"}`),
		"not selected": stream(`{"Action":"pass","Package":"p","Test":"TestB"}`),
		"subtest only": stream(`{"Action":"pass","Package":"p","Test":"TestA/x"}`),
		"other fails":  stream(pass, `{"Action":"fail","Package":"q","Test":"TestC"}`),
		"build fails":  stream(pass, `{"Action":"fail","Package":"q"}`),
		"build error":  stream(pass, `{"ImportPath":"q","Action":"build-fail"}`),
		"empty":        stream(),
		"malformed":    stream(pass, "FAIL q [build failed]"),
	}
	for name, input := range cases {
		if err := requireGoTests(input, required, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func writeReceiptArtifact(t *testing.T, dir, kind string, p platform, attempt, index int, status string, mutate func(*receipt)) string {
	t.Helper()
	artifactDir := filepath.Join(dir, receiptArtifactName(kind, testRunID, attempt, index))
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statusData, err := json.Marshal(receiptStatus{
		Schema: statusSchema, Kind: kind, Target: p.target(), RunID: testRunID, RunAttempt: attempt, Status: status,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(artifactDir, "status.json"), string(statusData))
	if status != "success" {
		return artifactDir
	}
	artifact := cliBinaryName(p)
	if kind == "extension" {
		artifact = extensionArchiveName(p)
	}
	r := receipt{
		Schema: receiptSchema, Kind: kind, Target: p.target(), Artifact: artifact,
		SHA256: strings.Repeat("b", 64), RunID: testRunID, RunAttempt: attempt,
		SourceSHA: testSHA, Revision: testRevision, Version: testVersion,
		Runner: p.Runner, Checks: requiredChecks, DashboardAssets: 2, Seconds: 30,
	}
	if mutate != nil {
		mutate(&r)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(artifactDir, receiptName(kind, p)), string(data))
	return artifactDir
}

func writeReceipts(t *testing.T, dir string, mutate func(*receipt)) {
	t.Helper()
	for index, p := range platforms {
		for _, kind := range []string{"cli", "extension"} {
			if p.Evidence != evidenceNative || kind == "extension" && !p.Extension {
				continue
			}
			writeReceiptArtifact(t, dir, kind, p, 1, index, "success", mutate)
		}
	}
}

func TestCollectEvidence(t *testing.T) {
	dir := t.TempDir()
	writeReceipts(t, dir, nil)
	table, err := collectEvidence(dir, testSHA, testRevision, testVersion, testRunID, testAttempt)
	if err != nil {
		t.Fatalf("complete evidence rejected: %v\n%s", err, table)
	}
	for _, want := range []string{"| darwin/arm64 | `macos-latest` | native | pass (30 s, 2 assets) | pass (30 s, 2 assets) |", "| linux/arm64 | `ubuntu-24.04-arm` | native | pass (30 s, 2 assets) | not selected |"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if _, err := collectEvidence(dir, testSHA, testSHA, testVersion, testRunID, testAttempt); err == nil {
		t.Error("receipts for another package revision accepted")
	}
	mutations := map[string]func(*receipt){
		"missing check":   func(r *receipt) { r.Checks = r.Checks[:len(r.Checks)-1] },
		"no digest":       func(r *receipt) { r.SHA256 = "" },
		"no assets":       func(r *receipt) { r.DashboardAssets = 0 },
		"other version":   func(r *receipt) { r.Version = "0.0.1" },
		"other artifact":  func(r *receipt) { r.Artifact = "waza" },
		"other run":       func(r *receipt) { r.RunID = "987654321" },
		"other attempt":   func(r *receipt) { r.RunAttempt = 0 },
		"reordered check": func(r *receipt) { r.Checks = append([]string{r.Checks[1], r.Checks[0]}, r.Checks[2:]...) },
	}
	for name, mutate := range mutations {
		dir := t.TempDir()
		writeReceipts(t, dir, mutate)
		if _, err := collectEvidence(dir, testSHA, testRevision, testVersion, testRunID, testAttempt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	missing := t.TempDir()
	writeReceipts(t, missing, nil)
	darwin, _ := findPlatform("darwin/amd64")
	if err := os.Remove(filepath.Join(missing, receiptArtifactName("cli", testRunID, 1, 2), receiptName("cli", darwin))); err != nil {
		t.Fatal(err)
	}
	if table, err := collectEvidence(missing, testSHA, testRevision, testVersion, testRunID, testAttempt); err == nil || !strings.Contains(table, "| darwin/amd64 | `macos-15-intel` | native | **missing or invalid** |") {
		t.Errorf("missing receipt accepted: %v\n%s", err, table)
	}
	unexpected := t.TempDir()
	writeReceipts(t, unexpected, nil)
	mustWrite(t, filepath.Join(unexpected, "unexpected.txt"), "{}")
	if _, err := collectEvidence(unexpected, testSHA, testRevision, testVersion, testRunID, testAttempt); err == nil {
		t.Error("unexpected receipt accepted")
	}
	unselected := t.TempDir()
	writeReceipts(t, unselected, nil)
	linuxARM, _ := findPlatform("linux/arm64")
	writeReceiptArtifact(t, unselected, "extension", linuxARM, 1, 7, "success", nil)
	if _, err := collectEvidence(unselected, testSHA, testRevision, testVersion, testRunID, testAttempt); err == nil {
		t.Error("receipt for an unselected extension accepted")
	}
	if _, err := collectEvidence(filepath.Join(t.TempDir(), "none"), testSHA, testRevision, testVersion, testRunID, testAttempt); err == nil {
		t.Error("absent receipts accepted")
	}

	partial := t.TempDir()
	writeReceipts(t, partial, nil)
	writeReceiptArtifact(t, partial, "cli", darwin, 2, 0, "success", nil)
	if table, err := collectEvidence(partial, testSHA, testRevision, testVersion, testRunID, testAttempt); err != nil ||
		!strings.Contains(table, "| darwin/amd64 | `macos-15-intel` | native | pass (30 s, 2 assets) | not selected |") {
		t.Errorf("partial rerun did not retain and validate earlier successful receipts: %v\n%s", err, table)
	}
	failedRerun := t.TempDir()
	writeReceipts(t, failedRerun, nil)
	writeReceiptArtifact(t, failedRerun, "cli", darwin, 2, 0, "failure", nil)
	if table, err := collectEvidence(failedRerun, testSHA, testRevision, testVersion, testRunID, testAttempt); err == nil ||
		!strings.Contains(err.Error(), "latest attempt 2 ended with status failure") {
		t.Errorf("failed rerun was hidden by an earlier successful receipt: %v\n%s", err, table)
	}
	mismatchedStatus := t.TempDir()
	writeReceipts(t, mismatchedStatus, nil)
	statusPath := filepath.Join(mismatchedStatus, receiptArtifactName("cli", testRunID, 1, 0), "status.json")
	var status receiptStatus
	statusData, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatal(err)
	}
	status.RunAttempt = 2
	statusData, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, statusPath, string(statusData))
	if _, err := collectEvidence(mismatchedStatus, testSHA, testRevision, testVersion, testRunID, testAttempt); err == nil {
		t.Error("status with a mismatched producer attempt accepted")
	}
}

func TestUnavailablePlatformIsNeverPassed(t *testing.T) {
	saved := platforms
	t.Cleanup(func() { platforms = saved })
	platforms = slices.Clone(saved)
	platforms[5] = platform{OS: "windows", Arch: "arm64", Runner: "windows-11-arm", Evidence: evidenceUnavailable, Reason: "no hosted runner"}
	dir := t.TempDir()
	writeReceipts(t, dir, nil)
	table, err := collectEvidence(dir, testSHA, testRevision, testVersion, testRunID, testAttempt)
	if err != nil || !strings.Contains(table, "| windows/arm64 | `windows-11-arm` | unavailable: no hosted runner | unavailable | unavailable |") {
		t.Fatalf("unavailable target misreported: %v\n%s", err, table)
	}
	err = run(context.Background(), []string{"cli", "-dir", dir, "-target", "windows/arm64", "-source-sha", testSHA, "-version", testVersion, "-receipt-dir", dir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not a native smoke target") {
		t.Fatalf("smoke ran on an unavailable target: %v", err)
	}
}

func TestDashboardReportsEarlyExit(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(exitEarlyEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := checkDashboard(ctx, self)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "exited before becoming ready") {
			t.Fatalf("early exit misreported: %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("dashboard check hung after the package exited")
	}
}

func TestFetchRequiresContentAndType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write([]byte("x"))
		case "/empty.js":
			w.Header().Set("Content-Type", "text/javascript")
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		}
	}))
	defer server.Close()
	ctx, client := context.Background(), server.Client()
	if _, err := fetch(ctx, client, server.URL+"/app.js", "javascript"); err != nil {
		t.Fatalf("script rejected: %v", err)
	}
	if _, err := fetch(ctx, client, server.URL+"/empty.js", "javascript"); err == nil {
		t.Error("empty asset accepted")
	}
	if _, err := fetch(ctx, client, server.URL+"/missing.js", "javascript"); err == nil {
		t.Error("SPA fallback HTML accepted as a script")
	}
}

func TestRunRejectsIncompleteInvocations(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"publish"},
		{"cli", "-target", "linux/amd64"},
		{"extension", "-dir", ".", "-target", "linux/arm64", "-source-sha", testSHA, "-version", testVersion, "-receipt-dir", "."},
		{"cli", "-dir", ".", "-target", "linux/386", "-source-sha", testSHA, "-version", testVersion, "-receipt-dir", "."},
		{"go-tests", "-input", "x"},
		{"evidence", "-dir", "."},
	} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Errorf("run(%q) accepted", args)
		}
	}
}

type callerJob struct {
	Needs           []string          `yaml:"needs"`
	ContinueOnError string            `yaml:"continue-on-error"`
	Uses            string            `yaml:"uses"`
	With            map[string]string `yaml:"with"`
	Outputs         map[string]string `yaml:"outputs"`
	If              string            `yaml:"if"`
	Permissions     map[string]string `yaml:"permissions"`
	Strategy        struct {
		Matrix struct {
			Include []map[string]string `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []struct {
		ID              string            `yaml:"id"`
		Name            string            `yaml:"name"`
		Run             string            `yaml:"run"`
		ContinueOnError string            `yaml:"continue-on-error"`
		Uses            string            `yaml:"uses"`
		If              string            `yaml:"if"`
		With            map[string]string `yaml:"with"`
	} `yaml:"steps"`
}

func callerJobs(t *testing.T, source []byte) map[string]callerJob {
	t.Helper()
	var workflow struct {
		Jobs map[string]callerJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs
}

// checkReleaseHandoff requires the tag release to smoke the exact CLI and
// extension packages it publishes, without rebuilding them.
func checkReleaseHandoff(jobs map[string]callerJob) error {
	smoke := jobs["native-smoke"]
	if smoke.Uses != "./.github/workflows/native-package-smoke.yml" || smoke.If != "" ||
		!slices.Contains(smoke.Needs, "qualification") || !slices.Contains(smoke.Needs, "release-cli") ||
		!slices.Contains(smoke.Needs, "release-extension") ||
		smoke.With["source_sha"] != "${{ github.sha }}" || smoke.With["package_revision"] != "" ||
		smoke.With["version"] != "${{ needs.setup-version.outputs.version }}" ||
		smoke.With["package_run_attempt"] != "${{ needs.qualification.outputs.evidence_run_attempt }}" ||
		smoke.With["extension_package_run_attempt"] != "${{ needs.release-extension.outputs.extension_package_run_attempt }}" ||
		smoke.With["report_only"] != "true" ||
		len(smoke.Permissions) != 1 || smoke.Permissions["contents"] != "read" {
		return errors.New("release native-smoke must consume exact-attempt qualification CLI packages and extension packages")
	}
	provenance, attempt, upload := -1, -1, -1
	for i, step := range jobs["release-extension"].Steps {
		switch step.Name {
		case "Record extension artifact provenance":
			provenance = i
		case "Record extension package producer attempt":
			if step.ID != "extension_handoff" || !strings.Contains(step.Run, "$GITHUB_RUN_ATTEMPT") {
				return errors.New("release-extension must expose its exact package producer attempt")
			}
			attempt = i
		case "Upload extension packages for native smoke":
			if step.If == "" && step.ContinueOnError == "true" && strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
				step.With["name"] == "extension-packages-${{ github.run_id }}-${{ steps.extension_handoff.outputs.run_attempt }}" &&
				step.With["path"] == "artifacts" && step.With["if-no-files-found"] == "error" {
				upload = i
			}
		}
	}
	if jobs["release-extension"].Outputs["extension_package_run_attempt"] != "${{ steps.extension_handoff.outputs.run_attempt }}" ||
		provenance < 0 || attempt != provenance+1 || upload != attempt+1 {
		return errors.New("release-extension must hand off provenance-recorded packages with the exact producer attempt")
	}
	return nil
}

func TestReleaseSmokesExactPublishedPackages(t *testing.T) {
	source := repoFile(t, ".github", "workflows", "release.yml")
	if err := checkReleaseHandoff(callerJobs(t, source)); err != nil {
		t.Fatal(err)
	}
	var qualification struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(repoFile(t, ".github", "workflows", "release-qualification.yml"), &qualification); err != nil {
		t.Fatal(err)
	}
	expectedPaths := make(map[string][]string, len(platforms))
	var expectedPackages []string
	for _, p := range platforms {
		name := cliBinaryName(p)
		artifact := "cli-waza-" + p.OS + "-" + p.Arch + "-${{ github.run_id }}-${{ github.run_attempt }}"
		expectedPackages = append(expectedPackages, artifact)
		expectedPaths[artifact] = []string{
			"dist/" + name,
			"dist/" + name + ".sha256",
			"dist/" + name + ".source-sha.txt",
		}
	}
	var packages []string
	for _, step := range qualification.Jobs["qualify"].Steps {
		if !strings.HasPrefix(step.Name, "Upload CLI package for native smoke ") {
			continue
		}
		if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") ||
			step.With["if-no-files-found"] != "error" {
			t.Fatalf("native smoke CLI package must be uploaded as a required artifact: %+v", step)
		}
		artifact := step.With["name"]
		if !slices.Contains(expectedPackages, artifact) ||
			!slices.Equal(strings.Fields(step.With["path"]), expectedPaths[artifact]) {
			t.Fatalf("native smoke CLI artifact %q has an unexpected file set: %q", artifact, step.With["path"])
		}
		packages = append(packages, artifact)
	}
	if !slices.Equal(packages, expectedPackages) {
		t.Fatalf("qualification CLI artifacts = %v, want %v", packages, expectedPackages)
	}
	for _, mutation := range [][2]string{
		{"uses: ./.github/workflows/native-package-smoke.yml", "uses: ./.github/workflows/release-validation.yml"},
		{"needs: [setup-version, qualification, release-cli, release-extension]", "needs: [setup-version, release-cli, release-extension]"},
		{"    uses: ./.github/workflows/native-package-smoke.yml\n    with:\n      source_sha: ${{ github.sha }}",
			"    uses: ./.github/workflows/native-package-smoke.yml\n    with:\n      source_sha: main"},
		{"      report_only: true", "      report_only: false"},
		{"        continue-on-error: true\n        uses: actions/upload-artifact", "        uses: actions/upload-artifact"},
		{"          name: extension-packages-${{ github.run_id }}-${{ steps.extension_handoff.outputs.run_attempt }}\n",
			"          name: extension-rebuilt\n"},
		{"      - name: Upload extension packages for native smoke", "      - name: Upload later"},
	} {
		normalized := strings.ReplaceAll(string(source), "\r\n", "\n")
		mutated := strings.Replace(normalized, mutation[0], mutation[1], 1)
		if mutated == normalized {
			t.Fatalf("mutation %q did not apply", mutation[0])
		}
		if checkReleaseHandoff(callerJobs(t, []byte(mutated))) == nil {
			t.Errorf("mutation not rejected: %q", mutation[0])
		}
	}
}

func TestCandidateWorkflowSmokesItsOwnPackages(t *testing.T) {
	jobs := callerJobs(t, repoFile(t, ".github", "workflows", "native-platform.yml"))
	smoke := jobs["native-smoke"]
	if smoke.Uses != "./.github/workflows/native-package-smoke.yml" || !slices.Equal(smoke.Needs, []string{"candidate-packages"}) ||
		smoke.With["source_sha"] != "${{ github.sha }}" ||
		smoke.With["package_revision"] != "${{ needs.candidate-packages.outputs.revision }}" ||
		smoke.With["version"] != "${{ needs.candidate-packages.outputs.version }}" ||
		smoke.With["package_run_attempt"] != "${{ needs.candidate-packages.outputs.artifact_run_attempt }}" ||
		smoke.With["report_only"] != "" {
		t.Fatalf("candidate smoke must bind the candidate build outputs: %+v", smoke)
	}
	candidateWorkflow := string(repoFile(t, ".github", "workflows", "native-platform.yml"))
	if !strings.Contains(candidateWorkflow, "artifact_run_attempt: ${{ steps.source.outputs.run_attempt }}") ||
		!strings.Contains(candidateWorkflow, "run_attempt=%s") {
		t.Fatal("candidate package handoff must preserve the artifact producer attempt")
	}
	uploads := map[string]bool{}
	for _, step := range jobs["candidate-packages"].Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			uploads[step.With["name"]] = true
		}
	}
	for _, p := range platforms {
		if !uploads["cli-waza-"+p.OS+"-"+p.Arch+"-${{ github.run_id }}-${{ github.run_attempt }}"] {
			t.Errorf("candidate build does not upload %s", p.target())
		}
	}
	if !uploads["extension-packages-${{ github.run_id }}-${{ steps.source.outputs.run_attempt }}"] || len(uploads) != len(platforms)+1 {
		t.Errorf("candidate uploads = %v", uploads)
	}
}
