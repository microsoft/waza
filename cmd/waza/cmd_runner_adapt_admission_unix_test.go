//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/rootedfile"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerAdapterWriterlessFIFOCompiledProcess(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, source := range []string{"eval", "task"} {
		for _, symlink := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "/direct", true: "/symlink"}[symlink], func(t *testing.T) {
				evalPath, taskPath, _, _ := adapterCommandInputs(t)
				directory := filepath.Dir(evalPath)
				path := evalPath
				if source == "task" {
					path = taskPath
				}
				require.NoError(t, os.Remove(path))
				fifo := path
				if symlink {
					fifo = filepath.Join(directory, "writerless.fifo")
				}
				require.NoError(t, unix.Mkfifo(fifo, 0o600))
				if symlink {
					require.NoError(t, os.Symlink(fifo, path))
				}
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeNamedPipe, "setup must expose a writerless FIFO")
				output, diagnostic := adapterProcess(t, binary, directory, nil, 2,
					append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...)...)
				require.Empty(t, output)
				require.Contains(t, diagnostic, "regular file")
				artifact := filepath.Join(directory, "forbidden.json")
				_, diagnostic = adapterProcess(t, binary, directory, nil, 2, "run", evalPath,
					"--runner-compat", "--output", artifact)
				require.Contains(t, diagnostic, "regular file")
				_, err = os.Stat(artifact)
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
}

func TestRunnerAdapterSourceHandleRaces(t *testing.T) {
	if control := os.Getenv("WAZA_ADAPTER_HANDLE_RACE"); control != "" {
		directory := t.TempDir()
		parent := filepath.Join(directory, "parent")
		require.NoError(t, os.Mkdir(parent, 0o700))
		path := filepath.Join(parent, "input.yaml")
		original := []byte("captured original bytes")
		require.NoError(t, os.WriteFile(path, original, 0o600))
		replacement := filepath.Join(directory, "replacement")
		require.NoError(t, os.Mkdir(replacement, 0o700))
		require.NoError(t, unix.Mkfifo(filepath.Join(replacement, "input.yaml"), 0o600))
		data, err := readRunnerAdapterSourceWithOpener(path, func(name string) (*os.File, error) {
			replaceParent := func() {
				require.NoError(t, os.Rename(parent, parent+".original"))
				require.NoError(t, os.Symlink(replacement, parent))
			}
			if control == "before open" {
				replaceParent()
				return rootedfile.OpenPath(name)
			}
			file, err := rootedfile.OpenPath(name)
			require.NoError(t, err)
			replaceParent()
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.NotZero(t, info.Mode()&os.ModeNamedPipe)
			return file, nil
		})
		if control == "before open" {
			require.ErrorContains(t, err, "regular file")
			require.Nil(t, data)
		} else {
			require.NoError(t, err)
			require.Equal(t, original, data, "read the checked opened handle, not the replacement path")
		}
		return
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	for _, control := range []string{"before open", "after open"} {
		t.Run(control, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRunnerAdapterSourceHandleRaces$", "-test.count=1")
			cmd.Env = append(os.Environ(), "WAZA_ADAPTER_HANDLE_RACE="+control)
			cmd.WaitDelay = time.Second
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "race helper must not leak a blocked FIFO opener")
			require.NoError(t, err, "%s", output)
		})
	}
}

func TestRunnerAdapterRegularSymlinkAndOpenedErrors(t *testing.T) {
	directory := t.TempDir()
	external := filepath.Join(t.TempDir(), "regular.yaml")
	original := []byte("regular outside source parent")
	require.NoError(t, os.WriteFile(external, original, 0o600))
	link := filepath.Join(directory, "source.yaml")
	require.NoError(t, os.Symlink(external, link))
	t.Chdir(directory)
	for _, path := range []string{link, "source.yaml"} {
		data, err := readRunnerAdapterSource(path)
		require.NoError(t, err)
		require.Equal(t, original, data, "a regular symlink target is not a confinement violation")
	}
	sentinel := errors.New("injected open failure")
	_, err := readRunnerAdapterSourceWithOpener(link, func(string) (*os.File, error) {
		return nil, sentinel
	})
	require.ErrorIs(t, err, sentinel)
	_, err = readRunnerAdapterSourceWithOpener(link, func(name string) (*os.File, error) {
		file, err := rootedfile.OpenPath(name)
		require.NoError(t, err)
		require.NoError(t, file.Close())
		return file, nil
	})
	require.ErrorContains(t, err, "inspecting adapter source")
	require.ErrorContains(t, err, "closing adapter source")
}
