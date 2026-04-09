// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

// FileInfo contains metadata about a file or directory.
type FileInfo struct {
	// Name is the file or directory name.
	Name string `json:"name"`
	// Path is the full path to the file or directory.
	Path string `json:"path"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
	// Mode is the file mode/permissions (Unix-style).
	Mode int `json:"mode"`
	// IsDir indicates whether this is a directory.
	IsDir bool `json:"isDir"`
	// IsSymlink indicates whether this is a symbolic link.
	IsSymlink bool `json:"isSymlink"`
	// SymlinkTarget is the target path if this is a symlink.
	SymlinkTarget string `json:"symlinkTarget"`
	// ModifiedTime is the last modification time (Unix timestamp).
	ModifiedTime int64 `json:"modifiedTime"`
}

// DirListing contains the result of listing a directory.
type DirListing struct {
	// Path is the path of the listed directory.
	Path string `json:"path"`
	// Entries is the list of files and subdirectories.
	Entries []FileInfo `json:"entries"`
}

// WriteFileResult is the result of a file write operation.
type WriteFileResult struct {
	// Success indicates whether the write succeeded.
	Success bool `json:"success"`
	// Error contains the error message if failed.
	Error string `json:"error,omitempty"`
	// BytesWritten is the number of bytes written.
	BytesWritten int64 `json:"bytesWritten"`
}

// FileOpResult is the result of a generic file operation.
type FileOpResult struct {
	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`
	// Error contains the error message if failed.
	Error string `json:"error,omitempty"`
	// Message contains additional information.
	Message string `json:"message,omitempty"`
}

// MkDirRequest is the request to create a directory.
type MkDirRequest struct {
	// Path is the path of the directory to create.
	Path string `json:"path"`
	// CreateParents creates parent directories if they don't exist.
	CreateParents bool `json:"createParents,omitempty"`
	// Mode is the directory permissions (Unix-style).
	Mode int `json:"mode,omitempty"`
}
