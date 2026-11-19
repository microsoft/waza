package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkpointSourceInputs(t *testing.T, root string) []string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "list", "-mod=readonly", "-deps", "-json", "./cmd/waza")
	command.Dir = root
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	output, err := command.Output()
	require.NoError(t, err, "discovering actual native build inputs: %s", diagnostic.String())
	names := map[string]bool{"go.mod": true}
	if _, err := os.Stat(filepath.Join(root, "go.sum")); !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err)
		names["go.sum"] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg struct {
			Dir                                                         string
			GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles []string
			SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles      []string
		}
		err := decoder.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		directory, err := filepath.Rel(root, pkg.Dir)
		require.NoError(t, err)
		if !filepath.IsLocal(directory) {
			continue
		}
		for _, group := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles,
			pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles} {
			for _, name := range group {
				name = filepath.Join(directory, name)
				require.True(t, filepath.IsLocal(name), "nonlocal build input %q", name)
				require.False(t, filepath.Base(name) == ".env" || strings.HasPrefix(filepath.Base(name), ".env."),
					"environment files must not be copied into a source fixture")
				names[name] = true
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func checkpointFixtureGit(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{
		"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
		"-c", "core.attributesFile=/dev/null", "-c", "core.excludesFile=/dev/null",
		"-c", "user.name=Checkpoint source fixture", "-c", "user.email=fixture@example.invalid",
	}, args...)...)
	command.Dir = directory
	command.Env = slices.DeleteFunc(os.Environ(), func(value string) bool { return strings.HasPrefix(value, "GIT_") })
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "fixture-local git %s: %s", strings.Join(args, " "), output)
	return output
}

func checkpointSourceSnapshot(t *testing.T, root string) string {
	t.Helper()
	names := checkpointSourceInputs(t, root)
	snapshot := t.TempDir()
	digests := make(map[string][sha256.Size]byte, len(names))
	for _, name := range names {
		original, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		destination := filepath.Join(snapshot, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o700))
		require.NoError(t, os.WriteFile(destination, original, 0o600))
		copied, err := os.ReadFile(destination)
		require.NoError(t, err)
		require.Equal(t, original, copied, "copied build input %s", name)
		digests[name] = sha256.Sum256(original)
	}
	require.Equal(t, names, checkpointSourceInputs(t, root), "build input inventory changed during snapshot")
	for _, name := range names {
		current, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, digests[name], sha256.Sum256(current), "build input changed during snapshot: %s", name)
	}
	checkpointFixtureGit(t, snapshot, "init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(snapshot, ".git", "info", "attributes"), []byte("* -text -filter\n"), 0o600))
	checkpointFixtureGit(t, snapshot, append([]string{"add", "--force", "--"}, names...)...)
	checkpointFixtureGit(t, snapshot, "commit", "--quiet", "-m", "Snapshot actual native CLI source and embedded assets")
	require.Empty(t, checkpointFixtureGit(t, snapshot, "status", "--porcelain", "--untracked-files=all"))
	t.Logf("native source snapshot: files=%d; fixture revision=%s", len(names),
		strings.TrimSpace(string(checkpointFixtureGit(t, snapshot, "rev-parse", "HEAD"))))
	return snapshot
}

func TestCheckpointSourceSnapshotIncludesDirtyAndIgnoredInputs(t *testing.T) {
	root := t.TempDir()
	write := func(name, data string) {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	}
	write("go.mod", "module fixture.invalid/checkpoint\n\ngo 1.26\n")
	write(".gitignore", "/cmd/waza/assets/ignored.txt\n")
	write("cmd/waza/main.go", "package main\nfunc main() {}\n")
	write("cmd/waza/deleted.go", "package main\nconst deleted = true\n")
	write("cmd/waza/assets/tracked.txt", "old asset")
	checkpointFixtureGit(t, root, "init", "--quiet")
	checkpointFixtureGit(t, root, "add", ".")
	checkpointFixtureGit(t, root, "commit", "--quiet", "-m", "Original source control")
	write("cmd/waza/main.go", `package main
import ("embed"; "fmt")
//go:embed all:assets
var assets embed.FS
func main() {
	tracked, err := assets.ReadFile("assets/tracked.txt")
	if err != nil { panic(err) }
	ignored, err := assets.ReadFile("assets/ignored.txt")
	if err != nil { panic(err) }
	fmt.Print(current, "|", string(tracked), "|", string(ignored))
}
`)
	write("cmd/waza/current.go", "package main\nconst current = \"untracked current source\"\n")
	write("cmd/waza/assets/tracked.txt", "modified tracked asset")
	write("cmd/waza/assets/ignored.txt", "ignored current asset")
	require.NoError(t, os.Remove(filepath.Join(root, "cmd", "waza", "deleted.go")))
	before := checkpointFixtureGit(t, root, "status", "--porcelain", "--untracked-files=all")
	require.Contains(t, string(before), " M cmd/waza/main.go")
	require.Contains(t, string(before), " D cmd/waza/deleted.go")
	require.Contains(t, string(before), "?? cmd/waza/current.go")
	require.NotContains(t, string(before), "ignored.txt")
	snapshot := checkpointSourceSnapshot(t, root)
	for _, name := range []string{"cmd/waza/main.go", "cmd/waza/current.go",
		"cmd/waza/assets/tracked.txt", "cmd/waza/assets/ignored.txt"} {
		original, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		copied, err := os.ReadFile(filepath.Join(snapshot, name))
		require.NoError(t, err)
		require.Equal(t, original, copied, name)
	}
	require.NoFileExists(t, filepath.Join(snapshot, "cmd", "waza", "deleted.go"))
	require.Equal(t, before, checkpointFixtureGit(t, root, "status", "--porcelain", "--untracked-files=all"),
		"snapshot must not stage, commit or modify the caller's repository")
	binary := buildRecoveryCLIFromDirectory(t, filepath.Join(snapshot, "cmd", "waza"))
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	require.Equal(t, "false", settings["vcs.modified"])
	require.Equal(t, strings.TrimSpace(string(checkpointFixtureGit(t, snapshot, "rev-parse", "HEAD"))), settings["vcs.revision"])
	output, err := exec.CommandContext(t.Context(), binary).CombinedOutput()
	require.NoError(t, err)
	require.Equal(t, "untracked current source|modified tracked asset|ignored current asset", string(output))
}
