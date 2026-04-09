// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPError represents an error returned by the ADC API.
type HTTPError struct {
	// Status is the HTTP status code.
	Status int
	// StatusText is the HTTP status text.
	StatusText string
	// Data contains the response body data, if available.
	Data interface{}
	// Message is the error message.
	Message string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.StatusText)
}

// RequestOptions contains options for an individual HTTP request.
type RequestOptions struct {
	// Params are query parameters to append to the URL.
	Params map[string]string
	// Headers are additional headers for this request.
	Headers map[string]string
	// ResponseType specifies the expected response type: "json" (default), "binary", or "text".
	ResponseType string
}

// HTTPClient is a lightweight HTTP client wrapper for the ADC API.
type HTTPClient struct {
	baseURL        string
	defaultHeaders map[string]string
	timeout        time.Duration
	defaultParams  map[string]string
	client         *http.Client
}

// HTTPClientConfig contains configuration for creating an HTTPClient.
type HTTPClientConfig struct {
	BaseURL       string
	Headers       map[string]string
	Timeout       time.Duration
	DefaultParams map[string]string
}

// NewHTTPClient creates a new HTTPClient with the given configuration.
func NewHTTPClient(config HTTPClientConfig) *HTTPClient {
	baseURL := strings.TrimSuffix(config.BaseURL, "/")
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &HTTPClient{
		baseURL:        baseURL,
		defaultHeaders: config.Headers,
		timeout:        timeout,
		defaultParams:  config.DefaultParams,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// buildURL constructs the full URL with query parameters.
func (c *HTTPClient) buildURL(path string, params map[string]string) string {
	// Ensure path starts with /
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	fullURL := c.baseURL + path

	// Merge default params with request params (request params take precedence)
	mergedParams := make(map[string]string)
	for k, v := range c.defaultParams {
		mergedParams[k] = v
	}
	for k, v := range params {
		mergedParams[k] = v
	}

	if len(mergedParams) > 0 {
		values := url.Values{}
		for k, v := range mergedParams {
			values.Set(k, v)
		}
		fullURL += "?" + values.Encode()
	}

	return fullURL
}

// request performs an HTTP request and returns the response.
func (c *HTTPClient) request(ctx context.Context, method, path string, body interface{}, options *RequestOptions) ([]byte, error) {
	if options == nil {
		options = &RequestOptions{}
	}

	reqURL := c.buildURL(path, options.Params)

	var bodyReader io.Reader
	var contentType string

	if body != nil {
		switch v := body.(type) {
		case []byte:
			bodyReader = bytes.NewReader(v)
			contentType = "application/octet-stream"
		case string:
			bodyReader = strings.NewReader(v)
			contentType = "application/octet-stream"
		default:
			jsonData, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			bodyReader = bytes.NewReader(jsonData)
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set default headers
	for k, v := range c.defaultHeaders {
		req.Header.Set(k, v)
	}

	// Set content type if we have a body
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Set request-specific headers (override defaults)
	for k, v := range options.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &HTTPError{
				Status:     0,
				StatusText: "Request timeout",
				Message:    fmt.Sprintf("Request timed out after %v", c.timeout),
			}
		}
		return nil, &HTTPError{
			Status:     0,
			StatusText: "Network error",
			Message:    err.Error(),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errorData interface{}
		var message string

		// Try to parse error response as JSON
		if len(respBody) > 0 {
			var jsonErr map[string]interface{}
			if json.Unmarshal(respBody, &jsonErr) == nil {
				errorData = jsonErr
				if msg, ok := jsonErr["message"].(string); ok {
					message = msg
				}
			} else {
				errorData = string(respBody)
			}
		}

		return nil, &HTTPError{
			Status:     resp.StatusCode,
			StatusText: resp.Status,
			Data:       errorData,
			Message:    message,
		}
	}

	return respBody, nil
}

// Get performs a GET request.
func (c *HTTPClient) Get(ctx context.Context, path string, options *RequestOptions) ([]byte, error) {
	return c.request(ctx, http.MethodGet, path, nil, options)
}

// Post performs a POST request.
func (c *HTTPClient) Post(ctx context.Context, path string, body interface{}, options *RequestOptions) ([]byte, error) {
	return c.request(ctx, http.MethodPost, path, body, options)
}

// Put performs a PUT request.
func (c *HTTPClient) Put(ctx context.Context, path string, body interface{}, options *RequestOptions) ([]byte, error) {
	return c.request(ctx, http.MethodPut, path, body, options)
}

// Patch performs a PATCH request.
func (c *HTTPClient) Patch(ctx context.Context, path string, body interface{}, options *RequestOptions) ([]byte, error) {
	return c.request(ctx, http.MethodPatch, path, body, options)
}

// Delete performs a DELETE request.
func (c *HTTPClient) Delete(ctx context.Context, path string, options *RequestOptions) ([]byte, error) {
	return c.request(ctx, http.MethodDelete, path, nil, options)
}

// GetJSON performs a GET request and unmarshals the response into the provided value.
func (c *HTTPClient) GetJSON(ctx context.Context, path string, options *RequestOptions, v interface{}) error {
	data, err := c.Get(ctx, path, options)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// PostJSON performs a POST request and unmarshals the response into the provided value.
func (c *HTTPClient) PostJSON(ctx context.Context, path string, body interface{}, options *RequestOptions, v interface{}) error {
	data, err := c.Post(ctx, path, body, options)
	if err != nil {
		return err
	}
	if len(data) == 0 || v == nil {
		return nil
	}
	return json.Unmarshal(data, v)
}

// PostBinary performs a POST request with binary data and a custom content type, then unmarshals the response.
func (c *HTTPClient) PostBinary(ctx context.Context, path string, body io.Reader, contentType string, options *RequestOptions, v interface{}) error {
	if options == nil {
		options = &RequestOptions{}
	}
	if options.Headers == nil {
		options.Headers = make(map[string]string)
	}
	options.Headers["Content-Type"] = contentType

	// Read the body into bytes
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("failed to read body: %w", err)
	}

	data, err := c.Post(ctx, path, bodyBytes, options)
	if err != nil {
		return err
	}
	if len(data) == 0 || v == nil {
		return nil
	}
	return json.Unmarshal(data, v)
}

// PutJSON performs a PUT request and unmarshals the response into the provided value.
func (c *HTTPClient) PutJSON(ctx context.Context, path string, body interface{}, options *RequestOptions, v interface{}) error {
	data, err := c.Put(ctx, path, body, options)
	if err != nil {
		return err
	}
	if len(data) == 0 || v == nil {
		return nil
	}
	return json.Unmarshal(data, v)
}

// PatchJSON performs a PATCH request and unmarshals the response into the provided value.
func (c *HTTPClient) PatchJSON(ctx context.Context, path string, body interface{}, options *RequestOptions, v interface{}) error {
	data, err := c.Patch(ctx, path, body, options)
	if err != nil {
		return err
	}
	if len(data) == 0 || v == nil {
		return nil
	}
	return json.Unmarshal(data, v)
}

// GetStream performs a GET request and returns the response for streaming.
// The caller is responsible for closing the response body.
func (c *HTTPClient) GetStream(ctx context.Context, path string) (*http.Response, error) {
	reqURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set default headers
	for key, value := range c.defaultHeaders {
		req.Header.Set(key, value)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to perform request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, &HTTPError{
			Status:     resp.StatusCode,
			StatusText: resp.Status,
		}
	}

	return resp, nil
}
