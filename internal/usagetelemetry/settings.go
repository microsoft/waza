package usagetelemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Persistent collection choices stored by `waza telemetry enable|disable`.
const (
	CollectionEnabled  = "enabled"
	CollectionDisabled = "disabled"
)

const (
	maxSettingsBytes      = 4096
	maxProjectConfigBytes = 1 << 20
	settingsLockWait      = 500 * time.Millisecond
	staleLockAge          = 10 * time.Second
)

var errSettingsLocked = errors.New("telemetry settings are locked by another process")

// readRegularFile reads at most limit bytes from a regular file. Policy files
// are read before every command, so FIFOs, devices, and directories are
// rejected before opening; opening them could block startup indefinitely.
func readRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.Open(path) //nolint:gosec // fixed policy file path
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return data, nil
}

// withSettingsLock serializes read-modify-write updates across waza processes
// so a concurrent installation-ID write cannot overwrite a disable.
func withSettingsLock(path string, fn func() error) error {
	return withSettingsLockWait(path, settingsLockWait, fn)
}

func withSettingsLockWait(path string, wait time.Duration, fn func() error) error {
	if path == "" {
		return errors.New("telemetry settings path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock := path + ".lock"
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			closeErr := f.Close()
			fnErr := fn()
			return errors.Join(fnErr, closeErr, os.Remove(lock))
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > staleLockAge {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return errSettingsLocked
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Settings is the local-only persistent telemetry state.
type Settings struct {
	Collection     string `json:"collection,omitempty"`
	InstallationID string `json:"installationId,omitempty"`
}

// DefaultSettingsPath returns ~/.waza/telemetry.json, or "" when the home
// directory is unknown (which leaves collection inactive).
func DefaultSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".waza", "telemetry.json")
}

// LoadSettings reads the settings file. A missing file yields defaults; any
// unreadable, oversized, malformed, or unknown content is an error, which the
// policy treats as denial.
func LoadSettings(path string) (Settings, error) {
	if path == "" {
		return Settings{}, errors.New("telemetry settings path is unavailable")
	}
	data, err := readRegularFile(path, maxSettingsBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	s, err := decodeSettings(data)
	if err != nil {
		return Settings{}, fmt.Errorf("parsing telemetry settings: %w", err)
	}
	switch s.Collection {
	case "", CollectionEnabled, CollectionDisabled:
	default:
		return Settings{}, fmt.Errorf("unsupported telemetry collection value")
	}
	if s.InstallationID != "" && !validUUIDv4(s.InstallationID) {
		return Settings{}, errors.New("telemetry installation ID is not a UUIDv4")
	}
	if s.Collection == CollectionDisabled && s.InstallationID != "" {
		return Settings{}, errors.New("disabled telemetry settings must not hold an installation ID")
	}
	return s, nil
}

// decodeSettings accepts exactly one JSON object whose known keys appear at
// most once with string values; null, duplicates, and other types are errors.
func decodeSettings(data []byte) (Settings, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if tok, err := decoder.Token(); err != nil || tok != json.Delim('{') {
		return Settings{}, errors.New("settings must be a JSON object")
	}
	var s Settings
	seen := map[string]bool{}
	for decoder.More() {
		keyTok, err := decoder.Token()
		if err != nil {
			return Settings{}, err
		}
		key, _ := keyTok.(string)
		if seen[key] {
			return Settings{}, fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		valueTok, err := decoder.Token()
		if err != nil {
			return Settings{}, err
		}
		value, ok := valueTok.(string)
		if !ok {
			return Settings{}, fmt.Errorf("key %q must be a string", key)
		}
		switch key {
		case "collection":
			s.Collection = value
		case "installationId":
			s.InstallationID = value
		default:
			return Settings{}, fmt.Errorf("unknown key %q", key)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return Settings{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Settings{}, errors.New("trailing data")
	}
	return s, nil
}

// SaveSettings atomically replaces the settings file.
func SaveSettings(path string, s Settings) error {
	if path == "" {
		return errors.New("telemetry settings path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".telemetry-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(append(data, '\n'))
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	return nil
}

// Disable persists the disabled choice, deletes the installation ID, and
// discards any pending in-memory events. It never sends anything.
func Disable(path string) error {
	discardPending()
	// Waiting past staleLockAge lets a disable break an abandoned lock without
	// ever racing a live installation-ID writer.
	return withSettingsLockWait(path, staleLockAge+settingsLockWait, func() error {
		return SaveSettings(path, Settings{Collection: CollectionDisabled})
	})
}

// Enable persists the enabled choice. It does not create an installation ID.
func Enable(path string) error {
	return withSettingsLock(path, func() error {
		s, err := LoadSettings(path)
		if err != nil {
			s = Settings{}
		}
		s.Collection = CollectionEnabled
		return SaveSettings(path, s)
	})
}

// ResetID deletes the installation ID; a new random one is created only when
// an event is next sent by an active, non-CI client.
func ResetID(path string) error {
	if s, err := LoadSettings(path); err == nil && s.InstallationID == "" {
		return nil
	}
	return withSettingsLock(path, func() error {
		s, err := LoadSettings(path)
		if err != nil {
			return err
		}
		if s.InstallationID == "" {
			return nil
		}
		s.InstallationID = ""
		return SaveSettings(path, s)
	})
}

func validUUIDv4(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == value
}
