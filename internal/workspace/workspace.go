// Package workspace provides unified skill workspace detection for waza commands.
// It analyzes directory structures to identify single-skill or multi-skill workspaces
// and locates eval files using a priority-based search.
package workspace

import (
	"errors"
	"fmt"
	"io"
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
	skillsDir        string // subdirectory name for skills (default "skills")
	evalsDir         string // subdirectory name for evals (default "evals")
	evalFile         string // eval filename (default "eval.yaml")
	validateSymlinks bool
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

// WithDiscoverySymlinkValidation reports unresolved discovery links instead
// of treating them as absent. Symlinked skill-scan roots are unsupported.
func WithDiscoverySymlinkValidation() DetectOption {
	return func(o *detectOptions) {
		o.validateSymlinks = true
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
	Type             ContextType
	Root             string      // workspace root directory
	Skills           []SkillInfo // discovered skills
	EvalsDir         string      // configured evals subdirectory name (default "evals")
	EvalFile         string      // configured eval filename (default "eval.yaml")
	validateSymlinks bool
}

// DetectContext analyzes the given directory to determine workspace type.
// It checks:
// 1. CWD for SKILL.md → single-skill
// 2. Walk up parents for SKILL.md → single-skill (nested inside skill dir)
// 3. Check known compiled skill output directories such as .apm/skills/
// 4. Check for skills/ directory with SKILL.md descendants → multi-skill
// 5. Scan CWD for child dirs containing SKILL.md → multi-skill
// With discovery symlink validation, it may return a partial context and an
// error so callers can inspect independent skills without ignoring failures.
func DetectContext(dir string, opts ...DetectOption) (*WorkspaceContext, error) {
	o := defaultDetectOptions()
	for _, fn := range opts {
		fn(&o)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute path: %w", err)
	}
	var discoveryErrors []error
	recordError := func(err error) bool {
		if !o.validateSymlinks {
			return true
		}
		discoveryErrors = append(discoveryErrors, err)
		return false
	}

	// 1. Check if SKILL.md exists in the given directory
	info, ok, err := inspectSkillDirectory(absDir, o.validateSymlinks, false)
	if err != nil {
		if recordError(err) {
			return nil, err
		}
	} else if ok {
		return &WorkspaceContext{
			Type:             ContextSingleSkill,
			Root:             absDir,
			Skills:           []SkillInfo{info},
			EvalsDir:         o.evalsDir,
			EvalFile:         o.evalFile,
			validateSymlinks: o.validateSymlinks,
		}, errors.Join(discoveryErrors...)
	}

	// 2. Walk up parent directories looking for SKILL.md or known compiled outputs
	current := absDir
	for i := 0; i < maxParentWalk; i++ {
		parent := filepath.Dir(current)
		if parent == current {
			break // reached filesystem root
		}
		current = parent

		info, ok, err := inspectSkillDirectory(current, o.validateSymlinks, true)
		if err != nil {
			if recordError(err) {
				return nil, err
			}
		} else if ok {
			return &WorkspaceContext{
				Type:             ContextSingleSkill,
				Root:             current,
				Skills:           []SkillInfo{info},
				EvalsDir:         o.evalsDir,
				EvalFile:         o.evalFile,
				validateSymlinks: o.validateSymlinks,
			}, errors.Join(discoveryErrors...)
		}
		if skills, err := scanDirectAPMSkills(current, o.validateSymlinks, true); err != nil {
			err = fmt.Errorf("scanning APM skills directory %s: %w", filepath.Join(current, ".apm", "skills"), err)
			if recordError(err) {
				return nil, err
			}
			if len(skills) > 0 {
				return contextFromSkills(current, skills, o), errors.Join(discoveryErrors...)
			}
		} else if len(skills) > 0 {
			return contextFromSkills(current, skills, o), errors.Join(discoveryErrors...)
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
	hasSkillsDir, err := skillScanRootExists(skillsDir, o.validateSymlinks)
	if err != nil {
		if recordError(err) {
			return nil, err
		}
	}
	if hasSkillsDir {
		skills, err = scanForSkills(skillsDir, true, o.validateSymlinks)
		if err != nil {
			err = fmt.Errorf("scanning skills directory %s: %w", skillsDir, err)
			if recordError(err) {
				return nil, err
			}
		}
	}

	// 3b. Also check .github/skills/ directory (GitHub Copilot convention)
	githubSkillsDir := filepath.Join(absDir, ".github", "skills")
	hasGithubSkillsDir, err := skillScanRootExists(githubSkillsDir, o.validateSymlinks)
	if err != nil {
		if recordError(err) {
			return nil, err
		}
	}
	if hasGithubSkillsDir && !samePath(skillsDir, githubSkillsDir) {
		githubSkills, err := scanForSkills(githubSkillsDir, true, o.validateSymlinks)
		if err != nil {
			err = fmt.Errorf("scanning GitHub skills directory %s: %w", githubSkillsDir, err)
			if recordError(err) {
				return nil, err
			}
		}
		skills = mergeSkillsByName(skills, githubSkills)
	}

	if hasSkillsDir {
		apmSkills, err := scanForAPMSkillsUnder(skillsDir, o.validateSymlinks)
		if err != nil {
			err = fmt.Errorf("scanning APM skills under %s: %w", skillsDir, err)
			if recordError(err) {
				return nil, err
			}
		}
		skills = mergeSkillsByName(skills, apmSkills)
	}

	rootAPMSkills, err := scanDirectAPMSkills(absDir, o.validateSymlinks, false)
	if err != nil {
		err = fmt.Errorf("scanning APM skills directory %s: %w", filepath.Join(absDir, ".apm", "skills"), err)
		if recordError(err) {
			return nil, err
		}
	}
	skills = mergeSkillsByName(skills, rootAPMSkills)

	if len(skills) > 0 {
		return &WorkspaceContext{
			Type:             ContextMultiSkill,
			Root:             absDir,
			Skills:           skills,
			EvalsDir:         o.evalsDir,
			EvalFile:         o.evalFile,
			validateSymlinks: o.validateSymlinks,
		}, errors.Join(discoveryErrors...)
	}

	// 4. Scan immediate children of dir for SKILL.md
	skills, err = scanForSkills(absDir, false, o.validateSymlinks)
	if err != nil {
		err = fmt.Errorf("scanning directory %s: %w", absDir, err)
		if recordError(err) {
			return nil, err
		}
	}
	if len(skills) > 0 {
		return &WorkspaceContext{
			Type:             ContextMultiSkill,
			Root:             absDir,
			Skills:           skills,
			EvalsDir:         o.evalsDir,
			EvalFile:         o.evalFile,
			validateSymlinks: o.validateSymlinks,
		}, errors.Join(discoveryErrors...)
	}

	// Nothing found
	return &WorkspaceContext{
		Type:             ContextNone,
		Root:             absDir,
		Skills:           nil,
		EvalsDir:         o.evalsDir,
		EvalFile:         o.evalFile,
		validateSymlinks: o.validateSymlinks,
	}, errors.Join(discoveryErrors...)
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
	if separated, err := findSeparatedEvalWithValidation(wsCtx.Root, evalsDir, skillName, evalFiles, wsCtx.validateSymlinks); err != nil || separated != "" {
		return separated, err
	}

	// Priority 2: nested subdir inside skill directory
	if path, err := findEvalInDir(filepath.Join(evalLookupDir(si), "evals"), evalFiles, wsCtx.validateSymlinks); err != nil || path != "" {
		return path, err
	}

	// Priority 3: co-located / legacy
	if path, err := findEvalInDir(evalLookupDir(si), evalFiles, wsCtx.validateSymlinks); err != nil || path != "" {
		return path, err
	}

	if si.SourceDir != "" && !samePath(si.SourceDir, si.Dir) {
		if path, err := findEvalInDir(filepath.Join(si.Dir, "evals"), evalFiles, wsCtx.validateSymlinks); err != nil || path != "" {
			return path, err
		}
		if path, err := findEvalInDir(si.Dir, evalFiles, wsCtx.validateSymlinks); err != nil || path != "" {
			return path, err
		}
	}

	return "", nil
}

// FindEvalForSkill resolves evals from the target skill rather than the process
// working directory. Without project config, ancestor lookup is bounded.
func FindEvalForSkill(si SkillInfo) (string, error) {
	dir, err := filepath.Abs(si.Dir)
	if err != nil {
		return "", fmt.Errorf("resolving skill directory: %w", err)
	}
	si.Dir = dir
	if si.SourceDir == "" {
		current := dir
		for i := 0; i < maxParentWalk; i++ {
			parent := filepath.Dir(current)
			if filepath.Base(current) == "skills" && filepath.Base(parent) == ".apm" {
				si.SourceDir = filepath.Dir(parent)
				break
			}
			if parent == current {
				break
			}
			current = parent
		}
	}
	skillDir, err := filepath.Abs(evalLookupDir(&si))
	if err != nil {
		return "", fmt.Errorf("resolving skill directory: %w", err)
	}
	if si.SourceDir != "" {
		si.SourceDir = skillDir
	}
	cfg, err := projectconfig.Load(skillDir)
	if err != nil {
		return "", fmt.Errorf("loading skill workspace configuration: %w", err)
	}
	ctx := &WorkspaceContext{
		Root:     skillDir,
		Skills:   []SkillInfo{si},
		EvalsDir: cfg.Paths.Evals,
		EvalFile: cfg.Files.EvalFile,
	}
	if cfg.Dir != "" {
		ctx.Root = cfg.Dir
		return FindEval(ctx, si.Name)
	}

	if si.Name != "" {
		current := skillDir
		for i := 0; i < maxParentWalk; i++ {
			path, err := findSeparatedEval(current, ctx.EvalsDir, si.Name, evalFilenames(ctx.EvalFile))
			if err != nil || path != "" {
				return path, err
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	return FindEval(ctx, si.Name)
}

func findSeparatedEval(root, evalsDir, skillName string, evalFiles []string) (string, error) {
	return findSeparatedEvalWithValidation(root, evalsDir, skillName, evalFiles, false)
}

func findSeparatedEvalWithValidation(root, evalsDir, skillName string, evalFiles []string, validateSymlinks bool) (string, error) {
	if !filepath.IsAbs(evalsDir) {
		evalsDir = filepath.Join(root, evalsDir)
	}
	return findEvalInDir(filepath.Join(evalsDir, skillName), evalFiles, validateSymlinks)
}

func findEvalInDir(dir string, evalFiles []string, validateSymlinks bool) (string, error) {
	for _, evalFile := range evalFiles {
		path := filepath.Join(dir, evalFile)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			if validateSymlinks {
				if err := ValidateDiscoveryPath(path); err != nil {
					return "", err
				}
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("checking eval file %q: %w", path, err)
		}
		if info.Mode().IsRegular() {
			return path, nil
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

// ValidateDiscoveryPath allows an absent path but reports an unavailable
// symlink in that path, including a symlinked parent.
func ValidateDiscoveryPath(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking discovery path %q: %w", path, err)
	}
	for current := path; ; {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if _, err := os.Stat(current); err != nil {
					return fmt.Errorf("discovery symlink %q cannot be resolved: %w", current, err)
				}
			}
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("checking discovery path %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func skillScanRootExists(path string, validateSymlinks bool) (bool, error) {
	if !validateSymlinks {
		return isDir(path), nil
	}
	if err := ValidateDiscoveryPath(path); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking skill discovery root %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("skill discovery root %q is a symlink; workspace discovery does not follow symlinked skill roots", path)
	}
	return info.IsDir(), nil
}

func validateSkippedSkillLink(path string, recursive bool) error {
	info, err := os.Stat(path)
	if err != nil {
		if pathErr := ValidateDiscoveryPath(path); pathErr != nil {
			return pathErr
		}
		return fmt.Errorf("checking skill discovery entry %q: %w", path, err)
	}
	if !info.IsDir() {
		return nil
	}
	containsSkill, err := linkedDirectoryContainsSkill(path, recursive)
	if err != nil {
		return err
	}
	if containsSkill {
		return fmt.Errorf("skill discovery entry %q is a symlinked directory; workspace discovery does not follow it", path)
	}
	return nil
}

func linkedDirectoryContainsSkill(path string, recursive bool) (bool, error) {
	type scanKey struct {
		path        string
		includeRoot bool
	}
	visited := make(map[scanKey]bool)
	var scan func(string, bool) (bool, error)
	scan = func(dir string, includeRoot bool) (bool, error) {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return false, fmt.Errorf("resolving skill discovery symlink %q: %w", dir, err)
		}
		clean := filepath.Clean(resolved)
		if os.PathSeparator == '\\' {
			clean = strings.ToLower(clean)
		}
		key := scanKey{path: clean, includeRoot: includeRoot}
		if visited[key] {
			return false, nil
		}
		visited[key] = true
		if !recursive {
			definition, err := skillDefinitionPath(resolved, false)
			return definition != "", err
		}
		if includeRoot {
			apmRoot := filepath.Join(resolved, ".apm", "skills")
			exists, err := skillScanRootExists(apmRoot, true)
			if err != nil {
				return false, err
			}
			if exists {
				found, err := scan(apmRoot, false)
				if err != nil || found {
					return found, err
				}
			}
		}

		found := false
		err = filepath.WalkDir(resolved, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("checking linked skill discovery path %q: %w", current, walkErr)
			}
			if entry.IsDir() {
				if current == resolved && !includeRoot {
					return nil
				}
				if current != resolved && shouldSkipSkillScanDir(entry.Name()) {
					return fs.SkipDir
				}
				definition, err := skillDefinitionPath(current, false)
				if err != nil {
					return err
				}
				if definition != "" {
					found = true
					return fs.SkipAll
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink == 0 || shouldSkipSkillScanDir(entry.Name()) {
				return nil
			}
			info, err := os.Stat(current)
			if err != nil {
				if pathErr := ValidateDiscoveryPath(current); pathErr != nil {
					return pathErr
				}
				return fmt.Errorf("checking linked skill discovery entry %q: %w", current, err)
			}
			if info.IsDir() {
				nested, err := scan(current, true)
				if err != nil {
					return err
				}
				if nested {
					found = true
					return fs.SkipAll
				}
			}
			return nil
		})
		return found, err
	}
	return scan(path, true)
}

func inspectSkillDirectory(dir string, validateSymlinks, optionalAncestor bool) (SkillInfo, bool, error) {
	if validateSymlinks {
		definition, err := skillDefinitionPath(dir, optionalAncestor)
		if err != nil {
			return SkillInfo{}, false, err
		}
		if definition == "" {
			return SkillInfo{}, false, nil
		}
		data, err := readSkillDefinition(definition)
		if err != nil {
			return SkillInfo{}, false, fmt.Errorf("reading skill definition %q: %w", definition, err)
		}
		name, err := parseDefinitionName(definition, data)
		if err != nil || name == "" {
			name = filepath.Base(dir)
		}
		return SkillInfo{Name: name, Dir: dir, SkillPath: definition}, true, nil
	}
	info, ok := tryParseSkill(dir)
	return info, ok, nil
}

func skillDefinitionPath(dir string, optionalAncestor bool) (string, error) {
	skillPath := filepath.Join(dir, "SKILL.md")
	if err := ValidateDiscoveryPath(skillPath); err != nil {
		return "", err
	}
	info, err := os.Stat(skillPath)
	if err == nil && !info.IsDir() {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("skill definition %q is not a regular file", skillPath)
		}
		return skillPath, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("checking skill definition %q: %w", skillPath, err)
	}
	entries, err := os.ReadDir(dir)
	if optionalAncestor && errors.Is(err, fs.ErrPermission) {
		// Ancestors are optional ownership probes in native discovery.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading skill directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !skill.IsAgentFile(entry.Name()) {
			continue
		}
		agentPath := filepath.Join(dir, entry.Name())
		if err := ValidateDiscoveryPath(agentPath); err != nil {
			return "", err
		}
		info, err := os.Stat(agentPath)
		if err != nil {
			return "", fmt.Errorf("checking agent definition %q: %w", agentPath, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("agent definition %q is not a regular file", agentPath)
		}
		return agentPath, nil
	}
	return "", nil
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
			if info, err := os.Stat(agentPath); err == nil && !info.Mode().IsRegular() {
				continue
			}
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
	data, err := readSkillDefinition(path)
	if err != nil {
		return ""
	}
	name, _ := parseDefinitionName(path, data)
	return name
}

func parseDefinitionName(path string, data []byte) (string, error) {
	if filepath.Base(path) == "SKILL.md" {
		var s skill.Skill
		if err := s.UnmarshalTextWithoutTokenCount(data); err != nil {
			return "", fmt.Errorf("parsing SKILL.md: %w", err)
		}
		return strings.TrimSpace(s.Frontmatter.Name), nil
	}
	fm, _, err := skill.ParseAgentFrontmatter(string(data))
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".agent.md")
	}
	return name, nil
}

// scanForSkills scans directories under parentDir for skill definitions.
func scanForSkills(parentDir string, recursive, validateSymlinks bool) ([]SkillInfo, error) {
	if recursive {
		return scanForSkillsRecursive(parentDir, validateSymlinks)
	}

	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil, err
	}

	var skills []SkillInfo
	var discoveryErrors []error
	for _, entry := range entries {
		if shouldSkipSkillScanDir(entry.Name()) {
			continue
		}
		childDir := filepath.Join(parentDir, entry.Name())
		if !entry.IsDir() {
			if validateSymlinks && entry.Type()&os.ModeSymlink != 0 {
				if err := validateSkippedSkillLink(childDir, false); err != nil {
					discoveryErrors = append(discoveryErrors, err)
				}
			}
			continue
		}
		info, ok, err := inspectSkillDirectory(childDir, validateSymlinks, false)
		if err != nil {
			if !validateSymlinks {
				return nil, err
			}
			discoveryErrors = append(discoveryErrors, err)
			continue
		}
		if ok {
			skills = append(skills, info)
		}
	}
	return skills, errors.Join(discoveryErrors...)
}

func scanForSkillsRecursive(parentDir string, validateSymlinks bool) ([]SkillInfo, error) {
	var skills []SkillInfo
	var discoveryErrors []error
	recordError := func(err error) error {
		if !validateSymlinks {
			return err
		}
		discoveryErrors = append(discoveryErrors, err)
		return nil
	}
	err := filepath.WalkDir(parentDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return recordError(fmt.Errorf("error walking %s: %w", path, err))
		}
		if !d.IsDir() {
			if validateSymlinks && d.Type()&os.ModeSymlink != 0 && !shouldSkipSkillScanDir(d.Name()) {
				if err := validateSkippedSkillLink(path, true); err != nil {
					return recordError(err)
				}
			}
			return nil
		}
		if path == parentDir {
			return nil
		}
		if shouldSkipSkillScanDir(d.Name()) {
			return fs.SkipDir
		}
		info, ok, err := inspectSkillDirectory(path, validateSymlinks, false)
		if err != nil {
			return recordError(err)
		}
		if ok {
			skills = append(skills, info)
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return skills, errors.Join(discoveryErrors...)
}

func scanDirectAPMSkills(dir string, validateSymlinks, optionalAncestor bool) ([]SkillInfo, error) {
	apmSkillsDir := filepath.Join(dir, ".apm", "skills")
	exists, err := skillScanRootExists(apmSkillsDir, validateSymlinks)
	if err != nil {
		if optionalAncestor && errors.Is(err, fs.ErrPermission) {
			return nil, nil
		}
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	skills, err := scanForSkills(apmSkillsDir, true, validateSymlinks)
	for i := range skills {
		skills[i].SourceDir = dir
	}
	return skills, err
}

// scanForAPMSkillsUnder looks for APM-compiled skills one level down from
// parentDir. For each immediate child skill directory it checks for
// <child>/.apm/skills and, when present, scans it without recursing further
// into the skill's own contents (tasks/, fixtures, etc.). Anything deeper
// than a skill root is intentionally ignored.
func scanForAPMSkillsUnder(parentDir string, validateSymlinks bool) ([]SkillInfo, error) {
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", parentDir, err)
	}
	var skills []SkillInfo
	var discoveryErrors []error
	for _, entry := range entries {
		if !entry.IsDir() || shouldSkipSkillScanDir(entry.Name()) {
			continue
		}
		apmSkills, err := scanDirectAPMSkills(filepath.Join(parentDir, entry.Name()), validateSymlinks, false)
		if err != nil {
			if !validateSymlinks {
				return nil, err
			}
			discoveryErrors = append(discoveryErrors, err)
		}
		skills = append(skills, apmSkills...)
	}
	return skills, errors.Join(discoveryErrors...)
}

func shouldSkipSkillScanDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
}

// parseSkillName reads a SKILL.md file and extracts the skill name from frontmatter.
func parseSkillName(path string) (string, error) {
	data, err := readSkillDefinition(path)
	if err != nil {
		return "", fmt.Errorf("reading skill file: %w", err)
	}
	return parseDefinitionName(path, data)
}

// Supported Unix systems request a nonblocking open and validate the opened
// descriptor. Windows and other platforms cannot guarantee a nonblocking open
// after concurrent path replacement; none of these reads authenticate content.
func readSkillDefinition(path string) (data []byte, err error) {
	file, err := openSkillDefinition(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skill definition %q is not a regular file", path)
	}
	return io.ReadAll(file)
}

// isFile returns true if path exists and is a regular file.
func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
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
		Type:             ctxType,
		Root:             root,
		Skills:           skills,
		EvalsDir:         o.evalsDir,
		EvalFile:         o.evalFile,
		validateSymlinks: o.validateSymlinks,
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
