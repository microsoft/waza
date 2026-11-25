//go:build linux

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDetectContextRejectsReplacedSkillDefinition(t *testing.T) {
	const childEnv = "WAZA_WORKSPACE_REPLACEMENT_CHILD"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestDetectContextRejectsReplacedSkillDefinition$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, ctx.Err(), "workspace discovery blocked after a FIFO replacement: %s", output)
		require.NoError(t, err, string(output))
		require.Contains(t, string(output), "PASS: TestDetectContextRejectsReplacedSkillDefinition/skill")
		require.Contains(t, string(output), "PASS: TestDetectContextRejectsReplacedSkillDefinition/agent")
		return
	}

	for _, name := range []string{"skill", "agent"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "skills", "reviewed")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			filename := "SKILL.md"
			if name == "agent" {
				filename = "helper.agent.md"
			}
			path := filepath.Join(dir, filename)
			require.NoError(t, os.WriteFile(path, []byte("---\nname: declared-name\n---\n# Definition\n"), 0o600))

			healthy, err := DetectContext(root, WithDiscoverySymlinkValidation())
			require.NoError(t, err)
			require.Len(t, healthy.Skills, 1)
			require.Equal(t, "declared-name", healthy.Skills[0].Name)

			open := openSkillDefinition
			replaced := false
			openSkillDefinition = func(candidate string) (*os.File, error) {
				if candidate == path && !replaced {
					if err := os.Remove(path); err != nil {
						return nil, err
					}
					if err := syscall.Mkfifo(path, 0o600); err != nil {
						return nil, err
					}
					replaced = true
				}
				return open(candidate)
			}
			t.Cleanup(func() { openSkillDefinition = open })

			_, err = DetectContext(root, WithDiscoverySymlinkValidation())
			require.True(t, replaced, "fixture must replace the definition between discovery and open")
			require.ErrorContains(t, err, "not a regular file")
			require.ErrorContains(t, err, path)
		})
	}
}
