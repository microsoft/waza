package reporting

import (
	"encoding/xml"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spboyer/waza/internal/models"
)

// JUnit XML schema types

// JUnitTestSuites is the top-level container.
type JUnitTestSuites struct {
	XMLName    xml.Name         `xml:"testsuites"`
	Tests      int              `xml:"tests,attr"`
	Failures   int              `xml:"failures,attr"`
	Errors     int              `xml:"errors,attr"`
	Time       float64          `xml:"time,attr"`
	TestSuites []JUnitTestSuite `xml:"testsuite"`
}

// JUnitTestSuite maps to one evaluation run.
type JUnitTestSuite struct {
	XMLName    xml.Name        `xml:"testsuite"`
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Errors     int             `xml:"errors,attr"`
	Skipped    int             `xml:"skipped,attr"`
	Time       float64         `xml:"time,attr"`
	Timestamp  string          `xml:"timestamp,attr"`
	Properties []JUnitProperty `xml:"properties>property,omitempty"`
	TestCases  []JUnitTestCase `xml:"testcase"`
}

// JUnitTestCase maps to one eval task.
type JUnitTestCase struct {
	XMLName   xml.Name      `xml:"testcase"`
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	Failure   *JUnitFailure `xml:"failure,omitempty"`
	Error     *JUnitError   `xml:"error,omitempty"`
	Skipped   *JUnitSkipped `xml:"skipped,omitempty"`
}

// JUnitFailure represents a test assertion failure.
type JUnitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

// JUnitError represents an unexpected error during test execution.
type JUnitError struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

// JUnitSkipped marks a test as skipped.
type JUnitSkipped struct {
	Message string `xml:"message,attr,omitempty"`
}

// JUnitProperty is a key-value metadata entry.
type JUnitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// ConvertToJUnit converts an EvaluationOutcome to JUnit XML format.
func ConvertToJUnit(outcome *models.EvaluationOutcome) *JUnitTestSuites {
	durationSec := float64(outcome.Digest.DurationMs) / 1000.0

	suite := JUnitTestSuite{
		Name:      outcome.BenchName,
		Tests:     outcome.Digest.TotalTests,
		Failures:  outcome.Digest.Failed,
		Errors:    outcome.Digest.Errors,
		Skipped:   outcome.Digest.Skipped,
		Time:      durationSec,
		Timestamp: outcome.Timestamp.Format(time.RFC3339),
		Properties: []JUnitProperty{
			{Name: "skill", Value: outcome.SkillTested},
			{Name: "model", Value: outcome.Setup.ModelID},
			{Name: "engine", Value: outcome.Setup.EngineType},
			{Name: "score", Value: fmt.Sprintf("%.4f", outcome.Digest.AggregateScore)},
		},
	}

	for _, to := range outcome.TestOutcomes {
		tc := convertTestOutcome(outcome.SkillTested, &to)
		suite.TestCases = append(suite.TestCases, tc)
	}

	return &JUnitTestSuites{
		Tests:      outcome.Digest.TotalTests,
		Failures:   outcome.Digest.Failed,
		Errors:     outcome.Digest.Errors,
		Time:       durationSec,
		TestSuites: []JUnitTestSuite{suite},
	}
}

func convertTestOutcome(skill string, to *models.TestOutcome) JUnitTestCase {
	// Compute duration from stats or runs
	var durationSec float64
	if to.Stats != nil && to.Stats.AvgDurationMs > 0 {
		durationSec = float64(to.Stats.AvgDurationMs) / 1000.0
	} else if len(to.Runs) > 0 {
		var totalMs int64
		for _, r := range to.Runs {
			totalMs += r.DurationMs
		}
		durationSec = float64(totalMs) / float64(len(to.Runs)) / 1000.0
	}

	tc := JUnitTestCase{
		Name:      to.DisplayName,
		Classname: skill,
		Time:      durationSec,
	}

	switch to.Status {
	case models.StatusFailed:
		tc.Failure = buildFailure(to)
	case models.StatusError:
		tc.Error = buildError(to)
	}

	return tc
}

func buildFailure(to *models.TestOutcome) *JUnitFailure {
	// Collect failed graders from the first failed run
	var details string
	for _, run := range to.Runs {
		if run.Status != models.StatusPassed {
			details = formatFailedGraders(run.Validations)
			break
		}
	}

	score := 0.0
	if to.Stats != nil {
		score = to.Stats.AvgScore
	}

	return &JUnitFailure{
		Message: fmt.Sprintf("%s: score=%.2f", to.DisplayName, score),
		Type:    "GraderFailure",
		Body:    details,
	}
}

func buildError(to *models.TestOutcome) *JUnitError {
	var msg string
	for _, run := range to.Runs {
		if run.ErrorMsg != "" {
			msg = run.ErrorMsg
			break
		}
	}
	if msg == "" {
		msg = "execution error"
	}

	return &JUnitError{
		Message: msg,
		Type:    "ExecutionError",
	}
}

func formatFailedGraders(validations map[string]models.GraderResults) string {
	if len(validations) == 0 {
		return ""
	}

	// Sort for deterministic output
	names := make([]string, 0, len(validations))
	for name := range validations {
		names = append(names, name)
	}
	sort.Strings(names)

	var result string
	for _, name := range names {
		g := validations[name]
		if !g.Passed {
			result += fmt.Sprintf("[FAIL] %s (%s): score=%.2f — %s\n", name, g.Type, g.Score, g.Feedback)
		}
	}
	return result
}

// WriteJUnitXML writes JUnit XML to the specified file path.
func WriteJUnitXML(outcome *models.EvaluationOutcome, path string) error {
	suites := ConvertToJUnit(outcome)

	data, err := xml.MarshalIndent(suites, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JUnit XML: %w", err)
	}

	output := append([]byte(xml.Header), data...)
	return os.WriteFile(path, output, 0644)
}
