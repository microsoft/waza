package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type host struct {
	OS, Arch string
	Getenv   func(string) string
	// Translated reports whether this process runs under binary translation.
	Translated func() (bool, error)
}

func currentHost() host {
	return host{OS: runtime.GOOS, Arch: runtime.GOARCH, Getenv: os.Getenv, Translated: darwinTranslated}
}

var runnerOS = map[string]string{"Linux": "linux", "macOS": "darwin", "Windows": "windows"}
var runnerArch = map[string]string{"X64": "amd64", "ARM64": "arm64"}

// requireNativeHost rejects cross-architecture and emulated execution. The
// checker is compiled for the host by the runner's Go toolchain, so its own
// GOOS/GOARCH, the runner-reported platform and (on macOS) the translation
// state must all name the target.
func requireNativeHost(p platform, h host) error {
	if h.OS != p.OS || h.Arch != p.Arch {
		return fmt.Errorf("host %s/%s is not native for %s", h.OS, h.Arch, p.target())
	}
	if h.Getenv("GITHUB_ACTIONS") == "true" {
		osName, arch := runnerOS[h.Getenv("RUNNER_OS")], runnerArch[h.Getenv("RUNNER_ARCH")]
		if osName != p.OS || arch != p.Arch {
			return fmt.Errorf("runner reports %q/%q, not native %s", h.Getenv("RUNNER_OS"), h.Getenv("RUNNER_ARCH"), p.target())
		}
	}
	if p.OS == "darwin" {
		translated, err := h.Translated()
		if err != nil {
			return fmt.Errorf("cannot prove native macOS execution: %w", err)
		}
		if translated {
			return fmt.Errorf("host runs under Rosetta translation, not native %s", p.target())
		}
	}
	return nil
}

func darwinTranslated() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", "sysctl.proc_translated").Output()
	if err != nil {
		return false, fmt.Errorf("sysctl.proc_translated: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	}
	return false, fmt.Errorf("unexpected sysctl.proc_translated value %q", out)
}

// verifyBinary binds the executable to its declared target and exact clean
// source through both the object header and embedded Go build metadata.
func verifyBinary(path string, p platform, sourceSHA string) error {
	goos, arch, err := objectTarget(path)
	if err != nil {
		return err
	}
	if goos != p.OS || arch != p.Arch {
		return fmt.Errorf("%s header is %s/%s, want %s", path, goos, arch, p.target())
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s build info: %w", path, err)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != p.OS || settings["GOARCH"] != p.Arch {
		return fmt.Errorf("%s build info targets %s/%s, want %s", path, settings["GOOS"], settings["GOARCH"], p.target())
	}
	if settings["vcs.revision"] != sourceSHA || settings["vcs.modified"] != "false" {
		return fmt.Errorf("%s build info revision %q modified %q, want clean %s", path, settings["vcs.revision"], settings["vcs.modified"], sourceSHA)
	}
	return nil
}

func objectTarget(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return "", "", fmt.Errorf("%s: read header: %w", path, err)
	}
	switch {
	case bytes.Equal(magic, []byte(elf.ELFMAG)):
		file, err := elf.NewFile(f)
		if err != nil {
			return "", "", err
		}
		return "linux", map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[file.Machine], nil
	case magic[0] == 'M' && magic[1] == 'Z':
		file, err := pe.NewFile(f)
		if err != nil {
			return "", "", err
		}
		return "windows", map[uint16]string{pe.IMAGE_FILE_MACHINE_AMD64: "amd64", pe.IMAGE_FILE_MACHINE_ARM64: "arm64"}[file.Machine], nil
	}
	file, err := macho.NewFile(f)
	if err != nil {
		var formatErr *macho.FormatError
		if errors.As(err, &formatErr) {
			return "", "", fmt.Errorf("%s is not an ELF, PE or thin Mach-O executable", path)
		}
		return "", "", err
	}
	return "darwin", map[macho.Cpu]string{macho.CpuAmd64: "amd64", macho.CpuArm64: "arm64"}[file.Cpu], nil
}
