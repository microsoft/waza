package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/skill"
	"github.com/stretchr/testify/require"
)

func TestMetadataDiscoveryColdProcess(t *testing.T) {
	mode := os.Getenv("WAZA_TEST_METADATA_COLD")
	if mode == "" {
		for _, control := range []string{"metadata", "full"} {
			t.Run(control, func(t *testing.T) {
				executable, err := os.Executable()
				require.NoError(t, err)
				cmd := exec.Command(executable, "-test.run=^TestMetadataDiscoveryColdProcess$", "-test.v")
				cmd.Env = append(os.Environ(), "WAZA_TEST_METADATA_COLD="+control)
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
				t.Logf("%s", output)
			})
		}
		return
	}

	root := t.TempDir()
	path := filepath.Join(root, "SKILL.md")
	content := skillMD("cold-skill")
	writeFile(t, path, content)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if mode == "full" {
		var parsed skill.Skill
		require.NoError(t, parsed.UnmarshalText([]byte(content)))
		require.Positive(t, parsed.Tokens)
	} else {
		ctx, err := DetectContext(root)
		require.NoError(t, err)
		require.Equal(t, []SkillInfo{{Name: "cold-skill", Dir: root, SkillPath: path}}, ctx.Skills)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("cold %s discovery: %d allocated bytes", mode, allocated)
	// The full-parser control proves the process is cold and the tokenizer's
	// roughly 80 MB initialization would breach this deliberately loose budget.
	if mode == "full" {
		require.Greater(t, allocated, uint64(16<<20))
	} else {
		require.Less(t, allocated, uint64(16<<20))
	}
}

func TestMetadataDiscoveryRefresh(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "custom-skills", "nested", "example")
	path := filepath.Join(skillDir, "SKILL.md")
	writeFile(t, path, skillMD("original"))
	options := []DetectOption{WithSkillsDir("custom-skills"), WithEvalsDir("custom-evals"), WithEvalFile("suite.yml")}
	eval := filepath.Join(root, "custom-evals", "original", "suite.yml")
	writeFile(t, eval, "name: example\n")

	first, err := DetectContext(root, options...)
	require.NoError(t, err)
	require.Equal(t, ContextMultiSkill, first.Type)
	require.Equal(t, []SkillInfo{{Name: "original", Dir: skillDir, SkillPath: path}}, first.Skills)
	found, err := FindEval(first, "original")
	require.NoError(t, err)
	require.Equal(t, eval, found)

	writeFile(t, path, "---\r\nname: ' refreshed '\r\ndescription: changed\r\n---\r\nBody\r\n")
	refreshed, err := DetectContext(root, options...)
	require.NoError(t, err)
	require.Equal(t, []SkillInfo{{Name: "refreshed", Dir: skillDir, SkillPath: path}}, refreshed.Skills)
	require.Equal(t, "original", first.Skills[0].Name, "refresh must not mutate earlier results")
	found, err = FindEval(refreshed, "refreshed")
	require.NoError(t, err)
	require.Empty(t, found)

	writeFile(t, path, "---\nname: [invalid\n---\n")
	invalid, err := DetectContext(root, options...)
	require.NoError(t, err)
	require.NotContains(t, invalid.Skills, refreshed.Skills[0], "invalid replacement cannot retain stale metadata")
}

func BenchmarkDetectContextMetadata(b *testing.B) {
	for _, lifecycle := range []string{"Construct", "Refresh"} {
		b.Run(lifecycle, func(b *testing.B) {
			root := b.TempDir()
			for _, name := range []string{"alpha", "beta", "gamma", "delta"} {
				dir := filepath.Join(root, "skills", name)
				if err := os.MkdirAll(dir, 0755); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD(name)), 0644); err != nil {
					b.Fatal(err)
				}
			}
			if lifecycle == "Refresh" {
				if _, err := DetectContext(root); err != nil {
					b.Fatal(err)
				}
				path := filepath.Join(root, "skills", "alpha", "SKILL.md")
				if err := os.WriteFile(path, []byte(skillMD("refreshed")), 0644); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				ctx, err := DetectContext(root)
				if err != nil {
					b.Fatal(err)
				}
				if ctx.Type != ContextMultiSkill || len(ctx.Skills) != 4 {
					b.Fatalf("unexpected discovery: %+v", ctx)
				}
				if lifecycle == "Refresh" && ctx.Skills[0].Name != "refreshed" {
					b.Fatalf("stale metadata: %+v", ctx.Skills[0])
				}
			}
		})
	}
}
