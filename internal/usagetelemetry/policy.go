// Package usagetelemetry is Waza's opt-out product usage telemetry client
// (#1045). It is separate from the OpenTelemetry tracing in internal/telemetry
// and never reads OpenTelemetry configuration, environment, or attributes.
//
// Released builds collect by default on developer machines and never collect
// from CI. Every opt-out (flag, environment, persistent setting, project
// config, or hosting tool denial) wins over the default.
package usagetelemetry

import (
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/microsoft/waza/internal/projectconfig"
	"gopkg.in/yaml.v3"
)

// Environment variables and flags that control product usage telemetry.
const (
	OptOutEnv  = "WAZA_TELEMETRY_OPTOUT"
	NestedEnv  = "WAZA_TELEMETRY_NESTED"
	CIEnv      = "WAZA_TELEMETRY_CI"
	NoFlagName = "no-telemetry"

	doNotTrackEnv   = "DO_NOT_TRACK"
	azdTelemetryEnv = "AZURE_DEV_COLLECT_TELEMETRY"
	azdServerEnv    = "AZD_SERVER"
)

// ApprovedEndpoint is the team collector gateway approved for #1045.
const ApprovedEndpoint = "https://apim-waza-z6cdmnvatt43k.azure-api.net/v1/events"

// endpoint can be overridden at build time (-ldflags -X); an empty value
// produces an inert build.
var endpoint = ApprovedEndpoint

// Approved activation policy (#1045): on by default for developers, never in CI.
var (
	defaultCollectionOn = true
	ciCollectionOn      = false
	// devBuildCollectionOn is only changed by OverrideActivationForTesting.
	devBuildCollectionOn = false
)

var ciEnvVars = []string{"CI", "GITHUB_ACTIONS", "TF_BUILD", "JENKINS_URL", "CODEBUILD_BUILD_ID",
	"GITLAB_CI", "BUILDKITE", "CIRCLECI", "TEAMCITY_VERSION", "BITBUCKET_BUILD_NUMBER"}

// Reasons collection is not active. They are reported by `waza telemetry status`.
const (
	ReasonNoEndpoint      = "no approved collector endpoint is configured in this build"
	ReasonFlag            = "--no-telemetry was passed"
	ReasonEnvOptOut       = OptOutEnv + " requests opt-out"
	ReasonHostDenied      = "the hosting tool denies telemetry (DO_NOT_TRACK or AZURE_DEV_COLLECT_TELEMETRY)"
	ReasonUserDisabled    = "disabled with `waza telemetry disable`"
	ReasonUserInvalid     = "the telemetry settings file is invalid or unreadable"
	ReasonProjectDisabled = "the project .waza.yaml sets telemetry.disabled"
	ReasonProjectInvalid  = "the project .waza.yaml telemetry section is invalid or unreadable"
	ReasonCI              = "CI environments are never collected"
	ReasonDevBuild        = "development builds are never collected"
	ReasonNotEnabled      = "collection is off by default and has not been enabled"
)

// Inputs are the process facts used to resolve policy.
type Inputs struct {
	Args         []string
	Getenv       func(string) string
	WorkDir      string
	SettingsPath string
	// Version is the Waza build version. Only release builds (plain X.Y.Z)
	// collect, so source and test builds stay inert.
	Version string
	// Full reads persistent settings even when no endpoint is configured.
	// Only `waza telemetry status` sets it.
	Full bool
}

// Policy is the resolved, side-effect-free collection decision.
type Policy struct {
	Enabled bool
	// Denied is true when an opt-out, invalid configuration, or unapproved
	// CI collection applies; children inherit it.
	Denied     bool
	FlagDenied bool
	CI         bool
	Nested     bool
	Reasons    []string

	endpoint     *url.URL
	settingsPath string
	distribution string
}

// Resolve computes the policy without network, identifier, or queue side
// effects. Without an approved endpoint it does not read any files.
func Resolve(in Inputs) Policy {
	getenv := in.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	p := Policy{CI: detectCI(getenv), Nested: truthy(getenv(NestedEnv)), settingsPath: in.SettingsPath,
		distribution: "standalone"}
	if getenv(azdServerEnv) != "" {
		p.distribution = "azd_extension"
	}
	deny := func(reason string) {
		p.Denied = true
		p.Reasons = append(p.Reasons, reason)
	}
	if flagDenies(in.Args) {
		p.FlagDenied = true
		deny(ReasonFlag)
	}
	if v := getenv(OptOutEnv); v != "" && !falsy(v) {
		deny(ReasonEnvOptOut)
	}
	if hostDenies(getenv) {
		deny(ReasonHostDenied)
	}
	if p.CI && !ciCollectionOn {
		deny(ReasonCI)
	}
	p.endpoint = approvedEndpoint(endpoint)
	if p.endpoint == nil {
		p.Reasons = append(p.Reasons, ReasonNoEndpoint)
		if !in.Full {
			return p
		}
	} else if !devBuildCollectionOn && !releaseBuild(in.Version) {
		p.Reasons = append(p.Reasons, ReasonDevBuild)
		if !in.Full {
			return p
		}
	}
	explicitEnable := false
	if p.settingsPath == "" {
		p.settingsPath = DefaultSettingsPath()
	}
	settings, err := LoadSettings(p.settingsPath)
	switch {
	case err != nil:
		deny(ReasonUserInvalid)
	case settings.Collection == CollectionDisabled:
		deny(ReasonUserDisabled)
	case settings.Collection == CollectionEnabled:
		explicitEnable = true
	}
	switch projectDenial(in.WorkDir) {
	case projectDisabled:
		deny(ReasonProjectDisabled)
	case projectInvalid:
		deny(ReasonProjectInvalid)
	}
	if !defaultCollectionOn && !explicitEnable {
		p.Reasons = append(p.Reasons, ReasonNotEnabled)
	}
	p.Enabled = len(p.Reasons) == 0
	return p
}

// ResolveProcess resolves policy for the current process and build version.
func ResolveProcess(args []string, full bool, version string) Policy {
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	return Resolve(Inputs{Args: args, WorkDir: wd, Full: full, Version: version})
}

// releaseBuild reports whether version is a plain X.Y.Z release stamp.
func releaseBuild(version string) bool {
	v, err := semver.StrictNewVersion(version)
	return err == nil && v.Prerelease() == "" && v.Metadata() == ""
}

// PolicyEnvKeys lists the variables that carry the telemetry policy to child
// processes. Code that builds an allowlisted child environment must keep them.
func PolicyEnvKeys() []string {
	return []string{OptOutEnv, NestedEnv, CIEnv, doNotTrackEnv, azdTelemetryEnv}
}

// Propagate exports denial and a sanitized CI marker to children. Active
// collection also marks them as nested so only the parent reports commands.
func Propagate(p Policy) {
	if p.CI {
		_ = os.Setenv(CIEnv, "1")
	}
	if p.Denied {
		_ = os.Setenv(OptOutEnv, "1")
	}
	if p.Enabled {
		_ = os.Setenv(NestedEnv, "1")
	}
}

func approvedEndpoint(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil
	}
	return u
}

func flagDenies(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--"+NoFlagName {
			return true
		}
		if value, ok := strings.CutPrefix(arg, "--"+NoFlagName+"="); ok {
			if !falsy(value) {
				return true
			}
		}
	}
	return false
}

func hostDenies(getenv func(string) string) bool {
	if v := getenv(doNotTrackEnv); v != "" && !falsy(v) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(getenv(azdTelemetryEnv))) {
	case "no", "false", "0", "off":
		return true
	}
	return false
}

func detectCI(getenv func(string) string) bool {
	if CIContextMarkerEnabled(getenv(CIEnv)) {
		return true
	}
	for _, name := range ciEnvVars {
		if v := getenv(name); v != "" && !falsy(v) {
			return true
		}
	}
	return false
}

// CIContextMarkerEnabled interprets the internal CI marker using the same
// false values as policy resolution.
func CIContextMarkerEnabled(value string) bool {
	return truthy(value)
}

func falsy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

func truthy(v string) bool {
	return v != "" && !falsy(v)
}

type projectState int

const (
	projectAllowed projectState = iota
	projectDisabled
	projectInvalid
)

// projectDenial reads only the telemetry section of the nearest .waza.yaml.
// Errors elsewhere in the file are left to the normal project loader, but a
// file that cannot be read or parsed at all is treated as denial.
func projectDenial(workDir string) projectState {
	if workDir == "" {
		return projectAllowed
	}
	path, err := projectconfig.FindPath(workDir)
	if errors.Is(err, os.ErrNotExist) {
		return projectAllowed
	}
	if err != nil {
		return projectInvalid
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is the discovered project config
	if err != nil {
		return projectInvalid
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return projectInvalid
	}
	raw, present := doc["telemetry"]
	if !present {
		return projectAllowed
	}
	section, ok := raw.(map[string]any)
	if !ok {
		return projectInvalid
	}
	state := projectAllowed
	for key, value := range section {
		disabled, isBool := value.(bool)
		if key != "disabled" || !isBool {
			return projectInvalid
		}
		if disabled {
			state = projectDisabled
		}
	}
	return state
}
