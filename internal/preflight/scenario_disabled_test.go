package preflight

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectExplicitScenarioDisabledDiscoveryIgnoresUnusedDirectories(t *testing.T) {
	eval := strings.ReplaceAll(validEval, `schemaVersion: "1.4"`, `schemaVersion: "2.0"`)
	eval = strings.ReplaceAll(eval, "skill: example", "scenario: local-workflow\nskill: example")
	eval = strings.ReplaceAll(eval, "  model: harness", "  model: harness\n  skill_directories: [absent-unused-directory]")
	path, _ := writeSuite(t, eval, validTask+"\nskill_directories: []\n")
	r := Inspect(path, Options{})
	require.False(t, r.Failed(true), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "skills.discovery_disabled", Verified))
	require.False(t, hasDiagnostic(r, "skill.directory", Invalid))
	require.False(t, hasDiagnostic(r, "boundary.requested_policy", Unresolved))
}
