package runnercontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// digest uses Go's deterministic JSON object ordering, UTF-8 and integer decimal
// encoding over fixed structures. No resolved host paths, timestamps or process IDs enter.
func digest(namespace string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(IdentityVersion+"\x00"+namespace+"\x00"), data...))
	return hex.EncodeToString(sum[:]), nil
}

func derive(req *Request, eval, task any) (*Identity, error) {
	run, err := digest("run", []any{req.RunKey, req.TimeoutMS, eval})
	if err != nil {
		return nil, err
	}
	shard, err := digest("shard", []any{run, req.Selection.Shard})
	if err != nil {
		return nil, err
	}
	taskID, err := digest("task", []any{shard, req.Selection.Task, task})
	if err != nil {
		return nil, err
	}
	trial, err := digest("trial", []any{taskID, req.Selection.Trial})
	if err != nil {
		return nil, err
	}
	attempt, err := digest("attempt", []any{trial, req.Selection.Attempt})
	if err != nil {
		return nil, err
	}
	canonical, err := digest("input", []any{run, shard, taskID, trial, attempt, req.TimeoutMS})
	if err != nil {
		return nil, err
	}
	return &Identity{Version: IdentityVersion, CanonicalDigest: canonical,
		Run: "run-" + run, Shard: "shard-" + shard, Task: "task-" + taskID,
		Trial: "trial-" + trial, Attempt: "attempt-" + attempt}, nil
}
