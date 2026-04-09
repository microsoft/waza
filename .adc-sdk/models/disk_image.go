// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import "time"

// DiskImageState represents the state of a disk image.
type DiskImageState string

const (
	// DiskImageStateReady indicates the disk image is ready to use.
	DiskImageStateReady DiskImageState = "Ready"
	// DiskImageStateBuilding indicates the disk image is being built.
	DiskImageStateBuilding DiskImageState = "Building"
	// DiskImageStateFailed indicates the disk image build failed.
	DiskImageStateFailed DiskImageState = "Failed"
)

// DiskImageImage contains the image configuration for a disk image.
type DiskImageImage struct {
	// Base is the base container image (e.g., "python:3.12").
	Base string `json:"base"`
	// Entrypoint overrides the container entrypoint.
	Entrypoint []string `json:"entrypoint,omitempty"`
	// Cmd overrides the container command.
	Cmd []string `json:"cmd,omitempty"`
}

// DiskImageStatus contains the status information for a disk image.
type DiskImageStatus struct {
	// State is the current state of the disk image.
	State string `json:"state"`
	// ErrorMessage contains error details if the build failed.
	ErrorMessage string `json:"errorMessage,omitempty"`
	// CreatedAt is when the disk image was created.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the disk image was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// DiskImage represents an ADC disk image.
type DiskImage struct {
	// ID is the unique identifier for the disk image.
	ID string `json:"id"`
	// Labels are key-value pairs attached to the disk image.
	Labels map[string]string `json:"labels"`
	// Image contains the image configuration.
	Image DiskImageImage `json:"image"`
	// Status contains the current status.
	Status DiskImageStatus `json:"status"`
}

// RegistryCredentials contains credentials for authenticating to private container registries.
type RegistryCredentials struct {
	// Username is the username for registry authentication.
	Username string `json:"username"`
	// Token is the password or token for registry authentication.
	Token string `json:"token"`
}

// CreateDiskImageRequest is the request to create a new disk image.
type CreateDiskImageRequest struct {
	// Labels are key-value pairs to attach to the disk image.
	Labels map[string]string `json:"labels"`
	// Image contains the image configuration.
	Image DiskImageImage `json:"image"`
	// RegistryCredentials are optional credentials for authenticating to private container registries.
	// When provided, these credentials will be used to pull the container image from a private registry.
	// Both username and token must be provided if this field is set.
	RegistryCredentials *RegistryCredentials `json:"registryCredentials,omitempty"`
}

// PublicDiskImage represents a publicly available disk image.
type PublicDiskImage struct {
	// Name is the public name of the disk image (e.g., "python:3.12").
	Name string `json:"name"`
	// Status contains the current status.
	Status DiskImageStatus `json:"status"`
}

// CreateDiskImageOptions contains options for creating a disk image.
type CreateDiskImageOptions struct {
	// Labels are key-value pairs to attach to the disk image.
	Labels map[string]string
	// BaseImage is the base container image (e.g., "docker.io/library/ubuntu:latest").
	BaseImage string
	// Entrypoint overrides the container entrypoint.
	Entrypoint []string
	// RegistryCredentials are optional credentials for authenticating to private container registries.
	// When provided, these credentials will be used to pull the container image from a private registry.
	RegistryCredentials *RegistryCredentials
}

// BuildDiskImageFromDockerfileRequest is the request to build a disk image from a Dockerfile.
type BuildDiskImageFromDockerfileRequest struct {
	// Name is the human-readable name for the disk image (required).
	Name string `json:"name"`
	// Dockerfile is the Dockerfile content to build.
	Dockerfile string `json:"dockerfile"`
	// Labels are optional key-value pairs to attach to the disk image.
	Labels map[string]string `json:"labels,omitempty"`
	// BuildArgs are build arguments to pass to the Dockerfile build (--build-arg).
	// Keys are argument names, values are argument values.
	// These are visible in the image history and should not contain secrets.
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	// Secrets are secrets to make available during the Dockerfile build.
	// Keys are secret IDs (referenced in Dockerfile via RUN --mount=type=secret,id=<key>),
	// values are the plaintext secret content.
	// Secrets are written to tmpfs inside the build sandbox and never stored in image layers.
	Secrets map[string]string `json:"secrets,omitempty"`
	// ContextContentPackageID is an optional content package ID to use as the build context.
	// The content package must be in .tar.gz format and will be downloaded and extracted
	// to the build context directory before running the Dockerfile build.
	ContextContentPackageID string `json:"contextContentPackageId,omitempty"`
}

// BuildDiskImageFromDockerfileOptions contains options for building a disk image from a Dockerfile.
type BuildDiskImageFromDockerfileOptions struct {
	// Name is the human-readable name for the disk image (required).
	Name string
	// Dockerfile is the Dockerfile content to build.
	Dockerfile string
	// Labels are optional key-value pairs to attach to the disk image.
	Labels map[string]string
	// BuildArgs are build arguments to pass to the Dockerfile build (--build-arg).
	BuildArgs map[string]string
	// Secrets are secrets to make available during the Dockerfile build.
	Secrets map[string]string
	// ContextContentPackageID is an optional content package ID to use as the build context.
	// The content package must be in .tar.gz format.
	ContextContentPackageID string
}
