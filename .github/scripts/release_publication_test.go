package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func validateExtensionTail(steps map[string]releaseStep) error {
	if steps["Sync Version Files"].Shell != "bash" {
		return fmt.Errorf("manifest admission must use explicit Bash pipefail semantics")
	}
	contracts := map[string][]string{
		"Verify selected Extensions Kit contract": {
			`kit_info="$(azd x version)" || {`,
			`kit_version="$(printf '%s\n' "$kit_info" | sed -n 's/^Version: //p')"`,
			`if [ "$kit_version" != "0.14.0" ]; then`,
		},
		"Sync Version Files": {
			`file_version="$(cat version.txt)"`,
			`extension_version="$(sed -n -e 's/^version:[[:space:]]*//p' extension.yaml | sed -e 's/[[:space:]]\{1,\}#.*$//' -e 's/[[:space:]]*$//')"`,
			`if [ "$file_version" != "$VERSION" ] || [ "$extension_version" != "$VERSION" ]; then`,
		},
		"Create exact extension tag exclusively": {
			`extension_tag="azd-ext-microsoft-azd-waza_${VERSION}"`,
			`created_sha="$(gh api --method POST "repos/${{ github.repository }}/git/refs"`,
			`-f "ref=refs/tags/$extension_tag" -f "sha=$GITHUB_SHA" --jq '.object.sha')" || {`,
			`test "$created_sha" = "$GITHUB_SHA"`,
		},
		"Require package-only extension release": {
			`asset_names="$(gh release view "azd-ext-microsoft-azd-waza_${VERSION}"`,
			`--repo "${{ github.repository }}" --json assets --jq '.assets[].name')"`,
			`test -n "$asset_names"`,
			`*.zip|*.tar.gz) ;;`,
			`exit 1`,
		},
		"Create Release PR": {
			`push_receipt="$(git push --porcelain --force-with-lease="refs/heads/$BRANCH:" origin "HEAD:refs/heads/$BRANCH")" || {`,
			`$1 == "*" && $2 == ref && $3 == "[new branch]"`,
			`Co-authored-by: Copilot App <223556219+Copilot@users.noreply.github.com>`,
			`.github/PULL_REQUEST_TEMPLATE.md > "$PR_BODY"`,
			`--body-file "$PR_BODY"`,
		},
	}
	for name, fragments := range contracts {
		for _, fragment := range fragments {
			if !strings.Contains(steps[name].Run, fragment) {
				return fmt.Errorf("extension boundary %s must retain %q", name, fragment)
			}
		}
		if strings.Contains(steps[name].Run, "|| true") {
			return fmt.Errorf("extension boundary %s must propagate failures", name)
		}
	}
	if strings.Contains(steps["Sync Version Files"].Run, "> version.txt") ||
		strings.Contains(steps["Sync Version Files"].Run, "sed -i") ||
		strings.Contains(steps["Create Release PR"].Run, "--delete") ||
		strings.Contains(steps["Publish Extension Provenance"].Run, "--clobber") {
		return fmt.Errorf("publication must not repair source or replace existing refs/provenance")
	}
	return nil
}

func publicationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func installReleaseTagCheck(t *testing.T, dir string) {
	t.Helper()
	content, err := os.ReadFile("release_tag_check.sh")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".github", "scripts", "release_tag_check.sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeReleaseFile(t, path, content)
}

func publicationRepository(t *testing.T) (dir, remote, source, other string) {
	t.Helper()
	dir, remote = t.TempDir(), t.TempDir()
	publicationGit(t, dir, "init", "-q", "--initial-branch=main")
	publicationGit(t, remote, "init", "-q", "--bare")
	publicationGit(t, dir, "config", "user.name", "release fixture")
	publicationGit(t, dir, "config", "user.email", "release@example.invalid")
	writeReleaseFile(t, filepath.Join(dir, "registry.json"), []byte("{}\n"))
	writeReleaseFile(t, filepath.Join(dir, "version.txt"), []byte("1.2.3\n"))
	writeReleaseFile(t, filepath.Join(dir, "extension.yaml"), []byte("version: 1.2.3\n"))
	publicationGit(t, dir, "add", ".")
	publicationGit(t, dir, "commit", "-qm", "validated source")
	source = publicationGit(t, dir, "rev-parse", "HEAD")
	writeReleaseFile(t, filepath.Join(dir, "registry.json"), []byte("{\"later\":true}\n"))
	publicationGit(t, dir, "commit", "-qam", "moving default branch")
	other = publicationGit(t, dir, "rev-parse", "HEAD")
	publicationGit(t, dir, "remote", "add", "origin", remote)
	publicationGit(t, dir, "push", "-q", "origin", "main")
	return
}

func TestReleaseRemoteTagAdmission(t *testing.T) {
	script, err := os.ReadFile("release_tag_check.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"lightweight", "annotated", "missing", "wrong-source", "lookup-output-error"} {
		t.Run(state, func(t *testing.T) {
			dir, _, source, other := publicationRepository(t)
			if state != "missing" {
				args := []string{"tag", "v1.2.3", source}
				switch state {
				case "annotated":
					args = []string{"tag", "-a", "-m", "validated release", "v1.2.3", source}
				case "wrong-source":
					args = []string{"tag", "v1.2.3", other}
				}
				publicationGit(t, dir, args...)
				publicationGit(t, dir, "push", "-q", "origin", "refs/tags/v1.2.3")
			}
			before := publicationGit(t, dir, "ls-remote", "origin", "refs/tags/v1.2.3", "refs/tags/v1.2.3^{}")
			wrapper := `
git() {
  command git "$@" || return
  if [ "$1" = ls-remote ] && [ "$STATE" = lookup-output-error ]; then return 73; fi
}
`
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", wrapper+string(script), "--", "v1.2.3")
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "GITHUB_SHA="+source, "STATE="+state)
			output, err := cmd.CombinedOutput()
			valid := state == "lightweight" || state == "annotated"
			if (err == nil) != valid {
				t.Fatalf("actual remote tag admission %s: %v\n%s", state, err, output)
			}
			if after := publicationGit(t, dir, "ls-remote", "origin", "refs/tags/v1.2.3", "refs/tags/v1.2.3^{}"); before != after {
				t.Fatal("remote tag admission changed existing/absent tag")
			}
		})
	}
}

func TestReleaseStableCandidateTagAdmission(t *testing.T) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(readReleaseWorkflow(t, "squad-release.yml"), &workflow); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(releaseSteps(workflow.Jobs["release"])["Create Release"].Run, "${{ github.repository }}", "microsoft/waza")
	for _, state := range []string{"unchanged", "annotated", "moved", "removed", "lookup-output-error"} {
		t.Run(state, func(t *testing.T) {
			dir, remote, source, other := publicationRepository(t)
			publicationGit(t, dir, "tag", "v1.2.3", source)
			publicationGit(t, dir, "push", "-q", "origin", "refs/tags/v1.2.3")
			if got := publicationGit(t, dir, "--git-dir="+remote, "rev-parse", "refs/tags/v1.2.3"); got != source {
				t.Fatal("remote candidate must initially identify the validated source")
			}
			switch state {
			case "annotated":
				publicationGit(t, dir, "tag", "-a", "-m", "candidate", "annotated-candidate", source)
				publicationGit(t, dir, "push", "-q", "origin", "refs/tags/annotated-candidate")
				tagObject := publicationGit(t, dir, "rev-parse", "annotated-candidate")
				publicationGit(t, dir, "--git-dir="+remote, "update-ref", "refs/tags/v1.2.3", tagObject, source)
			case "moved":
				publicationGit(t, dir, "--git-dir="+remote, "update-ref", "refs/tags/v1.2.3", other, source)
			case "removed":
				publicationGit(t, dir, "--git-dir="+remote, "update-ref", "-d", "refs/tags/v1.2.3", source)
			}
			if got := publicationGit(t, dir, "rev-parse", "v1.2.3"); got != source {
				t.Fatal("local candidate must remain at validated source despite remote mutation")
			}
			before := publicationGit(t, dir, "ls-remote", "origin", "refs/tags/v1.2.3", "refs/tags/v1.2.3^{}")
			installReleaseTagCheck(t, dir)
			wrappers := `
gh() {
  if [ "$2" = view ]; then return 1; fi
  test "$1" = release
  test "$2" = create
  printf '%s\n' "$@" > published-arguments
}
git() {
  command git "$@" || return
  if [ "$1" = ls-remote ] && [ "$STATE" = lookup-output-error ]; then return 73; fi
}
export -f git
`
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", wrappers+script)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "VERSION=v1.2.3", "GITHUB_SHA="+source, "STATE="+state)
			output, err := cmd.CombinedOutput()
			valid := state == "unchanged" || state == "annotated"
			if (err == nil) != valid {
				t.Fatalf("actual stable candidate admission %s: %v\n%s", state, err, output)
			}
			args, readErr := os.ReadFile(filepath.Join(dir, "published-arguments"))
			if valid && readErr != nil || !valid && !os.IsNotExist(readErr) {
				t.Fatalf("stable publication did not honor mutated remote tag: %q, %v", args, readErr)
			}
			if valid && !strings.Contains(string(args), "--verify-tag\n--target\n"+source+"\n") {
				t.Fatalf("stable publication must consume existing exact-source tag: %q", args)
			}
			if after := publicationGit(t, dir, "ls-remote", "origin", "refs/tags/v1.2.3", "refs/tags/v1.2.3^{}"); before != after {
				t.Fatal("admission changed the moved/removed/valid remote tag")
			}
		})
	}
}

func TestReleaseRegistryBranchExclusive(t *testing.T) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(readReleaseWorkflow(t, "release.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	script := releaseSteps(workflow.Jobs["release-extension"])["Create Release PR"].Run
	script = strings.NewReplacer("${{ github.repository }}", "microsoft/waza",
		"${{ github.server_url }}", "https://github.com", "${{ github.run_id }}", "fixture-run").Replace(script)
	template, err := os.ReadFile("../PULL_REQUEST_TEMPLATE.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"valid", "existing", "concurrent", "concurrent-same", "push-output-error", "pr-error"} {
		t.Run(state, func(t *testing.T) {
			dir, remote, source, current := publicationRepository(t)
			ref := "refs/heads/release/v1.2.3"
			if state == "existing" {
				publicationGit(t, dir, "--git-dir="+remote, "update-ref", ref, source)
			}
			if err := os.Mkdir(filepath.Join(dir, ".github"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeReleaseFile(t, filepath.Join(dir, ".github", "PULL_REQUEST_TEMPLATE.md"), template)
			writeReleaseFile(t, filepath.Join(dir, "registry.json"), []byte("{\"published\":true}\n"))
			wrappers := `
	git() {
	  if [ "$1" = push ]; then
	    if [ "$STATE" = concurrent ]; then command git --git-dir="$REMOTE" update-ref refs/heads/release/v1.2.3 "$SOURCE"; fi
	    if [ "$STATE" = concurrent-same ]; then
	      command git push -q origin HEAD:refs/heads/release/v1.2.3 || return
	      printf peer > competitor-created
	    fi
	    command git "$@" || return
	    if [ "$STATE" = push-output-error ]; then return 73; fi
	    return
	  fi
	  command git -c commit.gpgsign=false "$@"
	}
	gh() {
	  test "$1" = pr
	  test "$2" = create
	  printf called > pr-called
	  local previous=''
	  for arg in "$@"; do
	    if [ "$previous" = --body-file ]; then cp "$arg" generated-body; fi
	    previous="$arg"
	  done
	  printf 'https://github.com/microsoft/waza/pull/12345\n'
	  if [ "$STATE" = pr-error ]; then return 73; fi
	}
	`
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", wrappers+script)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "VERSION=1.2.3", "GITHUB_SHA="+current,
				"REMOTE="+remote, "SOURCE="+source, "STATE="+state)
			output, err := cmd.CombinedOutput()
			if state == "concurrent-same" {
				if _, setupErr := os.Stat(filepath.Join(dir, "competitor-created")); setupErr != nil {
					t.Fatalf("identical competing ref was not created: %v\n%s", setupErr, output)
				}
			}
			if (err == nil) != (state == "valid") {
				t.Fatalf("exclusive generated branch %s: %v\n%s", state, err, output)
			}
			_, markerErr := os.Stat(filepath.Join(dir, "pr-called"))
			wantPR := state == "valid" || state == "pr-error"
			if wantPR && markerErr != nil || !wantPR && !os.IsNotExist(markerErr) {
				t.Fatalf("PR called without exclusive branch admission: %s, %v", state, markerErr)
			}
			head := publicationGit(t, dir, "rev-parse", "HEAD")
			want := head
			if state == "existing" || state == "concurrent" {
				want = source
			}
			if got := publicationGit(t, dir, "--git-dir="+remote, "rev-parse", ref); got != want {
				t.Fatalf("existing/generated ref changed incorrectly: %s != %s", got, want)
			}
			if wantPR {
				body, err := os.ReadFile(filepath.Join(dir, "generated-body"))
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(string(template), "\n") {
					if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "<!--") {
						if !strings.Contains(string(body), line) {
							t.Fatalf("generated PR lost template heading/comment %q", line)
						}
					}
				}
				if !strings.Contains(string(body), current) || !strings.Contains(string(body), "current-head CI remain required") {
					t.Fatal("generated PR omitted source or claimed completed future CI")
				}
				if !strings.Contains(publicationGit(t, dir, "log", "-1", "--format=%B"), "Co-authored-by: Copilot App") {
					t.Fatal("generated commit lost required trailer")
				}
			}
		})
	}
}
func TestReleaseExtensionPublicationBoundary(t *testing.T) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(readReleaseWorkflow(t, "release.yml"), &workflow); err != nil {
		t.Fatal(err)
	}
	steps := releaseSteps(workflow.Jobs["release-extension"])
	names := []string{"Verify selected Extensions Kit contract", "Create exact extension tag exclusively", "Release Extension",
		"Require package-only extension release", "Publish Extension", "Publish Extension Provenance"}
	var script strings.Builder
	for _, name := range names {
		script.WriteString(strings.ReplaceAll(steps[name].Run, "${{ github.repository }}", "microsoft/waza"))
		script.WriteByte('\n')
	}
	for _, state := range []string{"valid", "existing", "existing-same", "concurrent", "tag-error", "tag-output-error",
		"kit-unknown", "kit-output-error", "sidecar-before-publish", "empty-inventory", "inventory-output-error", "registry-error", "sidecar-error"} {
		t.Run(state, func(t *testing.T) {
			dir, remote, source, other := publicationRepository(t)
			ref := "refs/tags/azd-ext-microsoft-azd-waza_1.2.3"
			if state == "existing" || state == "existing-same" {
				target := other
				if state == "existing-same" {
					target = source
				}
				publicationGit(t, dir, "--git-dir="+remote, "update-ref", ref, target)
			}
			wrappers := `
gh() {
  if [ "$1" = api ]; then
    local ref='' sha=''
    for arg in "$@"; do
      case "$arg" in ref=*) ref="${arg#ref=}";; sha=*) sha="${arg#sha=}";; esac
    done
    test "$ref" = refs/tags/azd-ext-microsoft-azd-waza_1.2.3
    test "$sha" = "$GITHUB_SHA"
    if [ "$STATE" = tag-error ]; then return 73; fi
    if [ "$STATE" = concurrent ]; then git --git-dir="$REMOTE" update-ref "$ref" "$OTHER"; fi
    git --git-dir="$REMOTE" update-ref "$ref" "$sha" 0000000000000000000000000000000000000000 || return
    printf '%s\n' "$sha"
    if [ "$STATE" = tag-output-error ]; then return 73; fi
    return
  fi
  test "$1" = release
  if [ "$2" = view ]; then
    if [ "$STATE" = empty-inventory ]; then return; fi
    printf 'microsoft-azd-waza-linux-amd64.tar.gz\n'
    if [ "$STATE" = sidecar-before-publish ]; then printf 'release-provenance.txt\n'; fi
    if [ "$STATE" = inventory-output-error ]; then return 73; fi
    return
  fi
  test "$2" = upload
  test -f registry-published
  if [ "$STATE" = sidecar-error ]; then return 73; fi
  printf sidecar > sidecar-published
}
azd() {
  test "$1" = x
  case "$2" in
    version)
      if [ "$STATE" = kit-unknown ]; then printf 'Version: unknown\n'; else printf 'Version: 0.14.0\nCommit: recorded\nBuild Date: recorded\n'; fi
      if [ "$STATE" = kit-output-error ]; then return 73; fi
      ;;
    release)
      test "$(git --git-dir="$REMOTE" rev-parse refs/tags/azd-ext-microsoft-azd-waza_1.2.3)" = "$GITHUB_SHA"
      printf package > package-released
      ;;
    publish)
      test -f package-released
      test ! -f sidecar-published
      if [ "$STATE" = registry-error ]; then return 73; fi
      printf registry > registry-published
      ;;
    *) return 73;;
  esac
}
`
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", wrappers+script.String())
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "VERSION=1.2.3", "GITHUB_SHA="+source,
				"REMOTE="+remote, "OTHER="+other, "STATE="+state)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (state == "valid") {
				t.Fatalf("actual publication tail %s: %v\n%s", state, err, output)
			}
			for _, marker := range []string{"package-released", "registry-published", "sidecar-published"} {
				want := state == "valid"
				switch marker {
				case "package-released":
					want = state == "valid" || state == "sidecar-before-publish" || state == "empty-inventory" ||
						state == "inventory-output-error" || state == "registry-error" || state == "sidecar-error"
				case "registry-published":
					want = state == "valid" || state == "sidecar-error"
				}
				_, statErr := os.Stat(filepath.Join(dir, marker))
				if want && statErr != nil || !want && !os.IsNotExist(statErr) {
					t.Fatalf("%s reached incorrectly for %s: %v", marker, state, statErr)
				}
			}
			if state == "existing" || state == "concurrent" || state == "existing-same" {
				want := other
				if state == "existing-same" {
					want = source
				}
				if got := publicationGit(t, dir, "--git-dir="+remote, "rev-parse", ref); got != want {
					t.Fatalf("competing tag changed: %s != %s", got, want)
				}
			}
			if state == "valid" && publicationGit(t, dir, "--git-dir="+remote, "rev-parse", ref) != source {
				t.Fatal("extension tag followed moving default instead of validated source")
			}
		})
	}
}
