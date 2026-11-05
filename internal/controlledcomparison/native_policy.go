package controlledcomparison

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativesnapshot"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
)

const NativePolicyKind = "waza.release-native-policy"

// NativePolicy is a separately selected public collection contract. The core
// policy/method and historical release 1.0 documents retain their own versions.
type NativePolicy struct {
	Kind          string                `json:"kind"`
	Version       string                `json:"version"`
	Digest        models.EvidenceDigest `json:"digest"`
	Policy        json.RawMessage       `json:"policy"`
	Profile       json.RawMessage       `json:"profile"`
	CaptureDigest models.EvidenceDigest `json:"capture_digest"`
}

func IsNativePolicy(raw []byte) bool {
	var header struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(raw, &header) == nil && header.Kind == NativePolicyKind
}

func DecodeNativePolicy(raw []byte) (*NativePolicy, *releasepolicy.Policy, *nativetask.Profile, error) {
	if len(raw) > 16<<20 {
		return nil, nil, nil, fmt.Errorf("native policy exceeds 16 MiB")
	}
	value, err := jsonutil.Parse(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 6 {
		return nil, nil, nil, fmt.Errorf("native policy requires its exact six members")
	}
	if err := models.ValidateNativeJSONKeys(raw, NativePolicy{}); err != nil {
		return nil, nil, nil, err
	}
	var native NativePolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&native); err != nil {
		return nil, nil, nil, err
	}
	delete(object, "digest")
	digest, err := evidence.JSONDigest(object)
	if err != nil || digest == nil || *digest != native.Digest ||
		native.Kind != NativePolicyKind || native.Version != "1.0" ||
		native.CaptureDigest.Encoding != "source-bytes" || len(native.CaptureDigest.SHA256) != 64 {
		return nil, nil, nil, fmt.Errorf("unsupported or inconsistent native policy identity")
	}
	policy, err := releasepolicy.DecodePolicy(native.Policy)
	if err != nil {
		return nil, nil, nil, err
	}
	if policy.CollectionProfile != releasepolicy.NativeCollectionProfile {
		return nil, nil, nil, fmt.Errorf("native inner policy lacks its required collection discriminator")
	}
	if err := nativeRequirements(policy.Requirements); err != nil {
		return nil, nil, nil, err
	}
	profile, err := nativetask.DecodeProfile(native.Profile)
	if err != nil {
		return nil, nil, nil, err
	}
	if profile.Version != "2.0" || profile.PolicyDigest != policy.Digest {
		return nil, nil, nil, fmt.Errorf("public native collection requires the bound 2.0 task profile")
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if profile.Arms[arm].PlanDigest != policy.Arms[arm].Digest {
			return nil, nil, nil, fmt.Errorf("native profile differs from selected plan")
		}
	}
	return &native, policy, profile, nil
}

func nativeRequirements(requirements releasepolicy.Requirements) error {
	if requirements.Assurance || requirements.Runtime {
		return fmt.Errorf("native profile cannot provide authenticated assurance or observed runtime versions; remove unsupported required dimensions before collection")
	}
	for _, budget := range requirements.Billing {
		if budget.Axis == "provider_currency" {
			return fmt.Errorf("native profile has no authoritative provider-currency observation; currency budgets are unsupported")
		}
	}
	return nil
}

func nativeEnvironment() error {
	for _, name := range []string{"COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL"} {
		if os.Getenv(name) != "" {
			return fmt.Errorf("native comparison does not support custom providers (%s is set)", name)
		}
	}
	return nil
}

// captureNative retains one rooted source association for both arms. Handles
// remain owned until the collector has rechecked and finished publication.
func captureNative(ctx context.Context, baseline, candidate Source) (_ *nativesnapshot.CollectionCapture, closeCapture func() error, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, nil, err
	}
	// A shared filesystem root also accommodates an executable outside the
	// eval directory. Only the resolver's selected inventories are captured.
	volume := filepath.VolumeName(cwd) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, root.Close())
		}
	}()
	exe, err := filepath.Rel(volume, executable)
	if err != nil {
		return nil, nil, err
	}
	locations := map[releasepolicy.Arm]nativesnapshot.ArmLocation{}
	for arm, source := range map[releasepolicy.Arm]Source{releasepolicy.Baseline: baseline, releasepolicy.Candidate: candidate} {
		absolute, err := filepath.Abs(source.EvalPath)
		if err != nil {
			return nil, nil, err
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, nil, err
		}
		eval, err := filepath.Rel(volume, absolute)
		if err != nil {
			return nil, nil, err
		}
		contextDir := source.ContextDir
		if contextDir != "" {
			contextDir, err = filepath.Abs(contextDir)
			if err != nil {
				return nil, nil, err
			}
			contextDir, err = filepath.EvalSymlinks(contextDir)
			if err != nil {
				return nil, nil, err
			}
		}
		locations[arm] = nativesnapshot.ArmLocation{Root: root, EvalPath: filepath.ToSlash(eval),
			CWD: cwd, ContextDir: contextDir, Executable: filepath.ToSlash(exe)}
	}
	capture, err := nativesnapshot.CaptureCollection(ctx, locations)
	return capture, root.Close, err
}

func PlanNative(ctx context.Context, baseline, candidate Source, design releasepolicy.Design, requirements releasepolicy.Requirements, allowedFields []string) (_ []byte, err error) {
	if err := nativeRequirements(requirements); err != nil {
		return nil, err
	}
	if err := nativeEnvironment(); err != nil {
		return nil, err
	}
	capture, closeCapture, err := captureNative(ctx, baseline, candidate)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeCapture()) }()
	projections, err := capture.Projections(ctx)
	if err != nil {
		return nil, err
	}
	policyData, err := planProjections(projections, design, requirements, allowedFields)
	if err != nil {
		return nil, err
	}
	policy, err := releasepolicy.DecodePolicy(policyData)
	if err != nil {
		return nil, err
	}
	policy.CollectionProfile = releasepolicy.NativeCollectionProfile
	policyData, err = releasepolicy.SealJSON(policy)
	if err != nil {
		return nil, err
	}
	policy, err = releasepolicy.DecodePolicy(policyData)
	if err != nil {
		return nil, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	profile := nativetask.Profile{Kind: "waza.release-native-task-profile", Version: "2.0",
		Nonce: hex.EncodeToString(nonce[:]), Selection: nativetask.Selection{Operation: "native_text_task", Version: "2.0"},
		PolicyDigest: policy.Digest, PermissionMode: "deny_all", Arms: map[releasepolicy.Arm]nativetask.ArmPlan{}}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		plan := nativetask.ArmPlan{PlanDigest: policy.Arms[arm].Digest, Requests: []nativetask.RequestPlan{}}
		for _, task := range policy.Arms[arm].Plan.Tasks {
			request, err := capture.Request(ctx, arm, task.ID)
			if err != nil {
				return nil, err
			}
			digest, err := nativetask.CollectionRequestDigest(request)
			if err != nil {
				return nil, err
			}
			plan.Requests = append(plan.Requests, nativetask.RequestPlan{TaskID: task.ID, RequestDigest: digest})
		}
		slices.SortFunc(plan.Requests, func(a, b nativetask.RequestPlan) int { return strings.Compare(a.TaskID, b.TaskID) })
		profile.Arms[arm] = plan
	}
	profileData, err := releasepolicy.SealJSON(profile)
	if err != nil {
		return nil, err
	}
	captured, err := capture.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	if err := capture.Recheck(ctx); err != nil {
		return nil, err
	}
	native := NativePolicy{Kind: NativePolicyKind, Version: "1.0", Policy: policyData,
		Profile: profileData, CaptureDigest: releasepolicy.SourceDigest(captured)}
	data, err := releasepolicy.SealJSON(native)
	if err != nil {
		return nil, err
	}
	_, _, _, err = DecodeNativePolicy(data)
	return data, err
}

func verifyNativeCapture(ctx context.Context, native *NativePolicy, policy *releasepolicy.Policy, capture *nativesnapshot.CollectionCapture, raw []byte) error {
	if len(raw) == 0 || releasepolicy.SourceDigest(raw) != native.CaptureDigest {
		var err error
		raw, err = capture.Marshal(ctx)
		if err != nil {
			return err
		}
		if releasepolicy.SourceDigest(raw) != native.CaptureDigest {
			return fmt.Errorf("native source/current configuration differs from precollection capture")
		}
	}
	projections, err := capture.Projections(ctx)
	if err != nil {
		return err
	}
	profile, err := nativetask.DecodeProfile(native.Profile)
	if err != nil {
		return err
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if !reflect.DeepEqual(projections[arm].Arm, policy.Arms[arm]) {
			return fmt.Errorf("native source projection differs from selected policy")
		}
		requests := map[string]models.EvidenceDigest{}
		for _, request := range profile.Arms[arm].Requests {
			requests[request.TaskID] = request.RequestDigest
		}
		if len(requests) != len(policy.Arms[arm].Plan.Tasks) {
			return fmt.Errorf("native profile request inventory differs from retained tasks")
		}
		for _, task := range policy.Arms[arm].Plan.Tasks {
			request, err := capture.Request(ctx, arm, task.ID)
			if err != nil {
				return err
			}
			digest, err := nativetask.CollectionRequestDigest(request)
			if err != nil {
				return err
			}
			if digest != requests[task.ID] {
				return fmt.Errorf("native profile request differs from retained source declaration")
			}
			if _, err := capture.Primitive(ctx, arm, task.ID); err != nil {
				return err
			}
		}
	}
	golden, err := controlledprojection.GoldenUnion(projections[releasepolicy.Baseline], projections[releasepolicy.Candidate])
	if err != nil {
		return err
	}
	if !slices.Equal(golden, policy.GoldenIDs) {
		return fmt.Errorf("native required golden union differs from retained sources")
	}
	return nil
}
