package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheKey(t *testing.T) {
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-spec",
		},
		SkillName: "test-skill",
		Config: models.Config{
			ModelID:    "gpt-4",
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
		},
		Graders: []models.GraderConfig{
			{Kind: models.GraderKindRegex, Identifier: "test-grader"},
		},
	}

	task := &models.TestCase{
		TestID:      "test-1",
		DisplayName: "Test Task",
		Stimulus: models.TestStimulus{
			Message: "Do something",
			Resources: []models.ResourceRef{
				{Location: "file1.txt"},
				{Location: "file2.txt"},
			},
		},
	}

	tempDir := t.TempDir()

	// Create fixture files
	file1 := filepath.Join(tempDir, "file1.txt")
	file2 := filepath.Join(tempDir, "file2.txt")
	require.NoError(t, os.WriteFile(file1, []byte("content1"), 0644))
	require.NoError(t, os.WriteFile(file2, []byte("content2"), 0644))

	// Generate key
	key1, err := CacheKey(spec, task, tempDir)
	require.NoError(t, err)
	assert.NotEmpty(t, key1)
	assert.Len(t, key1, 64) // SHA256 hex is 64 chars

	// Same inputs should produce same key
	key2, err := CacheKey(spec, task, tempDir)
	require.NoError(t, err)
	assert.Equal(t, key1, key2)
}

func TestCacheKey_DifferentModelChangesKey(t *testing.T) {
	spec1 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:    "gpt-4",
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
		},
	}

	spec2 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:    "gpt-4o", // Different model
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
		},
	}

	task := &models.TestCase{
		TestID:      "test-1",
		DisplayName: "Test",
		Stimulus: models.TestStimulus{
			Message: "Test",
		},
	}

	key1, err := CacheKey(spec1, task, "")
	require.NoError(t, err)

	key2, err := CacheKey(spec2, task, "")
	require.NoError(t, err)

	assert.NotEqual(t, key1, key2)
}

func TestCacheKey_DifferentSkillPathsChangesKey(t *testing.T) {
	spec1 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:    "gpt-4",
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
			SkillPaths: []string{"skills/my-skill"},
		},
	}

	spec2 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:    "gpt-4",
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
			SkillPaths: []string{}, // No skills (baseline pass)
		},
	}

	task := &models.TestCase{
		TestID:      "test-1",
		DisplayName: "Test",
		Stimulus: models.TestStimulus{
			Message: "Test",
		},
	}

	key1, err := CacheKey(spec1, task, "")
	require.NoError(t, err)

	key2, err := CacheKey(spec2, task, "")
	require.NoError(t, err)

	assert.NotEqual(t, key1, key2, "with-skills and without-skills runs must have different cache keys")
}

func TestCacheKey_DifferentFixturesChangesKey(t *testing.T) {
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:    "gpt-4",
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
		},
	}

	task := &models.TestCase{
		TestID:      "test-1",
		DisplayName: "Test",
		Stimulus: models.TestStimulus{
			Message: "Test",
			Resources: []models.ResourceRef{
				{Location: "file1.txt"},
			},
		},
	}

	tempDir := t.TempDir()
	file1 := filepath.Join(tempDir, "file1.txt")
	require.NoError(t, os.WriteFile(file1, []byte("content1"), 0644))

	key1, err := CacheKey(spec, task, tempDir)
	require.NoError(t, err)

	// Change file content
	require.NoError(t, os.WriteFile(file1, []byte("different content"), 0644))

	key2, err := CacheKey(spec, task, tempDir)
	require.NoError(t, err)

	assert.NotEqual(t, key1, key2)
}

func TestCache_GetPut(t *testing.T) {
	cacheDir := t.TempDir()
	c := New(cacheDir)

	key := "test-key-123"
	outcome := &models.TestOutcome{
		TestID:      "test-1",
		DisplayName: "Test Task",
		Status:      models.StatusPassed,
		Runs: []models.RunResult{
			{
				RunNumber:  1,
				Status:     models.StatusPassed,
				DurationMs: 1000,
			},
		},
	}

	// Cache miss
	retrieved, found := c.Get(key)
	assert.False(t, found)
	assert.Nil(t, retrieved)

	// Store in cache
	err := c.Put(key, outcome)
	require.NoError(t, err)

	// Cache hit
	retrieved, found = c.Get(key)
	assert.True(t, found)
	require.NotNil(t, retrieved)
	assert.Equal(t, outcome.TestID, retrieved.TestID)
	assert.Equal(t, outcome.DisplayName, retrieved.DisplayName)
	assert.Equal(t, outcome.Status, retrieved.Status)
	assert.Len(t, retrieved.Runs, 1)
}

func TestCache_Clear(t *testing.T) {
	cacheDir := t.TempDir()
	c := New(cacheDir)

	// Add some entries
	key1 := "key1"
	key2 := "key2"
	outcome := &models.TestOutcome{
		TestID: "test",
		Status: models.StatusPassed,
	}

	require.NoError(t, c.Put(key1, outcome))
	require.NoError(t, c.Put(key2, outcome))

	// Verify entries exist
	_, found := c.Get(key1)
	assert.True(t, found)
	_, found = c.Get(key2)
	assert.True(t, found)

	// Clear cache
	err := c.Clear()
	require.NoError(t, err)

	// Verify cache is empty
	_, found = c.Get(key1)
	assert.False(t, found)
	_, found = c.Get(key2)
	assert.False(t, found)

	// Directory should not exist
	_, err = os.Stat(cacheDir)
	assert.True(t, os.IsNotExist(err))
}

func TestCache_EmptyDir(t *testing.T) {
	c := New("")

	// Get should return false
	_, found := c.Get("any-key")
	assert.False(t, found)

	// Put should be no-op
	outcome := &models.TestOutcome{TestID: "test"}
	err := c.Put("key", outcome)
	assert.NoError(t, err)

	// Clear should be no-op
	err = c.Clear()
	assert.NoError(t, err)
}

func TestHasNonDeterministicGraders(t *testing.T) {
	tests := []struct {
		name     string
		graders  []models.GraderConfig
		expected bool
	}{
		{
			name:     "no graders",
			graders:  []models.GraderConfig{},
			expected: false,
		},
		{
			name: "only deterministic graders",
			graders: []models.GraderConfig{
				{Kind: models.GraderKindRegex},
				{Kind: models.GraderKindFile},
				{Kind: models.GraderKindInlineScript},
			},
			expected: false,
		},
		{
			name: "has behavior grader",
			graders: []models.GraderConfig{
				{Kind: models.GraderKindRegex},
				{Kind: models.GraderKindBehavior},
			},
			expected: true,
		},
		{
			name: "has prompt grader",
			graders: []models.GraderConfig{
				{Kind: models.GraderKindRegex},
				{Kind: models.GraderKindPrompt},
			},
			expected: true,
		},
		{
			name: "has both non-deterministic graders",
			graders: []models.GraderConfig{
				{Kind: models.GraderKindBehavior},
				{Kind: models.GraderKindPrompt},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &models.BenchmarkSpec{
				Graders: tt.graders,
			}
			result := HasNonDeterministicGraders(spec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCacheKey_FixtureOrdering(t *testing.T) {
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:    "gpt-4",
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
		},
	}

	// Different fixture order in task definition will produce different keys
	// because the task structure itself is different. This is acceptable.
	task1 := &models.TestCase{
		TestID: "test-1",
		Stimulus: models.TestStimulus{
			Message: "Test",
			Resources: []models.ResourceRef{
				{Location: "b.txt"},
				{Location: "a.txt"},
				{Location: "c.txt"},
			},
		},
	}

	task2 := &models.TestCase{
		TestID: "test-1",
		Stimulus: models.TestStimulus{
			Message: "Test",
			Resources: []models.ResourceRef{
				{Location: "a.txt"},
				{Location: "c.txt"},
				{Location: "b.txt"},
			},
		},
	}

	tempDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "a.txt"), []byte("a"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "b.txt"), []byte("b"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "c.txt"), []byte("c"), 0644))

	key1, err := CacheKey(spec, task1, tempDir)
	require.NoError(t, err)

	key2, err := CacheKey(spec, task2, tempDir)
	require.NoError(t, err)

	// Different task structure = different keys (this is expected)
	assert.NotEqual(t, key1, key2, "different fixture order in task creates different cache keys")
}

func TestCacheKey_MissingFixtures(t *testing.T) {
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:    "gpt-4",
			EngineType: "copilot-sdk",
			TimeoutSec: 300,
		},
	}

	task := &models.TestCase{
		TestID: "test-1",
		Stimulus: models.TestStimulus{
			Message: "Test",
			Resources: []models.ResourceRef{
				{Location: "nonexistent.txt"},
			},
		},
	}

	tempDir := t.TempDir()

	// Should not error on missing fixtures
	key, err := CacheKey(spec, task, tempDir)
	require.NoError(t, err)
	assert.NotEmpty(t, key)
}

func TestCacheKey_DifferentRunsPerTaskChangesKey(t *testing.T) {
	spec1 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:     "gpt-4",
			EngineType:  "copilot-sdk",
			TimeoutSec:  300,
			RunsPerTest: 3,
		},
	}

	spec2 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "test"},
		SkillName:    "skill",
		Config: models.Config{
			ModelID:     "gpt-4",
			EngineType:  "copilot-sdk",
			TimeoutSec:  300,
			RunsPerTest: 5, // Different runs
		},
	}

	task := &models.TestCase{
		TestID: "test-1",
		Stimulus: models.TestStimulus{
			Message: "Test",
		},
	}

	key1, err := CacheKey(spec1, task, "")
	require.NoError(t, err)

	key2, err := CacheKey(spec2, task, "")
	require.NoError(t, err)

	assert.NotEqual(t, key1, key2, "changing RunsPerTest should change cache key")
}

func TestCacheKey_NoHashCollision(t *testing.T) {
	// Test that field delimiters prevent hash collisions
	spec1 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "ab"},
		SkillName:    "cd",
		Config: models.Config{
			ModelID:     "gpt-4",
			EngineType:  "copilot-sdk",
			TimeoutSec:  300,
			RunsPerTest: 1,
		},
	}

	spec2 := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "abc"},
		SkillName:    "d",
		Config: models.Config{
			ModelID:     "gpt-4",
			EngineType:  "copilot-sdk",
			TimeoutSec:  300,
			RunsPerTest: 1,
		},
	}

	task := &models.TestCase{
		TestID: "test-1",
		Stimulus: models.TestStimulus{
			Message: "Test",
		},
	}

	key1, err := CacheKey(spec1, task, "")
	require.NoError(t, err)

	key2, err := CacheKey(spec2, task, "")
	require.NoError(t, err)

	assert.NotEqual(t, key1, key2, "field delimiters should prevent hash collisions")
}

func TestCache_Clear_SafetyChecks(t *testing.T) {
	t.Run("refuses to clear directory with subdirectories", func(t *testing.T) {
		cacheDir := t.TempDir()
		c := New(cacheDir)

		// Add a cache file
		outcome := &models.TestOutcome{TestID: "test", Status: models.StatusPassed}
		require.NoError(t, c.Put("key1", outcome))

		// Add a subdirectory
		subDir := filepath.Join(cacheDir, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0755))

		// Clear should fail
		err := c.Clear()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "subdirectories")

		// Cache directory should still exist
		_, err = os.Stat(cacheDir)
		assert.NoError(t, err)
	})

	t.Run("refuses to clear directory with non-json files", func(t *testing.T) {
		cacheDir := t.TempDir()
		c := New(cacheDir)

		// Add a cache file
		outcome := &models.TestOutcome{TestID: "test", Status: models.StatusPassed}
		require.NoError(t, c.Put("key1", outcome))

		// Add a non-JSON file
		nonCacheFile := filepath.Join(cacheDir, "README.txt")
		require.NoError(t, os.WriteFile(nonCacheFile, []byte("test"), 0644))

		// Clear should fail
		err := c.Clear()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "non-cache files")

		// Cache directory should still exist
		_, err = os.Stat(cacheDir)
		assert.NoError(t, err)
	})

	t.Run("successfully clears valid cache directory", func(t *testing.T) {
		cacheDir := t.TempDir()
		c := New(cacheDir)

		// Add cache files
		outcome := &models.TestOutcome{TestID: "test", Status: models.StatusPassed}
		require.NoError(t, c.Put("key1", outcome))
		require.NoError(t, c.Put("key2", outcome))

		// Clear should succeed
		err := c.Clear()
		assert.NoError(t, err)

		// Directory should not exist
		_, err = os.Stat(cacheDir)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("successfully clears empty cache directory", func(t *testing.T) {
		cacheDir := t.TempDir()
		c := New(cacheDir)

		// Clear empty directory should succeed
		err := c.Clear()
		assert.NoError(t, err)

		// Directory should not exist
		_, err = os.Stat(cacheDir)
		assert.True(t, os.IsNotExist(err))
	})
}

func TestCache_ConcurrentOperations(t *testing.T) {
	cacheDir := t.TempDir()
	c := New(cacheDir)

	// Create test outcomes for different keys
	numGoroutines := 10
	numOperations := 50

	t.Run("concurrent Put operations on different keys", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					key := fmt.Sprintf("key-%d-%d", id, j)
					outcome := &models.TestOutcome{
						TestID: fmt.Sprintf("test-%d-%d", id, j),
						Status: models.StatusPassed,
					}
					err := c.Put(key, outcome)
					assert.NoError(t, err)
				}
			}(i)
		}
		wg.Wait()

		// Verify all entries were written
		entries, err := os.ReadDir(cacheDir)
		require.NoError(t, err)
		assert.Equal(t, numGoroutines*numOperations, len(entries))
	})

	t.Run("concurrent Get operations", func(t *testing.T) {
		// Pre-populate cache
		testKey := "shared-key"
		testOutcome := &models.TestOutcome{
			TestID: "shared-test",
			Status: models.StatusPassed,
		}
		require.NoError(t, c.Put(testKey, testOutcome))

		// Concurrent reads
		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					outcome, found := c.Get(testKey)
					assert.True(t, found)
					if found {
						assert.Equal(t, "shared-test", outcome.TestID)
					}
				}
			}()
		}
		wg.Wait()
	})

	t.Run("concurrent Put on same key", func(t *testing.T) {
		// This tests that concurrent writes to the same key don't cause corruption
		sharedKey := "same-key"
		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				outcome := &models.TestOutcome{
					TestID: fmt.Sprintf("test-%d", id),
					Status: models.StatusPassed,
				}
				err := c.Put(sharedKey, outcome)
				assert.NoError(t, err)
			}(i)
		}
		wg.Wait()

		// Verify the cache file is valid JSON and can be read
		outcome, found := c.Get(sharedKey)
		assert.True(t, found, "cache entry should exist after concurrent writes")
		assert.NotNil(t, outcome, "cached outcome should be valid")
	})

	t.Run("concurrent mixed operations", func(t *testing.T) {
		// Mix of Gets and Puts
		var wg sync.WaitGroup
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					if j%2 == 0 {
						// Put operation
						key := fmt.Sprintf("mixed-key-%d", id)
						outcome := &models.TestOutcome{
							TestID: fmt.Sprintf("mixed-test-%d", id),
							Status: models.StatusPassed,
						}
						require.NoError(t, c.Put(key, outcome))
					} else {
						// Get operation
						key := fmt.Sprintf("mixed-key-%d", id)
						_, _ = c.Get(key)
					}
				}
			}(i)
		}
		wg.Wait()
	})
}
