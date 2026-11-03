package execution

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

// UserInputScript answers native SDK user-input requests from an ordered,
// fixture-authored script and records every request. When a fallback (the
// adaptive responder) is installed, requests the next scripted entry does not
// match are answered by it instead. Otherwise unscripted or mismatched
// requests fail closed: the SDK receives an error, never a default answer.
// One script is shared by all turns of a single run.
type UserInputScript struct {
	mu         sync.Mutex
	responses  []models.UserInputResponseConfig
	patterns   []*regexp.Regexp
	next       int
	records    []models.UserInputRecord
	rejections []error
	fallback   UserInputFallback
	turnCtx    context.Context
	inflight   sync.WaitGroup
	ended      bool
}

// UserInputDrainTimeout bounds how long Drain waits for callbacks still answering.
// Responder calls use the asking turn's context, so they normally end with it.
var UserInputDrainTimeout = 30 * time.Second

// UserInputFallback answers a native user-input request that no scripted
// entry handles. ctx belongs to the agent turn that asked, so the call ends
// with that turn. Returning an error fails the request closed.
type UserInputFallback func(ctx context.Context, request copilot.UserInputRequest) (string, error)

// NewUserInputScript validates responses and returns a fresh script.
func NewUserInputScript(responses []models.UserInputResponseConfig) (*UserInputScript, error) {
	if err := models.ValidateUserInputResponses(responses); err != nil {
		return nil, err
	}
	s := &UserInputScript{
		responses: slices.Clone(responses),
		patterns:  make([]*regexp.Regexp, len(responses)),
		records:   []models.UserInputRecord{},
	}
	for i, response := range responses {
		if response.Question == "" {
			continue
		}
		pattern, err := models.CompileUserInputQuestion(response.Question)
		if err != nil {
			return nil, fmt.Errorf("user_input_responses[%d].question is not a valid regular expression: %w", i, err)
		}
		s.patterns[i] = pattern
	}
	return s, nil
}

// SetFallback installs the answerer for unscripted requests. It must be called
// before the script is used. Fallback calls are serialized with the script.
func (s *UserInputScript) SetFallback(fallback UserInputFallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = fallback
}

// Handler returns the SDK callback, or nil for a nil script.
func (s *UserInputScript) Handler() copilot.UserInputHandler {
	if s == nil {
		return nil
	}
	return s.handle
}

// BindTurn makes ctx the context passed to the fallback until the returned
// release function is called. An engine must bind each agent turn; questions
// that arrive outside one, or after it was canceled, are rejected.
func (s *UserInputScript) BindTurn(ctx context.Context) func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	s.turnCtx = ctx
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.turnCtx == ctx {
			s.turnCtx = nil
		}
		s.mu.Unlock()
	}
}

func (s *UserInputScript) handle(request copilot.UserInputRequest, invocation copilot.UserInputInvocation) (copilot.UserInputResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		// The run's records are already saved; a late question cannot be recorded.
		return copilot.UserInputResponse{}, errors.New("native user input request rejected: the run already ended")
	}
	s.inflight.Add(1)
	defer s.inflight.Done()

	// Reserve the record slot so arrival order is kept while the lock is
	// released for a responder call.
	index := len(s.records)
	s.records = append(s.records, models.UserInputRecord{
		Sequence:  index + 1,
		SessionID: invocation.SessionID,
		Question:  request.Question,
		Choices:   slices.Clone(request.Choices),
	})

	// Answer only while the asking turn is active, whatever the answer source.
	if s.turnCtx == nil {
		return s.reject(index, "the question arrived outside an active agent turn")
	}
	if err := s.turnCtx.Err(); err != nil {
		return s.reject(index, fmt.Sprintf("the asking agent turn already ended: %v", err))
	}

	var answer string
	switch {
	case s.next < len(s.responses) && s.matches(s.next, request.Question):
		s.records[index].Source = models.UserInputSourceScript
		answer = *s.responses[s.next].Answer
		s.next++
	case s.fallback != nil:
		s.records[index].Source = models.UserInputSourceResponder
		fallback, ctx := s.fallback, s.turnCtx
		s.mu.Unlock()
		reply, err := fallback(ctx, request)
		s.mu.Lock()
		if failed := s.records[index].Error; failed != "" {
			// Drain already ended this request as an error.
			return copilot.UserInputResponse{}, errors.New(failed)
		}
		if err != nil {
			return s.reject(index, fmt.Sprintf("responder could not answer: %v", err))
		}
		answer = reply
	case s.next < len(s.responses):
		return s.reject(index, fmt.Sprintf("question does not match user_input_responses[%d].question", s.next))
	default:
		return s.reject(index, "no scripted user_input_responses entry remains")
	}

	// An answer equal to an offered choice selects it; anything else is typed.
	wasFreeform := !slices.Contains(request.Choices, answer)
	if wasFreeform && request.AllowFreeform != nil && !*request.AllowFreeform {
		return s.reject(index, fmt.Sprintf("answer %q is not an offered choice and the request does not allow typed answers", answer))
	}
	s.records[index].Answer = &answer
	s.records[index].WasFreeform = &wasFreeform
	return copilot.UserInputResponse{Answer: answer, WasFreeform: wasFreeform}, nil
}

func (s *UserInputScript) matches(index int, question string) bool {
	pattern := s.patterns[index]
	return pattern == nil || pattern.MatchString(question)
}

// reject records a fail-closed request. The caller holds s.mu.
func (s *UserInputScript) reject(index int, reason string) (copilot.UserInputResponse, error) {
	err := fmt.Errorf("native user input request %d rejected: %s", index+1, reason)
	s.records[index].Error = err.Error()
	s.rejections = append(s.rejections, err)
	return copilot.UserInputResponse{}, err
}

// Records returns the requests observed so far, in arrival order. A request
// still waiting for the responder has neither Answer nor Error.
func (s *UserInputScript) Records() []models.UserInputRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records)
}

// rejectionCount returns how many requests have been rejected so far.
func (s *UserInputScript) rejectionCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rejections)
}

// rejectionsSince returns rejections recorded after mark, joined.
func (s *UserInputScript) rejectionsSince(mark int) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if mark >= len(s.rejections) {
		return nil
	}
	return errors.Join(s.rejections[mark:]...)
}

// Drain stops accepting new requests, waits within a bound for callbacks
// still answering, then ends any request that is still pending as an error so
// no record is left without an answer or error.
func (s *UserInputScript) Drain() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(UserInputDrainTimeout):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		if s.records[i].Answer == nil && s.records[i].Error == "" {
			_, _ = s.reject(i, "the responder did not answer before the run ended")
		}
	}
}

// Finish drains pending callbacks, then reports any rejection or unused
// scripted answer for the run.
func (s *UserInputScript) Finish() error {
	if s == nil {
		return nil
	}
	s.Drain()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rejections) > 0 {
		return errors.Join(s.rejections...)
	}
	if remaining := len(s.responses) - s.next; remaining > 0 {
		return fmt.Errorf("user_input_responses: %d scripted answer(s) were not used, starting at user_input_responses[%d]", remaining, s.next)
	}
	return nil
}
