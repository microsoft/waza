package scripts

import (
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseDashboardCheckerDistribution(t *testing.T) {
	unit := exec.CommandContext(t.Context(), "go", "test", "-count=1", "-timeout=1m", "./release-dashboard-check")
	unit.Env = append(os.Environ(), "GOOS=", "GOARCH=", "GOPROXY=off", "GOSUMDB=off")
	if output, err := unit.CombinedOutput(); err != nil {
		t.Fatalf("explicit checker test discovery: %v\n%s", err, output)
	}
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "host-native", true: "missing-pinned-dependency"}[missing], func(t *testing.T) {
			dir := t.TempDir()
			sources := filepath.Join(dir, ".github", "scripts", "release-dashboard-check")
			if err := os.MkdirAll(sources, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"main.go", "open_unix.go", "open_windows.go"} {
				content, err := os.ReadFile(filepath.Join("release-dashboard-check", name))
				if err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, filepath.Join(sources, name), content)
			}
			shared := filepath.Join("internal", "dashboarddocument", "document.go")
			content, err := os.ReadFile(shared)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(dir, ".github", "scripts", shared)
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				t.Fatal(err)
			}
			writeReleaseFile(t, destination, content)
			writeReleaseFile(t, filepath.Join(dir, "go.mod"), []byte("module github.com/microsoft/waza\n\ngo 1.26.4\n\nrequire (\n\tgolang.org/x/net v0.61.0\n\tgolang.org/x/sys v0.49.0\n)\n"))
			sum, err := os.ReadFile("../../go.sum")
			if err != nil {
				t.Fatal(err)
			}
			writeReleaseFile(t, filepath.Join(dir, "go.sum"), sum)
			for path, content := range map[string]string{
				"web/dist/index.html":  "<script src='/assets/a.js'></script><link rel='stylesheet' href='/assets/a.css'>",
				"web/dist/favicon.svg": "<svg/>", "web/dist/assets/a.js": "export {};", "web/dist/assets/a.css": "body{}",
			} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700); err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, filepath.Join(dir, path), []byte(content))
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", releaseDashboardCheck)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "GOOS=windows", "GOARCH=arm64", "GOTOOLCHAIN=local")
			if missing {
				cmd.Env = append(cmd.Env, "GOMODCACHE="+t.TempDir())
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != missing {
				t.Fatalf("actual offline host command, missing=%t: %v\n%s", missing, err, output)
			}
			if missing && !strings.Contains(string(output), "GOPROXY=off") {
				t.Fatalf("missing dependency did not fail explicitly without fetching: %s", output)
			}
		})
	}
}

func TestPackagedDashboardRejectsPartialDiscoveryAndPreservesConcurrentOutput(t *testing.T) {
	dir, dist, fixtureDist, bin, script := packagedDashboardFixture(t)
	writeReleaseFile(t, filepath.Join(bin, "find"), []byte(`#!/usr/bin/env bash
mkdir -p web/dist
printf concurrent > web/dist/concurrent.txt
printf '%s\0' "$1/app.js"
exit 71
`))
	cmd := exec.CommandContext(t.Context(), "bash", script, filepath.Join(bin, "waza"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FIXTURE_DIST="+fixtureDist)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "asset discovery failed") ||
		!strings.Contains(string(output), "Concurrent dashboard output preserved") {
		t.Fatalf("partial discovery and cleanup collision must fail safely: %v\n%s", err, output)
	}
	restored, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil || string(restored) != "<!doctype html><script src='/assets/app.js'></script>" {
		t.Fatalf("original dashboard was not restored: %v, %q", err, restored)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "web", "dist.release-dashboard-concurrent.*", "concurrent.txt"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("concurrent dashboard output was not preserved: %v, %v", err, backups)
	}
	concurrent, err := os.ReadFile(backups[0])
	if err != nil || string(concurrent) != "concurrent" {
		t.Fatalf("preserved concurrent dashboard = %q, %v", concurrent, err)
	}
}

func TestPackagedDashboardRestoreRejectsDestinationCreationRace(t *testing.T) {
	dir, _, fixtureDist, bin, script := packagedDashboardFixture(t)
	writeReleaseFile(t, filepath.Join(bin, "find"), []byte(`#!/usr/bin/env bash
printf '%s\0' "$1/app.js"
exit 71
`))
	writeReleaseFile(t, filepath.Join(bin, "mv"), []byte(`#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -eq 4 ] && [ "$1" = -T ] && [ "$2" = --no-clobber ] && [ "$4" = web/dist ]; then
  mkdir -p web/dist
  printf racing > web/dist/racing.txt
fi
exec "$REAL_MV" "$@"
`))
	realMove, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", script, filepath.Join(bin, "waza"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FIXTURE_DIST="+fixtureDist, "REAL_MV="+realMove)
	output, err := cmd.CombinedOutput()
	marker := "Failed to restore the release dashboard; original remains at "
	position := strings.Index(string(output), marker)
	if err == nil || position < 0 {
		t.Fatalf("destination creation race must fail without nesting: %v\n%s", err, output)
	}
	line := string(output)[position+len(marker):]
	line = strings.TrimSuffix(strings.SplitN(line, "\n", 2)[0], ".")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(line)) })
	if _, err := os.Stat(filepath.Join(line, "index.html")); err != nil {
		t.Fatalf("original dashboard was not preserved at %s: %v", line, err)
	}
	racing, err := os.ReadFile(filepath.Join(dir, "web", "dist", "racing.txt"))
	if err != nil || string(racing) != "racing" {
		t.Fatalf("competing dashboard was modified: %q, %v", racing, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "web", "dist", "source-dist")); !os.IsNotExist(err) {
		t.Fatalf("original dashboard nested into competing output: %v", err)
	}
}

func packagedDashboardFixture(t *testing.T) (dir, dist, fixtureDist, bin, script string) {
	t.Helper()
	dir = t.TempDir()
	dist = filepath.Join(dir, "web", "dist")
	fixtureDist = filepath.Join(dir, "fixture-dist")
	for _, root := range []string{dist, fixtureDist} {
		for path, content := range map[string]string{
			"index.html":    "<!doctype html><script src='/assets/app.js'></script>",
			"favicon.svg":   "<svg/>",
			"assets/app.js": "export {};",
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o700); err != nil {
				t.Fatal(err)
			}
			writeReleaseFile(t, filepath.Join(root, path), []byte(content))
		}
	}
	if err := os.MkdirAll(filepath.Join(fixtureDist, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeReleaseFile(t, filepath.Join(fixtureDist, "api", "health"), []byte("{}"))
	bin = filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeReleaseFile(t, filepath.Join(bin, "waza"), []byte(`#!/usr/bin/env bash
set -euo pipefail
port=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = --port ]; then port="$2"; shift 2; else shift; fi
done
exec python3 -m http.server "$port" --bind 127.0.0.1 --directory "$FIXTURE_DIST"
`))
	var err error
	script, err = filepath.Abs("release_dashboard_integration_test.sh")
	if err != nil {
		t.Fatal(err)
	}
	return dir, dist, fixtureDist, bin, script
}

func TestReleaseLastBuilderAdmission(t *testing.T) {
	build, err := os.ReadFile("../../build.sh")
	if err != nil {
		t.Fatal(err)
	}
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(t.TempDir(), "checker")
	compile := exec.CommandContext(t.Context(), "go", "build", "-o", checker, "./release-dashboard-check")
	compile.Env = append(os.Environ(), "GOOS=", "GOARCH=", "GOPROXY=off", "GOSUMDB=off")
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("host checker compilation: %v\n%s", err, output)
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"valid", "last-build-missing", "last-build-dirty-index", "last-build-dirty-favicon",
		"last-build-untracked", "dirty-at-compile", "missing-vcs", "metadata-output-error", "ordinary-dirty-index"} {
		t.Run(state, func(t *testing.T) {
			dir, _, _, _ := publicationRepository(t)
			files := map[string]string{
				"go.mod": "module builder.fixture\n\ngo 1.26.4\n", "cmd/waza/main.go": "package main\nvar version string\nfunc main(){}\n",
				".gitignore": "bin/\nweb/node_modules/\n", "web/dist/index.html": "<script src='/assets/a.js'></script><link rel='stylesheet' href='/assets/a.css'>",
				"web/package.json": "{}\n", "web/package-lock.json": "{}\n",
				"web/dist/favicon.svg": "<svg/>", "web/dist/assets/a.js": "export {};", "web/dist/assets/a.css": "body{}",
			}
			for path, content := range files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700); err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, filepath.Join(dir, path), []byte(content))
			}
			writeReleaseFile(t, filepath.Join(dir, "build.sh"), build)
			writeReleaseFile(t, filepath.Join(dir, "Makefile"), makefile)
			for _, name := range []string{"release_source_check.sh", "release_binary_check.sh"} {
				content, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, ".github", "scripts", name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, path, content)
			}
			publicationGit(t, dir, "add", ".")
			publicationGit(t, dir, "commit", "-qm", "coherent source and assets")
			sha := publicationGit(t, dir, "rev-parse", "HEAD")
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			writeReleaseFile(t, filepath.Join(bin, "npm"), []byte(`#!/bin/bash
set -euo pipefail
if [ "$1" = ci ]; then mkdir -p node_modules; exit 0; fi
test "$1" = run
test "$2" = build
printf build > ../bin/npm-build-ran
printf '%s' "<script src='/assets/a.js'></script><link rel='stylesheet' href='/assets/a.css'>" > dist/index.html
printf '%s' '<svg/>' > dist/favicon.svg
printf '%s' 'export {};' > dist/assets/a.js
printf '%s' 'body{}' > dist/assets/a.css
case "$STATE" in
  last-build-missing) rm dist/assets/a.css;;
  last-build-dirty-index|ordinary-dirty-index) printf '\n<!-- rebuilt -->' >> dist/index.html;;
  last-build-dirty-favicon) printf '\n' >> dist/favicon.svg;;
  last-build-untracked) printf untracked > ../unexpected-file;;
esac
`))
			writeReleaseFile(t, filepath.Join(bin, "go"), []byte(`#!/bin/bash
set -euo pipefail
if [ "$1" = run ]; then
  test -z "$GOOS"
  test -z "$GOARCH"
  exec "$CHECKER"
fi
if [ "$1" = build ]; then
  printf compile > bin/compile-started
  if [ "$STATE" = dirty-at-compile ]; then printf dirty > unexpected-file; fi
  if [ "$STATE" = missing-vcs ]; then shift; set -- build -buildvcs=false "$@"; fi
fi
"$REAL_GO" "$@"
if [ "$1" = version ] && [ "$STATE" = metadata-output-error ]; then exit 73; fi
`))
			cmd := exec.CommandContext(t.Context(), "bash", "build.sh")
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"REAL_GO="+realGo, "CHECKER="+checker, "STATE="+state, "GITHUB_SHA="+sha, "VERSION=1.2.3",
				"REQUIRE_RELEASE_ADMISSION=1", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local",
				"OUTPUT_DIR="+bin, "PLATFORM=", "GOFLAGS=")
			if state != "valid" {
				cmd.Env = append(cmd.Env, "PLATFORM=linux/amd64")
			}
			if state == "ordinary-dirty-index" {
				cmd.Env = append(cmd.Env, "REQUIRE_RELEASE_ADMISSION=0")
			}
			output, err := cmd.CombinedOutput()
			if _, producerErr := os.Stat(filepath.Join(bin, "npm-build-ran")); producerErr != nil {
				t.Fatalf("actual final npm producer was not exercised: %v\n%s", producerErr, output)
			}
			if (err == nil) != (state == "valid" || state == "ordinary-dirty-index") {
				t.Fatalf("actual final-builder admission %s: %v\n%s", state, err, output)
			}
			if strings.HasPrefix(state, "last-build-") {
				if _, err := os.Stat(filepath.Join(bin, "compile-started")); !os.IsNotExist(err) {
					t.Fatalf("invalid final npm output reached compilation: %v", err)
				}
			}
			if state == "valid" {
				for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64.exe", "windows-arm64.exe"} {
					info, err := buildinfo.ReadFile(filepath.Join(bin, "microsoft-azd-waza-"+platform))
					if err != nil {
						t.Fatal(err)
					}
					values := make(map[string]string)
					for _, setting := range info.Settings {
						values[setting.Key] = setting.Value
					}
					if values["vcs.revision"] != sha || values["vcs.modified"] != "false" {
						t.Fatalf("actual cross-built metadata %s: %v", platform, values)
					}
				}
			}
		})
	}
}
