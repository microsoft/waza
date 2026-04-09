// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import "time"

// SnapshotGpu represents GPU configuration for a snapshot.
type SnapshotGpu struct {
	// Sku is the GPU SKU.
	Sku string `json:"sku,omitempty"`
	// Quantity is the number of GPUs.
	Quantity string `json:"quantity,omitempty"`
}

// SnapshotResources represents resources from a snapshot.
type SnapshotResources struct {
	// CPU is the CPU allocation.
	CPU string `json:"cpu"`
	// Memory is the memory allocation.
	Memory string `json:"memory"`
	// Disk is the disk allocation.
	Disk string `json:"disk,omitempty"`
	// GPU is the GPU configuration.
	GPU *SnapshotGpu `json:"gpu,omitempty"`
}

// Snapshot represents an ADC snapshot.
type Snapshot struct {
	// ID is the snapshot ID.
	ID string `json:"id"`
	// Labels are key-value pairs attached to the snapshot.
	Labels map[string]string `json:"labels,omitempty"`
	// CreatedAt is when the snapshot was created.
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// SandboxID is the ID of the sandbox this snapshot was taken from.
	SandboxID string `json:"sandboxId,omitempty"`
	// VmmType is the VMM type used by the sandbox.
	VmmType string `json:"vmmType,omitempty"`
	// Resources is the resource configuration from the sandbox.
	Resources *SnapshotResources `json:"resources,omitempty"`
	// ClusterHostname is the cluster hostname where the snapshot is stored.
	ClusterHostname string `json:"clusterHostname,omitempty"`
}

// CreateSnapshotRequest is the request to create a snapshot.
type CreateSnapshotRequest struct {
	// Labels are key-value pairs to attach to the snapshot.
	Labels map[string]string `json:"labels,omitempty"`
}
