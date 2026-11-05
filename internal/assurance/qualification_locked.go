package assurance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"gopkg.in/yaml.v3"
)

func qualificationExpandLockedGraders(ctx context.Context, spec *models.EvalSpec, eval qualificationSourceRead) error {
	if len(registry.CollectGraderRefs(spec)) == 0 {
		return nil
	}
	data, err := qualificationReadSource(ctx, qualificationSourceRead{
		Root: eval.Root, Path: filepath.Join(filepath.Dir(eval.Path), models.LockfileName),
	}, qualificationDocumentLimit)
	if err != nil {
		return fmt.Errorf("qualification: acquiring grader lock: %w", err)
	}
	var lock models.Lockfile
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&lock); err != nil {
		return fmt.Errorf("qualification: decoding grader lock: %w", err)
	}
	if lock.SchemaVersion == 0 {
		lock.SchemaVersion = 1
	}
	if err := lock.Validate(); err != nil {
		return fmt.Errorf("qualification: validating grader lock: %w", err)
	}
	resolver, err := registry.NewResolver()
	if err != nil {
		return fmt.Errorf("qualification: locating offline grader cache: %w", err)
	}
	for i, grader := range spec.Graders {
		if grader.Ref == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, ok := lock.Grader(grader.Ref)
		if !ok {
			return errors.New("qualification: selected grader has no matching lock entry")
		}
		ref, err := registry.ParseRef(grader.Ref)
		if err != nil {
			return fmt.Errorf("qualification: parsing locked grader reference: %w", err)
		}
		preset, err := resolver.LoadLockedGraderOffline(ctx, ref, entry)
		if err != nil {
			return fmt.Errorf("qualification: acquiring offline locked grader: %w", err)
		}
		merged, err := registry.MergeGraderConfigOffline(preset, grader)
		if err != nil {
			return fmt.Errorf("qualification: merging offline locked grader: %w", err)
		}
		merged.Ref = ""
		spec.Graders[i] = merged
	}
	return nil
}
