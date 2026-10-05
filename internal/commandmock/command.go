package commandmock

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var sensitiveArgPattern = regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9_]{20,}|sk-(?:proj-)?[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|bearer\s+[A-Za-z0-9._\-+/=]{16,})`)

const redactedArg = "[REDACTED]"

func sensitiveName(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	for _, suffix := range []string{
		"secret", "token", "password", "passwd", "credential", "credentials",
		"key", "auth", "authorization", "sig", "signature", "connection",
		"connection-string", "connectionstring", "apikey", "accesskey",
		"privatekey", "clientsecret", "clientpassword", "accesstoken", "refreshtoken",
	} {
		if name == suffix || strings.HasSuffix(name, "-"+suffix) {
			return true
		}
	}
	return false
}

func sanitizeArgs(args []string, env []string) []string {
	secretValues := make([]string, 0)
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || value == "" {
			continue
		}
		if sensitiveName(key) {
			secretValues = append(secretValues, value)
		}
	}

	out := make([]string, len(args))
	hideNext := false
	for i, arg := range args {
		if hideNext {
			out[i] = redactedArg
			hideNext = false
			continue
		}
		key, _, hasValue := strings.Cut(arg, "=")
		sensitiveKey := strings.HasPrefix(key, "-") && sensitiveName(strings.TrimLeft(key, "-"))
		if sensitiveKey && !hasValue {
			out[i] = redactedArg
			hideNext = true
			continue
		}
		value := arg
		if sensitiveKey && hasValue {
			value = key + "=" + redactedArg
		} else {
			for _, secret := range secretValues {
				value = strings.ReplaceAll(value, secret, redactedArg)
			}
			value = sensitiveArgPattern.ReplaceAllString(value, redactedArg)
		}
		out[i] = value
	}
	return out
}

// RunCommand is the entry point for the private executable shims.
func RunCommand(root, name string, args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "waza command mock: cannot determine working directory")
		return 127
	}
	result, err := Invoke(root, os.Getenv(SessionEnvironmentVariable), name, args, cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "waza command mock: failed to load task configuration")
		return 127
	}
	if result.PassThrough {
		return runHostCommand(root, name, args)
	}
	if len(result.Stdout) > 0 {
		if _, err := os.Stdout.Write(result.Stdout); err != nil {
			return 1
		}
	}
	if result.Stderr != "" {
		_, _ = fmt.Fprint(os.Stderr, result.Stderr)
	}
	return result.ExitCode
}

func runHostCommand(root, name string, args []string) int {
	shimDir := filepath.Join(root, "bin")
	hostDirs := make([]string, 0)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" && !samePath(dir, shimDir) {
			hostDirs = append(hostDirs, dir)
		}
	}
	path := strings.Join(hostDirs, string(os.PathListSeparator))
	program := findHostExecutable(name, hostDirs)
	if program == "" {
		fmt.Fprintf(os.Stderr, "waza command mock: no fixture for %q and no host executable was found\n", name)
		return 127
	}
	cmd := exec.Command(program, args...)
	cmd.Env = hostCommandEnvironment(os.Environ(), path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "waza command mock: running host command %q failed\n", name)
		return 127
	}
	return 0
}

func findHostExecutable(name string, dirs []string) string {
	names := []string{name}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		extensions := strings.Split(os.Getenv("PATHEXT"), ";")
		if len(extensions) == 1 && extensions[0] == "" {
			extensions = []string{".COM", ".EXE", ".BAT", ".CMD"}
		}
		for _, ext := range extensions {
			names = append(names, name+strings.ToLower(ext), name+strings.ToUpper(ext))
		}
	}
	for _, dir := range dirs {
		for _, candidate := range names {
			path := filepath.Join(dir, candidate)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS == "windows" || info.Mode()&0111 != 0 {
				return path
			}
		}
	}
	return ""
}

func samePath(left, right string) bool {
	left, _ = filepath.Abs(left)
	right, _ = filepath.Abs(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func hostCommandEnvironment(env []string, path string) []string {
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "WAZA_COMMAND_MOCK_") {
			out = append(out, item)
		}
	}
	setEnv(&out, "PATH", path)
	return out
}
