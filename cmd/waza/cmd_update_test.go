package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateCommand_ConfirmedRunsInstaller(t *testing.T) {
	var stdout bytes.Buffer
	var ran bool

	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		BashInstallerURL: "https://example.com/install.sh",
		GOOS:             "linux",
		LookPath: func(name string) (string, error) {
			switch name {
			case "bash":
				return "/usr/bin/bash", nil
			case "curl":
				return "/usr/bin/curl", nil
			default:
				t.Fatalf("unexpected lookup for %s", name)
				return "", errors.New("unexpected lookup")
			}
		},
		RunCommand: func(ctx context.Context, name string, args []string, env []string, stdin io.Reader, out, errOut io.Writer) error {
			ran = true
			assert.Equal(t, "/usr/bin/bash", name)
			require.Len(t, args, 4)
			assert.Equal(t, "-c", args[0])
			assert.Contains(t, args[1], "curl -fsSL")
			assert.Equal(t, "https://example.com/install.sh", args[3])
			assert.Empty(t, env)
			return nil
		},
	})
	cmd.SetIn(strings.NewReader("yes\n"))
	cmd.SetArgs([]string{})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	require.NoError(t, cmd.Execute())
	assert.True(t, ran)
	assert.Contains(t, stdout.String(), "Continue? [y/N]:")
	assert.Contains(t, stdout.String(), "Bash installer")
	assert.Contains(t, stdout.String(), "Update complete")
}

func TestUpdateCommand_DeclinedDoesNotRunInstaller(t *testing.T) {
	var stdout bytes.Buffer

	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		GOOS: "linux",
		RunCommand: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			t.Fatal("installer should not run when update is declined")
			return nil
		},
	})
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetArgs([]string{})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	require.NoError(t, cmd.Execute())
	assert.Contains(t, stdout.String(), "Update canceled.")
}

func TestUpdateCommand_YesFlagSkipsConfirmation(t *testing.T) {
	var stdout bytes.Buffer
	var ran bool

	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		GOOS: "linux",
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		RunCommand: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			ran = true
			return nil
		},
	})
	cmd.SetArgs([]string{"--yes"})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	require.NoError(t, cmd.Execute())
	assert.True(t, ran)
	assert.NotContains(t, stdout.String(), "Continue? [y/N]:")
}

func TestUpdateCommand_MissingBashReturnsGuidance(t *testing.T) {
	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		GOOS: "linux",
		LookPath: func(name string) (string, error) {
			return "", errors.New("not found")
		},
		RunCommand: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			t.Fatal("installer should not run when bash is missing")
			return nil
		},
	})
	cmd.SetArgs([]string{"--yes"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bash is required")
	assert.Contains(t, err.Error(), latestReleaseURL)
}

func TestUpdateCommand_RunFailureIncludesContext(t *testing.T) {
	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		GOOS: "linux",
		LookPath: func(name string) (string, error) {
			return "/usr/bin/bash", nil
		},
		RunCommand: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			return errors.New("boom")
		},
	})
	cmd.SetArgs([]string{"--yes"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "running waza installer")
	assert.Contains(t, err.Error(), "boom")
}

func TestUpdateCommand_DarwinUsesBashInstaller(t *testing.T) {
	var ran bool
	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		BashInstallerURL: "https://example.com/install.sh",
		GOOS:             "darwin",
		LookPath: func(name string) (string, error) {
			switch name {
			case "bash":
				return "/bin/bash", nil
			case "curl":
				return "/usr/bin/curl", nil
			default:
				t.Fatalf("unexpected lookup for %s", name)
				return "", errors.New("unexpected lookup")
			}
		},
		RunCommand: func(ctx context.Context, name string, args []string, env []string, stdin io.Reader, out, errOut io.Writer) error {
			ran = true
			assert.Equal(t, "/bin/bash", name)
			assert.Equal(t, "https://example.com/install.sh", args[3])
			assert.Empty(t, env)
			return nil
		},
	})
	cmd.SetArgs([]string{"--yes"})

	require.NoError(t, cmd.Execute())
	assert.True(t, ran)
}

func TestUpdateCommand_MissingCurlReturnsGuidance(t *testing.T) {
	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		GOOS: "linux",
		LookPath: func(name string) (string, error) {
			if name == "bash" {
				return "/usr/bin/bash", nil
			}
			return "", errors.New("not found")
		},
		RunCommand: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			t.Fatal("installer should not run when curl is missing")
			return nil
		},
	})
	cmd.SetArgs([]string{"--yes"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "curl is required")
	assert.Contains(t, err.Error(), latestReleaseURL)
}

func TestUpdateCommand_WindowsUsesPowerShellInstaller(t *testing.T) {
	var stdout bytes.Buffer
	var lookups []string
	var ran bool
	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		PowerShellInstallerURL: "https://example.com/install.ps1",
		GOOS:                   "windows",
		ExecutablePath:         "C:/tools/waza.exe",
		LookPath: func(name string) (string, error) {
			lookups = append(lookups, name)
			if name == "pwsh" {
				return "", errors.New("not found")
			}
			return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
		},
		RunCommand: func(ctx context.Context, name string, args []string, env []string, stdin io.Reader, out, errOut io.Writer) error {
			ran = true
			assert.Contains(t, name, "powershell.exe")
			require.Len(t, args, 6)
			assert.Equal(t, "-NoProfile", args[0])
			assert.Equal(t, "-ExecutionPolicy", args[1])
			assert.Equal(t, "Bypass", args[2])
			assert.Equal(t, "-Command", args[3])
			assert.Contains(t, args[4], "Invoke-RestMethod")
			assert.Equal(t, "https://example.com/install.ps1", args[5])
			require.Len(t, env, 2)
			assert.Contains(t, env[0], "WAZA_UPDATE_PARENT_PID=")
			assert.Equal(t, "WAZA_INSTALL_DIR="+filepath.Dir("C:/tools/waza.exe"), env[1])
			return nil
		},
	})
	cmd.SetArgs([]string{"--yes"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	require.NoError(t, cmd.Execute())
	assert.True(t, ran)
	assert.Equal(t, []string{"pwsh", "powershell"}, lookups)
	assert.Contains(t, stdout.String(), "PowerShell installer")
	assert.Contains(t, stdout.String(), "Update started")
}

func TestUpdateCommand_MissingPowerShellReturnsGuidance(t *testing.T) {
	cmd := newUpdateCommandWithOptions(&updateCommandOptions{
		GOOS: "windows",
		LookPath: func(name string) (string, error) {
			return "", errors.New("not found")
		},
		RunCommand: func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			t.Fatal("installer should not run when PowerShell is missing")
			return nil
		},
	})
	cmd.SetArgs([]string{"--yes"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PowerShell is required")
	assert.Contains(t, err.Error(), latestReleaseURL)
}

func TestUpdateCommand_UnsupportedOS(t *testing.T) {
	cmd := newUpdateCommandWithOptions(&updateCommandOptions{GOOS: "freebsd"})
	cmd.SetArgs([]string{"--yes"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported OS")
}

func TestRootCommand_RegistersUpdateCommand(t *testing.T) {
	cmd := newRootCommand()
	found, _, err := cmd.Find([]string{"update"})
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "update", found.Name())
}

func TestShouldRunUpdateCheck_SkipsUpdateCommand(t *testing.T) {
	root := newRootCommand()
	updateCmd, _, err := root.Find([]string{"update"})
	require.NoError(t, err)

	assert.False(t, shouldRunUpdateCheck(updateCmd, false))
}

func TestShouldRunUpdateCheck_RespectsOptOuts(t *testing.T) {
	root := newRootCommand()

	assert.False(t, shouldRunUpdateCheck(root, true))

	t.Setenv("WAZA_NO_UPDATE_CHECK", "1")
	assert.False(t, shouldRunUpdateCheck(root, false))

	require.NoError(t, os.Unsetenv("WAZA_NO_UPDATE_CHECK"))
	assert.True(t, shouldRunUpdateCheck(root, false))
}
