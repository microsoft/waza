package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyCLIArtifact checks the sidecars produced by the release build: a
// sha256sum line naming the binary and the exact source commit.
func verifyCLIArtifact(dir string, p platform, sourceSHA string) (string, string, error) {
	name := cliBinaryName(p)
	binary := filepath.Join(dir, name)
	digest, err := fileSHA256(binary)
	if err != nil {
		return "", "", fmt.Errorf("missing CLI artifact: %w", err)
	}
	recorded, err := os.ReadFile(binary + ".sha256")
	if err != nil {
		return "", "", fmt.Errorf("missing CLI digest: %w", err)
	}
	if string(recorded) != digest+"  "+name+"\n" {
		return "", "", fmt.Errorf("%s digest sidecar %q does not match actual %s", name, recorded, digest)
	}
	source, err := os.ReadFile(binary + ".source-sha.txt")
	if err != nil {
		return "", "", fmt.Errorf("missing CLI source record: %w", err)
	}
	if string(source) != sourceSHA+"\n" {
		return "", "", fmt.Errorf("%s source record %q, want %s", name, source, sourceSHA)
	}
	return binary, digest, nil
}

// verifyExtensionProvenance requires the packed archive to be listed with its
// actual digest in release-provenance.txt for the exact source commit.
func verifyExtensionProvenance(dir string, p platform, sourceSHA string) (string, string, error) {
	archive := filepath.Join(dir, extensionArchiveName(p))
	digest, err := fileSHA256(archive)
	if err != nil {
		return "", "", fmt.Errorf("missing extension archive: %w", err)
	}
	provenance, err := os.Open(filepath.Join(dir, "release-provenance.txt"))
	if err != nil {
		return "", "", fmt.Errorf("missing extension provenance: %w", err)
	}
	defer provenance.Close()
	scanner := bufio.NewScanner(provenance)
	if !scanner.Scan() || scanner.Text() != "source_sha="+sourceSHA {
		return "", "", fmt.Errorf("extension provenance is not bound to %s", sourceSHA)
	}
	listed := ""
	for scanner.Scan() {
		hash, path, ok := strings.Cut(scanner.Text(), "  ")
		if !ok || !sha256Pattern.MatchString(hash) {
			return "", "", fmt.Errorf("malformed extension provenance line %q", scanner.Text())
		}
		if path == "artifacts/"+extensionArchiveName(p) {
			if listed != "" {
				return "", "", fmt.Errorf("extension provenance lists %s twice", path)
			}
			listed = hash
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if listed != digest {
		return "", "", fmt.Errorf("%s digest %s is not the recorded %q", extensionArchiveName(p), digest, listed)
	}
	return archive, digest, nil
}

type archiveEntry struct {
	data       []byte
	executable bool
}

// extractExtension reads the archive and requires exactly the manifest and
// the platform entry point as regular files.
func extractExtension(archive string, p platform, manifest []byte, version, dest string) (string, error) {
	entries, err := readArchive(archive)
	if err != nil {
		return "", err
	}
	// Windows checkouts may convert the source manifest to CRLF; packs are built on Linux.
	manifest = bytes.ReplaceAll(manifest, []byte("\r\n"), []byte("\n"))
	entry := extensionEntryPoint(p)
	if len(entries) != 2 || entries["extension.yaml"] == nil || entries[entry] == nil {
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		return "", fmt.Errorf("%s layout %q, want exactly extension.yaml and %s", filepath.Base(archive), names, entry)
	}
	if !bytes.Equal(entries["extension.yaml"].data, manifest) {
		return "", fmt.Errorf("%s extension.yaml differs from the source manifest", filepath.Base(archive))
	}
	if !bytes.Contains(manifest, []byte("\nversion: "+version+"\n")) {
		return "", fmt.Errorf("extension manifest does not declare version %s", version)
	}
	if p.OS == "linux" && !entries[entry].executable {
		return "", fmt.Errorf("%s entry point %s is not executable", filepath.Base(archive), entry)
	}
	path := filepath.Join(dest, entry)
	if err := os.WriteFile(path, entries[entry].data, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func readArchive(path string) (map[string]*archiveEntry, error) {
	entries := map[string]*archiveEntry{}
	add := func(name string, regular bool, executable bool, r io.Reader) error {
		if !regular || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("%s contains non-regular or nested entry %q", filepath.Base(path), name)
		}
		if entries[name] != nil {
			return fmt.Errorf("%s repeats entry %q", filepath.Base(path), name)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		entries[name] = &archiveEntry{data: data, executable: executable}
		return nil
	}
	if strings.HasSuffix(path, ".zip") {
		reader, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		for _, file := range reader.File {
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			err = add(file.Name, file.Mode().IsRegular(), file.Mode()&0o111 != 0, rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
		return entries, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		if err := add(header.Name, header.Typeflag == tar.TypeReg, header.Mode&0o111 != 0, tr); err != nil {
			return nil, err
		}
	}
}
