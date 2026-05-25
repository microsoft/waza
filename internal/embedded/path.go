//go:build (linux && (amd64 || arm64)) || (darwin && (amd64 || arm64)) || (windows && (amd64 || arm64))

package embedded

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const embeddedCLIVersion = "1.0.49"

var pathOnce = sync.OnceValues(installEmbeddedCLI)

// Path installs the embedded Copilot CLI, if needed, and returns its path.
func Path() (string, error) {
	return pathOnce()
}

func installEmbeddedCLI() (string, error) {
	installDir, err := embeddedCLIInstallDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("creating embedded Copilot CLI install directory: %w", err)
	}

	finalPath := filepath.Join(installDir, embeddedCLIBinaryName())
	expectedHash, err := hashEmbeddedCLI()
	if err != nil {
		return "", err
	}

	if existingHash, err := hashFile(finalPath); err == nil {
		if bytes.Equal(existingHash, expectedHash) {
			if err := writeEmbeddedCLILicense(finalPath); err != nil {
				return "", err
			}
			return finalPath, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("checking existing embedded Copilot CLI: %w", err)
	}

	if err := writeEmbeddedCLI(finalPath, expectedHash); err != nil {
		return "", err
	}
	if err := writeEmbeddedCLILicense(finalPath); err != nil {
		return "", err
	}
	return finalPath, nil
}

func embeddedCLIInstallDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	if cacheDir == "" {
		return "", fmt.Errorf("could not determine a cache directory for the embedded Copilot CLI")
	}
	return filepath.Join(cacheDir, "copilot-sdk"), nil
}

func embeddedCLIBinaryName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("copilot_%s%s", sanitizeVersion(embeddedCLIVersion), ext)
}

func writeEmbeddedCLI(finalPath string, expectedHash []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(finalPath), ".copilot-cli-*")
	if err != nil {
		return fmt.Errorf("creating temporary embedded Copilot CLI file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	reader := cliReader()
	if closer, ok := reader.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	if _, err := io.Copy(tmp, io.TeeReader(reader, h)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing embedded Copilot CLI: %w", err)
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("marking embedded Copilot CLI executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing embedded Copilot CLI file: %w", err)
	}
	if !bytes.Equal(h.Sum(nil), expectedHash) {
		return fmt.Errorf("embedded Copilot CLI hash mismatch while writing")
	}

	if err := replaceFile(tmpPath, finalPath); err != nil {
		return err
	}
	removeTmp = false
	return nil
}

func replaceFile(src, dst string) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if runtime.GOOS == "windows" {
			_ = os.Remove(dst)
			err = os.Rename(src, dst)
			if err == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("installing embedded Copilot CLI (file may be locked by antivirus or another process): %w", err)
}

func writeEmbeddedCLILicense(cliPath string) error {
	if len(localEmbeddedCopilotCLILicense) == 0 {
		return nil
	}
	if err := os.WriteFile(cliPath+".license", localEmbeddedCopilotCLILicense, 0644); err != nil {
		return fmt.Errorf("writing embedded Copilot CLI license: %w", err)
	}
	return nil
}

func hashEmbeddedCLI() ([]byte, error) {
	reader := cliReader()
	if closer, ok := reader.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	h := sha256.New()
	if _, err := io.Copy(h, reader); err != nil {
		return nil, fmt.Errorf("hashing embedded Copilot CLI: %w", err)
	}
	return h.Sum(nil), nil
}

func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func sanitizeVersion(version string) string {
	if version == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range version {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
