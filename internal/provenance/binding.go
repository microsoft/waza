package provenance

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
)

var (
	kindPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)
	namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	// checkCode bounds kind-specific failure codes surfaced by binding verifiers.
	checkCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// BindingVerifier verifies one additional member bound into the statement.
// The member bytes have already matched the signed digest; implementations
// verify kind-specific content and its relation to the statement (for
// example, that a component inventory describes the declared runner image
// digest). Returning an error fails verification closed; an error exposing
// BindingCode() string surfaces that code instead of binding_invalid.
type BindingVerifier interface {
	Verify(binding Binding, data []byte, statement *Statement) error
}

// BindingVerifierFunc adapts a function to BindingVerifier.
type BindingVerifierFunc func(Binding, []byte, *Statement) error

func (f BindingVerifierFunc) Verify(b Binding, data []byte, s *Statement) error { return f(b, data, s) }

// BindingInput is one additional member to bind during generation.
type BindingInput struct {
	Kind      string
	Name      string
	MediaType string
	Data      []byte
}

var (
	bindingMu        sync.RWMutex
	bindingVerifiers = map[string]BindingVerifier{}
)

// RegisterBindingVerifier registers the verifier for one binding kind.
// Generation rejects kinds without a verifier, and verification fails closed
// on unknown kinds, so a kind is only usable once registered.
func RegisterBindingVerifier(kind string, verifier BindingVerifier) error {
	if !kindPattern.MatchString(kind) || verifier == nil {
		return fmt.Errorf("invalid binding kind %q or nil verifier", kind)
	}
	bindingMu.Lock()
	defer bindingMu.Unlock()
	if _, exists := bindingVerifiers[kind]; exists {
		return fmt.Errorf("binding kind %q is already registered", kind)
	}
	bindingVerifiers[kind] = verifier
	return nil
}

func unregisterBindingVerifier(kind string) {
	bindingMu.Lock()
	defer bindingMu.Unlock()
	delete(bindingVerifiers, kind)
}

func lookupBindingVerifier(kind string) (BindingVerifier, bool) {
	bindingMu.RLock()
	defer bindingMu.RUnlock()
	v, ok := bindingVerifiers[kind]
	return v, ok
}

func validateBindingInputs(inputs []BindingInput) error {
	if len(inputs) > maxBindings {
		return fmt.Errorf("at most %d bindings are supported", maxBindings)
	}
	seen := map[string]bool{}
	for _, in := range inputs {
		if _, ok := lookupBindingVerifier(in.Kind); !ok {
			return fmt.Errorf("binding kind %q has no registered verifier", in.Kind)
		}
		if !namePattern.MatchString(in.Name) || seen[in.Name] {
			return fmt.Errorf("binding name %q is invalid or duplicated", in.Name)
		}
		seen[in.Name] = true
		if in.MediaType == "" || len(in.MediaType) > 128 {
			return fmt.Errorf("binding %q requires a media type", in.Name)
		}
		if len(in.Data) > maxBindingBytes {
			return errors.New("binding member exceeds size bound")
		}
	}
	return nil
}
