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
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func flagPresenceSetup(t *testing.T, command string) (args []string, workspace, destination string) {
	t.Helper()
	artifact, digest, workspace := installFixture(t, command == "verify-installed")
	if command == "install" || command == "verify-installed" {
		return installArguments(command, artifact, workspace, digest), workspace, ""
	}
	input := artifact
	if command == "pack" {
		input = fixture(t)
	}
	destination = filepath.Join(temp(t), "new-output")
	return arguments(command, input, destination, digest, testIdentity), workspace, destination
}

func flagPresenceCLI(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIProcess$")
	cmd.Env = append(os.Environ(), "WAZA_ASSET_HELPER_TEST_CHILD=1", "WAZA_ASSET_HELPER_TEST_ARGS="+string(encoded))
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err = cmd.Run()
	return out.String(), diagnostic.String(), err
}

func TestForbiddenFlagPresenceThroughRunAndCLI(t *testing.T) {
	for _, command := range []string{"pack", "verify", "restore", "install", "verify-installed"} {
		for _, value := range []string{"", "nonempty"} {
			for _, surface := range []string{"run", "CLI"} {
				t.Run(command+"/"+map[bool]string{true: "empty", false: "nonempty"}[value == ""]+"/"+surface, func(t *testing.T) {
					args, workspace, destination := flagPresenceSetup(t, command)
					flagName := "--workspace"
					if command == "install" || command == "verify-installed" {
						flagName = "--destination"
					}
					args = append(args, flagName, value)
					before := installedFiles(t, workspace)
					var stdout, diagnostic string
					var err error
					if surface == "CLI" {
						stdout, diagnostic, err = flagPresenceCLI(t, args)
					} else {
						var out bytes.Buffer
						err = run(args, &out, hooks{})
						stdout = out.String()
						if err != nil {
							diagnostic = err.Error()
						}
					}
					if err == nil || stdout != "" || !strings.Contains(diagnostic, "require input") {
						t.Errorf("forbidden flag was not rejected before success: err=%v stdout=%q diagnostic=%q", err, stdout, diagnostic)
					}
					if !equalFiles(before, installedFiles(t, workspace)) {
						t.Error("forbidden flag caused asset installation")
					}
					if destination != "" {
						if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
							t.Errorf("forbidden flag created destination: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestOmittedForbiddenFlagsAndLegacyVerifyDestination(t *testing.T) {
	for _, command := range []string{"pack", "verify", "restore", "install", "verify-installed"} {
		for _, surface := range []string{"run", "CLI"} {
			t.Run(command+"/"+surface, func(t *testing.T) {
				args, workspace, destination := flagPresenceSetup(t, command)
				var stdout, diagnostic string
				var err error
				if surface == "CLI" {
					stdout, diagnostic, err = flagPresenceCLI(t, args)
				} else {
					var out bytes.Buffer
					err = run(args, &out, hooks{})
					stdout = out.String()
				}
				if err != nil || !strings.HasPrefix(stdout, "manifest_sha256=") || diagnostic != "" {
					t.Fatalf("valid omitted forbidden flag failed: err=%v stdout=%q diagnostic=%q", err, stdout, diagnostic)
				}
				if command == "install" && len(installedFiles(t, workspace)) != 5 {
					t.Fatal("valid install did not produce full inventory")
				}
				if command == "pack" || command == "restore" {
					if _, err := os.Stat(destination); err != nil {
						t.Fatalf("valid destination not created: %v", err)
					}
				}
				if command == "verify" {
					if _, err := os.Stat(destination); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("legacy verify wrote ignored destination: %v", err)
					}
				}
			})
		}
	}
	for _, value := range []string{"", "ignored-nonempty"} {
		t.Run("legacy-verify-destination/"+map[bool]string{true: "empty", false: "nonempty"}[value == ""], func(t *testing.T) {
			artifact, digest := packed(t)
			for _, surface := range []string{"run", "CLI"} {
				args := arguments("verify", artifact, value, digest, testIdentity)
				var stdout, diagnostic string
				var err error
				if surface == "CLI" {
					stdout, diagnostic, err = flagPresenceCLI(t, args)
				} else {
					var out bytes.Buffer
					err = run(args, &out, hooks{})
					stdout = out.String()
				}
				if err != nil || stdout != "manifest_sha256="+digest+"\n" || diagnostic != "" {
					t.Fatalf("legacy verify destination changed on %s: err=%v stdout=%q stderr=%q", surface, err, stdout, diagnostic)
				}
			}
		})
	}
}

func TestFrozenV1WireAndOriginalCallers(t *testing.T) {
	// Independent literal oracle for the fixture bytes and v1 wire contract
	// inherited from 8ce; it is not marshaled by the changed implementation.
	const wire = `{"version":1,"root":"web/dist","source":{"repository":"microsoft/waza","source_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","run_id":"123","run_attempt":"1"},"files":[{"path":"assets/chunk-abcdefgh.js","size":22,"sha256":"5156281c95f2327acd7930cdbe71309a14893c8acfdd50275075edde0da1a072"},{"path":"assets/index-abcdefgh.js","size":24,"sha256":"fd55679df5c2d12f9a5cd9e312d2e5749aeeca60a13591bcb6ba2152067d2e29"},{"path":"assets/style-abcdefgh.css","size":6,"sha256":"7c98040a541657584690ae2a1cc3b42a8b53b159cc60c5d3abbfecbaeac6c94a"},{"path":"favicon.svg","size":11,"sha256":"b12e0d83ce2357d80b89c57694814d0a3abdaf8c40724f2049af8b7f01b7812b"},{"path":"index.html","size":130,"sha256":"b15a23bfa022488819bdb6bd64ace8d4fa8d1cfbb00857664ef8bc7ac3dba0f1"}]}`
	artifact, digest := packed(t)
	if got := read(t, filepath.Join(artifact, "manifest.json")); !bytes.Equal(got, []byte(wire)) || digest != sum([]byte(wire)) {
		t.Fatalf("pack changed frozen wire bytes: got=%s digest=%s", got, digest)
	}
	frozen := filepath.Join(temp(t), "frozen")
	for name, data := range installedFiles(t, artifact) {
		writeFixture(t, filepath.Join(frozen, "web/dist", name), []byte(data))
	}
	writeFixture(t, filepath.Join(frozen, "manifest.json"), []byte(wire))
	if _, err := caller(t, arguments("verify", frozen, "", sum([]byte(wire)), testIdentity), hooks{}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(temp(t), "restored")
	if _, err := caller(t, arguments("restore", frozen, dest, sum([]byte(wire)), testIdentity), hooks{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, filepath.Join(dest, "index.html")), read(t, filepath.Join(artifact, "web/dist/index.html"))) {
		t.Fatal("restore changed frozen index bytes")
	}
}

func TestInstallMissingStaticAndProtectedDist(t *testing.T) {
	for _, mode := range []string{"static", "dist", "web-link", "assets-fifo", "assets-link"} {
		t.Run(mode, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, false)
			switch mode {
			case "static":
				source := fixture(t)
				writeFixture(t, filepath.Join(source, "web/dist/static.txt"), []byte("required"))
				artifact = filepath.Join(temp(t), "artifact")
				var err error
				digest, err = caller(t, arguments("pack", source, artifact, "", testIdentity), hooks{})
				if err != nil {
					t.Fatal(err)
				}
			case "dist":
				if err := os.Rename(filepath.Join(workspace, "web/dist"), filepath.Join(workspace, "web/dist-owned")); err != nil {
					t.Fatal(err)
				}
			case "web-link":
				web := filepath.Join(workspace, "web")
				if err := os.Rename(web, web+"-owned"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(web+"-owned", web); err != nil {
					t.Fatal(err)
				}
			case "assets-fifo":
				if err := syscall.Mkfifo(filepath.Join(workspace, "web/dist/assets"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "assets-link":
				if err := os.Symlink(filepath.Join(artifact, "web/dist/assets"), filepath.Join(workspace, "web/dist/assets")); err != nil {
					t.Fatal(err)
				}
			}
			for _, command := range []string{"install", "verify-installed"} {
				if _, err := caller(t, installArguments(command, artifact, workspace, digest), hooks{}); err == nil {
					t.Fatalf("%s admitted missing/static/unsafe parent", command)
				}
			}
			if _, err := os.Lstat(filepath.Join(workspace, "web/dist/assets/style-abcdefgh.css")); mode != "assets-link" &&
				!errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
				t.Fatalf("failed preflight created assets: %v", err)
			}
			if mode == "assets-fifo" {
				info, err := os.Lstat(filepath.Join(workspace, "web/dist/assets"))
				if err != nil || info.Mode()&fs.ModeNamedPipe == 0 {
					t.Fatalf("preexisting FIFO changed: %v", err)
				}
			}
		})
	}
}

func TestInstallAdmissionReplacementHooks(t *testing.T) {
	for _, mode := range []string{"protected-fifo", "asset-parent-link"} {
		t.Run(mode, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, true)
			changed := false
			visits := 0
			faults := hooks{}
			if mode == "protected-fifo" {
				faults.beforeOpen = func(name string) error {
					if name != "index.html" || changed {
						return nil
					}
					visits++
					if visits == 2 {
						target := filepath.Join(workspace, "web/dist/index.html")
						changed = true
						if err := os.Rename(target, target+"-owned"); err != nil {
							return err
						}
						return syscall.Mkfifo(target, 0o600)
					}
					return nil
				}
			} else {
				faults.beforeRoot = func(name string) error {
					if name != "assets" || changed {
						return nil
					}
					visits++
					if visits != 2 {
						return nil
					}
					changed = true
					target := filepath.Join(workspace, "web/dist/assets")
					if err := os.Rename(target, target+"-owned"); err != nil {
						return err
					}
					return os.Symlink(target+"-owned", target)
				}
			}
			if _, err := caller(t, installArguments("install", artifact, workspace, digest), faults); !changed || err == nil {
				t.Fatalf("replacement not exercised/rejected: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestInstallBoundedActualDiscovery(t *testing.T) {
	artifact, digest, workspace := installFixture(t, false)
	dist := filepath.Join(workspace, "web/dist")
	for i := range 2*maxFiles + 1 {
		writeFixture(t, filepath.Join(dist, "unexpected-"+strconv.Itoa(i)), nil)
	}
	for _, command := range []string{"install", "verify-installed"} {
		_, err := caller(t, installArguments(command, artifact, workspace, digest), hooks{})
		if err == nil || !strings.Contains(err.Error(), "entry limit") {
			t.Fatalf("unbounded actual discovery admitted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dist, "assets")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("bounded preflight created assets: %v", err)
		}
	}
}

func TestInstallUnchangedRewriteAndCleanupOnlyFailure(t *testing.T) {
	for _, command := range []string{"install", "verify-installed"} {
		t.Run(command, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, true)
			rewrites := 0
			faults := hooks{read: func(f *os.File) ([]byte, error) {
				data, err := io.ReadAll(f)
				if filepath.Base(f.Name()) == "manifest.json" {
					rewrites++
					err = errors.Join(err, os.WriteFile(filepath.Join(artifact, "manifest.json"), data, 0o600))
				}
				return data, err
			}}
			if _, err := caller(t, installArguments(command, artifact, workspace, digest), faults); err != nil || rewrites != 6 {
				t.Fatalf("same-byte manifest control failed: rewrites=%d err=%v", rewrites, err)
			}
			before := installedFiles(t, workspace)
			failure := errors.New("cleanup only")
			faults = hooks{closeRoot: func(r *os.Root) error { return errors.Join(r.Close(), failure) }}
			got, err := execute(options{
				command: command, input: artifact, workspace: workspace, digest: digest,
				source: testIdentity, checkout: testIdentity.SourceSHA,
			}, faults)
			if !errors.Is(err, failure) || got != "" || !equalFiles(before, installedFiles(t, workspace)) {
				t.Fatalf("cleanup emitted digest or changed equal files: digest=%s err=%v", got, err)
			}
		})
	}
}
