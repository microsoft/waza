// Package usagetelemetry is Waza's opt-out product usage telemetry client
// (#1045). It is separate from the OpenTelemetry tracing in internal/telemetry
// and never reads OpenTelemetry configuration, environment, or attributes.
//
// The client ships inert: no collector endpoint is compiled in, so Resolve
// reports an inactive policy, Record* calls return immediately, and no network
// request, installation identifier, or queue write ever happens. Activation
// requires privacy/security approval, a ready collector, and an explicit
// decision on the collection default and CI policy.
package usagetelemetry

import (
	"errors"
	"net/url"
	"os"
	"strings"

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

// endpoint is the approved collector URL. It is intentionally empty and can
// only be set at build time (-ldflags -X) once activation is approved.
var endpoint = ""

// Activation decisions that remain pending (see package docs). Collection is
// off by default and CI never sends until these are explicitly changed.
var (
	defaultCollectionOn = false
	ciCollectionOn      = false
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
	ReasonCI              = "CI collection has not been approved"
	ReasonNotEnabled      = "collection is off by default and has not been enabled"
)

// Inputs are the process facts used to resolve policy.
type Inputs struct {
	Args         []string
	Getenv       func(string) string
	WorkDir      string
	SettingsPath string
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

// ResolveProcess resolves policy for the current process.
func ResolveProcess(args []string, full bool) Policy {
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	return Resolve(Inputs{Args: args, WorkDir: wd, Full: full})
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
	if truthy(getenv(CIEnv)) {
		return true
	}
	for _, name := range ciEnvVars {
		if v := getenv(name); v != "" && !falsy(v) {
			return true
		}
	}
	return false
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
