package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dashboardRoot struct {
	file      *os.File
	afterOpen func(string)
}

func openDashboardRoot(path string) (*dashboardRoot, error) {
	parts, err := dashboardPathParts(path)
	if err != nil {
		return nil, err
	}
	current, err := os.Open(".")
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		child, openErr := openDashboardComponent(current, part, true)
		closeErr := current.Close()
		if openErr != nil || closeErr != nil {
			if child != nil {
				closeErr = errors.Join(closeErr, child.Close())
			}
			return nil, errors.Join(openErr, closeErr)
		}
		current = child
	}
	return &dashboardRoot{file: current}, nil
}

func (root *dashboardRoot) Close() error {
	return root.file.Close()
}

func (root *dashboardRoot) openDirectory(path string) (*os.File, error) {
	return root.open(path, true)
}

func (root *dashboardRoot) openFile(path string) (*os.File, error) {
	return root.open(path, false)
}

func (root *dashboardRoot) open(path string, directory bool) (*os.File, error) {
	parts, err := dashboardPathParts(path)
	if err != nil {
		return nil, err
	}
	current := root.file
	owned := false
	for i, part := range parts {
		child, openErr := openDashboardComponent(current, part, i < len(parts)-1 || directory)
		if owned {
			openErr = errors.Join(openErr, current.Close())
		}
		if openErr != nil {
			if child != nil {
				openErr = errors.Join(openErr, child.Close())
			}
			return nil, openErr
		}
		current, owned = child, true
		if i < len(parts)-1 && root.afterOpen != nil {
			root.afterOpen(strings.Join(parts[:i+1], "/"))
		}
	}
	return current, nil
}

func openDashboardComponent(parent *os.File, name string, directory bool) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(parent.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	options := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ, attributes, &windows.IO_STATUS_BLOCK{}, nil,
		windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("opening nonsymlink dashboard path component %s: %w", name, err)
	}
	return os.NewFile(uintptr(handle), name), nil
}
