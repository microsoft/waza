package snapshot

import (
	"os"
	"sort"
	"strings"
)

// CaptureEnv returns the captured-env section of a snapshot under the
// default-deny / allow-list policy described in the issue.
//
// Rules:
//   - When allowList is empty, no env values are captured and DeniedKeys
//     is left empty so the snapshot does not leak the full set of
//     environment variable names from the host (which can be large,
//     unstable across machines, and occasionally sensitive on its own).
//   - When allowList is non-empty, variables that do not match are added
//     to DeniedKeys for auditing. This makes the allow-list explicit
//     without enumerating every unrelated key from the host.
//   - When allowList contains a name, that variable's value is captured.
//     If the policy marks the key as sensitive (IsSensitiveKey), the value
//     is replaced with RedactionPlaceholder; otherwise the value is run
//     through the redaction policy's regex rules.
//   - allowList entries may end with `*` to match prefixes (e.g. `WAZA_*`).
//
// The returned SnapshotEnv is always non-nil; absent fields are nil/empty.
func CaptureEnv(allowList []string, policy *Policy) SnapshotEnv {
	return captureEnvFrom(os.Environ(), allowList, policy)
}

func captureEnvFrom(environ []string, allowList []string, policy *Policy) SnapshotEnv {
	allow := normaliseAllowList(allowList)
	captured := map[string]string{}
	var denied []string
	recordDenied := len(allow) > 0

	for _, e := range environ {
		idx := strings.IndexByte(e, '=')
		if idx <= 0 {
			continue
		}
		key, val := e[:idx], e[idx+1:]
		if !allowList_match(allow, key) {
			if recordDenied {
				denied = append(denied, key)
			}
			continue
		}
		switch {
		case policy.IsSensitiveKey(key):
			captured[key] = RedactionPlaceholder
		default:
			captured[key] = policy.RedactString(val)
		}
	}
	sort.Strings(denied)

	out := SnapshotEnv{
		AllowList: append([]string(nil), allowList...),
	}
	if len(captured) > 0 {
		out.Captured = captured
	}
	if len(denied) > 0 {
		out.DeniedKeys = denied
	}
	return out
}

type allowEntry struct {
	exact  string
	prefix string // empty if exact
}

func normaliseAllowList(in []string) []allowEntry {
	out := make([]allowEntry, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.HasSuffix(e, "*") {
			out = append(out, allowEntry{prefix: strings.TrimSuffix(e, "*")})
		} else {
			out = append(out, allowEntry{exact: e})
		}
	}
	return out
}

// allowList_match (snake_case to avoid clashing with the AllowList field
// name in SnapshotEnv when reading code grep-style).
func allowList_match(entries []allowEntry, key string) bool {
	for _, e := range entries {
		if e.exact != "" && e.exact == key {
			return true
		}
		if e.prefix != "" && strings.HasPrefix(key, e.prefix) {
			return true
		}
	}
	return false
}
