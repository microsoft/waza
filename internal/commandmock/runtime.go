package commandmock

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	rootEnv       = "WAZA_COMMAND_MOCK_ROOT"
	executableEnv = "WAZA_COMMAND_MOCK_EXECUTABLE"
)

var runtimeState struct {
	sync.Mutex
	root       string
	shimDir    string
	executable string
}

var shimMu sync.Mutex

// RuntimeEnvironment returns an environment for the Copilot child process that
// puts the command-mock shims before, but does not replace, the host PATH.
func RuntimeEnvironment() ([]string, error) {
	runtimeState.Lock()
	defer runtimeState.Unlock()

	if runtimeState.root == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locating waza executable for command mocks: %w", err)
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return nil, fmt.Errorf("resolving waza executable for command mocks: %w", err)
		}
		root, err := os.MkdirTemp("", "waza-command-mocks-*")
		if err != nil {
			return nil, fmt.Errorf("creating command-mock runtime directory: %w", err)
		}
		shimDir := filepath.Join(root, "bin")
		if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("creating command-mock runtime directories: %w", err)
		}
		if err := os.MkdirAll(shimDir, 0700); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("creating command-mock shim directory: %w", err)
		}
		runtimeState.root = root
		runtimeState.shimDir = shimDir
		runtimeState.executable = executable
	}

	env := append([]string(nil), os.Environ()...)
	path := os.Getenv("PATH")
	setEnv(&env, "PATH", runtimeState.shimDir+string(os.PathListSeparator)+path)
	setEnv(&env, rootEnv, runtimeState.root)
	setEnv(&env, executableEnv, runtimeState.executable)
	return env, nil
}

// CloseRuntime removes the temporary shim registry after the Copilot runtime
// has stopped.
func CloseRuntime() error {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	if runtimeState.root == "" {
		return nil
	}
	err := os.RemoveAll(runtimeState.root)
	runtimeState.root = ""
	runtimeState.shimDir = ""
	runtimeState.executable = ""
	return err
}

func runtimePaths() (root, shimDir string, err error) {
	if _, err = RuntimeEnvironment(); err != nil {
		return "", "", err
	}
	runtimeState.Lock()
	defer runtimeState.Unlock()
	return runtimeState.root, runtimeState.shimDir, nil
}

func setEnv(env *[]string, key, value string) {
	for i, item := range *env {
		name, _, ok := strings.Cut(item, "=")
		if ok && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			(*env)[i] = name + "=" + value
			return
		}
	}
	*env = append(*env, key+"="+value)
}

func shimPath(dir, name string) string {
	return shimPathFor(runtime.GOOS, dir, name)
}

func shimPathFor(goos, dir, name string) string {
	if goos == "windows" {
		name += ".cmd"
	}
	return filepath.Join(dir, name)
}

func renderShim(name string) string {
	return renderShimFor(runtime.GOOS, name)
}

func renderShimFor(goos, name string) string {
	if goos == "windows" {
		return "@echo off\r\n\"%WAZA_COMMAND_MOCK_EXECUTABLE%\" __command-mock --root \"%WAZA_COMMAND_MOCK_ROOT%\" --name \"" +
			name + "\" -- %*\r\n@exit /b %ERRORLEVEL%\r\n"
	}
	return "#!/bin/sh\nexec \"$WAZA_COMMAND_MOCK_EXECUTABLE\" __command-mock --root \"$WAZA_COMMAND_MOCK_ROOT\" --name \"" +
		name + "\" -- \"$@\"\n"
}

func ensureShim(name, dir string) error {
	shimMu.Lock()
	defer shimMu.Unlock()
	path := shimPath(dir, name)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	mode := os.FileMode(0600)
	if runtime.GOOS != "windows" {
		mode = 0700
	}
	file, err := os.CreateTemp(dir, ".shim-*")
	if err != nil {
		return fmt.Errorf("creating command mock shim for %q: %w", name, err)
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return fmt.Errorf("setting command mock shim permissions for %q: %w", name, err)
	}
	if _, err := file.WriteString(renderShim(name)); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing command mock shim for %q: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing command mock shim for %q: %w", name, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("writing command mock shim for %q: %w", name, err)
	}
	return nil
}
