package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/workspace"
)

type projectWorkspaceDetectPaths struct {
	configDir string
	skillsDir string
	evalsDir  string
	evalFile  string
}

func loadProjectWorkspaceDetectPaths(root string) (projectWorkspaceDetectPaths, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return projectWorkspaceDetectPaths{}, fmt.Errorf("resolving workspace path: %w", err)
	}
	cfg, err := projectconfig.Load(absRoot)
	if err != nil {
		return projectWorkspaceDetectPaths{}, fmt.Errorf("loading workspace configuration: %w", err)
	}

	resolveDir := func(dir string) string {
		if filepath.IsAbs(dir) {
			return filepath.Clean(dir)
		}
		// Without a config file, let the detector anchor defaults to its own root.
		return filepath.Join(cfg.Dir, dir)
	}

	return projectWorkspaceDetectPaths{
		configDir: cfg.Dir,
		skillsDir: resolveDir(cfg.Paths.Skills),
		evalsDir:  resolveDir(cfg.Paths.Evals),
		evalFile:  cfg.Files.EvalFile,
	}, nil
}

func (p projectWorkspaceDetectPaths) options() []workspace.DetectOption {
	return []workspace.DetectOption{
		workspace.WithSkillsDir(p.skillsDir),
		workspace.WithEvalsDir(p.evalsDir),
		workspace.WithEvalFile(p.evalFile),
	}
}

// configDetectOptions returns workspace DetectOptions derived from project config.
func configDetectOptions() []workspace.DetectOption {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	paths, err := loadProjectWorkspaceDetectPaths(wd)
	if err != nil {
		return nil
	}
	return paths.options()
}

// resolveWorkspace uses workspace detection to resolve skills from CLI args.
// When a skill name is given, ctx.Skills is narrowed to that single skill.
// Behavior:
//   - Explicit path to a file (e.g. eval.yaml) → returns context with all skills (caller uses path directly)
//   - Skill name arg + workspace → returns context with that single skill
//   - No args + single-skill workspace → returns context with that skill
//   - No args + multi-skill workspace → returns context with all skills
//   - No workspace detected → returns error
func resolveWorkspace(args []string) (*workspace.WorkspaceContext, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	ctx, err := workspace.DetectContext(wd, configDetectOptions()...)
	if err != nil {
		return nil, fmt.Errorf("detecting workspace: %w", err)
	}

	if len(args) > 0 {
		arg := args[0]
		if workspace.LooksLikePath(arg) {
			return ctx, nil
		}
		if ctx.Type == workspace.ContextNone {
			return nil, fmt.Errorf("no workspace detected and %q is not a file path", arg)
		}
		si, err := workspace.FindSkill(ctx, arg)
		if err != nil {
			return nil, err
		}
		ctx.Skills = []workspace.SkillInfo{*si}
		return ctx, nil
	}

	switch ctx.Type {
	case workspace.ContextSingleSkill, workspace.ContextMultiSkill:
		return ctx, nil
	default:
		return nil, fmt.Errorf("no skills detected in workspace; provide a path or skill name")
	}
}

// resolveEvalPath finds eval.yaml for a skill using workspace detection.
func resolveEvalPath(si *workspace.SkillInfo) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}
	wsCtx, err := workspace.DetectContext(wd, configDetectOptions()...)
	if err != nil {
		return "", fmt.Errorf("detecting workspace: %w", err)
	}
	// Ensure the skill is in the context so FindEval can locate it
	found := false
	for _, s := range wsCtx.Skills {
		if s.Name == si.Name {
			found = true
			break
		}
	}
	if !found {
		wsCtx.Skills = append(wsCtx.Skills, *si)
	}
	evalPath, err := workspace.FindEval(wsCtx, si.Name)
	if err != nil {
		return "", err
	}
	if evalPath == "" {
		return "", fmt.Errorf("no eval file found for skill %q", si.Name)
	}
	return evalPath, nil
}
