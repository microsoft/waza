package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/.github/scripts/internal/dashboarddocument"
)

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check() (result error) {
	root, err := openDashboardRoot("web/dist")
	if err != nil {
		return fmt.Errorf("opening release dashboard: %w", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("closing release dashboard root: %w", err))
		}
	}()
	index, err := readFile(root, "index.html")
	if err != nil {
		return err
	}
	if _, err := readFile(root, "favicon.svg"); err != nil {
		return err
	}
	if err := directory(root, "assets"); err != nil {
		return err
	}
	assets, err := root.openDirectory("assets")
	if err != nil {
		return fmt.Errorf("opening release dashboard assets: %w", err)
	}
	entries, readErr := assets.ReadDir(-1)
	if err := errors.Join(readErr, assets.Close()); err != nil {
		return fmt.Errorf("reading release dashboard assets: %w", err)
	}
	hasJS, hasCSS := false, false
	for _, entry := range entries {
		switch filepath.Ext(entry.Name()) {
		case ".js":
			hasJS = true
		case ".css":
			hasCSS = true
		default:
			continue
		}
		if _, err := readFile(root, "assets/"+entry.Name()); err != nil {
			return err
		}
	}
	if !hasJS || !hasCSS {
		return errors.New("release dashboard requires JavaScript and CSS bundles")
	}
	return checkDocument(root, index)
}

func directory(root *dashboardRoot, path string) (result error) {
	dir, err := root.openDirectory(path)
	if err != nil {
		return fmt.Errorf("release dashboard directory %s: %w", path, err)
	}
	defer func() {
		if err := dir.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("closing release dashboard directory %s: %w", path, err))
		}
	}()
	info, err := dir.Stat()
	if err != nil {
		return fmt.Errorf("inspecting release dashboard directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("release dashboard requires a nonsymlink directory: %s", path)
	}
	return nil
}

func readFile(root *dashboardRoot, path string) (content []byte, result error) {
	file, err := root.openFile(path)
	if err != nil {
		return nil, fmt.Errorf("opening release dashboard file %s: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("closing release dashboard file %s: %w", path, err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting release dashboard file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("release dashboard file is no longer nonempty and regular: %s", path)
	}
	content, err = io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("reading release dashboard file %s: %w", path, err)
	}
	if len(content) == 0 {
		return nil, fmt.Errorf("release dashboard file became empty: %s", path)
	}
	return content, nil
}

func dashboardPathParts(path string) ([]string, error) {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, `\`) {
		return nil, fmt.Errorf("invalid release dashboard path: %s", path)
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid release dashboard path: %s", path)
		}
	}
	return parts, nil
}

func checkDocument(root *dashboardRoot, index []byte) error {
	return dashboarddocument.Check(index, func(path string) error {
		_, err := readFile(root, path)
		return err
	})
}

func uniqueAttributes(tag []byte) error {
	return dashboarddocument.UniqueAttributes(tag)
}
