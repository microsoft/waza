package controlledcomparison

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// Paths and current-source acceptance are caller-owned, never adopted from a
// contract or report. References remain outside the execution input workspace.
type AssuranceSource struct {
	Source             Source
	ReferencesPath     string
	ReviewPath         string
	AcceptReviewSource string
}

type preparedAssurance struct {
	binding  releasepolicy.AssuranceArm
	prepared *preparedPlan
	report   *assurance.Report
}

func readAssuranceFile(ctx context.Context, path string) (data []byte, err error) {
	if path == "" {
		return nil, fmt.Errorf("assurance requires an explicitly selected local input")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return assurance.ReadDocument(ctx, root, filepath.Base(path), assurance.MaxLabelBytes)
}

func prepareAssurance(ctx context.Context, source AssuranceSource) (_ *preparedAssurance, err error) {
	return prepareAssuranceWithVerifier(ctx, source, assurance.Verify)
}

func prepareAssuranceWithVerifier(ctx context.Context, source AssuranceSource,
	verify func(context.Context, assurance.VerifyRequest) (*assurance.Report, error)) (_ *preparedAssurance, err error) {
	prepared, err := prepare(source.Source)
	if err != nil {
		return nil, err
	}
	evalBytes, err := readAssuranceFile(ctx, source.Source.EvalPath)
	if err != nil {
		return nil, err
	}
	spec, err := models.LoadEvalSpecOffline(source.Source.EvalPath)
	if err != nil {
		return nil, err
	}
	// The verifier must observe exactly the declarations used by preparation.
	if !reflect.DeepEqual(spec, prepared.cfg.Spec()) {
		return nil, fmt.Errorf("assurance source changed during preparation")
	}
	confirmed, err := readAssuranceFile(ctx, source.Source.EvalPath)
	if err != nil {
		return nil, err
	}
	if releasepolicy.SourceDigest(confirmed) != releasepolicy.SourceDigest(evalBytes) {
		return nil, fmt.Errorf("assurance eval bytes changed during preparation")
	}
	labels, err := readAssuranceFile(ctx, source.ReferencesPath)
	if err != nil {
		return nil, err
	}
	references, err := assurance.ParseReferences(labels)
	if err != nil {
		return nil, err
	}
	document, err := references.Document()
	if err != nil {
		return nil, err
	}
	if document.Calibration != nil {
		return nil, fmt.Errorf("offline release assurance does not select a calibration plan or paid report")
	}
	a := releasepolicy.AssuranceArm{Mode: "authored_finite_output", PlanDigest: prepared.plan.Digest,
		EvalSourceDigest: releasepolicy.SourceDigest(evalBytes), LabelsDigest: releasepolicy.SourceDigest(labels),
		ReviewSourceID: source.AcceptReviewSource, Tasks: []releasepolicy.AssuranceTask{},
		Checks: []releasepolicy.AssuranceCheck{}, Inputs: []releasepolicy.AssuranceInput{}}
	resolved, err := evidence.JSONDigest(spec)
	if err != nil {
		return nil, err
	}
	a.ResolvedConfigDigest = *resolved
	executable, err := executableIdentity()
	if err != nil {
		return nil, err
	}
	a.ExecutableDigest = executable.Digest
	var review assurance.SuppliedReview
	if source.ReviewPath != "" {
		data, err := readAssuranceFile(ctx, source.ReviewPath)
		if err != nil {
			return nil, err
		}
		review, err = assurance.ParseReview(data)
		if err != nil {
			return nil, err
		}
		digest := releasepolicy.SourceDigest(data)
		a.ReviewDigest = &digest
	} else if source.AcceptReviewSource != "" {
		return nil, fmt.Errorf("accepting a current review source requires an explicitly supplied review")
	}
	ids := make([]string, 0, len(prepared.tasks))
	for id := range prepared.tasks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		task := prepared.tasks[id]
		if !reflect.ValueOf(task.Expectation).IsZero() {
			return nil, fmt.Errorf("%s implicit expectations are unsupported by scoped release assurance", id)
		}
		digest, err := evidence.JSONDigest(task)
		if err != nil {
			return nil, err
		}
		a.Tasks = append(a.Tasks, releasepolicy.AssuranceTask{TaskID: id, Digest: *digest})
		seen := map[string]bool{}
		for _, requirement := range task.Requirements {
			for _, check := range requirement.Checks {
				if (check.Scope != "eval" && check.Scope != "task") || check.AfterTurn != 0 ||
					seen[check.Grader] {
					return nil, fmt.Errorf("%s assurance mapping is unsupported or non-injective", id)
				}
				declaration, err := preflight.ResolveGrader(check, task, spec)
				if err != nil {
					return nil, err
				}
				var native any
				if declaration.Config != nil {
					native = declaration.Config
				} else if declaration.Inline != nil {
					native = declaration.Inline
				} else {
					return nil, fmt.Errorf("resolved assurance check has no native declaration")
				}
				digest, err := evidence.JSONDigest(native)
				if err != nil {
					return nil, err
				}
				a.Checks = append(a.Checks, releasepolicy.AssuranceCheck{TaskID: id,
					RequirementID: requirement.ID, Check: check, Declaration: *digest, ValidationKey: check.Grader})
				seen[check.Grader] = true
			}
		}
		if len(seen) != len(spec.Graders)+len(task.Validators) {
			return nil, fmt.Errorf("%s assurance must cover every actual native text endpoint grader", id)
		}
	}
	root, err := os.OpenRoot(filepath.Dir(source.ReferencesPath))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	inputs := map[string]models.EvidenceDigest{}
	for _, candidate := range document.Cases {
		if candidate.AuthoredInput == nil || candidate.Snapshot != "" {
			return nil, fmt.Errorf("release assurance only supports explicitly authored finite output")
		}
		name := candidate.AuthoredInput.Path
		if !filepath.IsLocal(name) || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name {
			return nil, fmt.Errorf("assurance input path must be local to the evaluator reference root")
		}
		data, err := assurance.ReadDocument(ctx, root, name, assurance.MaxAuthoredOutputBytes)
		if err != nil {
			return nil, err
		}
		if _, err := assurance.ParseAuthoredOutput(data); err != nil {
			return nil, err
		}
		inputs[name] = releasepolicy.SourceDigest(data)
	}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		a.Inputs = append(a.Inputs, releasepolicy.AssuranceInput{Path: name, Digest: inputs[name]})
	}
	report, err := verify(ctx, assurance.VerifyRequest{References: references, Review: review,
		Acceptance: assurance.ReviewSourceAcceptance{SourceID: source.AcceptReviewSource,
			AcceptCurrentDecision: source.AcceptReviewSource != ""},
		Now: time.Now().UTC(), EvalSource: evalBytes, Spec: spec, Tasks: prepared.tasks,
		SnapshotRoot: root, Calibrate: false})
	if err != nil {
		return nil, err
	}
	if report == nil || report.Kind != assurance.ReportKind || report.SchemaVersion != "1.0" ||
		report.Calibration.Executions != 0 || report.Calibration.Usage != nil || report.Calibration.Credits != nil {
		return nil, fmt.Errorf("unsupported assurance report version or calibration execution")
	}
	for _, input := range a.Inputs {
		data, err := assurance.ReadDocument(ctx, root, input.Path, assurance.MaxAuthoredOutputBytes)
		if err != nil {
			return nil, err
		}
		if releasepolicy.SourceDigest(data) != input.Digest {
			return nil, fmt.Errorf("assurance input changed during verification: %s", input.Path)
		}
	}
	for _, input := range []struct {
		path   string
		digest *models.EvidenceDigest
	}{{source.Source.EvalPath, &a.EvalSourceDigest}, {source.ReferencesPath, &a.LabelsDigest},
		{source.ReviewPath, a.ReviewDigest}} {
		if input.digest == nil {
			continue
		}
		data, err := readAssuranceFile(ctx, input.path)
		if err != nil {
			return nil, err
		}
		if releasepolicy.SourceDigest(data) != *input.digest {
			return nil, fmt.Errorf("assurance source changed during verification: %s", input.path)
		}
	}
	current, err := prepare(source.Source)
	if err != nil {
		return nil, err
	}
	currentExecutable, err := executableIdentity()
	if err != nil {
		return nil, err
	}
	if current.plan.Digest != a.PlanDigest || currentExecutable.Digest != a.ExecutableDigest {
		return nil, fmt.Errorf("assurance execution inputs changed during verification")
	}
	return &preparedAssurance{binding: a, prepared: prepared, report: report}, nil
}

func PlanAssurance(ctx context.Context, policyData []byte, baseline, candidate AssuranceSource) ([]byte, error) {
	p, err := releasepolicy.DecodePolicy(policyData)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	c := releasepolicy.AssuranceContract{Kind: releasepolicy.AssuranceContractKind,
		Version: releasepolicy.AssuranceVersion, Nonce: hex.EncodeToString(nonce),
		PolicyDigest: p.Digest, Arms: map[releasepolicy.Arm]releasepolicy.AssuranceArm{}}
	for arm, source := range map[releasepolicy.Arm]AssuranceSource{releasepolicy.Baseline: baseline, releasepolicy.Candidate: candidate} {
		prepared, err := prepareAssurance(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("%s assurance planning: %w", arm, err)
		}
		c.Arms[arm] = prepared.binding
	}
	data, err := releasepolicy.SealJSON(c)
	if err != nil {
		return nil, err
	}
	_, err = releasepolicy.DecodeAssuranceContract(data, p)
	return data, err
}

func verifyAssuranceSource(ctx context.Context, p *releasepolicy.Policy, contractData []byte,
	arm releasepolicy.Arm, source AssuranceSource) (*preparedAssurance, error) {
	c, err := releasepolicy.DecodeAssuranceContract(contractData, p)
	if err != nil {
		return nil, err
	}
	current, err := prepareAssurance(ctx, source)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current.binding, c.Arms[arm]) {
		return nil, fmt.Errorf("%s current assurance source/declaration/review/evidence binding differs from precollection", arm)
	}
	return current, nil
}

func CollectWithAssurance(ctx context.Context, policyData, contractData []byte, baseline, candidate AssuranceSource, directory string) error {
	return collectControlled(ctx, policyData, baseline.Source, candidate.Source, directory, contractData,
		map[releasepolicy.Arm]AssuranceSource{releasepolicy.Baseline: baseline, releasepolicy.Candidate: candidate})
}
