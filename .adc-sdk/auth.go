// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultGitHubClientID is the default GitHub OAuth App client ID.
	DefaultGitHubClientID = "Iv23licPqE4MFVU9FMW3"

	githubDeviceCodeURL = "https://github.com/login/device/code"
	githubTokenURL      = "https://github.com/login/oauth/access_token"
)

// AuthenticationError represents an authentication failure.
type AuthenticationError struct {
	Message string
}

func (e *AuthenticationError) Error() string {
	return e.Message
}

// DeviceCodeResponse contains the response from GitHub device code request.
type DeviceCodeResponse struct {
	// DeviceCode is the device verification code.
	DeviceCode string `json:"device_code"`
	// UserCode is the code the user enters on the verification page.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user enters the code.
	VerificationURI string `json:"verification_uri"`
	// ExpiresIn is the number of seconds until the codes expire.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds between polling requests.
	Interval int `json:"interval"`
}

// TokenResponse contains the response from GitHub token exchange.
type TokenResponse struct {
	// AccessToken is the GitHub access token.
	AccessToken string `json:"access_token"`
	// TokenType is the type of token (usually "bearer").
	TokenType string `json:"token_type"`
	// Scope is the granted scopes.
	Scope string `json:"scope"`
	// Error contains error code if the request failed.
	Error string `json:"error,omitempty"`
}

// AuthCallback is called when the user needs to authenticate.
// It receives the user code and verification URI for the user to complete authentication.
type AuthCallback func(userCode, verificationURI string)

// GitHubDeviceFlowAuth handles GitHub Device Flow authentication.
type GitHubDeviceFlowAuth struct {
	clientID string
	scopes   []string
	timeout  time.Duration
}

// NewGitHubDeviceFlowAuth creates a new GitHubDeviceFlowAuth.
func NewGitHubDeviceFlowAuth(clientID string, scopes ...string) *GitHubDeviceFlowAuth {
	if len(scopes) == 0 {
		scopes = []string{"user:email"}
	}

	return &GitHubDeviceFlowAuth{
		clientID: clientID,
		scopes:   scopes,
		timeout:  30 * time.Second,
	}
}

// Authenticate performs GitHub Device Flow authentication.
// If callback is nil, default instructions are printed to stdout.
func (a *GitHubDeviceFlowAuth) Authenticate(ctx context.Context, callback AuthCallback) (string, error) {
	// Step 1: Request device code
	deviceCodeResponse, err := a.requestDeviceCode(ctx)
	if err != nil {
		return "", err
	}

	// Step 2: Display user instructions
	if callback != nil {
		callback(deviceCodeResponse.UserCode, deviceCodeResponse.VerificationURI)
	} else {
		a.displayInstructions(deviceCodeResponse)
	}

	// Step 3: Poll for token
	tokenResponse, err := a.pollForToken(ctx, deviceCodeResponse)
	if err != nil {
		return "", err
	}

	return tokenResponse.AccessToken, nil
}

func (a *GitHubDeviceFlowAuth) requestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", a.clientID)
	data.Set("scope", strings.Join(a.scopes, " "))

	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, &AuthenticationError{Message: fmt.Sprintf("failed to create request: %v", err)}
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: a.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &AuthenticationError{Message: fmt.Sprintf("failed to request device code: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &AuthenticationError{Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &AuthenticationError{Message: fmt.Sprintf("failed to read response: %v", err)}
	}

	var response DeviceCodeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, &AuthenticationError{Message: fmt.Sprintf("failed to parse response: %v", err)}
	}

	return &response, nil
}

func (a *GitHubDeviceFlowAuth) displayInstructions(response *DeviceCodeResponse) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("GitHub Authentication Required")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("\n1. Visit: %s\n", response.VerificationURI)
	fmt.Printf("2. Enter code: %s\n", response.UserCode)
	fmt.Printf("\nWaiting for authentication (expires in %ds)...\n", response.ExpiresIn)
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println()
}

func (a *GitHubDeviceFlowAuth) pollForToken(ctx context.Context, deviceResponse *DeviceCodeResponse) (*TokenResponse, error) {
	startTime := time.Now()
	interval := time.Duration(deviceResponse.Interval) * time.Second

	for {
		// Check if expired
		elapsed := time.Since(startTime)
		if elapsed.Seconds() > float64(deviceResponse.ExpiresIn) {
			return nil, &AuthenticationError{Message: "authentication expired, please try again"}
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		tokenResponse, err := a.requestToken(ctx, deviceResponse.DeviceCode)
		if err != nil {
			var authErr *AuthenticationError
			if errors.As(err, &authErr) {
				return nil, authErr
			}
			// Network error, continue polling
			continue
		}

		if tokenResponse.Error != "" {
			switch tokenResponse.Error {
			case "authorization_pending":
				// User hasn't completed auth yet, continue polling
				continue
			case "slow_down":
				// Increase polling interval
				interval += 5 * time.Second
				continue
			case "expired_token":
				return nil, &AuthenticationError{Message: "device code expired, please try again"}
			case "access_denied":
				return nil, &AuthenticationError{Message: "authentication was denied by user"}
			default:
				return nil, &AuthenticationError{Message: fmt.Sprintf("authentication error: %s", tokenResponse.Error)}
			}
		}

		if tokenResponse.AccessToken != "" {
			fmt.Println()
			fmt.Println("✓ Authentication successful!")
			fmt.Println()
			return tokenResponse, nil
		}
	}
}

func (a *GitHubDeviceFlowAuth) requestToken(ctx context.Context, deviceCode string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", a.clientID)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: a.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &AuthenticationError{Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var response TokenResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &response, nil
}
