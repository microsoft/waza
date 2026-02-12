---
name: azd-publish
description: |
  Prepare and publish a new version of the waza azd extension.
  USE FOR: "publish extension", "release new version", "bump version",
  "prepare release", "update changelog", "azd publish", "new release",
  "version bump", "cut a release".
  DO NOT USE FOR: running evals (use waza), writing skills (use skill-authoring),
  CI/CD pipeline changes (edit workflow files directly).
metadata:
  author: spboyer
  version: "1.0"
---

# azd Extension Publish

> Automate version bumps, changelog updates, and PR creation for waza azd extension releases.

## When to Use

- Preparing a new release of the waza azd extension
- Bumping the version number (major, minor, or patch)
- Updating the changelog with changes since last release
- Creating a release PR for review

## Workflow

Follow these steps **in order**. Ask the user for input at each decision point.

### Step 1: Determine Current Version

Read the current version from `version.txt`:

```bash
cat version.txt
```

Also read `extension.yaml` to confirm the version matches. If they differ, flag it to the user before proceeding.

### Step 2: Gather Changes Since Last Version

Get the latest version tag and collect commits since then:

```bash
# Find the latest azd extension version tags
git tag --list 'azd-ext-microsoft-azd-waza_*' --sort=-v:refname | head -5

# Get commits since last azd extension tag
last_tag=$(git tag --list 'azd-ext-microsoft-azd-waza_*' --sort=-v:refname | head -1)
git log "${last_tag}..HEAD" --oneline --no-decorate
```

Summarize the changes grouped by type:
- **Added** — `feat:` commits
- **Fixed** — `fix:` commits  
- **Changed** — `refactor:`, `chore:`, `docs:` commits
- **Removed** — any removal-related commits

Present the summary to the user for review.

### Step 3: Ask for Version Bump Type

**ASK THE USER**: What type of version bump is this?

Choices:
- **major** — Breaking changes (X.0.0)
- **minor** — New features, backward compatible (0.X.0)
- **patch** — Bug fixes, small changes (0.0.X)

Compute the new version using standard semver semantics:
- Given current version `MAJOR.MINOR.PATCH`:
  - **major** → `(MAJOR+1).0.0`
  - **minor** → `MAJOR.(MINOR+1).0`
  - **patch** → `MAJOR.MINOR.(PATCH+1)`

Confirm the new version with the user before proceeding.

### Step 4: Update Version Files

Update these files with the new version:

1. **`version.txt`** — Replace contents with new version string
2. **`extension.yaml`** — Update the `version:` field

### Step 5: Update CHANGELOG.md

The changelog follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

Perform these updates:

1. **Create new version section**: Insert a new section below `## [Unreleased]` with today's date:
   ```markdown
   ## [X.Y.Z] - YYYY-MM-DD
   ```

2. **Move Unreleased content**: Move any items currently under `## [Unreleased]` into the new version section. If `[Unreleased]` is empty, populate from the git log summary gathered in Step 2.

3. **Populate from commits**: Add entries grouped under `### Added`, `### Fixed`, `### Changed` as appropriate based on the commits gathered in Step 2.

4. **Update comparison links** at the bottom of the file:
   ```markdown
   [Unreleased]: https://github.com/spboyer/waza/compare/azd-ext-microsoft-azd-waza_X.Y.Z...HEAD
   [X.Y.Z]: https://github.com/spboyer/waza/compare/azd-ext-microsoft-azd-waza_PREVIOUS...azd-ext-microsoft-azd-waza_X.Y.Z
   ```

5. **Clear the Unreleased section**: Leave `## [Unreleased]` with empty subsections or blank.

### Step 6: Review Changes

Show the user a summary of all changes made:
- New version number
- Files modified: `version.txt`, `extension.yaml`, `CHANGELOG.md`
- Show the diff with `git diff`

### Step 7: Ask About PR Creation

**ASK THE USER**: Should I create a PR with these changes?

If **yes**:

1. Create a feature branch:
   ```bash
   git checkout -b release/v{VERSION}
   ```

2. Stage and commit all changes:
   ```bash
   git add version.txt extension.yaml CHANGELOG.md
   git commit -m "chore: Prepare release v{VERSION}"
   ```

3. Push the branch:
   ```bash
   git push origin release/v{VERSION}
   ```

4. Create a PR using the GitHub CLI:
   ```bash
   gh pr create \
     --title "Release v{VERSION}" \
     --body "## Release v{VERSION}

   ### Changes
   {changelog entries for this version}

   ### Checklist
   - [ ] Version bumped in version.txt and extension.yaml
   - [ ] CHANGELOG.md updated
   - [ ] CI passes
   - [ ] Ready to publish via 'Publish azd Extension' workflow" \
     --base main \
     --head release/v{VERSION}
   ```

If **no**:
- Leave the changes uncommitted in the working tree
- Inform the user they can review and commit manually

## File Reference

| File | Purpose | What Gets Updated |
|------|---------|-------------------|
| `version.txt` | Single source of version truth | New semver version string |
| `extension.yaml` | azd extension manifest | `version:` field |
| `CHANGELOG.md` | Human-readable change history | New version section with entries |

## Important Notes

- Always use **conventional commit** prefixes (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`) when interpreting git history
- The changelog format must follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
- Version numbering must follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
- The PR branch naming convention is `release/v{VERSION}`
- After the PR is merged, the user should trigger the **Publish azd Extension** workflow (`azd-ext-release.yml`) to build, pack, and publish the extension
