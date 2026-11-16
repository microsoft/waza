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
	maxSettingsBytes = 4096
	settingsLockWait = 500 * time.Millisecond
	staleLockAge     = 10 * time.Second
)

var errSettingsLocked = errors.New("telemetry settings are locked by another process")

// withSettingsLock serializes read-modify-write updates across waza processes
// so a concurrent installation-ID write cannot overwrite a disable.
func withSettingsLock(path string, fn func() error) error {
	if path == "" {
		return errors.New("telemetry settings path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock := path + ".lock"
	deadline := time.Now().Add(settingsLockWait)
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
	file, err := os.Open(path) //nolint:gosec // fixed per-user settings path
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	defer file.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return Settings{}, err
	}
	if len(data) > maxSettingsBytes {
		return Settings{}, errors.New("telemetry settings file is too large")
	}
	var s Settings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return Settings{}, fmt.Errorf("parsing telemetry settings: %w", err)
	}
	if decoder.More() {
		return Settings{}, errors.New("telemetry settings contain trailing data")
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
	disabled := Settings{Collection: CollectionDisabled}
	err := withSettingsLock(path, func() error { return SaveSettings(path, disabled) })
	if errors.Is(err, errSettingsLocked) {
		// A disable must always win, even against a wedged lock holder.
		return SaveSettings(path, disabled)
	}
	return err
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
