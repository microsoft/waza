// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"fmt"
	"io"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// App represents an ADC app instance with methods for management.
type App struct {
	client *HTTPClient
	config *ConfigManager
	// Data contains the app data from the API.
	Data models.AppData
}

// NewApp creates a new App instance.
func NewApp(client *HTTPClient, config *ConfigManager, data models.AppData) *App {
	return &App{
		client: client,
		config: config,
		Data:   data,
	}
}

// ID returns the app ID.
func (a *App) ID() string {
	return a.Data.ID
}

// Name returns the app name.
func (a *App) Name() string {
	return a.Data.Name
}

// Labels returns the labels attached to the app.
func (a *App) Labels() map[string]string {
	return a.Data.Labels
}

// State returns the current state of the app.
func (a *App) State() models.AppState {
	return a.Data.State
}

// ContainerImage returns the container image of the app.
func (a *App) ContainerImage() string {
	return a.Data.ContainerImage
}

// Resources returns the resource configuration of the app.
func (a *App) Resources() *models.AppResources {
	return a.Data.Resources
}

// Ports returns the port mappings for the app.
func (a *App) Ports() []models.AppPort {
	return a.Data.Ports
}

// Connections returns the connection IDs associated with the app.
func (a *App) Connections() []string {
	return a.Data.Connections
}

// EgressPolicy returns the egress policy for the app.
func (a *App) EgressPolicy() *models.AppEgressPolicy {
	return a.Data.EgressPolicy
}

// Scale returns the scale configuration for the app.
func (a *App) Scale() *models.AppScale {
	return a.Data.Scale
}

// Environment returns the environment variables configured on the app.
func (a *App) Environment() []models.AppEnvironmentVariable {
	return a.Data.Environment
}

// Replicas returns the running replicas for this app.
func (a *App) Replicas() []models.AppReplica {
	return a.Data.Replicas
}

// AppURL returns the URL for accessing the app.
func (a *App) AppURL() string {
	return a.Data.AppURL
}

// Refresh updates the app data from the API.
func (a *App) Refresh(ctx context.Context) error {
	var data models.AppData
	err := a.client.GetJSON(ctx, fmt.Sprintf("/apps/%s", a.ID()), nil, &data)
	if err != nil {
		return err
	}
	a.Data = data
	return nil
}

// Stop stops the running app.
func (a *App) Stop(ctx context.Context) error {
	var data models.AppData
	err := a.client.PostJSON(ctx, fmt.Sprintf("/apps/%s/stop", a.ID()), nil, nil, &data)
	if err != nil {
		return err
	}
	a.Data = data
	return nil
}

// Resume resumes a stopped app from its snapshot.
func (a *App) Resume(ctx context.Context) error {
	var data models.AppData
	err := a.client.PostJSON(ctx, fmt.Sprintf("/apps/%s/resume", a.ID()), nil, nil, &data)
	if err != nil {
		return err
	}
	a.Data = data
	return nil
}

// Delete deletes the app.
func (a *App) Delete(ctx context.Context) error {
	_, err := a.client.Delete(ctx, fmt.Sprintf("/apps/%s", a.ID()), nil)
	return err
}

// AddPort adds a port mapping to the app.
func (a *App) AddPort(ctx context.Context, port int, protocol string) (*models.AppPort, error) {
	request := models.AddAppPortRequest{
		Port:     port,
		Protocol: protocol,
	}

	var response models.AppPortsListResponse
	err := a.client.PostJSON(ctx, fmt.Sprintf("/apps/%s/ports/add", a.ID()), request, nil, &response)
	if err != nil {
		return nil, err
	}

	if err := a.Refresh(ctx); err != nil {
		return nil, err
	}

	// Find the port we just added
	for _, p := range response.Ports {
		if p.Port == port {
			return &p, nil
		}
	}

	return nil, fmt.Errorf("port %d was added but not found in response", port)
}

// RemovePort removes a port mapping from the app.
func (a *App) RemovePort(ctx context.Context, port int) error {
	request := models.RemoveAppPortRequest{Port: port}
	_, err := a.client.Post(ctx, fmt.Sprintf("/apps/%s/ports/remove", a.ID()), request, nil)
	if err != nil {
		return err
	}
	return a.Refresh(ctx)
}

// ListPorts lists all port mappings for the app.
func (a *App) ListPorts(ctx context.Context) ([]models.AppPort, error) {
	var response models.AppPortsListResponse
	err := a.client.GetJSON(ctx, fmt.Sprintf("/apps/%s/ports", a.ID()), nil, &response)
	if err != nil {
		return nil, err
	}
	return response.Ports, nil
}

// UpdatePorts updates all port mappings for the app.
func (a *App) UpdatePorts(ctx context.Context, ports []models.AppPort) ([]models.AppPort, error) {
	request := models.UpdateAppPortsRequest{Ports: ports}

	var response models.AppPortsListResponse
	err := a.client.PutJSON(ctx, fmt.Sprintf("/apps/%s/ports", a.ID()), request, nil, &response)
	if err != nil {
		return nil, err
	}

	if err := a.Refresh(ctx); err != nil {
		return nil, err
	}

	return response.Ports, nil
}

// SetEgressPolicy sets the egress policy for the app.
func (a *App) SetEgressPolicy(ctx context.Context, policy models.AppEgressPolicy) error {
	_, err := a.client.Post(ctx, fmt.Sprintf("/apps/%s/egresspolicy", a.ID()), policy, nil)
	if err != nil {
		return err
	}
	return a.Refresh(ctx)
}

// StreamLogs streams the app logs, returning a reader for the caller to consume.
// The caller is responsible for closing the returned ReadCloser.
func (a *App) StreamLogs(ctx context.Context, instanceID string, tail int, logFormat string) (io.ReadCloser, error) {
	path := fmt.Sprintf("/apps/%s/instances/%s/logstream?tailLines=%d&logFormat=%s", a.ID(), instanceID, tail, logFormat)
	resp, err := a.client.GetStream(ctx, path)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
