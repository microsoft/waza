package dev

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/workspace"
)

func resolveSkillDir(arg string, cfg *projectconfig.ProjectConfig) (string, error) {
	if arg == "" {
		return "", errors.New("skill name or path required")
	}

	var nameErr error
	if !workspace.LooksLikePath(arg) {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
		ctx, err := workspace.DetectContext(wd, workspace.WithSkillsDir(cfg.Paths.Skills), workspace.WithEvalsDir(cfg.Paths.Evals), workspace.WithEvalFile(cfg.Files.EvalFile))
		if err != nil {
			return "", fmt.Errorf("detecting workspace: %w", err)
		}
		si, err := workspace.FindSkill(ctx, arg)
		if err == nil {
			return si.Dir, nil
		}
		nameErr = err
	}

	path, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("resolving skill target %q: %w", arg, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if nameErr != nil && errors.Is(err, os.ErrNotExist) {
			return "", nameErr
		}
		return "", fmt.Errorf("inspecting skill target %q: %w", arg, err)
	}
	if info.IsDir() {
		return path, nil
	}
	if filepath.Base(path) == "SKILL.md" && info.Mode().IsRegular() {
		return filepath.Dir(path), nil
	}
	return "", fmt.Errorf("skill target %q must be a directory or SKILL.md file", arg)
}
