package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestParseSkillFrontmatter(t *testing.T) {
	t.Run("valid frontmatter", func(t *testing.T) {
		content := "---\nname: azure-prepare\ndescription: Prepare Azure resources\n---\nBody content"
		name, desc := parseSkillFrontmatter(content)
		assert.Equal(t, "azure-prepare", name)
		assert.Equal(t, "Prepare Azure resources", desc)
	})

	t.Run("quoted values", func(t *testing.T) {
		content := "---\nname: \"my-skill\"\ndescription: 'Deploy things'\n---\nBody"
		name, desc := parseSkillFrontmatter(content)
		assert.Equal(t, "my-skill", name)
		assert.Equal(t, "Deploy things", desc)
	})

	t.Run("no frontmatter", func(t *testing.T) {
		content := "# Just a readme\nNo frontmatter here"
		name, desc := parseSkillFrontmatter(content)
		assert.Empty(t, name)
		assert.Empty(t, desc)
	})

	t.Run("unclosed frontmatter", func(t *testing.T) {
		content := "---\nname: broken\nno closing delimiter"
		name, desc := parseSkillFrontmatter(content)
		assert.Empty(t, name)
		assert.Empty(t, desc)
	})

	t.Run("empty frontmatter", func(t *testing.T) {
		content := "---\n---\nBody"
		name, desc := parseSkillFrontmatter(content)
		assert.Empty(t, name)
		assert.Empty(t, desc)
	})

	t.Run("name only", func(t *testing.T) {
		content := "---\nname: solo-skill\n---\nBody"
		name, desc := parseSkillFrontmatter(content)
		assert.Equal(t, "solo-skill", name)
		assert.Empty(t, desc)
	})

	t.Run("windows line endings", func(t *testing.T) {
		content := "---\r\nname: win-skill\r\ndescription: Windows style\r\n---\r\nBody"
		name, desc := parseSkillFrontmatter(content)
		assert.Equal(t, "win-skill", name)
		assert.Equal(t, "Windows style", desc)
	})
}

func TestLoadSkillDefinition(t *testing.T) {
	t.Run("directory with SKILL.md", func(t *testing.T) {
		dir := t.TempDir()
		skillContent := "---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\nBody content"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644))

		sd := loadSkillDefinition(dir)
		require.NotNil(t, sd)
		assert.Equal(t, "test-skill", sd.Name)
		assert.Equal(t, "A test skill", sd.Description)
		assert.Equal(t, dir, sd.Dir)
	})

	t.Run("directory without SKILL.md", func(t *testing.T) {
		dir := t.TempDir()
		sd := loadSkillDefinition(dir)
		assert.Nil(t, sd)
	})

	t.Run("SKILL.md with no name", func(t *testing.T) {
		dir := t.TempDir()
		skillContent := "---\ndescription: No name\n---\nBody"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644))

		sd := loadSkillDefinition(dir)
		assert.Nil(t, sd)
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		sd := loadSkillDefinition("/nonexistent/path/to/skills")
		assert.Nil(t, sd)
	})
}

func TestBuildSkillSystemMessage(t *testing.T) {
	t.Run("no skills found", func(t *testing.T) {
		dirs := []string{t.TempDir(), t.TempDir()}
		msg := buildSkillSystemMessage(dirs)
		assert.Empty(t, msg)
	})

	t.Run("single skill", func(t *testing.T) {
		dir := t.TempDir()
		skillContent := "---\nname: azure-prepare\ndescription: Prepare Azure resources\n---\nBody"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0644))

		msg := buildSkillSystemMessage([]string{dir})
		assert.Contains(t, msg, "<available_skills>")
		assert.Contains(t, msg, "<name>azure-prepare</name>")
		assert.Contains(t, msg, "<description>Prepare Azure resources</description>")
		assert.Contains(t, msg, "</available_skills>")
	})

	t.Run("multiple skills", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir1, "SKILL.md"),
			[]byte("---\nname: skill-a\ndescription: First skill\n---\nBody"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir2, "SKILL.md"),
			[]byte("---\nname: skill-b\ndescription: Second skill\n---\nBody"), 0644))

		msg := buildSkillSystemMessage([]string{dir1, dir2})
		assert.Contains(t, msg, "<name>skill-a</name>")
		assert.Contains(t, msg, "<name>skill-b</name>")
	})

	t.Run("mixed directories with and without skills", func(t *testing.T) {
		dirWithSkill := t.TempDir()
		dirWithout := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dirWithSkill, "SKILL.md"),
			[]byte("---\nname: found-skill\ndescription: I exist\n---\nBody"), 0644))

		msg := buildSkillSystemMessage([]string{dirWithout, dirWithSkill})
		assert.Contains(t, msg, "<name>found-skill</name>")
		assert.NotContains(t, msg, "dirWithout")
	})

	t.Run("skill without description", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: minimal-skill\n---\nBody"), 0644))

		msg := buildSkillSystemMessage([]string{dir})
		assert.Contains(t, msg, "<name>minimal-skill</name>")
		assert.NotContains(t, msg, "<description>")
	})
}

func TestCopilotEngine_Execute_InjectsSkillSystemMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)
	sessionMock := NewMockcopilotSession(ctrl)

	// Create a skill directory with a SKILL.md
	skillDir := t.TempDir()
	skillContent := "---\nname: test-deploy\ndescription: Deploy to test env\n---\n# Test Deploy\nDeploy things"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644))

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
			// Verify that SystemMessage is set with skill definitions
			require.NotNil(t, config.SystemMessage, "SystemMessage should be set when skills are available")
			assert.Equal(t, "append", config.SystemMessage.Mode)
			assert.Contains(t, config.SystemMessage.Content, "<available_skills>")
			assert.Contains(t, config.SystemMessage.Content, "<name>test-deploy</name>")
			assert.Contains(t, config.SystemMessage.Content, "<description>Deploy to test env</description>")
			return sessionMock, nil
		})
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	_, err = engine.Execute(context.Background(), &ExecutionRequest{
		Message:    "deploy my app",
		Timeout:    time.Minute,
		SkillPaths: []string{skillDir},
		SourceDir:  t.TempDir(), // CWD without SKILL.md
	})
	require.NoError(t, err)
}

func TestCopilotEngine_Execute_NoSystemMessageWithoutSkills(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)
	sessionMock := NewMockcopilotSession(ctrl)

	sourceDir := t.TempDir() // No SKILL.md here

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
			// SystemMessage should be nil when no skills are found
			assert.Nil(t, config.SystemMessage, "SystemMessage should be nil when no skills found")
			return sessionMock, nil
		})
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	_, err = engine.Execute(context.Background(), &ExecutionRequest{
		Message:   "hello",
		Timeout:   time.Minute,
		SourceDir: sourceDir,
	})
	require.NoError(t, err)
}

func TestCopilotEngine_ResumeSession_InjectsSkillSystemMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)
	sessionMock := NewMockcopilotSession(ctrl)

	// Create a skill directory with a SKILL.md
	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: resume-skill\ndescription: Skill for resume test\n---\nBody"), 0644))

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "existing-session", gomock.Any()).DoAndReturn(
		func(ctx context.Context, sessionID string, config *copilot.ResumeSessionConfig) (CopilotSession, error) {
			require.NotNil(t, config.SystemMessage, "SystemMessage should be set on resume when skills are available")
			assert.Equal(t, "append", config.SystemMessage.Mode)
			assert.Contains(t, config.SystemMessage.Content, "<name>resume-skill</name>")
			return sessionMock, nil
		})
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "existing-session")
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("existing-session")

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	_, err = engine.Execute(context.Background(), &ExecutionRequest{
		Message:    "continue",
		SessionID:  "existing-session",
		Timeout:    time.Minute,
		SkillPaths: []string{skillDir},
		SourceDir:  t.TempDir(),
	})
	require.NoError(t, err)
}
