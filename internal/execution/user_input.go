package execution

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"

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
}

// NewUserInputScript validates responses and returns a fresh script.
func NewUserInputScript(responses []models.UserInputResponseConfig) (*UserInputScript, error) {
	if err := models.ValidateUserInputResponses(responses); err != nil {
		return nil, err
	}
	s := &UserInputScript{
		responses: slices.Clone(responses),
		patterns:  make([]*regexp.Regexp, len(responses)),
	}
	if s.responses == nil {
		s.responses = []models.UserInputResponseConfig{}
	}
	for i, response := range responses {
		if response.QuestionRegex == "" {
			continue
		}
		pattern, err := models.CompileUserInputQuestionRegex(response.QuestionRegex)
		if err != nil {
			return nil, fmt.Errorf("user_input_responses[%d].question_regex is invalid: %w", i, err)
		}
		s.patterns[i] = pattern
	}
	return s, nil
}

// Handler returns the SDK callback, or nil for a nil script.
func (s *UserInputScript) Handler() copilot.UserInputHandler {
	if s == nil {
		return nil
	}
	return s.handle
}

func (s *UserInputScript) handle(request copilot.UserInputRequest, invocation copilot.UserInputInvocation) (copilot.UserInputResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := models.UserInputRecord{
		Sequence:  len(s.records) + 1,
		SessionID: invocation.SessionID,
		Question:  request.Question,
		Choices:   slices.Clone(request.Choices),
	}
	if request.AllowFreeform != nil {
		allow := *request.AllowFreeform
		record.AllowFreeform = &allow
	}
	reject := func(reason string) (copilot.UserInputResponse, error) {
		err := fmt.Errorf("native user input request %d rejected: %s", record.Sequence, reason)
		record.Outcome = models.UserInputOutcomeRejected
		record.Error = err.Error()
		s.records = append(s.records, record)
		s.rejections = append(s.rejections, err)
		return copilot.UserInputResponse{}, err
	}

	// The next scripted entry answers a matching question. With a fallback,
	// a non-matching question leaves the entry pending for a later request;
	// without one, it fails closed.
	if s.next >= len(s.responses) || (s.fallback != nil && !s.questionMatches(s.next, request.Question)) {
		if s.fallback == nil {
			return reject("no scripted user_input_responses entry remains")
		}
		return s.answerWithFallback(request, &record, reject)
	}
	index := s.next
	s.next++
	record.ResponseIndex = &index
	record.Source = models.UserInputSourceScript
	scripted := s.responses[index]

	if !s.questionMatches(index, request.Question) {
		field := "question"
		if s.patterns[index] != nil {
			field = "question_regex"
		}
		return reject(fmt.Sprintf("question does not match user_input_responses[%d].%s", index, field))
	}
	if scripted.Choices != nil && !slices.Equal(*scripted.Choices, request.Choices) {
		return reject(fmt.Sprintf("offered choices do not match user_input_responses[%d].choices", index))
	}

	var response copilot.UserInputResponse
	switch {
	case scripted.Choice != nil:
		if !slices.Contains(request.Choices, *scripted.Choice) {
			return reject(fmt.Sprintf("user_input_responses[%d].choice was not offered", index))
		}
		response = copilot.UserInputResponse{Answer: *scripted.Choice}
	case scripted.Freeform != nil:
		if request.AllowFreeform != nil && !*request.AllowFreeform {
			return reject(fmt.Sprintf("user_input_responses[%d].freeform is not allowed by the request", index))
		}
		response = copilot.UserInputResponse{Answer: *scripted.Freeform, WasFreeform: true}
	default:
		return reject(fmt.Sprintf("user_input_responses[%d] has no answer", index))
	}

	answer, wasFreeform := response.Answer, response.WasFreeform
	record.Outcome = models.UserInputOutcomeAnswered
	record.Answer = &answer
	record.WasFreeform = &wasFreeform
	s.records = append(s.records, record)
	return response, nil
}

func (s *UserInputScript) questionMatches(index int, question string) bool {
	if pattern := s.patterns[index]; pattern != nil {
		return pattern.MatchString(question)
	}
	return question == s.responses[index].Question
}

// answerWithFallback asks the fallback (the adaptive responder) and validates
// its answer against the request exactly as a scripted answer would be.
func (s *UserInputScript) answerWithFallback(request copilot.UserInputRequest, record *models.UserInputRecord,
	reject func(string) (copilot.UserInputResponse, error)) (copilot.UserInputResponse, error) {
	record.Source = models.UserInputSourceResponder
	response, err := s.fallback(request)
	if err != nil {
		return reject(fmt.Sprintf("responder could not answer: %v", err))
	}
	if response.WasFreeform {
		if request.AllowFreeform != nil && !*request.AllowFreeform {
			return reject("responder gave a freeform answer, which the request does not allow")
		}
	} else if !slices.Contains(request.Choices, response.Answer) {
		return reject("responder selected a choice that was not offered")
	}
	answer, wasFreeform := response.Answer, response.WasFreeform
	record.Outcome = models.UserInputOutcomeAnswered
	record.Answer = &answer
	record.WasFreeform = &wasFreeform
	s.records = append(s.records, *record)
	return response, nil
}

// UserInputFallback answers a native user-input request that no scripted
// entry handles. Returning an error fails the request closed.
type UserInputFallback func(request copilot.UserInputRequest) (copilot.UserInputResponse, error)

// SetFallback installs the answerer for unscripted requests. It must be called
// before the script is used. Fallback calls are serialized with the script.
func (s *UserInputScript) SetFallback(fallback UserInputFallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = fallback
}

// Records returns the requests observed so far, in arrival order.
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

// Finish reports any rejection or unconsumed scripted answer for the run.
func (s *UserInputScript) Finish() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rejections) > 0 {
		return errors.Join(s.rejections...)
	}
	if remaining := len(s.responses) - s.next; remaining > 0 {
		return fmt.Errorf("user_input_responses: %d scripted answer(s) were not consumed, starting at user_input_responses[%d]", remaining, s.next)
	}
	return nil
}
