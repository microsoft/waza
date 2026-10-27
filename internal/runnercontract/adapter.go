package runnercontract

import (
	"encoding/json"
	"fmt"
)

// AdaptLocal captures exactly the caller-named eval and task sources. It never
// expands eval.tasks or CSV rows. The caller must choose a single unit explicitly.
// Local single-unit mock runs also use orchestration's frozen request boundary.
// Unsupported historical semantics remain usable through the legacy commands.
func AdaptLocal(eval, task []byte, runKey string, selection Selection, timeoutMS int) (*Request, error) {
	request := Request{
		SchemaVersion: RequestVersion, Versions: []string{RequestVersion},
		RequiredCapabilities: []string{"exact-unit", "inline.mock-text", "terminal-result"}, OptionalCapabilities: []string{},
		RunKey: runKey, Selection: selection, TimeoutMS: timeoutMS,
		Input: Input{Shard: selection.Shard, Eval: string(eval), Task: string(task)},
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := Parse(data); err != nil {
		return nil, fmt.Errorf("cannot adapt local input: %w", err)
	}
	if _, err := admit(&request); err != nil {
		return nil, fmt.Errorf("cannot preserve local semantics in runner 1.0: %w; keep using waza run, or select one task/trial/attempt and inline its prompt/resources, disable ambient skills, and use builtin text checks before retrying waza runner adapt", err)
	}
	return &request, nil
}
