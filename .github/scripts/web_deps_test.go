package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalWebDependencyPreparation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local Make/shell entrypoints require POSIX shell fixtures")
	}
	for _, tool := range []string{"make", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	for _, dir := range []string{"web", "tools"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, contents string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Makefile", "build.sh"} {
		source, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		write(name, string(source), 0o700)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		write(filepath.Join("web", name), "{}\n", 0o600)
	}
	write("tools/npm", `#!/bin/sh
set -eu
printf 'npm %s\n' "$*" >> "$WAZA_BUILD_CALLS"
if [ "$1" = ci ]; then
  if [ "${WAZA_FAIL_INSTALL:-0}" = 1 ]; then exit 29; fi
  mkdir -p node_modules
fi
`, 0o700)
	write("tools/go", "#!/bin/sh\nprintf 'go %s\\n' \"$*\" >> \"$WAZA_BUILD_CALLS\"\n", 0o700)
	calls := filepath.Join(root, "calls")
	run := func(fail bool, tool string, args ...string) error {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), tool, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
			"WAZA_BUILD_CALLS="+calls, "PLATFORM=linux/amd64", "WAZA_FAIL_INSTALL=0",
			"OUTPUT_DIR="+filepath.Join(root, "bin"), "REQUIRE_RELEASE_ADMISSION=0")
		if fail {
			cmd.Env = append(cmd.Env, "WAZA_FAIL_INSTALL=1")
		}
		output, err := cmd.CombinedOutput()
		if err != nil && !fail {
			t.Fatalf("%s %v: %v\n%s", tool, args, err, output)
		}
		return err
	}
	mustRun := func(tool string, args ...string) {
		t.Helper()
		if err := run(false, tool, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(prefix string) int {
		t.Helper()
		data, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, prefix) {
				n++
			}
		}
		return n
	}
	stamp := filepath.Join(root, "web", "node_modules", ".waza-deps-stamp")
	mustRun("make", "--no-print-directory", "build")
	if count("npm ci --silent") != 1 || count("npm run build") != 1 || count("go build ") != 1 {
		t.Fatal("clean make build must install, build the dashboard, then compile Go")
	}
	if data, err := os.ReadFile(calls); err != nil ||
		!strings.HasPrefix(string(data), "npm ci --silent\nnpm run build\ngo build ") {
		t.Fatalf("dependency preparation must precede dashboard and Go builds: %s (%v)", data, err)
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Fatal("successful install must publish its stamp:", err)
	}
	mustRun("make", "--no-print-directory", "build")
	mustRun("bash", "build.sh")
	if count("npm ci --silent") != 1 || count("npm run build") != 3 || count("go build ") != 3 {
		t.Fatal("unchanged Make and shell builds must reuse dependencies but still build real outputs")
	}
	for i, name := range []string{"package.json", "package-lock.json"} {
		manifest := filepath.Join(root, "web", name)
		old := time.Now().Add(-3 * time.Hour)
		installed := old.Add(time.Hour)
		changed := installed.Add(time.Hour)
		for _, input := range []string{"package.json", "package-lock.json"} {
			if err := os.Chtimes(filepath.Join(root, "web", input), old, old); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(stamp, installed, installed); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join("web", name), "{\"changed\":true}\n", 0o600)
		if err := os.Chtimes(manifest, changed, changed); err != nil {
			t.Fatal(err)
		}
		m, err := os.Stat(manifest)
		if err != nil || !m.ModTime().After(installed) {
			t.Fatalf("manifest invalidation setup failed: %v", err)
		}
		other := "package.json"
		if name == other {
			other = "package-lock.json"
		}
		unchanged, err := os.Stat(filepath.Join(root, "web", other))
		if err != nil || !unchanged.ModTime().Before(installed) {
			t.Fatalf("non-selected manifest must not invalidate the cache: %v", err)
		}
		mustRun("make", "--no-print-directory", "web-deps")
		if count("npm ci --silent") != i+2 {
			t.Fatalf("%s change must force a locked reinstall", name)
		}
	}
	if run(true, "make", "--no-print-directory", "web-deps-force") == nil {
		t.Fatal("failed clean reinstall must propagate failure")
	}
	if _, err := os.Stat(stamp); !os.IsNotExist(err) {
		t.Fatalf("failed install must not leave a successful stamp: %v", err)
	}
	before := count("npm ci --silent")
	mustRun("make", "--no-print-directory", "web-deps")
	mustRun("make", "--no-print-directory", "web-deps-force")
	if count("npm ci --silent") != before+2 {
		t.Fatal("missing dependencies and explicit recovery must both reinstall")
	}
}
