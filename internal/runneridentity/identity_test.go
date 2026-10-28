package runneridentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type vector struct {
	Name          string                 `json:"name"`
	Profile       string                 `json:"profile"`
	Request       runnercontract.Request `json:"request"`
	CanonicalEval string                 `json:"canonical_eval"`
	CanonicalTask string                 `json:"canonical_task"`
	Identity      Identity               `json:"identity"`
}

func vectors(t *testing.T) []vector {
	t.Helper()
	data, err := os.ReadFile("testdata/vectors.json")
	require.NoError(t, err)
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	sum := sha256.Sum256(normalized)
	require.Equal(t, "3acf83109f3939bed296b005d9802e5bd67c8c6730c6894a3bd411e408c738ae", hex.EncodeToString(sum[:]),
		"frozen independent oracle changed; review ABI rather than regenerating expectations")
	var cases []vector
	require.NoError(t, json.Unmarshal(data, &cases))
	require.Len(t, cases, 29)
	return cases
}

func TestFrozenConformance(t *testing.T) {
	for _, tc := range vectors(t) {
		t.Run(tc.Name, func(t *testing.T) {
			eval, err := CanonicalSource(tc.Request.Input.Eval)
			require.NoError(t, err)
			require.Equal(t, tc.CanonicalEval, string(eval))
			task, err := CanonicalSource(tc.Request.Input.Task)
			require.NoError(t, err)
			require.Equal(t, tc.CanonicalTask, string(task))
			got, err := Derive(&tc.Request)
			require.NoError(t, err)
			require.Equal(t, tc.Identity, *got)
			data, err := json.Marshal(tc.Request)
			require.NoError(t, err)
			fromWire, err := DeriveJSON(data)
			require.NoError(t, err)
			require.Equal(t, got, fromWire)
			for _, id := range []string{got.Run, got.Shard, got.Task, got.Trial, got.Attempt} {
				prefix, hash, found := strings.Cut(id, "-")
				require.True(t, found)
				require.Contains(t, []string{"run", "shard", "task", "trial", "attempt"}, prefix)
				require.Len(t, hash, 64)
				_, err := hex.DecodeString(hash)
				require.NoError(t, err)
			}
		})
	}
}

func TestFrozenHierarchyBoundaries(t *testing.T) {
	cases := vectors(t)
	base := cases[0].Identity
	same := map[string]int{
		"baseline": 5, "presentation-equivalent": 5, "negotiation-only": 5,
		"run-key": 0, "timeout": 0, "eval-source": 0, "shard": 1,
		"task-source": 2, "task-key": 2, "trial": 3, "attempt": 4,
	}
	fields := func(id Identity) []string { return []string{id.Run, id.Shard, id.Task, id.Trial, id.Attempt} }
	seen := map[string]string{}
	for _, tc := range cases {
		wantSame, exists := same[tc.Name]
		if !exists {
			wantSame = 2
		}
		for i, value := range fields(tc.Identity) {
			if i < wantSame {
				require.Equal(t, fields(base)[i], value, tc.Name)
			} else {
				require.NotEqual(t, fields(base)[i], value, tc.Name)
			}
		}
		if wantSame == 5 {
			require.Equal(t, base.CanonicalDigest, tc.Identity.CanonicalDigest)
		} else {
			require.NotEqual(t, base.CanonicalDigest, tc.Identity.CanonicalDigest)
			require.NotContains(t, seen, tc.Identity.CanonicalDigest, tc.Name)
			seen[tc.Identity.CanonicalDigest] = tc.Name
		}
	}
}

func TestCanonicalEquivalentAndDistinctSources(t *testing.T) {
	for _, group := range [][]string{
		{"{v: 3}", "{v: +3}", "{v: 0x3}", "{v: 0o3}", "{v: 0b11}", "{v: 03}"},
		{"{v: 1.25}", "{v: 12.50e-1}", "{v: !!float 125e-2}"},
		{"{v: true}", "{v: TRUE}", "{v: True}"},
		{"{v: null}", "{v: ~}", "{v: }"},
		{"v: |\n  a\n  b\n", "v: |\r\n  a\r\n  b\r\n"},
		{`{v: "\U0001f600"}`, "{v: \"\U0001f600\"}"},
	} {
		first, err := CanonicalSource(group[0])
		require.NoError(t, err)
		for _, source := range group[1:] {
			got, err := CanonicalSource(source)
			require.NoError(t, err, source)
			require.Equal(t, string(first), string(got), source)
		}
	}
	for _, pair := range [][2]string{
		{`{v: 0}`, `{v: "0"}`},
		{`{v: false}`, `{v: "false"}`},
		{`{"a": 1, "b": 2}`, `{"a": 12, "b": null}`},
		{`{v: !!float 1.00000000000000000001}`, `{v: !!float 1.00000000000000000002}`},
		{`{v: !!int 9999999999999999999999999999}`, `{v: !!int 9999999999999999999999999998}`},
	} {
		a, err := CanonicalSource(pair[0])
		require.NoError(t, err)
		b, err := CanonicalSource(pair[1])
		require.NoError(t, err)
		require.NotEqual(t, a, b)
	}
	encoded, err := CanonicalSource(`{"\U00010000": a, "\uE000": b}`)
	require.NoError(t, err)
	require.Equal(t, "m2:s3:\ue000s1:bs4:\U00010000s1:a", string(encoded), "keys sort by UTF-8 bytes, not UTF-16")
}

func TestCanonicalRejectsAmbiguity(t *testing.T) {
	for _, source := range []string{
		"", "null", "[]", "a: [", "a: 1\n---\na: 2", "a: 1\n---\n[",
		"a: 1\na: 2", `{"a": 1, "\u0061": 2}`,
		"a: &anchor 1\nb: *anchor", "{a: &unused 1}", "{&key a: 1}",
		"{<<: {a: 1}}", "{1: a}", "{? [a, b]: 1}",
		"{a: .inf}", "{a: -.Inf}", "{a: .NaN}", "{a: !!float nope}",
		"{a: !!int nope}", "{a: !!bool nope}", "{a: !!null nope}",
		"{a: 2026-10-28}", "{a: !custom value}", "!custom {a: 1}", "{a: !custom []}",
		"a: \"\xff\"", `a: "\uD800"`,
		"a: " + strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
		strings.Repeat(" ", runnercontract.MaxRequestBytes+1),
	} {
		t.Run(fmt.Sprintf("%.70q", source), func(t *testing.T) {
			got, err := CanonicalSource(source)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestRequestRejectionAndPurity(t *testing.T) {
	base := vectors(t)[0].Request
	data, err := json.Marshal(base)
	require.NoError(t, err)
	for _, field := range []string{"run_key", "timeout_ms", "allow_paid_execution", "selection", "input"} {
		for _, value := range []string{"omitted", "null", `""`, "0", "false"} {
			if field == "allow_paid_execution" && value == "false" {
				continue
			}
			t.Run(field+"/"+value, func(t *testing.T) {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(data, &fields))
				if value == "omitted" {
					delete(fields, field)
				} else {
					fields[field] = json.RawMessage(value)
				}
				invalid, err := json.Marshal(fields)
				require.NoError(t, err)
				id, err := DeriveJSON(invalid)
				require.Error(t, err)
				require.Nil(t, id)
			})
		}
	}
	for _, invalid := range []string{
		strings.Replace(string(data), `"run_key":`, `"run_key":"duplicate","run_key":`, 1),
		strings.Replace(string(data), `"run_key":`, `"run_\u006bey":"duplicate","run_key":`, 1),
		string(data) + "{}",
		strings.Replace(string(data), `"attempt":1`, `"attempt":2147483648`, 1),
	} {
		_, err := DeriveJSON([]byte(invalid))
		require.Error(t, err)
	}
	_, err = Derive(nil)
	require.ErrorContains(t, err, "nil")
	for _, mutate := range []func(*runnercontract.Request){
		func(r *runnercontract.Request) { r.Selection.Trial = 0 },
		func(r *runnercontract.Request) { r.Selection.Attempt = -1 },
		func(r *runnercontract.Request) { r.Selection.Shard = "different" },
		func(r *runnercontract.Request) { r.Input.Eval = "[]" },
		func(r *runnercontract.Request) { r.Input.Task = "[]" },
		func(r *runnercontract.Request) { r.RunKey = "\xff" },
		func(r *runnercontract.Request) { r.Input.Eval = "name: \"\xff\"" },
		func(r *runnercontract.Request) { r.Input.Task = "name: \"\xff\"" },
	} {
		request := base
		mutate(&request)
		id, err := Derive(&request)
		require.Error(t, err)
		require.Nil(t, id)
	}
	first, err := Derive(&base)
	require.NoError(t, err)
	first.Run = "caller-mutated"
	second, err := Derive(&base)
	require.NoError(t, err)
	require.Equal(t, vectors(t)[0].Identity, *second)
	after, err := json.Marshal(base)
	require.NoError(t, err)
	require.Equal(t, data, after, "derivation must not default or mutate caller input")
}

func TestIdentityCrossProcess(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range vectors(t) {
		for _, zone := range []string{"UTC", "Pacific/Auckland"} {
			t.Run(tc.Name+"/"+zone, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, executable, "-test.run=^TestIdentityProcess$")
				command.Dir = t.TempDir()
				command.Env = slices.DeleteFunc(os.Environ(), func(value string) bool {
					key, _, _ := strings.Cut(value, "=")
					return slices.Contains([]string{"WAZA_IDENTITY_CHILD", "TZ", "HOME", "USERPROFILE", "COPILOT_CLI_PATH"}, key)
				})
				command.Env = append(command.Env, "WAZA_IDENTITY_CHILD=1", "TZ="+zone,
					"HOME="+command.Dir, "USERPROFILE="+command.Dir, "COPILOT_CLI_PATH=identity-must-not-launch-a-runtime")
				data, err := json.Marshal(tc.Request)
				require.NoError(t, err)
				command.Stdin = bytes.NewReader(data)
				output, err := command.CombinedOutput()
				require.NoError(t, err, string(output))
				var got Identity
				require.NoError(t, json.Unmarshal(output, &got), string(output))
				require.Equal(t, tc.Identity, got)
			})
		}
	}
}

func TestIdentityProcess(t *testing.T) {
	if os.Getenv("WAZA_IDENTITY_CHILD") != "1" {
		return
	}
	data, err := io.ReadAll(os.Stdin)
	require.NoError(t, err)
	id, err := DeriveJSON(data)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(id))
	os.Exit(0)
}

func TestDigestNamespaceAndFraming(t *testing.T) {
	require.NotEqual(t, digest("run", "ab", "c"), digest("run", "a", "bc"))
	require.NotEqual(t, digest("run", "same"), digest("task", "same"))
	require.NotEqual(t, digest("run", "a\x00b"), digest("run\x00a", "b"))
	var payload bytes.Buffer
	payload.WriteString(Version + "\x00run\x00a1:s4:same")
	sum := sha256.Sum256(payload.Bytes())
	require.Equal(t, hex.EncodeToString(sum[:]), digest("run", "same"))
}

func TestRequestFieldInventoryRequiresIdentityDecision(t *testing.T) {
	requestType := reflect.TypeFor[runnercontract.Request]()
	var fields []string
	for field := range requestType.Fields() {
		fields = append(fields, field.Name)
	}
	require.Equal(t, []string{
		"SchemaVersion", "Versions", "RequiredCapabilities", "OptionalCapabilities",
		"RunKey", "Selection", "Input", "TimeoutMS", "AllowPaidExecution",
	}, fields, "new request fields need an explicit identity stability decision")
}

func TestEveryAuthoredLeafAffectsItsHierarchy(t *testing.T) {
	base := vectors(t)[0].Request
	original, err := Derive(&base)
	require.NoError(t, err)
	for _, scope := range []string{"eval", "task"} {
		source := base.Input.Eval
		if scope == "task" {
			source = base.Input.Task
		}
		var doc yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(source), &doc))
		var leaves []*yaml.Node
		var visit func(*yaml.Node)
		visit = func(node *yaml.Node) {
			switch node.Kind {
			case yaml.ScalarNode:
				leaves = append(leaves, node)
			case yaml.MappingNode:
				for i := 1; i < len(node.Content); i += 2 {
					visit(node.Content[i])
				}
			default:
				for _, child := range node.Content {
					visit(child)
				}
			}
		}
		visit(&doc)
		require.NotEmpty(t, leaves)
		for i, leaf := range leaves {
			t.Run(fmt.Sprintf("%s/%d", scope, i), func(t *testing.T) {
				old := leaf.Value
				if leaf.Tag == "!!int" {
					leaf.Value = "7"
				} else {
					leaf.Value += "-changed"
				}
				mutated, err := yaml.Marshal(&doc)
				require.NoError(t, err)
				leaf.Value = old
				require.NotEqual(t, source, string(mutated))
				request := base
				if scope == "eval" {
					request.Input.Eval = string(mutated)
				} else {
					request.Input.Task = string(mutated)
				}
				got, err := Derive(&request)
				require.NoError(t, err)
				require.NotEqual(t, original.Task, got.Task)
				require.NotEqual(t, original.Trial, got.Trial)
				require.NotEqual(t, original.Attempt, got.Attempt)
				require.NotEqual(t, original.CanonicalDigest, got.CanonicalDigest)
				if scope == "eval" {
					require.NotEqual(t, original.Run, got.Run)
					require.NotEqual(t, original.Shard, got.Shard)
				} else {
					require.Equal(t, original.Run, got.Run)
					require.Equal(t, original.Shard, got.Shard)
				}
			})
		}
	}
}
