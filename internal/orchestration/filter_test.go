package orchestration

import (
	"testing"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleCases() []*models.TestCase {
	return []*models.TestCase{
		{TestID: "tc-001", DisplayName: "Create a REST API", Tags: []string{"fast", "red"}},
		{TestID: "tc-002", DisplayName: "Fix login bug", Tags: []string{"fast", "blue"}},
		{TestID: "tc-003", DisplayName: "Create a CLI tool", Tags: []string{"medium", "green"}},
		{TestID: "tc-004", DisplayName: "Optimize SQL query", Tags: []string{"slow", "chartreuse"}},
	}
}

func TestFilterTestCases_NoPatterns(t *testing.T) {
	cases := sampleCases()
	result, err := FilterTestCases(cases, nil, nil)
	require.NoError(t, err)
	assert.Len(t, result, 4, "empty patterns should return all cases")
}

func TestFilterTestCases_ExactName(t *testing.T) {
	cases := sampleCases()
	result, err := FilterTestCases(cases, []string{"Fix login bug"}, nil)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "tc-002", result[0].TestID)
}

func TestFilterTestCases_ExactID(t *testing.T) {
	cases := sampleCases()
	result, err := FilterTestCases(cases, []string{"tc-003"}, nil)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "Create a CLI tool", result[0].DisplayName)
}

func TestFilterTestCases_GlobPattern(t *testing.T) {
	cases := sampleCases()
	result, err := FilterTestCases(cases, []string{"Create*"}, nil)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "tc-001", result[0].TestID)
	assert.Equal(t, "tc-003", result[1].TestID)
}

func TestFilterTestCases_MultiplePatterns(t *testing.T) {
	cases := sampleCases()
	result, err := FilterTestCases(cases, []string{"tc-001", "Optimize*"}, nil)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "tc-001", result[0].TestID)
	assert.Equal(t, "tc-004", result[1].TestID)
}

func TestFilterTestCases_NoMatch(t *testing.T) {
	cases := sampleCases()
	result, err := FilterTestCases(cases, []string{"nonexistent"}, nil)
	require.NoError(t, err)
	assert.Len(t, result, 0)
}

func TestFilterTestCases_InvalidPattern(t *testing.T) {
	cases := sampleCases()
	_, err := FilterTestCases(cases, []string{"["}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid task filter pattern")
}

func TestFilterTestCases_IDGlob(t *testing.T) {
	cases := sampleCases()
	result, err := FilterTestCases(cases, []string{"tc-00?"}, nil)
	require.NoError(t, err)
	assert.Len(t, result, 4, "? should match single character in IDs")
}

func TestFilterTestCases_Tags(t *testing.T) {
	tt := []struct {
		Name       string
		Patterns   []string
		MatchedIDs []string
	}{
		{
			Name:       "exact",
			Patterns:   []string{"fast"}, // exact match
			MatchedIDs: []string{"tc-001", "tc-002"},
		},
		{
			Name: "exact",
			Patterns: []string{
				"fast", // exact match
				"red",  // same test case matche
			},
			MatchedIDs: []string{"tc-001", "tc-002"},
		},
		{
			Name:       "partial",
			Patterns:   []string{"gree*"}, // partial match
			MatchedIDs: []string{"tc-003"},
		},
		{
			Name:       "none",
			Patterns:   []string{"yellow"}, // no matches
			MatchedIDs: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.Name, func(t *testing.T) {
			cases := sampleCases()
			result, err := FilterTestCases(cases, nil, tc.Patterns)
			require.NoError(t, err)

			require.Equal(t, tc.MatchedIDs, testCaseIDs(result))
		})
	}
}

func TestFilterTestCases_TagsAndTasks_Intersection(t *testing.T) {
	tt := []struct {
		Name         string
		TagPatterns  []string
		TaskPatterns []string
		MatchedIDs   []string
	}{
		{
			Name:         "exact",
			TaskPatterns: []string{"*001"}, // wildcard match
			TagPatterns:  []string{"fast"}, // exact match
			MatchedIDs:   []string{"tc-001"},
		},
		{
			Name:         "matches task but not tag",
			TaskPatterns: []string{"*001"},
			TagPatterns:  []string{"nobody matches this"},
			MatchedIDs:   nil,
		},
		{
			Name:         "matches tag but not task",
			TaskPatterns: []string{"*999"},
			TagPatterns:  []string{"fast"},
			MatchedIDs:   nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.Name, func(t *testing.T) {
			cases := sampleCases()
			result, err := FilterTestCases(cases, tc.TaskPatterns, tc.TagPatterns)
			require.NoError(t, err)

			require.Equal(t, tc.MatchedIDs, testCaseIDs(result))
		})
	}
}

func testCaseIDs(testCases []*models.TestCase) []string {
	var ids []string
	for _, tc := range testCases {
		ids = append(ids, tc.TestID)
	}
	return ids
}
