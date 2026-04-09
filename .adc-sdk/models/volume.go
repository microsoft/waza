// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import "time"

// VolumeType represents the type of a volume.
type VolumeType string

const (
	// VolumeTypeAzureBlob represents an Azure Blob Storage volume.
	VolumeTypeAzureBlob VolumeType = "AzureBlob"
	// VolumeTypeDataDisk represents a persistent ext4 block device stored in cluster storage.
	VolumeTypeDataDisk VolumeType = "DataDisk"
)

// VolumeData represents volume data returned from the API.
type VolumeData struct {
	// VolumeName is the volume name.
	VolumeName string `json:"volumeName"`
	// Type is the volume type.
	Type VolumeType `json:"type"`
	// Labels are key-value pairs attached to the volume.
	Labels map[string]string `json:"labels"`
	// Usage is the usage information.
	Usage *VolumeUsage `json:"usage,omitempty"`
	// Size is the disk size (DataDisk volumes only), e.g. "256Mi".
	Size string `json:"size,omitempty"`
	// IsAttached indicates whether the volume is attached to a sandbox (DataDisk volumes only).
	IsAttached *bool `json:"isAttached,omitempty"`
}

// VolumeUsage contains usage information for a volume.
type VolumeUsage struct {
	// UsedBytes is the number of bytes used (AzureBlob volumes).
	UsedBytes int64 `json:"usedBytes"`
	// ItemCount is the number of items (AzureBlob volumes).
	ItemCount int64 `json:"itemCount"`
	// CalculatedAtUtc is when the usage was calculated (AzureBlob volumes).
	CalculatedAtUtc *time.Time `json:"calculatedAtUtc,omitempty"`
	// CompressedBlobSizeBytes is the compressed blob size in bytes (DataDisk volumes only).
	CompressedBlobSizeBytes *int64 `json:"compressedBlobSizeBytes,omitempty"`
	// UsedSizeBytes is the used size in bytes (DataDisk volumes only).
	UsedSizeBytes *int64 `json:"usedSizeBytes,omitempty"`
	// LastUploadedAtUtc is the last upload timestamp (DataDisk volumes only).
	LastUploadedAtUtc *time.Time `json:"lastUploadedAtUtc,omitempty"`
}

// CreateVolumeRequest is the request for creating a volume.
type CreateVolumeRequest struct {
	// Type is the volume type.
	Type VolumeType `json:"type"`
	// Labels are key-value pairs to attach to the volume.
	Labels map[string]string `json:"labels,omitempty"`
	// Size is the disk size (required for DataDisk volumes), e.g. "256Mi". Valid range: 64Mi-10Gi.
	Size string `json:"size,omitempty"`
}

// VolumePathItem represents a file or directory in a volume.
type VolumePathItem struct {
	// ItemName is the item name.
	ItemName string `json:"itemName"`
	// Path is the full path.
	Path string `json:"path"`
	// IsDirectory indicates whether the item is a directory.
	IsDirectory bool `json:"isDirectory"`
	// SizeBytes is the size in bytes (files only).
	SizeBytes *int64 `json:"sizeBytes,omitempty"`
	// LastModifiedUtc is when the item was last modified.
	LastModifiedUtc *time.Time `json:"lastModifiedUtc,omitempty"`
	// ContentType is the content type (files only).
	ContentType string `json:"contentType,omitempty"`
}

// VolumeDirectoryListing is the result of listing a directory in a volume.
type VolumeDirectoryListing struct {
	// Path is the directory path.
	Path string `json:"path"`
	// Items are the items in the directory.
	Items []VolumePathItem `json:"items"`
}

// SandboxVolume represents a volume mount for a sandbox.
type SandboxVolume struct {
	// VolumeName is the volume name.
	VolumeName string `json:"volumeName"`
	// Mountpoint is the mount point inside the sandbox.
	Mountpoint string `json:"mountpoint"`
	// ReadOnly indicates whether the volume is mounted read-only.
	ReadOnly bool `json:"readOnly,omitempty"`
}
