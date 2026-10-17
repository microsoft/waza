package execution

import (
	"context"
	"errors"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/embedded"
)

type evidenceJudgeClient interface {
	CopilotClient
	ForceStop()
}

type evidenceJudgeOwnedClient struct {
	*copilotClientWrapper
}

func newEvidenceJudgeClient(options *copilot.ClientOptions) evidenceJudgeClient {
	return &evidenceJudgeOwnedClient{copilotClientWrapper: &copilotClientWrapper{inner: copilot.NewClient(options)}}
}

func (c *evidenceJudgeOwnedClient) ForceStop() {
	c.inner.ForceStop()
}

func (c *evidenceJudgeOwnedClient) CreateSession(ctx context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
	session, err := c.inner.CreateSession(ctx, config)
	if err != nil {
		return nil, err
	}
	return &evidenceJudgeOwnedSession{copilotSessionWrapper: &copilotSessionWrapper{inner: session, client: c.inner}}, nil
}

type evidenceJudgeOwnedSession struct {
	*copilotSessionWrapper
}

func (s *evidenceJudgeOwnedSession) EvidenceHistory(ctx context.Context) ([]copilot.SessionEvent, error) {
	return s.inner.GetEvents(ctx)
}

func evidenceJudgeEmbeddedPath() (string, error) {
	path, err := embedded.Path()
	if err != nil {
		return "", errors.New("evidence judge embedded runtime unavailable; refusing PATH fallback")
	}
	return path, nil
}
