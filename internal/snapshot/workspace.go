package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// WorkspaceLimits bounds each file and the aggregate input and sanitized output.
// Both limits must be positive.
type WorkspaceLimits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// WorkspaceFile is detached, sanitized evidence of one explicitly approved file.
// SHA256 hashes Content, not the original file. Redacted reports a change, not
// proof that the content contains no secrets.
type WorkspaceFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	SHA256   string `json:"sha256"`
	Redacted bool   `json:"redacted"`
}

// CaptureWorkspace captures only reviewed, agent-visible, allowlisted UTF-8
// regular files. The caller must supply evaluator-only exclusions; an allowlist
// is not a confidentiality guarantee. This never reconstructs a full workspace.
// A non-nil error with returned files means partial evidence, not full capture.
// Nil policy uses the default policy, cloned independently of caller counters.
// JSON files are normalized and sensitive object values are redacted by key.
// Other text uses pattern-based redaction only and may retain undetected secrets;
// neither mode certifies secret absence.
func CaptureWorkspace(root string, paths []string, evaluatorOnly []string, policy *Policy, limits WorkspaceLimits) (files []WorkspaceFile, err error) {
	files, _, err = CaptureWorkspaceWithAccounting(root, paths, evaluatorOnly, policy, limits)
	return files, err
}

// CaptureWorkspaceWithAccounting adds detached redaction accounting to
// CaptureWorkspace. Counts describe actual rule matches encountered, including
// partial sanitization attempts, not whitespace normalization or proof of safety.
// Files and accumulated accounting remain available alongside partial errors.
func CaptureWorkspaceWithAccounting(root string, paths []string, evaluatorOnly []string, policy *Policy, limits WorkspaceLimits) (files []WorkspaceFile, summary SnapshotRedaction, err error) {
	if limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 {
		return nil, summary, errors.New("workspace: positive file and total byte limits are required")
	}
	if root == "" {
		return nil, summary, errors.New("workspace: a root directory is required")
	}
	if len(paths) == 0 {
		return nil, summary, errors.New("workspace: an explicit reviewed file allowlist is required")
	}
	p, err := policy.forCapture()
	if err != nil {
		return nil, summary, errors.New("workspace: invalid sanitization policy")
	}
	defer func() {
		summary = SnapshotRedaction{
			Policy: p.Label(), AppliedRules: p.MatchedRules(), RedactionCount: p.MatchCount(),
		}
	}()
	for _, excluded := range evaluatorOnly {
		if !workspaceRelativePath(excluded) {
			return nil, summary, errors.New("workspace: invalid evaluator-only exclusion")
		}
	}
	dir, err := openWorkspaceRoot(root)
	if err != nil {
		return nil, summary, err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("workspace: closing root failed; evidence may be partial"))
		}
	}()

	requested := append([]string(nil), paths...)
	sort.Strings(requested)
	var inputTotal, outputTotal int64
	var problems []error
	for i, name := range requested {
		if i > 0 && name == requested[i-1] {
			problems = append(problems, fmt.Errorf("workspace: allowlist entry %d is duplicated; evidence is partial", i+1))
			continue
		}
		if problem := workspacePathProblem(name, evaluatorOnly, p); problem != nil {
			problems = append(problems, fmt.Errorf("workspace: allowlist entry %d: %w; evidence is partial", i+1, problem))
			continue
		}
		inputLimit := min(limits.MaxFileBytes, limits.MaxTotalBytes-inputTotal)
		content, readErr := dir.readFile(name, inputLimit)
		inputTotal += int64(len(content))
		if readErr != nil {
			problems = append(problems, fmt.Errorf("workspace: allowlist entry %d: %w; evidence is partial", i+1, readErr))
			continue
		}
		if !workspaceText(content) {
			problems = append(problems, fmt.Errorf("workspace: allowlist entry %d: unsupported non-UTF-8 or binary content; evidence is partial", i+1))
			continue
		}
		original := string(content)
		outputLimit := min(limits.MaxFileBytes, limits.MaxTotalBytes-outputTotal)
		var sanitized string
		var sanitizeErr error
		if strings.EqualFold(path.Ext(name), ".json") {
			sanitized, sanitizeErr = workspaceSanitizeJSON(content, p, outputLimit)
		} else {
			sanitized, sanitizeErr = workspaceSanitize(original, p, outputLimit)
		}
		if sanitizeErr != nil {
			problems = append(problems, fmt.Errorf("workspace: allowlist entry %d: %w; evidence is partial", i+1, sanitizeErr))
			continue
		}
		outputTotal += int64(len(sanitized))
		digest := sha256.Sum256([]byte(sanitized))
		files = append(files, WorkspaceFile{
			Path: name, Content: sanitized, SHA256: hex.EncodeToString(digest[:]), Redacted: sanitized != original,
		})
	}
	return files, summary, errors.Join(problems...)
}

func workspaceRelativePath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return utf8.ValidString(name)
}

func workspacePathProblem(name string, excluded []string, p *Policy) error {
	if !workspaceRelativePath(name) {
		return errors.New("invalid slash-relative path")
	}
	for _, exclusion := range excluded {
		if name == exclusion || strings.HasPrefix(name, exclusion+"/") {
			return errors.New("evaluator-only files are excluded")
		}
	}
	defaultKeys := []string{"secret", "token", "password", "passwd", "pwd", "key", "credential", "auth"}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.EqualFold(part, ".env") {
			return errors.New("hidden files are unsupported")
		}
		sensitive := p.IsSensitiveKey(part)
		for _, key := range defaultKeys {
			sensitive = sensitive || strings.Contains(strings.ToLower(part), key)
		}
		if sensitive {
			return errors.New("credential-like path components are excluded")
		}
	}
	for _, rule := range p.Rules {
		if rule.compiled.MatchString(name) {
			return errors.New("file identity matches a redaction rule and cannot be captured")
		}
	}
	return nil
}

func workspaceText(content []byte) bool {
	if !utf8.Valid(content) {
		return false
	}
	for _, b := range content {
		if b < 32 && b != '\n' && b != '\r' && b != '\t' || b == 127 {
			return false
		}
	}
	return true
}

func workspaceSanitize(content string, p *Policy, limit int64) (string, error) {
	if int64(len(content)) > limit {
		return "", errors.New("sanitized content exceeds byte limits")
	}
	rules := p.Rules
	// Run one counting rule at a time on the capture-local policy so each
	// intermediate expansion remains bounded, including partial attempts.
	for _, rule := range rules {
		matches := rule.compiled.FindAllStringIndex(content, -1)
		size := int64(len(content))
		for _, match := range matches {
			size -= int64(match[1] - match[0])
		}
		// Check expansion before allocating replacement content.
		if int64(len(matches)) > (limit-size)/int64(len(RedactionPlaceholder)) {
			return "", errors.New("sanitized content exceeds byte limits")
		}
		p.Rules = []RedactionRule{rule}
		content = p.RedactString(content)
		p.Rules = rules
	}
	return content, nil
}

func workspaceSanitizeJSON(content []byte, p *Policy, limit int64) (string, error) {
	if !json.Valid(content) {
		return "", errors.New("invalid JSON content cannot be captured")
	}
	normalized, err := p.redactJSON(json.RawMessage(content))
	if err != nil {
		return "", fmt.Errorf("JSON content cannot be sanitized: %w", err)
	}
	sanitized, err := json.Marshal(normalized)
	if err != nil {
		return "", errors.New("sanitized JSON cannot be encoded")
	}
	if int64(len(sanitized)) > limit {
		return "", errors.New("sanitized JSON content exceeds byte limits")
	}
	return string(sanitized), nil
}

func workspaceRead(reader io.Reader, size, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("input byte budget is exhausted")
	}
	if size < 0 || size > limit {
		return nil, errors.New("file exceeds remaining byte limits")
	}
	// The descriptor's size is checked again by the caller after this read.
	// Never probe past the approved input budget, even to detect growth.
	content, err := io.ReadAll(io.LimitReader(reader, size))
	if err != nil {
		return content, errors.New("reading approved file failed")
	}
	if int64(len(content)) != size {
		return content, errors.New("approved file size changed during capture")
	}
	return content, nil
}
