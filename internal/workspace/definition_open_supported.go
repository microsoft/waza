//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

package workspace

import "github.com/microsoft/waza/internal/rootedfile"

var openSkillDefinition = rootedfile.OpenPath
