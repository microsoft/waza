package snapshot

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RedactionPlaceholder is what we write in place of a redacted value.
// Stable, easy to grep for, distinguishable from any plausible credential.
const RedactionPlaceholder = "[REDACTED]"

// RedactionRule is a single named regex that matches a secret-like string.
// Rule names appear in Snapshot.Redaction.AppliedRules so users can audit
// which rule fired without seeing the secret.
type RedactionRule struct {
	Name     string         `yaml:"name" json:"name"`
	Pattern  string         `yaml:"pattern" json:"pattern"`
	compiled *regexp.Regexp `yaml:"-" json:"-"`
}

// Policy is the redaction configuration used when capturing a snapshot.
// A nil *Policy means redaction is disabled — capture verbatim. This is
// useful for tests but the CLI never produces a nil policy in production.
type Policy struct {
	// Rules is the ordered list of redaction rules applied to every captured
	// string. Order matters: later rules see strings that earlier rules
	// have already partially redacted.
	Rules []RedactionRule `yaml:"rules" json:"rules"`

	// EnvKeyDenyList is a list of env-var name *substrings* (case-insensitive)
	// that mark an env var as sensitive even if its value does not match any
	// regex. Shipped defaults cover SECRET/TOKEN/PASSWORD/KEY/CREDENTIAL.
	EnvKeyDenyList []string `yaml:"envKeyDenyList" json:"envKeyDenyList"`

	// label is "default", "custom", or "default+custom", recorded in
	// Snapshot.Redaction.Policy.
	label string `yaml:"-" json:"-"`

	matchCount   int             `yaml:"-" json:"-"`
	matchedRules map[string]bool `yaml:"-" json:"-"`
}

// DefaultPolicy returns the shipped redaction policy. The rules target the
// most common secret formats so a snapshot is safe to share by default:
// GitHub PATs, OpenAI/Azure keys, generic 32+ hex/base64 tokens, JWT-shaped
// strings, AWS access keys, basic-auth headers, and email addresses.
func DefaultPolicy() *Policy {
	p := &Policy{
		Rules: []RedactionRule{
			{Name: "github_pat", Pattern: `gh[pousr]_[A-Za-z0-9_]{36,}`},
			{Name: "openai_key", Pattern: `sk-(?:proj-)?[A-Za-z0-9_-]{20,}`},
			{Name: "azure_openai_key", Pattern: `(?i)\b[a-f0-9]{32,64}\b`},
			{Name: "aws_access_key", Pattern: `AKIA[0-9A-Z]{16}`},
			{Name: "aws_secret_key", Pattern: `(?i)aws[_-]?secret[_-]?(?:access[_-]?)?key["'\s:=]+[A-Za-z0-9/+]{40}`},
			{Name: "jwt", Pattern: `eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`},
			{Name: "bearer_header", Pattern: `(?i)bearer\s+[A-Za-z0-9._\-+/=]{16,}`},
			{Name: "basic_auth_header", Pattern: `(?i)basic\s+[A-Za-z0-9+/=]{16,}`},
			{Name: "private_key_pem", Pattern: `-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]+?-----END [A-Z ]*PRIVATE KEY-----`},
			{Name: "email", Pattern: `\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`},
		},
		EnvKeyDenyList: []string{
			"SECRET", "TOKEN", "PASSWORD", "PASSWD", "PWD",
			"KEY", "CREDENTIAL", "API_KEY", "AUTH",
		},
		label: "default",
	}
	if err := p.compile(); err != nil {
		// Shipped defaults must compile; a panic here is a programmer error
		// caught by tests.
		panic(fmt.Sprintf("default redaction policy failed to compile: %v", err))
	}
	return p
}

// LoadPolicy reads a YAML file that may either replace or extend the shipped
// defaults. The on-disk format:
//
//	# extend: when true, defaults are prepended; otherwise the file's rules
//	# replace the defaults entirely.
//	extend: true
//	rules:
//	  - name: internal_token
//	    pattern: "INT-[A-Z0-9]{20}"
//	envKeyDenyList:
//	  - CUSTOM_SECRET
func LoadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read redaction policy %s: %w", path, err)
	}
	var doc struct {
		Extend         bool            `yaml:"extend"`
		Rules          []RedactionRule `yaml:"rules"`
		EnvKeyDenyList []string        `yaml:"envKeyDenyList"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse redaction policy %s: %w", path, err)
	}

	var p *Policy
	switch {
	case doc.Extend:
		p = DefaultPolicy()
		p.Rules = append(p.Rules, doc.Rules...)
		p.EnvKeyDenyList = append(p.EnvKeyDenyList, doc.EnvKeyDenyList...)
		p.label = "default+custom"
	default:
		p = &Policy{
			Rules:          doc.Rules,
			EnvKeyDenyList: doc.EnvKeyDenyList,
			label:          "custom",
		}
	}
	if err := p.compile(); err != nil {
		return nil, fmt.Errorf("compile redaction policy %s: %w", path, err)
	}
	return p, nil
}

func (p *Policy) compile() error {
	for i := range p.Rules {
		re, err := regexp.Compile(p.Rules[i].Pattern)
		if err != nil {
			return fmt.Errorf("rule %q: %w", p.Rules[i].Name, err)
		}
		p.Rules[i].compiled = re
	}
	return nil
}

// Label returns the policy label ("default", "custom", "default+custom").
func (p *Policy) Label() string {
	if p == nil {
		return "disabled"
	}
	return p.label
}

// MatchCount returns the number of replacements performed across this
// policy's lifetime. Reset to zero by ResetCounters.
func (p *Policy) MatchCount() int {
	if p == nil {
		return 0
	}
	return p.matchCount
}

// MatchedRules returns the sorted set of rule names that matched at least
// once.
func (p *Policy) MatchedRules() []string {
	if p == nil || len(p.matchedRules) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.matchedRules))
	for name := range p.matchedRules {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ResetCounters clears the per-capture statistics.
func (p *Policy) ResetCounters() {
	if p == nil {
		return
	}
	p.matchCount = 0
	p.matchedRules = nil
}

// RedactString applies every rule in order to s and returns the redacted
// string. Match counters are updated on Policy.
func (p *Policy) RedactString(s string) string {
	if p == nil || s == "" {
		return s
	}
	for _, r := range p.Rules {
		if r.compiled == nil {
			continue
		}
		// Count matches so callers can report rule names + total count.
		matches := r.compiled.FindAllStringIndex(s, -1)
		if len(matches) == 0 {
			continue
		}
		if p.matchedRules == nil {
			p.matchedRules = map[string]bool{}
		}
		p.matchedRules[r.Name] = true
		p.matchCount += len(matches)
		s = r.compiled.ReplaceAllString(s, RedactionPlaceholder)
	}
	return s
}

// IsSensitiveKey reports whether the env-var name should be treated as
// secret-bearing regardless of value. Allow-listed keys that match the deny
// list are captured with their value redacted, NOT entirely dropped, so the
// snapshot still records that the variable was present.
func (p *Policy) IsSensitiveKey(name string) bool {
	if p == nil || name == "" {
		return false
	}
	upper := strings.ToUpper(name)
	for _, frag := range p.EnvKeyDenyList {
		if strings.Contains(upper, strings.ToUpper(frag)) {
			return true
		}
	}
	return false
}

// RedactStringSlice applies redaction to every element. Returns a fresh
// slice; the input is not mutated.
func (p *Policy) RedactStringSlice(in []string) []string {
	if p == nil || len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = p.RedactString(s)
	}
	return out
}

// RedactStringMap applies redaction to every value in the map. Returns a
// fresh map; the input is not mutated.
func (p *Policy) RedactStringMap(in map[string]string) map[string]string {
	if p == nil || len(in) == 0 {
		return in
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if p.IsSensitiveKey(k) {
			out[k] = RedactionPlaceholder
			continue
		}
		out[k] = p.RedactString(v)
	}
	return out
}

// RedactAny walks a JSON-like value (string / []any / map[string]any /
// map[string]string / and pointers thereof) and applies redaction to every
// string it finds. Other types are returned as-is.
func (p *Policy) RedactAny(v any) any {
	if p == nil || v == nil {
		return v
	}
	switch x := v.(type) {
	case string:
		return p.RedactString(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if p.IsSensitiveKey(k) {
				out[k] = RedactionPlaceholder
				continue
			}
			out[k] = p.RedactAny(val)
		}
		return out
	case map[string]string:
		return p.RedactStringMap(x)
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = p.RedactAny(val)
		}
		return out
	case []string:
		return p.RedactStringSlice(x)
	default:
		return v
	}
}
