package main

import "fmt"

// Evidence classes for a distributed target. Only "native" produces execution
// evidence; every other class is reported as such and never as a pass.
const (
	evidenceNative      = "native"
	evidenceUnavailable = "unavailable"
)

type platform struct {
	OS        string
	Arch      string
	Runner    string
	Evidence  string
	Extension bool
	Reason    string
}

func (p platform) target() string { return p.OS + "/" + p.Arch }

// platforms is the supported distribution matrix. The native smoke workflow
// matrix must list exactly the native entries with the same runner labels.
// A target whose native runner becomes unavailable moves to
// evidenceUnavailable with a reason; it is then reported as unavailable, not
// as tested.
var platforms = []platform{
	{OS: "linux", Arch: "amd64", Runner: "ubuntu-latest", Evidence: evidenceNative, Extension: true},
	{OS: "linux", Arch: "arm64", Runner: "ubuntu-24.04-arm", Evidence: evidenceNative},
	{OS: "darwin", Arch: "amd64", Runner: "macos-15-intel", Evidence: evidenceNative},
	{OS: "darwin", Arch: "arm64", Runner: "macos-latest", Evidence: evidenceNative, Extension: true},
	{OS: "windows", Arch: "amd64", Runner: "windows-latest", Evidence: evidenceNative, Extension: true},
	{OS: "windows", Arch: "arm64", Runner: "windows-11-arm", Evidence: evidenceNative},
}

func findPlatform(target string) (platform, error) {
	for _, p := range platforms {
		if p.target() == target {
			return p, nil
		}
	}
	return platform{}, fmt.Errorf("unsupported distribution target %q", target)
}

func cliBinaryName(p platform) string {
	name := "waza-" + p.OS + "-" + p.Arch
	if p.OS == "windows" {
		name += ".exe"
	}
	return name
}

func extensionEntryPoint(p platform) string {
	name := "microsoft-azd-waza-" + p.OS + "-" + p.Arch
	if p.OS == "windows" {
		name += ".exe"
	}
	return name
}

func extensionArchiveName(p platform) string {
	if p.OS == "linux" {
		return "microsoft-azd-waza-" + p.OS + "-" + p.Arch + ".tar.gz"
	}
	return "microsoft-azd-waza-" + p.OS + "-" + p.Arch + ".zip"
}
