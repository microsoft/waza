// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

// ContentPackage represents an ADC content package.
type ContentPackage struct {
	// ID is the unique identifier for the content package.
	ID string `json:"id"`
	// Size is the size of the content package in bytes.
	Size int64 `json:"size"`
	// Labels are key-value pairs attached to the content package.
	Labels map[string]string `json:"labels"`
}

// ContentPackageListResponse is the response from listing content packages.
type ContentPackageListResponse struct {
	// Value is the list of content packages.
	Value []ContentPackage `json:"value"`
}

// ContentPackageDownload specifies a content package to download into a sandbox.
type ContentPackageDownload struct {
	// ContentPackageID is the ID of the content package to download.
	ContentPackageID string `json:"contentPackageId"`
	// TargetPath is the absolute path in the sandbox where the content will be delivered.
	TargetPath string `json:"targetPath"`
	// Action specifies how the content package should be delivered: "Download" (default) or "Mount".
	// Mount presents the package contents as a read-only FUSE filesystem (zip only).
	Action string `json:"action,omitempty"`
}
