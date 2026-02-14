package utils

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolvePaths(t *testing.T) {
	tests := []struct {
		name      string
		paths     []string
		baseDir   string
		expected  []string
	}{
		{
			name:     "empty list",
			paths:    []string{},
			baseDir:  "/base",
			expected: nil,
		},
		{
			name:     "nil list",
			paths:    nil,
			baseDir:  "/base",
			expected: nil,
		},
		{
			name:     "absolute paths unchanged",
			paths:    []string{"/abs/path1", "/abs/path2"},
			baseDir:  "/base",
			expected: []string{"/abs/path1", "/abs/path2"},
		},
		{
			name:     "relative paths resolved",
			paths:    []string{"rel1", "rel2/sub"},
			baseDir:  "/base",
			expected: []string{"/base/rel1", "/base/rel2/sub"},
		},
		{
			name:     "mixed paths",
			paths:    []string{"/abs", "rel", "../parent"},
			baseDir:  "/base/sub",
			expected: []string{"/abs", "/base/sub/rel", "/base/parent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolvePaths(tt.paths, tt.baseDir)
			
			// Clean paths for comparison (normalize separators and . .. references)
			if tt.expected != nil {
				cleanExpected := make([]string, len(tt.expected))
				for i, p := range tt.expected {
					cleanExpected[i] = filepath.Clean(p)
				}
				cleanResult := make([]string, len(result))
				for i, p := range result {
					cleanResult[i] = filepath.Clean(p)
				}
				assert.Equal(t, cleanExpected, cleanResult)
			} else {
				assert.Nil(t, result)
			}
		})
	}
}
