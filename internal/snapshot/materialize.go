package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

// VerifyWorkspace admits only explicitly required, complete, unchanged files.
// Subset capture never proves absence or recreates the entire original workspace.
func VerifyWorkspace(snap *Snapshot, origin models.EvidenceOrigin, manifestSHA string, required []string) error {
	if snap == nil || snap.Evidence == nil {
		return errors.New("snapshot: preserved workspace evidence is unavailable; hashes alone cannot reconstruct files")
	}
	if err := evidence.ValidateNative(snap.Evidence); err != nil {
		return err
	}
	if manifestSHA == "" || manifestSHA != snap.Evidence.SHA256 || origin != snap.Evidence.Origin {
		return errors.New("snapshot: selected evidence does not match the result's eval/task/run/attempt identity")
	}
	if origin.EvalID == "" || origin.TaskID == "" || origin.RunNumber < 1 || origin.AttemptCount < 1 ||
		snap.EvalID != origin.EvalID || snap.Task.TestID != origin.TaskID || snap.Task.RunNumber != origin.RunNumber {
		return errors.New("snapshot: preserved state has incomplete or inconsistent execution attribution")
	}
	if len(required) == 0 {
		return errors.New("snapshot: explicitly required workspace files are needed")
	}
	seen := make(map[string]bool)
	for _, file := range snap.WorkspaceFiles {
		if workspacePathProblem(file.Path, nil, DefaultPolicy()) != nil || seen[file.Path] {
			return errors.New("snapshot: invalid or duplicated preserved file identity")
		}
		seen[file.Path] = true
	}
	for _, name := range required {
		if workspacePathProblem(name, nil, DefaultPolicy()) != nil {
			return errors.New("snapshot: required workspace path is unsafe or unsupported")
		}
		found := false
		for index, file := range snap.WorkspaceFiles {
			if file.Path != name {
				continue
			}
			found = true
			reference, err := evidence.Reference(snap.Evidence, "workspace-file/"+name)
			if err != nil {
				return err
			}
			artifact, err := evidence.Resolve(snap.Evidence, reference)
			if err != nil {
				return err
			}
			if artifact.Pointer != fmt.Sprintf("/workspaceFiles/%d", index) {
				return errors.New("snapshot: preserved file locator is inconsistent")
			}
			if len(snap.rawWorkspaceFiles) > 0 {
				if index >= len(snap.rawWorkspaceFiles) {
					return errors.New("snapshot: preserved file bytes are unavailable")
				}
				if err := evidence.VerifyContent(snap.Evidence, reference, snap.rawWorkspaceFiles[index], true, true); err != nil {
					return err
				}
			}
			data, err := json.Marshal(file)
			if err != nil {
				return errors.New("snapshot: preserved file metadata cannot be encoded")
			}
			if err := evidence.VerifyContent(snap.Evidence, reference, data, true, true); err != nil {
				return err
			}
			if file.Redacted || !utf8.ValidString(file.Content) || shaBytes([]byte(file.Content)) != file.SHA256 {
				return errors.New("snapshot: preserved file was changed or has inconsistent content identity")
			}
		}
		if !found {
			return errors.New("snapshot: a required file was not preserved; use the actual agent workspace")
		}
	}
	return nil
}

type MaterializedWorkspace struct {
	dir     string
	cleanup func() error
}

func (workspace *MaterializedWorkspace) Path() string {
	return workspace.dir
}

func (workspace *MaterializedWorkspace) Close() error {
	if workspace == nil || workspace.dir == "" {
		return nil
	}
	cleanup := workspace.cleanup
	workspace.dir = ""
	workspace.cleanup = nil
	if cleanup == nil {
		return errors.New("snapshot: secure materialized workspace cleanup is unavailable")
	}
	return cleanup()
}

// MaterializeWorkspace writes verified required files only to a private new
// temporary directory. Callers must close it; no source tree is changed.
func MaterializeWorkspace(snap *Snapshot, origin models.EvidenceOrigin, manifestSHA string, required []string) (*MaterializedWorkspace, error) {
	if err := VerifyWorkspace(snap, origin, manifestSHA, required); err != nil {
		return nil, err
	}
	return materializeVerifiedWorkspace(snap, required)
}
