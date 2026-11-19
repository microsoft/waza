package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvidenceManifestCloneDetachesPrivateAndExportedState(t *testing.T) {
	sdk, model, noSkills := "sdk", "model", false
	valid := `{"version":"1.0","sha256":"291a3cecd450615b3f14df209e47565f668f0dad298eb69d746a30398353db42",
		"origin":{"eval_id":"eval","task_id":"task","run_number":1,"attempt_count":1,"prior_attempts":"none"},
		"runtime":{"waza_version":"","requested_engine":"","requested_model":"","execution_mode":"unknown",
		"dependency_mode":"unknown","requested_policy":"unknown","verified_enforcement":"unknown","go_version":"go1.26.4",
		"platform":"darwin/arm64","sdk_version":"sdk","effective_model_version":"model","no_skills":false,"native_skill_control":"unknown"},
		"redaction":{"policy":"default","applied_rules":["rule"],"match_count":0,"limitations":["limit"]},
		"artifacts":[{"id":"prompt","kind":"prompt","availability":"captured","completeness":"unknown","document":"snapshot","pointer":"/prompt",
		"content_digest":{"sha256":"15c53b41755b7dbbf631697798b043b1eb429674afb2580b605d468c7f8593b7","encoding":"json-v1"},
		"source_digest":{"sha256":"8c344b52ce41cfafde001b467a33a045b6a3e9c0de931832e673e64eacb05501","encoding":"json-v1"},
		"reason":"Collector completeness is not certified."}],
		"diagnostics":[{"category":"capture","code":"code","message":"message"}]`
	for name, data := range map[string]string{"valid": valid + `}`, "unknown-raw": valid + `,"future":true}`} {
		t.Run(name, func(t *testing.T) {
			var original EvidenceManifest
			require.NoError(t, json.Unmarshal([]byte(data), &original))
			require.Equal(t, name == "unknown-raw", original.HasUnknownFields())
			require.Equal(t, name == "unknown-raw", len(original.unknownRaw) > 0)
			wire, err := json.Marshal(original)
			require.NoError(t, err)

			clone := original.Clone()
			require.Equal(t, &original, clone)
			require.Equal(t, original.HasUnknownFields(), clone.HasUnknownFields())
			require.Equal(t, original.HasInvalidSchema(), clone.HasInvalidSchema())
			require.Equal(t, original.HasInvalidRequiredObjects(), clone.HasInvalidRequiredObjects())

			*clone.Runtime.SDKVersion = "mutated"
			*clone.Runtime.EffectiveModelVersion = "mutated"
			*clone.Runtime.NoSkills = true
			clone.Redaction.AppliedRules[0] = "mutated"
			clone.Redaction.Limitations[0] = "mutated"
			clone.Artifacts[0].ID = "mutated"
			clone.Artifacts[0].ContentDigest.SHA256 = "mutated"
			clone.Artifacts[0].SourceDigest.SHA256 = "mutated"
			clone.Diagnostics[0].Code = "mutated"
			if len(clone.unknownRaw) > 0 {
				clone.unknownRaw[0] = '!'
			}

			require.Equal(t, sdk, *original.Runtime.SDKVersion)
			require.Equal(t, model, *original.Runtime.EffectiveModelVersion)
			require.Equal(t, noSkills, *original.Runtime.NoSkills)
			after, err := json.Marshal(original)
			require.NoError(t, err)
			require.Equal(t, string(wire), string(after))
		})
	}
	require.Nil(t, (*EvidenceManifest)(nil).Clone())
}
