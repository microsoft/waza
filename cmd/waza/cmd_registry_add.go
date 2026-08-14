package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type registryAddOptions struct {
	evalPath  string
	name      string
	setValues []string
	allowExec bool
}

type resolvedRegistryRef struct {
	Ref          string
	Module       string
	Version      string
	Kind         string
	Commit       string
	Digest       string
	Dependencies []string
}

func newRegistryAddCommand() *cobra.Command {
	opts := registryAddOptions{evalPath: "eval.yaml"}

	cmd := &cobra.Command{
		Use:   "add <ref>",
		Short: "Add a registry artifact to eval.yaml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryAdd(cmd, args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.evalPath, "eval", "eval.yaml", "Path to eval.yaml")
	cmd.Flags().StringVar(&opts.name, "name", "", "Local alias for the added grader")
	cmd.Flags().StringArrayVar(&opts.setValues, "set", nil, "Set a local override as key=value (repeatable)")
	cmd.Flags().BoolVar(&opts.allowExec, "allow-exec", false, "Allow adding remote program graders without prompting")

	return cmd
}

func runRegistryAdd(cmd *cobra.Command, ref string, opts registryAddOptions) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("ref must not be empty")
	}

	resolved, err := resolveRegistryRef(cmd, ref)
	if err != nil {
		return err
	}

	trusted := opts.allowExec
	if resolved.Kind == "program-grader" {
		if !opts.allowExec {
			confirmed, err := confirmRemoteProgramGrader(cmd.InOrStdin(), cmd.OutOrStdout(), ref)
			if err != nil {
				return err
			}
			if !confirmed {
				return errors.New("remote program grader was not added")
			}
			trusted = true
		}
	}

	entry, err := buildRegistryGraderEntry(ref, opts.name, opts.setValues)
	if err != nil {
		return err
	}

	originalEval, err := os.ReadFile(opts.evalPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", opts.evalPath, err)
	}
	evalInfo, err := os.Stat(opts.evalPath)
	if err != nil {
		return fmt.Errorf("reading metadata for %s: %w", opts.evalPath, err)
	}
	if err := appendRegistryGrader(opts.evalPath, entry); err != nil {
		return err
	}
	if err := updateRegistryLock(opts.evalPath, resolved, trusted); err != nil {
		// The eval and lock file form one logical update. Restore the original
		// eval when the lock cannot be written so the repository is not left
		// with a grader entry that has no corresponding resolution metadata.
		if rollbackErr := os.WriteFile(opts.evalPath, originalEval, evalInfo.Mode().Perm()); rollbackErr != nil {
			return fmt.Errorf("updating registry lock: %w; restoring %s: %v", err, opts.evalPath, rollbackErr)
		}
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Added %s to %s\n", ref, opts.evalPath) //nolint:errcheck
	return nil
}

func resolveRegistryRef(_ *cobra.Command, ref string) (resolvedRegistryRef, error) {
	// TODO: call the shared resolver from issue #15 and use its resolved module metadata.
	module, version := splitRegistryRef(ref)
	kind := "grader-preset"
	if strings.Contains(strings.ToLower(ref), "program") || strings.Contains(strings.ToLower(ref), "exec") {
		kind = "program-grader"
	}
	return resolvedRegistryRef{
		Ref:     ref,
		Module:  module,
		Version: version,
		Kind:    kind,
		Commit:  "resolver-pending",
		Digest:  "resolver-pending",
	}, nil
}

func splitRegistryRef(ref string) (string, string) {
	module := ref
	if hash := strings.Index(module, "#"); hash >= 0 {
		module = module[:hash]
	}
	version := ""
	if at := strings.LastIndex(ref, "@"); at >= 0 && at < len(ref)-1 {
		version = ref[at+1:]
	}
	if at := strings.LastIndex(module, "@"); at >= 0 {
		module = module[:at]
	}
	return module, version
}

func confirmRemoteProgramGrader(in io.Reader, out io.Writer, ref string) (bool, error) {
	if _, err := fmt.Fprintf(out, "Remote program grader %s can execute local commands. Add it? [y/N]: ", ref); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func buildRegistryGraderEntry(ref, name string, setValues []string) (*yaml.Node, error) {
	entry := &yaml.Node{Kind: yaml.MappingNode}
	setMappingScalar(entry, "ref", ref)
	if strings.TrimSpace(name) != "" {
		setMappingScalar(entry, "name", strings.TrimSpace(name))
	}
	for _, raw := range setValues {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid --set %q: expected key=value", raw)
		}
		path := strings.Split(strings.TrimSpace(key), ".")
		for i := range path {
			segment := strings.TrimSpace(path[i])
			if segment == "" || segment != path[i] {
				return nil, fmt.Errorf("invalid --set %q: path segments must not be empty", raw)
			}
			path[i] = segment
		}
		setNestedValue(entry, path, scalarNode(strings.TrimSpace(value)))
	}
	return entry, nil
}

func appendRegistryGrader(evalPath string, entry *yaml.Node) error {
	data, err := os.ReadFile(evalPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", evalPath, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", evalPath, err)
	}
	root := documentMapping(&doc)
	if root == nil {
		return fmt.Errorf("%s must contain a YAML mapping", evalPath)
	}
	graders := mappingValue(root, "graders")
	if graders == nil {
		graders = &yaml.Node{Kind: yaml.SequenceNode}
		appendMappingPair(root, "graders", graders)
	}
	if graders.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s field graders must be a list", evalPath)
	}
	graders.Content = append(graders.Content, entry)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("encoding %s: %w", evalPath, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encoding %s: %w", evalPath, err)
	}
	return os.WriteFile(evalPath, buf.Bytes(), 0o644)
}

func updateRegistryLock(evalPath string, resolved resolvedRegistryRef, trusted bool) error {
	lockPath := filepath.Join(filepath.Dir(evalPath), "waza.lock")
	var root *yaml.Node
	if data, err := os.ReadFile(lockPath); err == nil {
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", lockPath, err)
		}
		root = documentMapping(&doc)
		if root == nil {
			return fmt.Errorf("%s must contain a YAML mapping", lockPath)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		root = &yaml.Node{Kind: yaml.MappingNode}
		setMappingScalar(root, "schema_version", "1")
	} else {
		return fmt.Errorf("reading %s: %w", lockPath, err)
	}

	modules := mappingValue(root, "modules")
	if modules == nil {
		modules = &yaml.Node{Kind: yaml.SequenceNode}
		appendMappingPair(root, "modules", modules)
	}
	if modules.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s field modules must be a list", lockPath)
	}

	lockEntry := registryLockEntry(resolved, trusted)
	replaced := false
	for i, module := range modules.Content {
		if module.Kind != yaml.MappingNode {
			continue
		}
		moduleRef := mappingValue(module, "ref")
		if moduleRef != nil && moduleRef.Value == resolved.Ref {
			modules.Content[i] = lockEntry
			replaced = true
			break
		}
	}
	if !replaced {
		modules.Content = append(modules.Content, lockEntry)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("encoding %s: %w", lockPath, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encoding %s: %w", lockPath, err)
	}
	return os.WriteFile(lockPath, buf.Bytes(), 0o644)
}

func registryLockEntry(resolved resolvedRegistryRef, trusted bool) *yaml.Node {
	entry := &yaml.Node{Kind: yaml.MappingNode}
	setMappingScalar(entry, "ref", resolved.Ref)
	setMappingScalar(entry, "module", resolved.Module)
	if resolved.Version != "" {
		setMappingScalar(entry, "version", resolved.Version)
	}
	setMappingScalar(entry, "commit", resolved.Commit)
	setMappingScalar(entry, "digest", resolved.Digest)
	setMappingScalar(entry, "resolved_at", time.Now().UTC().Format(time.RFC3339))
	if trusted && resolved.Kind == "program-grader" {
		appendMappingPair(entry, "trusted", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	}
	return entry
}

func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		if doc.Content[0].Kind == yaml.MappingNode {
			return doc.Content[0]
		}
		return nil
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func appendMappingPair(mapping *yaml.Node, key string, value *yaml.Node) {
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func setMappingScalar(mapping *yaml.Node, key, value string) {
	setNestedValue(mapping, []string{key}, scalarNode(value))
}

func setNestedValue(mapping *yaml.Node, path []string, value *yaml.Node) {
	if len(path) == 0 {
		return
	}
	if len(path) == 1 {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == path[0] {
				mapping.Content[i+1] = value
				return
			}
		}
		appendMappingPair(mapping, path[0], value)
		return
	}
	child := mappingValue(mapping, path[0])
	if child == nil || child.Kind != yaml.MappingNode {
		child = &yaml.Node{Kind: yaml.MappingNode}
		setNestedValue(mapping, []string{path[0]}, child)
	}
	setNestedValue(child, path[1:], value)
}

func scalarNode(value string) *yaml.Node {
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: lower}
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: value}
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil && strings.ContainsAny(value, ".eE") {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: value}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
