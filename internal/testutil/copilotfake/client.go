package copilotfake

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/execution"
)

// Client drives the production CopilotEngine without launching a CLI or model.
type Client struct {
	Send       func(*copilot.SessionConfig, copilot.MessageOptions) ([]copilot.SessionEvent, error)
	Disconnect func(*copilot.SessionConfig) error
	Creates    atomic.Int64
	Resumes    atomic.Int64
}

func (c *Client) Engine() *execution.CopilotEngine {
	return execution.NewCopilotEngineBuilder("actor-model", &execution.CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return c },
	}).Build()
}

func (c *Client) Start(context.Context) error                             { return nil }
func (c *Client) Stop() error                                             { return nil }
func (c *Client) DeleteSession(context.Context, string) error             { return nil }
func (c *Client) ListModels(context.Context) ([]copilot.ModelInfo, error) { return nil, nil }
func (c *Client) GetAuthStatus(context.Context) (*copilot.GetAuthStatusResponse, error) {
	return &copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil
}
func (c *Client) CreateSession(_ context.Context, config *copilot.SessionConfig) (execution.CopilotSession, error) {
	return &session{client: c, config: config, id: fmt.Sprintf("actor-%d", c.Creates.Add(1))}, nil
}
func (c *Client) ResumeSessionWithOptions(_ context.Context, id string, config *copilot.ResumeSessionConfig) (execution.CopilotSession, error) {
	c.Resumes.Add(1)
	return &session{client: c, id: id, config: &copilot.SessionConfig{
		Model: config.Model, Tools: config.Tools, WorkingDirectory: config.WorkingDirectory,
	}}, nil
}

type session struct {
	client   *Client
	config   *copilot.SessionConfig
	id       string
	mu       sync.Mutex
	handlers []copilot.SessionEventHandler
}

func (s *session) SessionID() string { return s.id }
func (s *session) On(handler copilot.SessionEventHandler) func() {
	s.mu.Lock()
	index := len(s.handlers)
	s.handlers = append(s.handlers, handler)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.handlers[index] = nil
		s.mu.Unlock()
	}
}
func (s *session) SendAndWait(ctx context.Context, options copilot.MessageOptions) (*copilot.SessionEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events, err := s.client.Send(s.config, options)
	for _, event := range events {
		s.mu.Lock()
		handlers := append([]copilot.SessionEventHandler(nil), s.handlers...)
		s.mu.Unlock()
		for _, handler := range handlers {
			if handler != nil {
				handler(event)
			}
		}
	}
	return nil, err
}
func (s *session) Disconnect() error {
	if s.client.Disconnect != nil {
		return s.client.Disconnect(s.config)
	}
	return nil
}
func (s *session) UsageMetrics(context.Context) (*rpc.UsageGetMetricsResult, error) {
	zero := 0.0
	return &rpc.UsageGetMetricsResult{TotalNanoAiu: &zero}, nil
}
func (s *session) ShutdownUsage(context.Context) (*copilot.SessionShutdownData, error) {
	return nil, nil
}
