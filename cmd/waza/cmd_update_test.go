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
			require.Len(t, args, 5)
			assert.Equal(t, "-NoProfile", args[0])
			assert.Equal(t, "-ExecutionPolicy", args[1])
			assert.Equal(t, "Bypass", args[2])
			assert.Equal(t, "-Command", args[3])
			assert.Contains(t, args[4], "Invoke-RestMethod")
			// The installer URL must be embedded directly in the script
			// (single-quoted) rather than passed as a positional argument,
			// because PowerShell's -Command does not bind trailing args to
			// $args reliably. Regression test for #448.
			assert.Contains(t, args[4], "'https://example.com/install.ps1'")
			assert.NotContains(t, args[4], "$args")
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

func TestUpdateCommand_WindowsEmbedsURLInsteadOfPassingAsArg(t *testing.T) {
	// Regression test for https://github.com/microsoft/waza/issues/448.
	//
	// Previously, the Windows update command passed the installer URL as a
	// trailing positional argument to `pwsh -Command "... $args[0] ..."`.
	// PowerShell's -Command does not bind trailing positional arguments to
	// $args inside the script block (unlike -File), which caused
	// Invoke-RestMethod to be invoked with a null/empty -Uri and fail.
	//
	// The fix embeds the URL directly in the -Command script as a
	// single-quoted PowerShell literal, so no argument binding is required.
	installer, err := installerForOS("windows", updateCommandOptions{
		PowerShellInstallerURL: "https://example.com/install.ps1",
		ExecutablePath:         "C:/tools/waza.exe",
	})
	require.NoError(t, err)

	// The final args slice must NOT include the URL as a trailing positional
	// argument, and the script must NOT reference $args.
	require.Len(t, installer.Args, 5)
	for _, arg := range installer.Args {
		assert.NotEqual(t, "https://example.com/install.ps1", arg,
			"URL must not be passed as a trailing positional argument (see #448)")
	}

	script := installer.Args[4]
	assert.NotContains(t, script, "$args", "script must not depend on $args (see #448)")
	assert.Contains(t, script, "Invoke-RestMethod -Uri 'https://example.com/install.ps1'",
		"URL must be embedded as a single-quoted literal in the script")
}

func TestUpdateCommand_WindowsEscapesSingleQuotesInURL(t *testing.T) {
	// Defense in depth: if the installer URL ever contains a single quote,
	// it must be escaped by doubling so it can't break out of the
	// single-quoted PowerShell literal.
	installer, err := installerForOS("windows", updateCommandOptions{
		PowerShellInstallerURL: "https://example.com/inst'all.ps1",
	})
	require.NoError(t, err)

	require.Len(t, installer.Args, 5)
	script := installer.Args[4]
	assert.Contains(t, script, "'https://example.com/inst''all.ps1'")
}

func TestQuotePowerShellSingleQuoted(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain URL", "https://example.com/install.ps1", "'https://example.com/install.ps1'"},
		{"empty", "", "''"},
		{"single quote is doubled", "a'b", "'a''b'"},
		{"multiple single quotes", "'x'y'", "'''x''y'''"},
		{"no interpolation of dollar sign", "$env:foo", "'$env:foo'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, quotePowerShellSingleQuoted(tc.in))
		})
	}
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
