package execution

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

type evidenceJudgeDirs struct {
	path, work, base, config, home, scratch string
	parent, root                            *os.Root
	identity                                os.FileInfo
	name                                    string
	remove                                  func(string) error
	closeRoot                               func() error
}

func newEvidenceJudgeDirs() (*evidenceJudgeDirs, error) {
	// os.Root does not retain directory identities on these platforms.
	if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		return nil, errors.New("evidence judge requires handle-rooted filesystem isolation on this platform")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, errors.New("evidence judge requires an absolute user cache directory outside project ancestry")
	}
	return createEvidenceJudgeDirs(filepath.Join(cache, "waza-evidence-judges"))
}

func createEvidenceJudgeDirs(cache string) (_ *evidenceJudgeDirs, resultErr error) {
	if !filepath.IsAbs(cache) {
		return nil, errors.New("evidence judge cache directory must be absolute")
	}
	if err := evidenceJudgeAdmitAncestors(cache); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, fmt.Errorf("creating judge cache parent: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(cache)
	if err != nil {
		return nil, fmt.Errorf("resolving judge cache parent: %w", err)
	}
	if err := evidenceJudgeAdmitAncestors(canonical); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("opening judge cache parent: %w", err)
	}
	name := "judge-" + rand.Text()
	if err := parent.Mkdir(name, 0o700); err != nil {
		return nil, errors.Join(fmt.Errorf("creating private judge directory: %w", err), parent.Close())
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		// Do not recursively remove a directory whose handle was not retained.
		return nil, errors.Join(fmt.Errorf("opening private judge directory (retained at %s): %w", filepath.Join(canonical, name), err), parent.Close())
	}
	d := &evidenceJudgeDirs{path: filepath.Join(canonical, name), parent: parent, root: root, name: name}
	d.remove, d.closeRoot = root.RemoveAll, root.Close
	d.identity, err = root.Stat(".")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("identifying private judge directory (retained at %s): %w", d.path, err), root.Close(), parent.Close())
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, d.close())
		}
	}()
	for _, name := range []string{"work", "base", "home", "scratch"} {
		if err := root.Mkdir(name, 0o700); err != nil {
			return nil, fmt.Errorf("creating private judge subdirectory: %w", err)
		}
	}
	d.work, d.base = filepath.Join(d.path, "work"), filepath.Join(d.path, "base")
	// The native session.delete RPC resolves persisted state under COPILOT_HOME.
	// Keep the explicitly isolated session config root aligned with that base.
	d.config = d.base
	d.home, d.scratch = filepath.Join(d.path, "home"), filepath.Join(d.path, "scratch")
	return d, nil
}

// A checkout may be a worktree (.git is a file), or may expose actor inputs
// without git. Inspect every canonical ancestor; ambient TMPDIR is never used.
func evidenceJudgeAdmitAncestors(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil {
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
				return errors.New("evidence judge cache ancestry must not be writable by other users")
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("checking evidence judge cache ancestry permissions: %w", err)
		}
		for _, marker := range []string{".git", ".waza.yaml", "skills"} {
			_, err := os.Lstat(filepath.Join(current, marker))
			if err == nil {
				return errors.New("evidence judge cache directory inherits project or skill configuration")
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("checking evidence judge cache ancestry: %w", err)
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func (d *evidenceJudgeDirs) environment() []string {
	env := []string{
		"HOME=" + d.home, "USERPROFILE=" + d.home,
		"XDG_CONFIG_HOME=" + d.home, "XDG_CACHE_HOME=" + d.home, "XDG_DATA_HOME=" + d.home,
		"APPDATA=" + d.home, "LOCALAPPDATA=" + d.home,
		"TMPDIR=" + d.scratch, "TMP=" + d.scratch, "TEMP=" + d.scratch,
		"COPILOT_HOME=" + d.base, "COPILOT_DISABLE_KEYTAR=1",
		"COPILOT_TELEMETRY_DISABLED=1", "DO_NOT_TRACK=1",
		"COPILOT_SKIP_UPDATE_CHECK=1", "NO_COLOR=1",
	}
	if runtime.GOOS == "windows" {
		// Windows requires its system directory even for native child startup.
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			env = append(env, "SystemRoot="+systemRoot)
		}
	} else {
		// Native bundled runtimes need no actor-provided PATH or shell helpers.
		env = append(env, "PATH=/usr/bin:/bin")
	}
	return env
}

func (d *evidenceJudgeDirs) close() error {
	var errs []error
	// Detect parent/leaf replacements before following any path used by the
	// runtime. Keep rooted handles until cleanup is complete. This protects
	// against path/symlink substitution, not a hostile process with our UID.
	current, err := os.Lstat(d.path)
	owned := err == nil && os.SameFile(current, d.identity) && current.IsDir()
	if !owned {
		errs = append(errs, fmt.Errorf("evidence judge directory identity changed; residue retained at %s", d.path))
	} else {
		entries, err := fs.ReadDir(d.root.FS(), ".")
		if err != nil {
			errs = append(errs, fmt.Errorf("reading evidence judge cleanup directory %s: %w", d.path, err))
		} else {
			for _, entry := range entries {
				if err := d.remove(entry.Name()); err != nil {
					errs = append(errs, fmt.Errorf("cleaning evidence judge residue %s: %w", filepath.Join(d.path, entry.Name()), err))
				}
			}
		}
	}
	if err := d.closeRoot(); err != nil {
		errs = append(errs, fmt.Errorf("closing evidence judge directory %s: %w", d.path, err))
	}
	if owned && len(errs) == 0 {
		info, err := d.parent.Lstat(d.name)
		if err != nil || !os.SameFile(info, d.identity) || !info.IsDir() {
			errs = append(errs, fmt.Errorf("evidence judge directory replaced during cleanup; retained at %s", d.path))
		} else if err := d.parent.Remove(d.name); err != nil {
			errs = append(errs, fmt.Errorf("removing evidence judge directory %s: %w", d.path, err))
		}
	}
	if err := d.parent.Close(); err != nil {
		errs = append(errs, fmt.Errorf("closing evidence judge cache parent: %w", err))
	}
	return errors.Join(errs...)
}
