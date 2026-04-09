// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import (
	"encoding/json"
	"testing"
)

func TestEgressPolicyRewriteRuleMarshal(t *testing.T) {
	policy := EgressPolicy{
		DefaultAction: EgressPolicyActionDeny,
		Rules: []EgressPolicyRule{
			{
				Name: "rewrite-openai",
				Match: EgressPolicyRuleMatch{
					Host:    "*.openai.com",
					Path:    "/v1/*",
					Methods: []string{"GET", "POST"},
				},
				Action: EgressPolicyRuleAction{
					Type:   EgressPolicyRuleActionTypeRewrite,
					Scheme: "https",
					Host:   "api.openai.com",
					Path:   "/v1/completions",
					Headers: []EgressPolicyHeaderTransform{
						{
							Operation: EgressPolicyHeaderOperationSet,
							Name:      "Authorization",
							Value:     "Bearer test-key",
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded EgressPolicy
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.DefaultAction != EgressPolicyActionDeny {
		t.Errorf("expected DefaultAction %q, got %q", EgressPolicyActionDeny, decoded.DefaultAction)
	}
	if len(decoded.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(decoded.Rules))
	}

	rule := decoded.Rules[0]
	if rule.Name != "rewrite-openai" {
		t.Errorf("expected rule name %q, got %q", "rewrite-openai", rule.Name)
	}
	if rule.Match.Host != "*.openai.com" {
		t.Errorf("expected host %q, got %q", "*.openai.com", rule.Match.Host)
	}
	if rule.Action.Type != EgressPolicyRuleActionTypeRewrite {
		t.Errorf("expected action type %q, got %q", EgressPolicyRuleActionTypeRewrite, rule.Action.Type)
	}
	if rule.Action.Host != "api.openai.com" {
		t.Errorf("expected action host %q, got %q", "api.openai.com", rule.Action.Host)
	}
	if len(rule.Action.Headers) != 1 || rule.Action.Headers[0].Name != "Authorization" {
		t.Error("expected Authorization header transform")
	}
}

func TestEgressPolicyTransformWithSecretRefMarshal(t *testing.T) {
	policy := EgressPolicy{
		DefaultAction: EgressPolicyActionAllow,
		Rules: []EgressPolicyRule{
			{
				Name:  "add-auth-header",
				Match: EgressPolicyRuleMatch{Host: "api.example.com"},
				Action: EgressPolicyRuleAction{
					Type: EgressPolicyRuleActionTypeTransform,
					Headers: []EgressPolicyHeaderTransform{
						{
							Operation: EgressPolicyHeaderOperationSet,
							Name:      "Authorization",
							ValueRef: &EgressPolicyValueRef{
								SecretRef: &EgressPolicySecretRef{
									SecretID:  "my-secret",
									SecretKey: "api-key",
									Format:    "Bearer {value}",
								},
							},
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded EgressPolicy
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	rule := decoded.Rules[0]
	if rule.Action.Type != EgressPolicyRuleActionTypeTransform {
		t.Errorf("expected action type %q, got %q", EgressPolicyRuleActionTypeTransform, rule.Action.Type)
	}
	header := rule.Action.Headers[0]
	if header.ValueRef == nil || header.ValueRef.SecretRef == nil {
		t.Fatal("expected secretRef in header valueRef")
	}
	if header.ValueRef.SecretRef.SecretID != "my-secret" {
		t.Errorf("expected secretId %q, got %q", "my-secret", header.ValueRef.SecretRef.SecretID)
	}
	if header.ValueRef.SecretRef.Format != "Bearer {value}" {
		t.Errorf("expected format %q, got %q", "Bearer {value}", header.ValueRef.SecretRef.Format)
	}
}

func TestEgressPolicyManagedIdentityRefMarshal(t *testing.T) {
	policy := EgressPolicy{
		DefaultAction: EgressPolicyActionDeny,
		Rules: []EgressPolicyRule{
			{
				Name:  "managed-id-rule",
				Match: EgressPolicyRuleMatch{Host: "management.azure.com"},
				Action: EgressPolicyRuleAction{
					Type: EgressPolicyRuleActionTypeTransform,
					Headers: []EgressPolicyHeaderTransform{
						{
							Operation: EgressPolicyHeaderOperationSet,
							Name:      "Authorization",
							ValueRef: &EgressPolicyValueRef{
								ManagedIdentityRef: &EgressPolicyManagedIdentityRef{
									Resource: "https://management.azure.com",
									Format:   "Bearer {value}",
								},
							},
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded EgressPolicy
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	ref := decoded.Rules[0].Action.Headers[0].ValueRef.ManagedIdentityRef
	if ref == nil {
		t.Fatal("expected managedIdentityRef")
	}
	if ref.Resource != "https://management.azure.com" {
		t.Errorf("expected resource %q, got %q", "https://management.azure.com", ref.Resource)
	}
}

func TestEgressPolicyLegacyHostRulesPreserved(t *testing.T) {
	policy := EgressPolicy{
		DefaultAction: EgressPolicyActionDeny,
		HostRules: []EgressHostRule{
			{Pattern: "*.github.com", Action: EgressPolicyActionAllow},
		},
		Rules: []EgressPolicyRule{
			{
				Name:   "allow-github",
				Match:  EgressPolicyRuleMatch{Host: "*.github.com"},
				Action: EgressPolicyRuleAction{Type: EgressPolicyRuleActionTypeAllow},
			},
		},
	}

	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded EgressPolicy
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(decoded.HostRules) != 1 {
		t.Errorf("expected 1 host rule, got %d", len(decoded.HostRules))
	}
	if len(decoded.Rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(decoded.Rules))
	}
}

func TestEgressPolicyHeaderRemoveOmitsValue(t *testing.T) {
	header := EgressPolicyHeaderTransform{
		Operation: EgressPolicyHeaderOperationRemove,
		Name:      "X-Unwanted",
	}

	data, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	if _, exists := m["value"]; exists {
		t.Error("expected 'value' to be omitted for Remove operation")
	}
	if _, exists := m["valueRef"]; exists {
		t.Error("expected 'valueRef' to be omitted for Remove operation")
	}
}

func TestSandboxState_IdleDeserializes(t *testing.T) {
	jsonStr := `{"id":"sb-789","state":"Idle","vmmType":"cloudhypervisor","ports":[]}`

	var sandbox SandboxData
	if err := json.Unmarshal([]byte(jsonStr), &sandbox); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if sandbox.State != SandboxStateIdle {
		t.Errorf("expected state %q, got %q", SandboxStateIdle, sandbox.State)
	}
	if sandbox.ID != "sb-789" {
		t.Errorf("expected ID sb-789, got %s", sandbox.ID)
	}
}

func TestSandboxState_AllValuesRoundTrip(t *testing.T) {
	states := []SandboxState{SandboxStateRunning, SandboxStateStopped, SandboxStateIdle}
	for _, state := range states {
		sandbox := SandboxData{ID: "sb-1", State: state}
		data, err := json.Marshal(sandbox)
		if err != nil {
			t.Fatalf("failed to marshal state %q: %v", state, err)
		}
		var decoded SandboxData
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal state %q: %v", state, err)
		}
		if decoded.State != state {
			t.Errorf("expected state %q, got %q", state, decoded.State)
		}
	}
}
