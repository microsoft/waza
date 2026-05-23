package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	versionpkg "github.com/microsoft/waza/internal/version"
	"github.com/spf13/cobra"
)

const latestReleaseURL = "https://github.com/microsoft/waza/releases/latest"

type updateCommandOptions struct {
	BashInstallerURL       string
	PowerShellInstallerURL string
	GOOS                   string
	ExecutablePath         string
	LookPath               func(string) (string, error)
	RunCommand             func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error
}

type updateInstaller struct {
	Name       string
	ScriptURL  string
	Candidates []string
	Requires   []string
	Args       []string
	Env        []string
	Async      bool
}

func newUpdateCommand() *cobra.Command {
	return newUpdateCommandWithOptions(nil)
}

func newUpdateCommandWithOptions(options *updateCommandOptions) *cobra.Command {
	opts := normalizeUpdateCommandOptions(options)
	var yes bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update waza to the latest release",
		Long: `Update waza to the latest release.

This command downloads and runs the official installer for your OS:
  macOS/Linux: Bash installer
  Windows: PowerShell installer

The selected installer detects the architecture for the current environment,
downloads the matching release asset, verifies its checksum, and updates the
waza binary.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdateCommand(cmd, opts, yes)
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Run the update without prompting for confirmation")

	return cmd
}

func normalizeUpdateCommandOptions(options *updateCommandOptions) updateCommandOptions {
	opts := updateCommandOptions{
		BashInstallerURL:       versionpkg.BashInstallScriptURL,
		PowerShellInstallerURL: versionpkg.PowerShellInstallScriptURL,
		GOOS:                   runtime.GOOS,
		LookPath:               exec.LookPath,
		RunCommand: func(ctx context.Context, name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
			cmd := exec.CommandContext(ctx, name, args...)
			if len(env) > 0 {
				cmd.Env = append(os.Environ(), env...)
			}
			cmd.Stdin = stdin
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			return cmd.Run()
		},
	}
	if options == nil {
		if executablePath, err := os.Executable(); err == nil {
			opts.ExecutablePath = executablePath
		}
		return opts
	}
	if options.BashInstallerURL != "" {
		opts.BashInstallerURL = options.BashInstallerURL
	}
	if options.PowerShellInstallerURL != "" {
		opts.PowerShellInstallerURL = options.PowerShellInstallerURL
	}
	if options.GOOS != "" {
		opts.GOOS = options.GOOS
	}
	if options.ExecutablePath != "" {
		opts.ExecutablePath = options.ExecutablePath
	} else if executablePath, err := os.Executable(); err == nil {
		opts.ExecutablePath = executablePath
	}
	if options.LookPath != nil {
		opts.LookPath = options.LookPath
	}
	if options.RunCommand != nil {
		opts.RunCommand = options.RunCommand
	}
	return opts
}

func runUpdateCommand(cmd *cobra.Command, opts updateCommandOptions, yes bool) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	installer, err := installerForOS(opts.GOOS, opts)
	if err != nil {
		return err
	}

	if !yes {
		ok, err := confirmUpdate(cmd.InOrStdin(), out, installer)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := fmt.Fprintln(out, "Update canceled."); err != nil {
				return err
			}
			return nil
		}
	}

	commandPath, err := lookPathAny(opts.LookPath, installer.Candidates)
	if err != nil {
		return missingInstallerError(installer)
	}
	for _, dependency := range installer.Requires {
		if _, err := opts.LookPath(dependency); err != nil {
			return missingDependencyError(dependency)
		}
	}

	if _, err := fmt.Fprintf(out, "Updating waza with the %s installer...\n", installer.Name); err != nil {
		return err
	}
	if err := opts.RunCommand(cmd.Context(), commandPath, installer.Args, installer.Env, cmd.InOrStdin(), out, errOut); err != nil {
		return fmt.Errorf("running waza installer: %w", err)
	}

	if installer.Async {
		if _, err := fmt.Fprintln(out, "Update started. The installer will finish after this waza process exits."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(out, "Update complete."); err != nil {
			return err
		}
	}
	return nil
}

func installerForOS(goos string, opts updateCommandOptions) (updateInstaller, error) {
	switch goos {
	case "darwin", "linux":
		return updateInstaller{
			Name:       "Bash",
			ScriptURL:  opts.BashInstallerURL,
			Candidates: []string{"bash"},
			Requires:   []string{"curl"},
			Args:       []string{"-c", `set -euo pipefail; curl -fsSL "$1" | bash`, "waza-installer", opts.BashInstallerURL},
		}, nil
	case "windows":
		script := `$ErrorActionPreference = 'Stop'; Invoke-Expression (Invoke-RestMethod -Uri $args[0])`
		env := []string{fmt.Sprintf("WAZA_UPDATE_PARENT_PID=%d", os.Getpid())}
		if opts.ExecutablePath != "" {
			env = append(env, fmt.Sprintf("WAZA_INSTALL_DIR=%s", filepath.Dir(opts.ExecutablePath)))
		}
		return updateInstaller{
			Name:       "PowerShell",
			ScriptURL:  opts.PowerShellInstallerURL,
			Candidates: []string{"pwsh", "powershell"},
			Args:       []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script, opts.PowerShellInstallerURL},
			Env:        env,
			Async:      true,
		}, nil
	default:
		return updateInstaller{}, fmt.Errorf("unsupported OS for waza update: %s", goos)
	}
}

func lookPathAny(lookPath func(string) (string, error), names []string) (string, error) {
	var errs []error
	for _, name := range names {
		path, err := lookPath(name)
		if err == nil {
			return path, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
	}
	return "", errors.Join(errs...)
}

func confirmUpdate(in io.Reader, out io.Writer, installer updateInstaller) (bool, error) {
	if _, err := fmt.Fprintf(out, "waza update will download and run the official %s installer:\n  %s\n\nContinue? [y/N]: ", installer.Name, installer.ScriptURL); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func missingInstallerError(installer updateInstaller) error {
	if installer.Name == "PowerShell" {
		return fmt.Errorf("PowerShell is required to run the native Windows waza installer; install PowerShell or download the native Windows binary from %s", latestReleaseURL)
	}
	return fmt.Errorf("bash is required to run the waza installer; install bash or download a release binary from %s", latestReleaseURL)
}

func missingDependencyError(name string) error {
	if name == "curl" {
		return fmt.Errorf("curl is required to download the waza installer; install curl or download a release binary from %s", latestReleaseURL)
	}
	return fmt.Errorf("%s is required to run the waza installer", name)
}
