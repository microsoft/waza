// Command native-package-smoke executes exact distributed Waza packages on
// native runners and turns the results into platform evidence (#905).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const receiptSchema = "waza.native-package-smoke/1"

// Every check a native receipt must report as passed.
var requiredChecks = []string{"native-host", "artifact-digest", "binary-target", "version", "help", "mock-run", "embedded-dashboard"}

type receipt struct {
	Schema          string   `json:"schema"`
	Kind            string   `json:"kind"`
	Target          string   `json:"target"`
	Artifact        string   `json:"artifact"`
	SHA256          string   `json:"sha256"`
	SourceSHA       string   `json:"source_sha"`
	Revision        string   `json:"revision"`
	Version         string   `json:"version"`
	Runner          string   `json:"runner"`
	Checks          []string `json:"checks"`
	DashboardAssets int      `json:"dashboard_assets"`
	Seconds         float64  `json:"seconds"`
}

func receiptName(kind string, p platform) string {
	return "native-smoke-" + kind + "-" + p.OS + "-" + p.Arch + ".json"
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "native-package-smoke:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: native-package-smoke cli|extension|go-tests|evidence [flags]")
	}
	switch args[0] {
	case "cli", "extension":
		return runSmoke(ctx, args[0], args[1:], stdout)
	case "go-tests":
		flags := flag.NewFlagSet("go-tests", flag.ContinueOnError)
		input := flags.String("input", "", "go test -json output")
		required := flags.String("require", "", "comma-separated top-level tests that must pass")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *input == "" || *required == "" {
			return fmt.Errorf("go-tests requires -input and -require")
		}
		f, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer f.Close()
		return requireGoTests(f, strings.Split(*required, ","), stdout)
	case "evidence":
		flags := flag.NewFlagSet("evidence", flag.ContinueOnError)
		dir := flags.String("dir", "", "directory of downloaded receipts")
		sourceSHA := flags.String("source-sha", "", "exact source commit")
		revision := flags.String("revision", "", "commit the packages were built from (default -source-sha)")
		version := flags.String("version", "", "package version")
		summary := flags.String("summary", "", "markdown summary file to append to")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || *sourceSHA == "" || *version == "" {
			return fmt.Errorf("evidence requires -dir, -source-sha and -version")
		}
		if *revision == "" {
			*revision = *sourceSHA
		}
		table, err := collectEvidence(*dir, *sourceSHA, *revision, *version)
		fmt.Fprint(stdout, table)
		if *summary != "" {
			if appendErr := appendFile(*summary, table); appendErr != nil && err == nil {
				err = appendErr
			}
		}
		return err
	}
	return fmt.Errorf("unknown subcommand %q", args[0])
}

func runSmoke(ctx context.Context, kind string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet(kind, flag.ContinueOnError)
	dir := flags.String("dir", "", "downloaded artifact directory")
	target := flags.String("target", "", "distribution target os/arch")
	sourceSHA := flags.String("source-sha", "", "exact source commit checked out for fixtures")
	revision := flags.String("revision", "", "commit the packages were built from (default -source-sha)")
	version := flags.String("version", "", "expected version")
	repo := flags.String("repo", ".", "checkout of the exact source")
	receiptDir := flags.String("receipt-dir", "", "receipt output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *target == "" || *sourceSHA == "" || *version == "" || *receiptDir == "" {
		return fmt.Errorf("%s requires -dir, -target, -source-sha, -version and -receipt-dir", kind)
	}
	if *revision == "" {
		*revision = *sourceSHA
	}
	p, err := findPlatform(*target)
	if err != nil {
		return err
	}
	if p.Evidence != evidenceNative || kind == "extension" && !p.Extension {
		return fmt.Errorf("%s %s is not a native smoke target", kind, p.target())
	}
	repoDir, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	started := time.Now()
	result := receipt{Schema: receiptSchema, Kind: kind, Target: p.target(), SourceSHA: *sourceSHA, Revision: *revision, Version: *version, Runner: os.Getenv("RUNNER_NAME")}
	pass := func(check string) {
		result.Checks = append(result.Checks, check)
		fmt.Fprintf(stdout, "PASS %s %s %s\n", kind, p.target(), check)
	}
	if err := requireNativeHost(p, currentHost()); err != nil {
		return err
	}
	pass("native-host")
	work, err := os.MkdirTemp("", "waza-native-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	var binary string
	if kind == "cli" {
		binary, result.SHA256, err = verifyCLIArtifact(*dir, p, *revision)
		result.Artifact = cliBinaryName(p)
		if err == nil {
			// Artifact download drops the executable bit; run a private copy.
			binary, err = copyExecutable(binary, filepath.Join(work, cliBinaryName(p)))
		}
	} else {
		var archive string
		archive, result.SHA256, err = verifyExtensionProvenance(*dir, p, *revision)
		result.Artifact = extensionArchiveName(p)
		if err == nil {
			var manifest []byte
			if manifest, err = os.ReadFile(filepath.Join(repoDir, "extension.yaml")); err == nil {
				binary, err = extractExtension(archive, p, manifest, *version, work)
			}
		}
	}
	if err != nil {
		return err
	}
	pass("artifact-digest")
	if err := verifyBinary(binary, p, *revision); err != nil {
		return err
	}
	pass("binary-target")
	if err := checkVersion(ctx, work, binary, *version); err != nil {
		return err
	}
	pass("version")
	if err := checkHelp(ctx, work, binary); err != nil {
		return err
	}
	pass("help")
	if err := checkMock(ctx, repoDir, filepath.Join(repoDir, ".github", "scripts", "release_integration_test.sh"), binary); err != nil {
		return err
	}
	pass("mock-run")
	if result.DashboardAssets, err = checkDashboard(ctx, binary); err != nil {
		return err
	}
	pass("embedded-dashboard")
	result.Seconds = time.Since(started).Round(time.Second).Seconds()
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*receiptDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*receiptDir, receiptName(kind, p)), append(data, '\n'), 0o644)
}

func copyExecutable(from, to string) (string, error) {
	data, err := os.ReadFile(from)
	if err != nil {
		return "", err
	}
	return to, os.WriteFile(to, data, 0o755)
}

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
