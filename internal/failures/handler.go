package failures

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/models"
)

// Handler detects and captures failure artifacts from eval runs
type Handler struct {
	captureStdout   bool
	captureStderr   bool
	maxArtifactSize int
}

// NewHandler creates a new failure handler
func NewHandler() *Handler {
	return &Handler{
		captureStdout:   true,
		captureStderr:   true,
		maxArtifactSize: 10000, // 10KB limit per artifact
	}
}

// CaptureFailure captures failure artifacts from a failed run
func (h *Handler) CaptureFailure(result *models.RunResult, exitCode int, stderr, stdout string) {
	if result == nil {
		return
	}

	if result.Status != models.StatusFailed && result.Status != models.StatusError {
		return
	}

	artifacts := &models.FailureArtifacts{
		CapturedAt: time.Now().UTC(),
		ExitCode:   exitCode,
		Context:    make(map[string]string),
	}

	// Capture stderr and stdout with size limits
	if h.captureStderr && len(stderr) > 0 {
		artifacts.StdErr = truncate(stderr, h.maxArtifactSize)
		artifacts.Context["stderr_truncated"] = fmt.Sprintf("%v", len(stderr) > h.maxArtifactSize)
	}

	if h.captureStdout && len(stdout) > 0 {
		artifacts.StdOut = truncate(stdout, h.maxArtifactSize)
		artifacts.Context["stdout_truncated"] = fmt.Sprintf("%v", len(stdout) > h.maxArtifactSize)
	}

	// Track failed validators (sorted for deterministic output)
	for name, grader := range result.Validations {
		if !grader.Passed {
			artifacts.FailedGraders = append(artifacts.FailedGraders, name)
		}
	}
	sort.Strings(artifacts.FailedGraders)

	// Extract error patterns
	artifacts.ErrorPatterns = extractErrorPatterns(artifacts.StdErr, artifacts.StdOut, result.ErrorMsg)

	// Generate triage summary
	artifacts.TriageSummary = generateTriageSummary(artifacts, result)

	result.FailureArtifacts = artifacts
}

// extractErrorPatterns identifies common error patterns in logs
func extractErrorPatterns(stderr, stdout, errorMsg string) []string {
	patterns := []string{
		`error[:\s]+(.+)`,
		`failed[:\s]+(.+)`,
		`panic[:\s]+(.+)`,
		`fatal[:\s]+(.+)`,
		`exception[:\s]+(.+)`,
		`timeout`,
		`out of memory`,
		`permission denied`,
		`file not found`,
		`connection refused`,
	}

	matches := make(map[string]bool)
	input := strings.ToLower(stderr + "\n" + stdout + "\n" + errorMsg)

	for _, pattern := range patterns {
		re := regexp.MustCompile("(?i)" + pattern)
		found := re.FindAllStringSubmatch(input, -1)
		for _, match := range found {
			if len(match) > 1 {
				trimmed := strings.TrimSpace(match[1])
				if trimmed != "" {
					matches[trimmed] = true
				}
			} else {
				matches[pattern] = true
			}
		}
	}

	result := make([]string, 0, len(matches))
	for pattern := range matches {
		result = append(result, pattern)
	}
	sort.Strings(result)
	return result
}

// generateTriageSummary creates a human-readable failure summary
func generateTriageSummary(artifacts *models.FailureArtifacts, result *models.RunResult) string {
	var summary strings.Builder

	summary.WriteString("**Failure Triage Summary**\n")
	summary.WriteString("---\n\n")

	// Overall status
	if result.Status == models.StatusError {
		summary.WriteString("**Status:** Error\n")
	} else {
		summary.WriteString("**Status:** Failed\n")
	}

	// Failed validators
	if len(artifacts.FailedGraders) > 0 {
		summary.WriteString("\n**Failed Validators:**\n")
		for _, grader := range artifacts.FailedGraders {
			fmt.Fprintf(&summary, "- %s\n", grader)
		}
	}

	// Error patterns
	if len(artifacts.ErrorPatterns) > 0 {
		summary.WriteString("\n**Error Patterns Detected:**\n")
		for _, pattern := range artifacts.ErrorPatterns {
			fmt.Fprintf(&summary, "- %s\n", pattern)
		}
	}

	// Last error message
	if result.ErrorMsg != "" {
		summary.WriteString("\n**Last Error Message:**\n")
		fmt.Fprintf(&summary, "```\n%s\n```\n", result.ErrorMsg)
	}

	// Recommendations
	summary.WriteString("\n**Recommendations:**\n")
	summary.WriteString(generateRecommendations(artifacts, result))

	return summary.String()
}

// generateRecommendations provides actionable remediation suggestions
func generateRecommendations(artifacts *models.FailureArtifacts, result *models.RunResult) string {
	var recs strings.Builder

	patterns := strings.ToLower(strings.Join(artifacts.ErrorPatterns, " "))

	if strings.Contains(patterns, "timeout") {
		recs.WriteString("- Consider increasing timeout for long-running tasks\n")
	}
	if strings.Contains(patterns, "memory") {
		recs.WriteString("- Check memory constraints; consider reducing dataset size or batch size\n")
	}
	if strings.Contains(patterns, "permission") {
		recs.WriteString("- Verify workspace permissions and file access rights\n")
	}
	if strings.Contains(patterns, "not found") {
		recs.WriteString("- Ensure all required files/fixtures are in the context directory\n")
	}
	if strings.Contains(patterns, "connection") {
		recs.WriteString("- Check network connectivity and service availability\n")
	}

	if len(artifacts.FailedGraders) > 0 {
		fmt.Fprintf(&recs, "- Review validator: %s\n", artifacts.FailedGraders[0])
	}

	if recs.Len() == 0 {
		recs.WriteString("- Review execution logs for detailed failure analysis\n")
		recs.WriteString("- Consider running with verbose flag for more diagnostics\n")
	}

	return recs.String()
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	if len(s) <= maxLen {
		return s
	}
	const suffix = "\n... (truncated)"
	if maxLen <= len(suffix) {
		return suffix[:maxLen]
	}
	prefixLen := max(0, maxLen-len(suffix))
	return s[:prefixLen] + suffix
}
