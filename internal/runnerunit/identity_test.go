package runnerunit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/stretchr/testify/require"
)

func TestProducerFrozenIdentities(t *testing.T) {
	data, err := os.ReadFile("testdata/identities.json")
	require.NoError(t, err)
	sum := sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))
	require.Equal(t, "fea761f0270c22dbf3a79760b8877bfac32ab8e14022f7d058401e149e9e88b0", hex.EncodeToString(sum[:]),
		"independent producer oracle changed; review ABI rather than regenerating expectations")
	var cases []struct {
		Name     string                  `json:"name"`
		Request  runnercontract.Request  `json:"request"`
		Identity runneridentity.Identity `json:"identity"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	require.Len(t, cases, 11)
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			request := wire(t, tc.Request)
			ready := Validate(request)
			require.Equal(t, "ready", ready.Status, "%+v", ready.Diagnostics)
			require.Equal(t, &tc.Identity, ready.Identity)
			factories := 0
			report := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
				factories++
				// Identity and prepared work must be frozen before caller-owned
				// factory code mutates the original wire buffer.
				clear(request)
				return execution.NewMockEngine(cfg.ModelID), nil
			}).Run(t.Context(), request)
			require.Equal(t, 1, factories)
			require.Equal(t, "completed", report.Status, "%+v", report.Diagnostics)
			require.Equal(t, &tc.Identity, report.Identity)
			require.Equal(t, 1, report.Calls)
			requireReceiptSchema(t, ready)
			requireReceiptSchema(t, report)
			encoded, err := json.Marshal(report)
			require.NoError(t, err)
			var saved Report
			require.NoError(t, json.Unmarshal(encoded, &saved))
			require.Equal(t, &tc.Identity, saved.Identity)
		})
	}
}

func TestIdentitySchemaPresenceAndNearMisses(t *testing.T) {
	schema := receiptSchema(t)
	report := Validate(wire(t, fixture(t)))
	require.NotNil(t, report.Identity)
	for _, control := range []string{"historical-absent", "null", "partial", "wrong-version", "short-digest", "uppercase", "wrong-prefix", "extra-field", "rejected-identity", "digest-newline", "attempt-newline"} {
		t.Run(control, func(t *testing.T) {
			var fields map[string]any
			require.NoError(t, json.Unmarshal(mustEncodeReport(t, report), &fields))
			require.NoError(t, schema.Validate(fields))
			identity, ok := fields["identity"].(map[string]any)
			require.True(t, ok)
			switch control {
			case "historical-absent":
				delete(fields, "identity")
			case "null":
				fields["identity"] = nil
			case "partial":
				delete(identity, "attempt")
			case "wrong-version":
				identity["version"] = "sha256-json-v1"
			case "short-digest":
				identity["canonical_digest"] = report.Identity.CanonicalDigest[:63]
			case "uppercase":
				identity["canonical_digest"] = strings.ToUpper(report.Identity.CanonicalDigest)
			case "wrong-prefix":
				identity["attempt"] = report.Identity.Trial
			case "extra-field":
				identity["verified"] = true
			case "digest-newline":
				identity["canonical_digest"] = report.Identity.CanonicalDigest + "\n"
			case "attempt-newline":
				identity["attempt"] = report.Identity.Attempt + "\n"
			case "rejected-identity":
				fields["status"], fields["validation"] = "rejected", "invalid"
				delete(fields, "resolved")
			}
			if control == "historical-absent" {
				require.NoError(t, schema.Validate(fields), "old receipts stay readable without invented identity")
			} else {
				require.Error(t, schema.Validate(fields))
			}
		})
	}
}

func TestProducerPreservesAuthoredPresence(t *testing.T) {
	base := fixture(t)
	base.Selection.Attempt = 1
	base.Input.Eval = strings.Replace(base.Input.Eval, "  max_attempts: 3\n", "", 1)
	var identities []*runneridentity.Identity
	for _, source := range []string{"", "skill_directories: null\n", "skill_directories: []\n"} {
		request := base
		request.Input.Task += source
		report := NewService(nil).Run(t.Context(), wire(t, request))
		require.Equal(t, "completed", report.Status, "%+v", report.Diagnostics)
		require.NotNil(t, report.Identity)
		require.Equal(t, 1, report.Calls)
		for _, prior := range identities {
			require.Equal(t, prior.Run, report.Identity.Run)
			require.NotEqual(t, prior.Task, report.Identity.Task, "omitted/null/empty must survive native defaulting")
		}
		identities = append(identities, report.Identity)
	}
	for _, explicit := range []string{"", "  max_attempts: 0\n", "  max_attempts: 1\n"} {
		request := base
		request.Input.Eval = strings.Replace(request.Input.Eval, "  trials_per_task: 3\n", "  trials_per_task: 3\n"+explicit, 1)
		report := Validate(wire(t, request))
		require.Equal(t, "ready", report.Status, "%+v", report.Diagnostics)
		require.NotNil(t, report.Identity)
		if explicit != "" {
			require.NotEqual(t, identities[0].Run, report.Identity.Run, "equivalent execution defaults do not erase authored values")
		}
	}
}

func TestCanonicalSupportDoesNotWidenAdmission(t *testing.T) {
	for _, source := range []string{
		"unknown: 0.0\n", "timeout_seconds: 1.0\n", "unknown: !!int 9999999999999999999999999999\n",
	} {
		t.Run(source, func(t *testing.T) {
			request := fixture(t)
			request.Input.Task += source
			_, err := runneridentity.Derive(&request)
			require.NoError(t, err, "canonical support is intentionally broader than execution")
			report := NewService(func(models.Config) (execution.AgentEngine, error) {
				t.Fatal("unadmitted canonical input constructed an engine")
				return nil, nil
			}).Run(t.Context(), wire(t, request))
			require.Equal(t, "rejected", report.Status)
			require.Nil(t, report.Identity)
			require.Zero(t, report.Calls)
			require.NotContains(t, string(mustEncodeReport(t, report)), `"identity"`)
		})
	}
	// Physical source CRLF normalizes, escaped content CRLF does not.
	a := fixture(t)
	b := fixture(t)
	a.Input.Task = strings.Replace(a.Input.Task, "prompt: hello", `prompt: "hello\nworld"`, 1)
	b.Input.Task = strings.Replace(b.Input.Task, "prompt: hello", `prompt: "hello\r\nworld"`, 1)
	first, second := Validate(wire(t, a)), Validate(wire(t, b))
	require.Equal(t, "ready", first.Status)
	require.Equal(t, "ready", second.Status)
	require.Equal(t, first.Identity.Run, second.Identity.Run)
	require.NotEqual(t, first.Identity.Task, second.Identity.Task)
}

func mustEncodeReport(t *testing.T, report Report) []byte {
	t.Helper()
	data, err := json.Marshal(report)
	require.NoError(t, err)
	return data
}
