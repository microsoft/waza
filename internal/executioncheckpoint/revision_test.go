package executioncheckpoint

import (
	"errors"
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildRevisionAdmission(t *testing.T) {
	revision := strings.Repeat("a", 40)
	valid := func() *debug.BuildInfo {
		return &debug.BuildInfo{
			Path: "github.com/microsoft/waza/cmd/waza", Main: debug.Module{Path: "github.com/microsoft/waza"},
			Settings: []debug.BuildSetting{
				{Key: "vcs", Value: "git"},
				{Key: "vcs.revision", Value: revision},
				{Key: "vcs.modified", Value: "false"},
			},
		}
	}
	got, err := runnerRevision(valid())
	if err != nil || got != revision {
		t.Fatalf("valid owning CLI metadata: %q, %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*debug.BuildInfo)
	}{
		{"foreign-module", func(info *debug.BuildInfo) { info.Main.Path = "example.com/other" }},
		{"library-build", func(info *debug.BuildInfo) { info.Path = "github.com/microsoft/waza/internal/runnerunit" }},
		{"missing-vcs", func(info *debug.BuildInfo) { info.Settings = info.Settings[1:] }},
		{"missing-revision", func(info *debug.BuildInfo) { info.Settings = append(info.Settings[:1], info.Settings[2:]...) }},
		{"missing-dirty-flag", func(info *debug.BuildInfo) { info.Settings = info.Settings[:2] }},
		{"dirty", func(info *debug.BuildInfo) { info.Settings[2].Value = "true" }},
		{"invalid-dirty-flag", func(info *debug.BuildInfo) { info.Settings[2].Value = "False" }},
		{"non-git", func(info *debug.BuildInfo) { info.Settings[0].Value = "hg" }},
		{"empty-revision", func(info *debug.BuildInfo) { info.Settings[1].Value = "" }},
		{"version-fallback", func(info *debug.BuildInfo) { info.Settings[1].Value = "0.41.0" }},
		{"uppercase", func(info *debug.BuildInfo) { info.Settings[1].Value = strings.Repeat("A", 40) }},
		{"trailing-newline", func(info *debug.BuildInfo) { info.Settings[1].Value += "\n" }},
		{"non-ascii", func(info *debug.BuildInfo) { info.Settings[1].Value = strings.Repeat("a", 39) + "é" }},
		{"non-hex", func(info *debug.BuildInfo) { info.Settings[1].Value = strings.Repeat("a", 39) + "g" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := valid()
			tc.mutate(info)
			if got, err := runnerRevision(info); got != "" || !errors.Is(err, ErrIncompatible) {
				t.Fatalf("invalid metadata accepted or cause lost: %q, %v", got, err)
			}
		})
	}
	for _, key := range []string{"vcs", "vcs.revision", "vcs.modified"} {
		info := valid()
		info.Settings = append(info.Settings, debug.BuildSetting{Key: key, Value: ""})
		if got, err := runnerRevision(info); got != "" || !errors.Is(err, ErrIncompatible) {
			t.Fatalf("ambiguous duplicate %s accepted: %q, %v", key, got, err)
		}
	}
	if got, err := runnerRevision(nil); got != "" || !errors.Is(err, ErrIncompatible) {
		t.Fatalf("nil build metadata accepted: %q, %v", got, err)
	}
}

func TestCurrentRevisionUsesActualBuildMetadata(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	got, err := CurrentRunnerRevision()
	if !ok {
		if got != "" || !errors.Is(err, ErrIncompatible) {
			t.Fatalf("missing actual metadata accepted: %q, %v", got, err)
		}
		return
	}
	expected, expectedErr := runnerRevision(info)
	if got != expected || (err == nil) != (expectedErr == nil) {
		t.Fatalf("actual build metadata replaced by a fallback: %q, %v; expected %q, %v", got, err, expected, expectedErr)
	}
}
