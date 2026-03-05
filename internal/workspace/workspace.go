// Package workspace provides unified skill workspace detection for waza commands.
// It analyzes directory structures to identify single-skill or multi-skill workspaces
// and locates eval.yaml files using a priority-based search.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/utils"
)

// ContextType represents the type of workspace detected.
type ContextType int

const (
	ContextNone        ContextType = iota
	ContextSingleSkill             // CWD is inside a single skill directory
	ContextMultiSkill              // Workspace contains multiple skills
)

// maxParentWalk is the maximum number of parent directories to walk up when searching.
const maxParentWalk = 10

// DetectOption configures workspace detection behavior.
type DetectOption func(*detectOptions)

type detectOptions struct {
	skillsDir string // subdirectory name for skills (default "skills")
	evalsDir  string // subdirectory name for evals (default "evals")
}

func defaultDetectOptions() detectOptions {
	return detectOptions{skillsDir: "skills", evalsDir: "evals"}
}

// WithSkillsDir overrides the skills subdirectory name used during detection.
func WithSkillsDir(dir string) DetectOption {
	return func(o *detectOptions) {
		if dir != "" {
			o.skillsDir = dir
		}
	}
}

// WithEvalsDir overrides the evals subdirectory name used during detection.
func WithEvalsDir(dir string) DetectOption {
	return func(o *detectOptions) {
		if dir != "" {
			o.evalsDir = dir
		}
	}
}

// SkillInfo holds information about a discovered skill.
type SkillInfo struct {
	Name      string // skill name from SKILL.md frontmatter
	Dir       string // absolute path to the skill directory (containing SKILL.md)
	SkillPath string // absolute path to SKILL.md
	EvalPath  string // absolute path to eval.yaml (empty if not found)
}

// WorkspaceContext represents the detected workspace.
type WorkspaceContext struct {
	Type     ContextType
	Root     string      // workspace root directory
	Skills   []SkillInfo // discovered skills
	EvalsDir string      // configured evals subdirectory name (default "evals")
}

// DetectContext analyzes the given directory to determine workspace type.
// It checks:
// 1. CWD for SKILL.md → single-skill
// 2. Walk up parents for SKILL.md → single-skill (nested inside skill dir)
// 3. Check for skills/ directory with SKILL.md children → multi-skill
// 4. Scan CWD for child dirs containing SKILL.md → multi-skill
func DetectContext(dir string, opts ...DetectOption) (*WorkspaceContext, error) {
	o := defaultDetectOptions()
	for _, fn := range opts {
		fn(&o)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute path: %w", err)
	}

	// 1. Check if SKILL.md exists in the given directory
	if info, ok := tryParseSkill(absDir); ok {
		return &WorkspaceContext{
			Type:     ContextSingleSkill,
			Root:     absDir,
			Skills:   []SkillInfo{info},
			EvalsDir: o.evalsDir,
		}, nil
	}

	// 2. Walk up parent directories looking for SKILL.md
	current := absDir
	for i := 0; i < maxParentWalk; i++ {
		parent := filepath.Dir(current)
		if parent == current {
			break // reached filesystem root
		}
		current = parent

		if info, ok := tryParseSkill(current); ok {
			return &WorkspaceContext{
				Type:     ContextSingleSkill,
				Root:     current,
				Skills:   []SkillInfo{info},
				EvalsDir: o.evalsDir,
			}, nil
		}
	}

	// 3. Check for configured skills subdirectory with SKILL.md children
	skillsDir := filepath.Join(absDir, o.skillsDir)
	var skills []SkillInfo
	if isDir(skillsDir) {
		skills = scanForSkills(skillsDir)
	}

	// 3b. Also check .github/skills/ directory (GitHub Copilot convention)
	githubSkillsDir := filepath.Join(absDir, ".github", "skills")
	if isDir(githubSkillsDir) && !samePath(skillsDir, githubSkillsDir) {
		githubSkills := scanForSkills(githubSkillsDir)
		skills = mergeSkillsByName(skills, githubSkills)
	}

	if len(skills) > 0 {
		return &WorkspaceContext{
			Type:     ContextMultiSkill,
			Root:     absDir,
			Skills:   skills,
			EvalsDir: o.evalsDir,
		}, nil
	}

	// 4. Scan immediate children of dir for SKILL.md
	skills = scanForSkills(absDir)
	if len(skills) > 0 {
		return &WorkspaceContext{
			Type:     ContextMultiSkill,
			Root:     absDir,
			Skills:   skills,
			EvalsDir: o.evalsDir,
		}, nil
	}

	// Nothing found
	return &WorkspaceContext{
		Type:     ContextNone,
		Root:     absDir,
		Skills:   nil,
		EvalsDir: o.evalsDir,
	}, nil
}

// FindSkill locates a named skill in the workspace.
func FindSkill(ctx *WorkspaceContext, name string) (*SkillInfo, error) {
	for i := range ctx.Skills {
		if ctx.Skills[i].Name == name {
			return &ctx.Skills[i], nil
		}
	}
	return nil, fmt.Errorf("skill %q not found in workspace", name)
}

// FindEval finds eval.yaml for a skill using priority order:
// 1. {root}/evals/{skill-name}/eval.yaml  (separated convention)
// 2. {skill-dir}/evals/eval.yaml          (nested subdir)
// 3. {skill-dir}/eval.yaml                (co-located/legacy)
// Returns empty string if none found (not an error).
func FindEval(ctx *WorkspaceContext, skillName string) (string, error) {
	si, err := FindSkill(ctx, skillName)
	if err != nil {
		return "", err
	}

	evalsDir := ctx.EvalsDir
	if evalsDir == "" {
		evalsDir = "evals"
	}

	// Priority 1: separated convention
	separated := filepath.Join(ctx.Root, evalsDir, skillName, "eval.yaml")
	if isFile(separated) {
		return separated, nil
	}

	// Priority 2: nested subdir inside skill directory
	nested := filepath.Join(si.Dir, "evals", "eval.yaml")
	if isFile(nested) {
		return nested, nil
	}

	// Priority 3: co-located / legacy
	colocated := filepath.Join(si.Dir, "eval.yaml")
	if isFile(colocated) {
		return colocated, nil
	}

	return "", nil
}

// tryParseSkill checks if dir contains SKILL.md and parses it.
func tryParseSkill(dir string) (SkillInfo, bool) {
	skillPath := filepath.Join(dir, "SKILL.md")
	if !isFile(skillPath) {
		return SkillInfo{}, false
	}

	name, err := parseSkillName(skillPath)
	if err != nil || name == "" {
		// Fall back to directory name when frontmatter is missing/invalid
		name = filepath.Base(dir)
	}

	return SkillInfo{
		Name:      name,
		Dir:       dir,
		SkillPath: skillPath,
	}, true
}

// scanForSkills scans immediate child directories of parentDir for SKILL.md files.
func scanForSkills(parentDir string) []SkillInfo {
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil
	}

	var skills []SkillInfo
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		childDir := filepath.Join(parentDir, entry.Name())
		if info, ok := tryParseSkill(childDir); ok {
			skills = append(skills, info)
		}
	}
	return skills
}

// parseSkillName reads a SKILL.md file and extracts the skill name from frontmatter.
func parseSkillName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading skill file: %w", err)
	}

	var s skill.Skill
	if err := s.UnmarshalText(data); err != nil {
		return "", fmt.Errorf("parsing SKILL.md: %w", err)
	}

	return strings.TrimSpace(s.Frontmatter.Name), nil
}

// isFile returns true if path exists and is a regular file.
func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// isDir returns true if path exists and is a directory.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func samePath(a, b string) bool {
	resolve := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return filepath.Clean(p)
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		return filepath.Clean(abs)
	}

	aResolved := resolve(a)
	bResolved := resolve(b)
	if os.PathSeparator == '\\' {
		return strings.EqualFold(aResolved, bResolved)
	}
	return aResolved == bResolved
}

func mergeSkillsByName(base, additional []SkillInfo) []SkillInfo {
	return utils.MergeByKey(base, additional, func(s SkillInfo) string {
		return s.Name
	})
}

// LooksLikePath returns true if the string appears to be a file path
// rather than a skill name. Exported so that CLI packages (cmd/waza,
// cmd/waza/dev) can share the same heuristic without duplication.
func LooksLikePath(s string) bool {
	return strings.ContainsAny(s, `/\`) ||
		filepath.Ext(s) != "" ||
		s == "."
}
