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

func installArguments(command, artifact, workspace, digest string) []string {
	args := arguments(command, artifact, "", digest, testIdentity)
	args = append(args, "--workspace", workspace)
	for i, arg := range args {
		if arg == "--destination" {
			return append(args[:i:i], args[i+2:]...)
		}
	}
	panic("missing destination argument in test setup")
}

func installFixture(t *testing.T, all bool) (string, string, string) {
	t.Helper()
	artifact, digest := packed(t)
	workspace := temp(t)
	for _, name := range []string{"index.html", "favicon.svg"} {
		writeFixture(t, filepath.Join(workspace, "web/dist", name), read(t, filepath.Join(artifact, "web/dist", name)))
	}
	if all {
		var m manifest
		if err := json.Unmarshal(read(t, filepath.Join(artifact, "manifest.json")), &m); err != nil {
			t.Fatal(err)
		}
		for _, file := range m.Files {
			writeFixture(t, filepath.Join(workspace, "web/dist", file.Path), read(t, filepath.Join(artifact, "web/dist", file.Path)))
		}
	}
	return artifact, digest, workspace
}

func installedFiles(t *testing.T, workspace string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	root := filepath.Join(workspace, "web/dist")
	if err := filepath.WalkDir(root, func(name string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(read(t, name))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestInstallAndVerifyInstalledRoundTrip(t *testing.T) {
	for _, mode := range []string{"missing-assets", "equal-existing", "empty-intended-directory", "nested-assets-and-static"} {
		t.Run(mode, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, mode == "equal-existing")
			if mode == "empty-intended-directory" {
				if err := os.Mkdir(filepath.Join(workspace, "web/dist/assets"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "nested-assets-and-static" {
				source := fixture(t)
				writeFixture(t, filepath.Join(source, "web/dist/assets/nested/chunk.js"), []byte("nested"))
				writeFixture(t, filepath.Join(source, "web/dist/static.txt"), []byte("static"))
				artifact = filepath.Join(temp(t), "artifact")
				var err error
				digest, err = caller(t, arguments("pack", source, artifact, "", testIdentity), hooks{})
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(workspace, "web/dist/static.txt"), []byte("static"))
			}
			protected := make(map[string]fs.FileInfo)
			for _, name := range []string{"index.html", "favicon.svg"} {
				info, err := os.Stat(filepath.Join(workspace, "web/dist", name))
				if err != nil {
					t.Fatal(err)
				}
				protected[name] = info
			}
			for _, command := range []string{"install", "verify-installed", "install"} {
				got, err := caller(t, installArguments(command, artifact, workspace, digest), hooks{})
				if err != nil || got != digest {
					t.Fatalf("%s: digest=%q err=%v", command, got, err)
				}
			}
			var m manifest
			if err := json.Unmarshal(read(t, filepath.Join(artifact, "manifest.json")), &m); err != nil {
				t.Fatal(err)
			}
			actual := installedFiles(t, workspace)
			if len(actual) != len(m.Files) || len(actual) < 5 {
				t.Fatalf("installed inventory has %d files, expected %d", len(actual), len(m.Files))
			}
			for _, file := range m.Files {
				if sum([]byte(actual[file.Path])) != file.SHA256 {
					t.Fatalf("installed %q differs from manifest", file.Path)
				}
			}
			for name, before := range protected {
				after, err := os.Stat(filepath.Join(workspace, "web/dist", name))
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("protected %q replaced: %v", name, err)
				}
			}
		})
	}
}

func TestInstallPrewriteAdmissionAndVerifyNonmutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, string)
	}{
		{"missing-index", func(t *testing.T, w string) { remove(t, filepath.Join(w, "web/dist/index.html")) }},
		{"missing-favicon", func(t *testing.T, w string) { remove(t, filepath.Join(w, "web/dist/favicon.svg")) }},
		{"different-index", func(t *testing.T, w string) {
			writeFixture(t, filepath.Join(w, "web/dist/index.html"), []byte("different"))
		}},
		{"extra-file", func(t *testing.T, w string) {
			writeFixture(t, filepath.Join(w, "web/dist/assets/extra.js"), []byte("extra"))
		}},
		{"conflicting-bundle", func(t *testing.T, w string) {
			writeFixture(t, filepath.Join(w, "web/dist/assets/index-abcdefgh.js"), []byte("competitor"))
		}},
		{"parent-file", func(t *testing.T, w string) { writeFixture(t, filepath.Join(w, "web/dist/assets"), []byte("parent")) }},
		{"extra-empty-directory", func(t *testing.T, w string) {
			if err := os.Mkdir(filepath.Join(w, "web/dist/extra"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"linked-index", func(t *testing.T, w string) {
			index := filepath.Join(w, "web/dist/index.html")
			remove(t, index)
			if err := os.Symlink("favicon.svg", index); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, false)
			tc.edit(t, workspace)
			before := installedFiles(t, workspace)
			for _, command := range []string{"install", "verify-installed"} {
				if _, err := caller(t, installArguments(command, artifact, workspace, digest), hooks{}); err == nil {
					t.Fatal("invalid existing dist admitted")
				}
				after := installedFiles(t, workspace)
				if !equalFiles(before, after) {
					t.Fatalf("%s changed preexisting files: before=%v after=%v", command, before, after)
				}
			}
		})
	}
	artifact, digest, workspace := installFixture(t, false)
	before := installedFiles(t, workspace)
	if _, err := caller(t, installArguments("verify-installed", artifact, workspace, digest), hooks{}); err == nil ||
		!equalFiles(before, installedFiles(t, workspace)) {
		t.Fatalf("incomplete verify-installed wrote or succeeded: %v", err)
	}
}

func equalFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, content := range a {
		if value, ok := b[name]; !ok || value != content {
			return false
		}
	}
	return true
}

func TestInstallManifestRereads(t *testing.T) {
	for _, command := range []string{"install", "verify-installed"} {
		for _, readNumber := range []int{1, 3, 5} {
			t.Run(command+"/"+string(rune('0'+readNumber)), func(t *testing.T) {
				artifact, digest, workspace := installFixture(t, command == "verify-installed")
				observed := 0
				changed := false
				faults := hooks{read: func(f *os.File) ([]byte, error) {
					data, err := io.ReadAll(f)
					if filepath.Base(f.Name()) == "manifest.json" {
						observed++
						if observed == readNumber {
							changed = true
							err = errors.Join(err, os.WriteFile(filepath.Join(artifact, "manifest.json"), []byte("corrupt observed manifest"), 0o600))
						}
					}
					return data, err
				}}
				_, err := caller(t, installArguments(command, artifact, workspace, digest), faults)
				if !changed || observed < readNumber+1 || err == nil {
					t.Fatalf("manifest reread mutation not observed/rejected: changed=%v reads=%d err=%v", changed, observed, err)
				}
				if readNumber < 5 && len(installedFiles(t, workspace)) != map[bool]int{true: 5, false: 2}[command == "verify-installed"] {
					t.Fatal("failed artifact admission created consumer assets")
				}
			})
		}
	}
}

func TestInstallReplacementAndContentControls(t *testing.T) {
	for _, mode := range []string{"artifact-root", "artifact-manifest-same-bytes", "source-bytes", "destination-parent", "destination-index-same-bytes", "destination-bytes", "appearing-directory"} {
		t.Run(mode, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, false)
			changed := false
			faults := hooks{beforeCopy: func() error {
				changed = true
				switch mode {
				case "artifact-root", "destination-parent":
					target := artifact
					if mode == "destination-parent" {
						target = filepath.Join(workspace, "web")
					}
					if err := os.Rename(target, target+"-owned"); err != nil {
						return err
					}
					return os.Mkdir(target, 0o700)
				case "artifact-manifest-same-bytes", "destination-index-same-bytes":
					target := filepath.Join(artifact, "manifest.json")
					if mode == "destination-index-same-bytes" {
						target = filepath.Join(workspace, "web/dist/index.html")
					}
					data := read(t, target)
					if err := os.Rename(target, target+"-owned"); err != nil {
						return err
					}
					return os.WriteFile(target, data, 0o600)
				case "source-bytes":
					return os.WriteFile(filepath.Join(artifact, "web/dist/assets/index-abcdefgh.js"), []byte("changed"), 0o600)
				case "destination-bytes":
					return os.WriteFile(filepath.Join(workspace, "web/dist/index.html"), []byte("changed"), 0o600)
				default:
					return os.Mkdir(filepath.Join(workspace, "web/dist/assets"), 0o700)
				}
			}}
			_, err := caller(t, installArguments("install", artifact, workspace, digest), faults)
			if !changed || err == nil {
				t.Fatalf("replacement/content change was not rejected: changed=%v err=%v", changed, err)
			}
			if _, err := os.Stat(filepath.Join(workspace, "web/dist/assets/style-abcdefgh.css")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("prewrite rejection installed assets: %v", err)
			}
		})
	}
}

func TestInstallLateFaultsAndCompetitorPreservation(t *testing.T) {
	primary := errors.New("injected write failure")
	cleanup := errors.New("injected cleanup failure")
	for _, mode := range []string{"write", "short-write", "corrupt-write", "close", "root-close", "combined", "appearing-leaf", "moved-assets", "moved-dist"} {
		t.Run(mode, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, false)
			dist := filepath.Join(workspace, "web/dist")
			writes := 0
			faults := hooks{write: func(f *os.File, data []byte) error {
				writes++
				if writes == 1 {
					switch mode {
					case "appearing-leaf":
						writeFixture(t, filepath.Join(dist, "assets/index-abcdefgh.js"), []byte("competitor"))
					case "moved-assets", "moved-dist":
						target := filepath.Join(dist, "assets")
						if mode == "moved-dist" {
							target = dist
						}
						if err := os.Rename(target, target+"-owned"); err != nil {
							return err
						}
						writeFixture(t, filepath.Join(target, "user.txt"), []byte("competitor"))
					case "write", "combined":
						_, err := f.Write(data[:1])
						return errors.Join(err, primary)
					case "short-write":
						_, err := f.Write(data[:1])
						return errors.Join(err, io.ErrShortWrite)
					case "corrupt-write":
						_, err := f.Write([]byte("corrupt"))
						return err
					}
				}
				_, err := f.Write(data)
				return err
			}}
			closed := make(map[*os.File]bool)
			faults.close = func(f *os.File) error {
				if closed[f] {
					t.Errorf("file closed twice: %s", f.Name())
				}
				closed[f] = true
				err := f.Close()
				if (mode == "close" || mode == "combined") && strings.HasSuffix(f.Name(), "chunk-abcdefgh.js") {
					err = errors.Join(err, cleanup)
				}
				return err
			}
			roots := make(map[*os.Root]bool)
			faults.closeRoot = func(r *os.Root) error {
				if roots[r] {
					t.Errorf("root closed twice: %s", r.Name())
				}
				roots[r] = true
				err := r.Close()
				if mode == "root-close" || mode == "combined" {
					err = errors.Join(err, cleanup)
				}
				return err
			}
			_, err := caller(t, installArguments("install", artifact, workspace, digest), faults)
			if writes == 0 || err == nil || !strings.Contains(err.Error(), "requested destination") {
				t.Fatalf("late failure/residue not exercised: writes=%d err=%v", writes, err)
			}
			if mode == "combined" && (!errors.Is(err, primary) || !errors.Is(err, cleanup)) {
				t.Fatalf("primary/cleanup errors lost: %v", err)
			}
			if len(closed) == 0 || len(roots) == 0 {
				t.Fatal("owned cleanup did not execute")
			}
			if mode == "appearing-leaf" && string(read(t, filepath.Join(dist, "assets/index-abcdefgh.js"))) != "competitor" {
				t.Fatal("competing leaf changed")
			}
			if mode == "moved-assets" || mode == "moved-dist" {
				target := filepath.Join(dist, "assets")
				if mode == "moved-dist" {
					target = dist
				}
				if string(read(t, filepath.Join(target, "user.txt"))) != "competitor" {
					t.Fatal("competing directory changed")
				}
				if _, err := os.Stat(filepath.Join(target, "style-abcdefgh.css")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("wrote next asset into competitor: %v", err)
				}
			} else if len(read(t, filepath.Join(dist, "assets/chunk-abcdefgh.js"))) == 0 {
				t.Fatal("partial created file was removed")
			}
		})
	}
}

func TestInstallDiscoveryReadAndArgumentFailures(t *testing.T) {
	for _, command := range []string{"install", "verify-installed"} {
		t.Run(command, func(t *testing.T) {
			artifact, digest, workspace := installFixture(t, true)
			failure := errors.New("injected admission failure")
			for _, faults := range []hooks{
				{discover: func(f *os.File) ([]fs.DirEntry, error) {
					names, err := f.ReadDir(1)
					return names, errors.Join(err, failure)
				}},
				{read: func(*os.File) ([]byte, error) { return nil, failure }},
			} {
				before := installedFiles(t, workspace)
				if _, err := caller(t, installArguments(command, artifact, workspace, digest), faults); !errors.Is(err, failure) {
					t.Fatalf("admission failure swallowed: %v", err)
				}
				if !equalFiles(before, installedFiles(t, workspace)) {
					t.Fatal("failed admission changed files")
				}
			}
			for _, args := range [][]string{
				append(installArguments(command, artifact, workspace, digest), "--destination", temp(t)),
				installArguments(command, artifact, "", digest),
				installArguments(command, artifact, workspace, ""),
			} {
				if _, err := caller(t, args, hooks{}); err == nil {
					t.Fatalf("invalid arguments admitted: %v", args)
				}
			}
		})
	}
}

func TestInstallCLIProcess(t *testing.T) {
	artifact, digest, workspace := installFixture(t, false)
	for i, command := range []string{"verify-installed", "install", "verify-installed"} {
		args, err := json.Marshal(installArguments(command, artifact, workspace, digest))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIProcess$")
		cmd.Env = append(os.Environ(), "WAZA_ASSET_HELPER_TEST_CHILD=1", "WAZA_ASSET_HELPER_TEST_ARGS="+string(args))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		if i == 0 {
			if err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "missing") || len(installedFiles(t, workspace)) != 2 {
				t.Fatalf("incomplete CLI accepted: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
		} else if err != nil || stdout.String() != "manifest_sha256="+digest+"\n" || stderr.Len() != 0 {
			t.Fatalf("CLI transfer failed: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	}
}
