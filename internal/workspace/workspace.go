// Package workspace provides unified skill workspace detection for waza commands.
// It analyzes directory structures to identify single-skill or multi-skill workspaces
// and locates eval files using a priority-based search.
package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/projectconfig"
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
	evalFile  string // eval filename (default "eval.yaml")
}

func defaultDetectOptions() detectOptions {
	return detectOptions{
		skillsDir: projectconfig.DefaultSkillsDir,
		evalsDir:  projectconfig.DefaultEvalsDir,
		evalFile:  projectconfig.DefaultEvalFile,
	}
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

// WithEvalFile overrides the eval filename used during discovery.
func WithEvalFile(filename string) DetectOption {
	return func(o *detectOptions) {
		if filename != "" {
			o.evalFile = filename
		}
	}
}

// SkillInfo holds information about a discovered skill.
type SkillInfo struct {
	Name      string // skill name from SKILL.md frontmatter
	Dir       string // absolute path to the skill directory (containing SKILL.md)
	SkillPath string // absolute path to SKILL.md
	EvalPath  string // absolute path to the eval file (empty if not found)
	SourceDir string // absolute path to the source skill directory when SKILL.md is compiled elsewhere
}

// WorkspaceContext represents the detected workspace.
type WorkspaceContext struct {
	Type     ContextType
	Root     string      // workspace root directory
	Skills   []SkillInfo // discovered skills
	EvalsDir string      // configured evals subdirectory name (default "evals")
	EvalFile string      // configured eval filename (default "eval.yaml")
}

// DetectContext analyzes the given directory to determine workspace type.
// It checks:
// 1. CWD for SKILL.md → single-skill
// 2. Walk up parents for SKILL.md → single-skill (nested inside skill dir)
// 3. Check known compiled skill output directories such as .apm/skills/
// 4. Check for skills/ directory with SKILL.md descendants → multi-skill
// 5. Scan CWD for child dirs containing SKILL.md → multi-skill
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
			EvalFile: o.evalFile,
		}, nil
	}

	// 2. Walk up parent directories looking for SKILL.md or known compiled outputs
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
				EvalFile: o.evalFile,
			}, nil
		}
		if skills, err := scanDirectAPMSkills(current); err != nil {
			return nil, fmt.Errorf("scanning APM skills directory %s: %w", filepath.Join(current, ".apm", "skills"), err)
		} else if len(skills) > 0 {
			return contextFromSkills(current, skills, o), nil
		}
	}

	// 3. Check for configured skills subdirectory with SKILL.md children
	var skillsDir string

	// in some situations we have an absolute path to the skillsDir (for instance, from .waza.yaml)
	// and it'd be incorrect to use a relative path.
	if filepath.IsAbs(o.skillsDir) {
		skillsDir = o.skillsDir
	} else {
		skillsDir = filepath.Join(absDir, o.skillsDir)
	}

	var skills []SkillInfo
	if isDir(skillsDir) {
		skills, err = scanForSkills(skillsDir, true)
		if err != nil {
			return nil, fmt.Errorf("scanning skills directory %s: %w", skillsDir, err)
		}
	}

	// 3b. Also check .github/skills/ directory (GitHub Copilot convention)
	githubSkillsDir := filepath.Join(absDir, ".github", "skills")
	if isDir(githubSkillsDir) && !samePath(skillsDir, githubSkillsDir) {
		githubSkills, err := scanForSkills(githubSkillsDir, true)
		if err != nil {
			return nil, fmt.Errorf("scanning GitHub skills directory %s: %w", githubSkillsDir, err)
		}
		skills = mergeSkillsByName(skills, githubSkills)
	}

	if isDir(skillsDir) {
		apmSkills, err := scanForAPMSkillsUnder(skillsDir)
		if err != nil {
			return nil, fmt.Errorf("scanning APM skills under %s: %w", skillsDir, err)
		}
		skills = mergeSkillsByName(skills, apmSkills)
	}

	rootAPMSkills, err := scanDirectAPMSkills(absDir)
	if err != nil {
		return nil, fmt.Errorf("scanning APM skills directory %s: %w", filepath.Join(absDir, ".apm", "skills"), err)
	}
	skills = mergeSkillsByName(skills, rootAPMSkills)

	if len(skills) > 0 {
		return &WorkspaceContext{
			Type:     ContextMultiSkill,
			Root:     absDir,
			Skills:   skills,
			EvalsDir: o.evalsDir,
			EvalFile: o.evalFile,
		}, nil
	}

	// 4. Scan immediate children of dir for SKILL.md
	skills, err = scanForSkills(absDir, false)
	if err != nil {
		return nil, fmt.Errorf("scanning directory %s: %w", absDir, err)
	}
	if len(skills) > 0 {
		return &WorkspaceContext{
			Type:     ContextMultiSkill,
			Root:     absDir,
			Skills:   skills,
			EvalsDir: o.evalsDir,
			EvalFile: o.evalFile,
		}, nil
	}

	// Nothing found
	return &WorkspaceContext{
		Type:     ContextNone,
		Root:     absDir,
		Skills:   nil,
		EvalsDir: o.evalsDir,
		EvalFile: o.evalFile,
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

// FindEval finds an eval file for a skill using priority order:
// 1. {root}/evals/{skill-name}/{eval-file}  (separated convention)
// 2. {source-or-skill-dir}/evals/{eval-file} (nested subdir)
// 3. {source-or-skill-dir}/{eval-file}       (co-located/legacy)
// 4. {skill-dir}/evals/{eval-file} and {skill-dir}/{eval-file} for compiled skills
// Returns empty string if none found (not an error).
func FindEval(wsCtx *WorkspaceContext, skillName string) (string, error) {
	si, err := FindSkill(wsCtx, skillName)
	if err != nil {
		return "", err
	}

	evalsDir := wsCtx.EvalsDir
	if evalsDir == "" {
		evalsDir = projectconfig.DefaultEvalsDir
	}
	evalFiles := evalFilenames(wsCtx.EvalFile)

	// Priority 1: separated convention
	for _, evalFile := range evalFiles {
		var separated string

		// in some situations we have an absolute path to the evalsDir (for instance, from .waza.yaml)
		// and it'd be incorrect to use a relative path.
		if !filepath.IsAbs(evalsDir) {
			separated = filepath.Join(wsCtx.Root, evalsDir, skillName, evalFile)
		} else {
			separated = filepath.Join(evalsDir, skillName, evalFile)
		}

		if isFile(separated) {
			return separated, nil
		}
	}

	// Priority 2: nested subdir inside skill directory
	for _, evalFile := range evalFiles {
		nested := filepath.Join(evalLookupDir(si), "evals", evalFile)
		if isFile(nested) {
			return nested, nil
		}
	}

	// Priority 3: co-located / legacy
	for _, evalFile := range evalFiles {
		colocated := filepath.Join(evalLookupDir(si), evalFile)
		if isFile(colocated) {
			return colocated, nil
		}
	}

	if si.SourceDir != "" && !samePath(si.SourceDir, si.Dir) {
		for _, evalFile := range evalFiles {
			nested := filepath.Join(si.Dir, "evals", evalFile)
			if isFile(nested) {
				return nested, nil
			}
		}
		for _, evalFile := range evalFiles {
			colocated := filepath.Join(si.Dir, evalFile)
			if isFile(colocated) {
				return colocated, nil
			}
		}
	}

	return "", nil
}

func evalLookupDir(si *SkillInfo) string {
	if si.SourceDir != "" {
		return si.SourceDir
	}
	return si.Dir
}

func evalFilenames(configured string) []string {
	if configured == "" {
		configured = projectconfig.DefaultEvalFile
	}
	if configured == projectconfig.DefaultEvalFile {
		return []string{projectconfig.DefaultEvalFile}
	}
	return []string{configured, projectconfig.DefaultEvalFile}
}

// tryParseSkill checks if dir contains SKILL.md or .agent.md and parses it.
// SKILL.md takes priority over .agent.md.
func tryParseSkill(dir string) (SkillInfo, bool) {
	// Check SKILL.md first
	skillPath := filepath.Join(dir, "SKILL.md")
	if isFile(skillPath) {
		name, err := parseSkillName(skillPath)
		if err != nil || name == "" {
			name = filepath.Base(dir)
		}
		return SkillInfo{
			Name:      name,
			Dir:       dir,
			SkillPath: skillPath,
		}, true
	}

	// Fall back to .agent.md files
	entries, err := os.ReadDir(dir)
	if err != nil {
		return SkillInfo{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() && skill.IsAgentFile(entry.Name()) {
			agentPath := filepath.Join(dir, entry.Name())
			name := parseAgentNameFromFile(agentPath)
			if name == "" {
				name = filepath.Base(dir)
			}
			return SkillInfo{
				Name:      name,
				Dir:       dir,
				SkillPath: agentPath,
			}, true
		}
	}

	return SkillInfo{}, false
}

// parseAgentNameFromFile reads an .agent.md file and extracts the agent name.
func parseAgentNameFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fm, _, err := skill.ParseAgentFrontmatter(string(data))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".agent.md")
	}
	return name
}

// scanForSkills scans directories under parentDir for skill definitions.
func scanForSkills(parentDir string, recursive bool) ([]SkillInfo, error) {
	if recursive {
		return scanForSkillsRecursive(parentDir)
	}

	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil, err
	}

	var skills []SkillInfo
	for _, entry := range entries {
		if !entry.IsDir() || shouldSkipSkillScanDir(entry.Name()) {
			continue
		}
		childDir := filepath.Join(parentDir, entry.Name())
		if info, ok := tryParseSkill(childDir); ok {
			skills = append(skills, info)
		}
	}
	return skills, nil
}

func scanForSkillsRecursive(parentDir string) ([]SkillInfo, error) {
	var skills []SkillInfo
	err := filepath.WalkDir(parentDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("error walking %s: %w", path, err)
		}
		if !d.IsDir() {
			return nil
		}
		if path == parentDir {
			return nil
		}
		if shouldSkipSkillScanDir(d.Name()) {
			return fs.SkipDir
		}
		if info, ok := tryParseSkill(path); ok {
			skills = append(skills, info)
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return skills, nil
}

func scanDirectAPMSkills(dir string) ([]SkillInfo, error) {
	apmSkillsDir := filepath.Join(dir, ".apm", "skills")
	if !isDir(apmSkillsDir) {
		return nil, nil
	}
	skills, err := scanForSkills(apmSkillsDir, true)
	if err != nil {
		return nil, err
	}
	for i := range skills {
		skills[i].SourceDir = dir
	}
	return skills, nil
}

// scanForAPMSkillsUnder looks for APM-compiled skills one level down from
// parentDir. For each immediate child skill directory it checks for
// <child>/.apm/skills and, when present, scans it without recursing further
// into the skill's own contents (tasks/, fixtures, etc.). Anything deeper
// than a skill root is intentionally ignored.
func scanForAPMSkillsUnder(parentDir string) ([]SkillInfo, error) {
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", parentDir, err)
	}
	var skills []SkillInfo
	for _, entry := range entries {
		if !entry.IsDir() || shouldSkipSkillScanDir(entry.Name()) {
			continue
		}
		apmSkills, err := scanDirectAPMSkills(filepath.Join(parentDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		skills = append(skills, apmSkills...)
	}
	return skills, nil
}

func shouldSkipSkillScanDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
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

func contextFromSkills(root string, skills []SkillInfo, o detectOptions) *WorkspaceContext {
	ctxType := ContextMultiSkill
	if len(skills) == 1 {
		ctxType = ContextSingleSkill
	}
	return &WorkspaceContext{
		Type:     ctxType,
		Root:     root,
		Skills:   skills,
		EvalsDir: o.evalsDir,
		EvalFile: o.evalFile,
	}
}

// LooksLikePath returns true if the string appears to be a file path
// rather than a skill name. Exported so that CLI packages (cmd/waza,
// cmd/waza/dev) can share the same heuristic without duplication.
func LooksLikePath(s string) bool {
	return strings.ContainsAny(s, `/\`) ||
		filepath.Ext(s) != "" ||
		s == "."
}
