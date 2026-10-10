package assurance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

// Decode JSON into the original concrete parameter type, rather than decoding
// a polymorphic native declaration into an untyped map or a YAML alias.
func calibrationJSONCopy[T any](source T) (T, error) {
	var target T
	data, err := json.Marshal(source)
	if err != nil {
		return target, err
	}
	if _, err := jsonutil.Parse(data); err != nil {
		return target, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&target); err != nil {
		return target, err
	}
	return target, nil
}

func calibrationParametersCopy(source models.GraderParameters) (models.GraderParameters, error) {
	if source == nil {
		return nil, nil
	}
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	if _, err := jsonutil.Parse(data); err != nil {
		return nil, err
	}
	target := reflect.New(reflect.TypeOf(source))
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target.Interface()); err != nil {
		return nil, err
	}
	parameters, ok := target.Elem().Interface().(models.GraderParameters)
	if !ok {
		return nil, errors.New("assurance: native parameter copy has an unsupported type")
	}
	return parameters, nil
}

func calibrationConfigCopy(source models.GraderConfig) (models.GraderConfig, error) {
	parameters, err := calibrationParametersCopy(source.Parameters)
	if err != nil {
		return models.GraderConfig{}, err
	}
	source.Parameters = nil
	target, err := calibrationJSONCopy(source)
	target.Parameters = parameters
	return target, err
}

func calibrationValidatorsCopy(source []models.ValidatorInline) ([]models.ValidatorInline, error) {
	if source == nil {
		return nil, nil
	}
	target := make([]models.ValidatorInline, len(source))
	for i, original := range source {
		parameters, err := calibrationParametersCopy(original.Parameters)
		if err != nil {
			return nil, err
		}
		original.Parameters = nil
		target[i], err = calibrationJSONCopy(original)
		if err != nil {
			return nil, err
		}
		target[i].Parameters = parameters
	}
	return target, nil
}

func freezeCalibrationRequest(source VerifyRequest) (VerifyRequest, error) {
	if source.Spec == nil {
		return VerifyRequest{}, errors.New("assurance: selected native specification is required")
	}
	target := source
	target.EvalSource = bytes.Clone(source.EvalSource)
	if source.Review.Decision != nil {
		decision := *source.Review.Decision
		target.Review.Decision = &decision
	}
	spec := *source.Spec
	spec.Graders = nil
	frozenSpec, err := calibrationJSONCopy(spec)
	if err != nil {
		return VerifyRequest{}, fmt.Errorf("assurance: freeze native specification: %w", err)
	}
	if source.Spec.Graders != nil {
		frozenSpec.Graders = make([]models.GraderConfig, len(source.Spec.Graders))
	}
	for i, grader := range source.Spec.Graders {
		frozenSpec.Graders[i], err = calibrationConfigCopy(grader)
		if err != nil {
			return VerifyRequest{}, fmt.Errorf("assurance: freeze native grader: %w", err)
		}
	}
	target.Spec = &frozenSpec
	target.Tasks = make(map[string]*models.TestCase, len(source.Tasks))
	for id, original := range source.Tasks {
		if original == nil {
			return VerifyRequest{}, errors.New("assurance: selected native task is unavailable")
		}
		skeleton := *original
		skeleton.Validators, skeleton.Checkpoints = nil, nil
		task, err := calibrationJSONCopy(skeleton)
		if err != nil {
			return VerifyRequest{}, fmt.Errorf("assurance: freeze native task: %w", err)
		}
		task.Validators, err = calibrationValidatorsCopy(original.Validators)
		if err != nil {
			return VerifyRequest{}, fmt.Errorf("assurance: freeze native task graders: %w", err)
		}
		if original.Checkpoints != nil {
			task.Checkpoints = make([]models.Checkpoint, len(original.Checkpoints))
		}
		for i, checkpoint := range original.Checkpoints {
			graders := checkpoint.Graders
			checkpoint.Graders = nil
			task.Checkpoints[i], err = calibrationJSONCopy(checkpoint)
			if err != nil {
				return VerifyRequest{}, err
			}
			task.Checkpoints[i].Graders, err = calibrationValidatorsCopy(graders)
			if err != nil {
				return VerifyRequest{}, err
			}
		}
		target.Tasks[id] = &task
	}
	// A copy must preserve the repository's exact native JSON identity,
	// including numeric values and every resolved declaration.
	for _, pair := range [][2]any{{source.Spec, target.Spec}, {source.Tasks, target.Tasks}} {
		before, err := json.Marshal(pair[0])
		if err != nil {
			return VerifyRequest{}, err
		}
		after, err := json.Marshal(pair[1])
		if err != nil || !bytes.Equal(before, after) {
			return VerifyRequest{}, errors.New("assurance: native JSON copy was not lossless")
		}
	}
	return target, nil
}
