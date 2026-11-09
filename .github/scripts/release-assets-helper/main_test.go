//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var testIdentity = identity{
	Repository: "microsoft/waza",
	SourceSHA:  strings.Repeat("a", 40),
	RunID:      "123",
	RunAttempt: "1",
}

func temp(t *testing.T) string {
	t.Helper()
	name, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func writeFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := temp(t)
	for name, data := range map[string]string{
		"index.html":                `<!doctype html><script src="/assets/index-abcdefgh.js"></script><link href='/assets/style-abcdefgh.css'><link href="/favicon.svg">`,
		"favicon.svg":               "<svg></svg>",
		"assets/index-abcdefgh.js":  "console.log('dashboard')",
		"assets/style-abcdefgh.css": "body{}",
		"assets/chunk-abcdefgh.js":  "export const chunk = 1",
	} {
		writeFixture(t, filepath.Join(root, "web", "dist", name), []byte(data))
	}
	return root
}

func arguments(command, input, dest, digest string, id identity) []string {
	return []string{
		command, "--input", input, "--destination", dest,
		"--manifest-sha256", digest, "--repository", id.Repository,
		"--source-sha", id.SourceSHA, "--checkout-sha", id.SourceSHA,
		"--run-id", id.RunID, "--run-attempt", id.RunAttempt,
	}
}

func caller(t *testing.T, args []string, faults hooks) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, &out, faults)
	if err != nil && out.Len() != 0 {
		t.Fatalf("failure emitted success-shaped output %q: %v", out.String(), err)
	}
	return strings.TrimSpace(strings.TrimPrefix(out.String(), "manifest_sha256=")), err
}

func packed(t *testing.T) (string, string) {
	t.Helper()
	dest := filepath.Join(temp(t), "artifact")
	digest, err := caller(t, arguments("pack", fixture(t), dest, "", testIdentity), hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if !digestPattern.MatchString(digest) {
		t.Fatalf("invalid digest output %q", digest)
	}
	return dest, digest
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProductionCallerRoundTrip(t *testing.T) {
	artifact, digest := packed(t)
	if got := sum(read(t, filepath.Join(artifact, "manifest.json"))); got != digest {
		t.Fatalf("output digest %q does not bind written manifest %q", digest, got)
	}

	if _, err := caller(t, arguments("verify", artifact, "", digest, testIdentity), hooks{}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(temp(t), "dist")
	if _, err := caller(t, arguments("restore", artifact, dest, digest, testIdentity), hooks{}); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(read(t, filepath.Join(artifact, "manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 5 {
		t.Fatalf("round trip inspected %d files, want 5", len(m.Files))
	}
	for _, file := range m.Files {
		data := read(t, filepath.Join(dest, file.Path))
		if sum(data) != file.SHA256 || int64(len(data)) != file.Size {
			t.Fatalf("restored %q does not match manifest", file.Path)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "manifest.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("manifest leaked into embedded dist: %v", err)
	}
}

func TestVerifyRejectsAssetDirectoryReplacementDuringRead(t *testing.T) {
	artifact, digest := packed(t)
	assets := filepath.Join(artifact, "web", "dist", "assets")
	moved := filepath.Join(artifact, "web", "dist", "assets-moved")
	changed := false
	faults := hooks{read: func(f *os.File) ([]byte, error) {
		data, err := io.ReadAll(f)
		if err == nil && !changed && filepath.Base(f.Name()) == "favicon.svg" {
			changed = true
			err = errors.Join(os.Rename(assets, moved), os.Mkdir(assets, 0o700))
		}
		return data, err
	}}
	if _, err := caller(t, arguments("verify", artifact, "", digest, testIdentity), faults); err == nil {
		t.Fatal("verify emitted a digest after the observed asset directory was replaced")
	}
	if !changed {
		t.Fatal("asset directory replacement was not exercised")
	}
}

func TestArtifactMutationsThroughCaller(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, string, *manifest)
	}{
		{"missing-manifest", func(t *testing.T, root string, _ *manifest) { remove(t, filepath.Join(root, "manifest.json")) }},
		{"truncated-bundle", func(t *testing.T, root string, _ *manifest) {
			writeFixture(t, filepath.Join(root, "web/dist/assets/index-abcdefgh.js"), []byte("x"))
		}},
		{"missing-hashed-bundle", func(t *testing.T, root string, _ *manifest) {
			remove(t, filepath.Join(root, "web/dist/assets/chunk-abcdefgh.js"))
		}},
		{"extra-file", func(t *testing.T, root string, _ *manifest) {
			writeFixture(t, filepath.Join(root, "web/dist/extra.txt"), []byte("extra"))
		}},
		{"extra-metadata-inside-dist", func(t *testing.T, root string, _ *manifest) {
			writeFixture(t, filepath.Join(root, "web/dist/receipt.json"), []byte("{}"))
		}},
		{"extra-empty-directory", func(t *testing.T, root string, _ *manifest) {
			if err := os.Mkdir(filepath.Join(root, "web/dist/extra"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"metadata-outside-layout", func(t *testing.T, root string, _ *manifest) {
			writeFixture(t, filepath.Join(root, "receipt.json"), []byte("{}"))
		}},
		{"manifest-truncation", func(t *testing.T, root string, _ *manifest) {
			writeFixture(t, filepath.Join(root, "manifest.json"), []byte("{"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, digest := packed(t)
			var m manifest
			tc.edit(t, root, &m)
			for _, command := range []string{"verify", "restore"} {
				dest := filepath.Join(temp(t), "output")
				if _, err := caller(t, arguments(command, root, dest, digest, testIdentity), hooks{}); err == nil {
					t.Fatal("mutated artifact was admitted")
				}
				if _, err := os.Stat(dest); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("verification failure created destination: %v", err)
				}
			}
		})
	}
}

func remove(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
}

func TestForgedManifestAndWireMutations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*manifest)
	}{
		{"wrong-sha", func(m *manifest) { m.Source.SourceSHA = strings.Repeat("b", 40) }},
		{"wrong-repository", func(m *manifest) { m.Source.Repository = "other/repo" }},
		{"wrong-run", func(m *manifest) { m.Source.RunID = "124" }},
		{"stale-attempt", func(m *manifest) { m.Source.RunAttempt = "2" }},
		{"wrong-layout", func(m *manifest) { m.Root = "dist" }},
		{"unsupported-version", func(m *manifest) { m.Version = 2 }},
		{"duplicate-path", func(m *manifest) { m.Files = append(m.Files, m.Files[0]) }},
		{"traversal", func(m *manifest) { m.Files[0].Path = "../escape" }},
		{"absolute", func(m *manifest) { m.Files[0].Path = "/escape" }},
		{"negative-size", func(m *manifest) { m.Files[0].Size = -1 }},
		{"oversize", func(m *manifest) { m.Files[0].Size = maxBytes + 1 }},
		{"bad-digest", func(m *manifest) { m.Files[0].SHA256 = "bad" }},
		{"missing-inventory-entry", func(m *manifest) { m.Files = m.Files[1:] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, originalDigest := packed(t)
			var m manifest
			if err := json.Unmarshal(read(t, filepath.Join(root, "manifest.json")), &m); err != nil {
				t.Fatal(err)
			}
			tc.edit(&m)
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(root, "manifest.json"), data)
			for _, digest := range []string{originalDigest, sum(data)} {
				if _, err := caller(t, arguments("verify", root, "", digest, testIdentity), hooks{}); err == nil {
					t.Fatal("forged manifest passed caller")
				}
			}
		})
	}
	for _, wire := range []string{
		`{"version":1,"version":1}`, `{"version":"1"}`, `null`, `{} {}`, `{"extra":true}`,
	} {
		t.Run(wire, func(t *testing.T) {
			root, _ := packed(t)
			writeFixture(t, filepath.Join(root, "manifest.json"), []byte(wire))
			if _, err := caller(t, arguments("verify", root, "", sum([]byte(wire)), testIdentity), hooks{}); err == nil {
				t.Fatal("invalid wire admitted")
			}
		})
	}
}

func TestInvalidExpectedIdentity(t *testing.T) {
	root, digest := packed(t)
	for _, tc := range []struct {
		name  string
		id    identity
		value string
	}{
		{"blank-digest", testIdentity, ""},
		{"uppercase-digest", testIdentity, strings.ToUpper(digest)},
		{"zero-run", identity{"microsoft/waza", testIdentity.SourceSHA, "0", "1"}, digest},
		{"blank-attempt", identity{"microsoft/waza", testIdentity.SourceSHA, "123", ""}, digest},
		{"short-sha", identity{"microsoft/waza", "aaa", "123", "1"}, digest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := caller(t, arguments("restore", root, filepath.Join(temp(t), "out"), tc.value, tc.id), hooks{}); err == nil {
				t.Fatal("invalid expected identity admitted")
			}
		})
	}
	args := arguments("verify", root, "", digest, testIdentity)
	for i := range args {
		if args[i] == "--checkout-sha" {
			args[i+1] = strings.Repeat("b", 40)
		}
	}
	if _, err := caller(t, args, hooks{}); err == nil {
		t.Fatal("checkout mismatch admitted")
	}
}

func TestPackRejectsIncompleteAndAlternativeLayouts(t *testing.T) {
	for _, value := range []string{
		`<script src="/assets/missing-abcdefgh.js"></script><link href="/assets/style-abcdefgh.css">`,
		`<script src="../../escape"></script>`,
		`<script src="https://example.test/bundle.js"></script>`,
		`<div>stub</div>`,
	} {
		t.Run(value, func(t *testing.T) {
			root := fixture(t)
			writeFixture(t, filepath.Join(root, "web/dist/index.html"), []byte(value))
			if _, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), hooks{}); err == nil {
				t.Fatal("incomplete index admitted")
			}
		})
	}
	root := fixture(t)
	writeFixture(t, filepath.Join(root, "web/dist/index.html"), []byte(
		`<script src='assets/index-abcdefgh.js?cache=1'></script><link href="assets/style-abcdefgh.css"><link href="#anchor">`))
	if _, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), hooks{}); err != nil {
		t.Fatalf("valid relative single-quoted/query/fragment refs rejected: %v", err)
	}
}

func TestSymlinkAndExclusiveDestination(t *testing.T) {
	for _, location := range []string{"web/dist/assets/index-abcdefgh.js", "web/dist/assets"} {
		t.Run(location, func(t *testing.T) {
			root := fixture(t)
			original := filepath.Join(root, location)
			moved := original + "-original"
			if err := os.Rename(original, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(moved), original); err != nil {
				t.Fatal(err)
			}
			if _, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), hooks{}); err == nil {
				t.Fatal("symlink input admitted")
			}
		})
	}
	artifact, digest := packed(t)
	dest := temp(t)
	writeFixture(t, filepath.Join(dest, "user.txt"), []byte("user-owned"))
	if _, err := caller(t, arguments("restore", artifact, dest, digest, testIdentity), hooks{}); err == nil {
		t.Fatal("existing destination overwritten")
	}
	if string(read(t, filepath.Join(dest, "user.txt"))) != "user-owned" {
		t.Fatal("user file changed")
	}
}

func TestLifecycleFaultsThroughCaller(t *testing.T) {
	fault := errors.New("injected lifecycle failure")
	for _, tc := range []struct {
		name   string
		hooks  hooks
		output bool
	}{
		{"read", hooks{read: func(*os.File) ([]byte, error) { return []byte("partial"), fault }}, false},
		{"partial-discovery", hooks{discover: func(f *os.File) ([]fs.DirEntry, error) {
			entries, err := f.ReadDir(1)
			return entries, errors.Join(err, fault)
		}}, false},
		{"write", hooks{write: func(f *os.File, data []byte) error {
			_, err := f.Write(data[:1])
			return errors.Join(err, fault)
		}}, true},
		{"file-close", hooks{close: func(f *os.File) error { return errors.Join(f.Close(), fault) }}, false},
		{"root-cleanup-close", hooks{closeRoot: func(r *os.Root) error { return errors.Join(r.Close(), fault) }}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(temp(t), "out")
			_, err := caller(t, arguments("pack", fixture(t), dest, "", testIdentity), tc.hooks)
			if !errors.Is(err, fault) {
				t.Fatalf("fault did not reach production caller: %v", err)
			}
			if tc.output && !strings.Contains(err.Error(), "partial output may remain") {
				t.Fatalf("output residue was not reported: %v", err)
			}
			if !tc.output {
				if _, err := os.Stat(dest); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("failed read/discovery created output: %v", err)
				}
			}
		})
	}
}

func TestObservedLeafReplacement(t *testing.T) {
	root := fixture(t)
	faults := hooks{beforeOpen: func(name string) error {
		if name != "index-abcdefgh.js" {
			return nil
		}
		original := filepath.Join(root, "web/dist/assets", name)
		if err := os.Rename(original, original+"-old"); err != nil {
			return err
		}
		return os.WriteFile(original, []byte("competing content"), 0o600)
	}}
	if _, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), faults); err == nil ||
		!strings.Contains(err.Error(), "changed during admission") {
		t.Fatalf("leaf replacement not rejected: %v", err)
	}
}

func TestDirectoryReplacementDuringAdmission(t *testing.T) {
	root := fixture(t)
	changed := false
	faults := hooks{beforeRoot: func(name string) error {
		if name != "assets" || changed {
			return nil
		}
		changed = true
		assets := filepath.Join(root, "web/dist/assets")
		if err := os.Rename(assets, assets+"-old"); err != nil {
			return err
		}
		return os.Mkdir(assets, 0o700)
	}}
	_, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), faults)
	if !changed || err == nil || !strings.Contains(err.Error(), "changed during admission") {
		t.Fatalf("directory replacement was not rejected: changed=%v err=%v", changed, err)
	}
}

func TestSourceMutationAfterSnapshotFailsHandoff(t *testing.T) {
	for _, mode := range []string{"bytes", "parent"} {
		t.Run(mode, func(t *testing.T) {
			root := fixture(t)
			changed := false
			faults := hooks{beforeCopy: func() error {
				changed = true
				if mode == "bytes" {
					return os.WriteFile(filepath.Join(root, "web/dist/assets/index-abcdefgh.js"), []byte("changed source"), 0o600)
				}
				dist := filepath.Join(root, "web/dist")
				if err := os.Rename(dist, dist+"-owned"); err != nil {
					return err
				}
				return os.Mkdir(dist, 0o700)
			}}
			_, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), faults)
			if !changed || err == nil {
				t.Fatalf("source mutation was not rejected: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestParentReplacementDoesNotWriteCompetingDirectory(t *testing.T) {
	artifact, digest := packed(t)
	parent := filepath.Join(temp(t), "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(parent, "dist")
	displaced := parent + "-old"
	changed := false
	faults := hooks{write: func(f *os.File, data []byte) error {
		if !changed {
			changed = true
			if err := os.Rename(parent, displaced); err != nil {
				return err
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				return err
			}
			writeFixture(t, filepath.Join(parent, "user.txt"), []byte("competing user"))
		}
		_, err := f.Write(data)
		return err
	}}
	_, err := caller(t, arguments("restore", artifact, dest, digest, testIdentity), faults)
	if !changed || err == nil {
		t.Fatalf("parent replacement control not exercised/rejected: changed=%v err=%v", changed, err)
	}
	if string(read(t, filepath.Join(parent, "user.txt"))) != "competing user" {
		t.Fatal("competing file changed")
	}
	if _, err := os.Stat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("wrote into replacement parent: %v", err)
	}
	if len(read(t, filepath.Join(displaced, "dist", "index.html"))) == 0 {
		t.Fatal("owned residue missing")
	}
}

func TestOutputSubdirectoryReplacementRetainsOwnedHandles(t *testing.T) {
	artifact, digest := packed(t)
	dest := filepath.Join(temp(t), "dist")
	changed := false
	faults := hooks{write: func(f *os.File, data []byte) error {
		if !changed {
			changed = true
			assets := filepath.Join(dest, "assets")
			if err := os.Rename(assets, assets+"-owned"); err != nil {
				return err
			}
			if err := os.Mkdir(assets, 0o700); err != nil {
				return err
			}
			writeFixture(t, filepath.Join(assets, "user.txt"), []byte("competing file"))
		}
		_, err := f.Write(data)
		return err
	}}
	_, err := caller(t, arguments("restore", artifact, dest, digest, testIdentity), faults)
	if !changed || err == nil || !strings.Contains(err.Error(), "reverify restored output") {
		t.Fatalf("subdirectory replacement did not fail reverify: changed=%v err=%v", changed, err)
	}
	if string(read(t, filepath.Join(dest, "assets/user.txt"))) != "competing file" {
		t.Fatal("competing directory was altered")
	}
	if _, err := os.Stat(filepath.Join(dest, "assets/style-abcdefgh.css")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("wrote to competing subdirectory: %v", err)
	}
	if len(read(t, filepath.Join(dest, "assets-owned/style-abcdefgh.css"))) == 0 {
		t.Fatal("owned directory handle was not retained for later writes")
	}
}

func TestExclusiveLeafPreservesCompetingFile(t *testing.T) {
	artifact, digest := packed(t)
	dest := filepath.Join(temp(t), "dist")
	changed := false
	faults := hooks{write: func(f *os.File, data []byte) error {
		if !changed {
			changed = true
			writeFixture(t, filepath.Join(dest, "assets/style-abcdefgh.css"), []byte("competitor"))
		}
		_, err := f.Write(data)
		return err
	}}
	_, err := caller(t, arguments("restore", artifact, dest, digest, testIdentity), faults)
	if !changed || err == nil || !strings.Contains(err.Error(), "exclusively create") {
		t.Fatalf("exclusive leaf creation control failed: %v", err)
	}
	if string(read(t, filepath.Join(dest, "assets/style-abcdefgh.css"))) != "competitor" {
		t.Fatal("competing leaf overwritten or removed")
	}
}

func TestReverifyAndOutputCloseFailures(t *testing.T) {
	artifact, digest := packed(t)
	for _, tc := range []struct {
		name   string
		faults hooks
		want   string
	}{
		{"corrupt-successful-write", hooks{write: func(f *os.File, _ []byte) error {
			_, err := f.Write([]byte("corrupted output"))
			return err
		}}, "reverify restored output"},
		{"output-close", hooks{close: func(f *os.File) error {
			info, err := f.Stat()
			closeErr := f.Close()
			if err != nil {
				return errors.Join(err, closeErr)
			}
			if info.Mode().IsRegular() && strings.Contains(f.Name(), "new-dist") {
				return errors.Join(closeErr, errors.New("injected output close failure"))
			}
			return closeErr
		}}, "injected output close failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(temp(t), "new-dist")
			_, err := caller(t, arguments("restore", artifact, dest, digest, testIdentity), tc.faults)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "partial output") {
				t.Fatalf("output lifecycle failure swallowed: %v", err)
			}
		})
	}
}

type rejectingWriter struct{}

func (rejectingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCLIUsageAndOutputFailure(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"verify", "--unknown"}, {"pack", "--input", "."}} {
		if err := run(args, io.Discard, hooks{}); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
	root, digest := packed(t)
	if err := run(arguments("verify", root, "", digest, testIdentity), rejectingWriter{}, hooks{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure swallowed: %v", err)
	}
}

func TestCLIProcess(t *testing.T) {
	if os.Getenv("WAZA_ASSET_HELPER_TEST_CHILD") == "1" {
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv("WAZA_ASSET_HELPER_TEST_ARGS")), &args); err != nil {
			panic(err)
		}
		os.Args = append([]string{"release-assets-helper"}, args...)
		main()
		os.Exit(0)
	}
	artifact, digest := packed(t)
	for _, value := range []string{digest, strings.Repeat("0", 64)} {
		args, err := json.Marshal(arguments("verify", artifact, "", value, testIdentity))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIProcess$")
		cmd.Env = append(os.Environ(), "WAZA_ASSET_HELPER_TEST_CHILD=1", "WAZA_ASSET_HELPER_TEST_ARGS="+string(args))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		if value == digest {
			if err != nil || stdout.String() != "manifest_sha256="+digest+"\n" || stderr.Len() != 0 {
				t.Fatalf("CLI success output/exit: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
		} else if err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "manifest digest does not match") {
			t.Fatalf("CLI failure output/exit: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	}
}
