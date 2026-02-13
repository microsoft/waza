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

### Step 1: Gather Changes and Update Changelog

Get the current version from `version.txt` and `extension.yaml`, then collect commits since the last release:

```bash
cat version.txt

# Find the latest azd extension version tags
git tag --list 'azd-ext-microsoft-azd-waza_*' --sort=-v:refname | head -5

# Get commits since last azd extension tag
last_tag=$(git tag --list 'azd-ext-microsoft-azd-waza_*' --sort=-v:refname | head -1)
git log "${last_tag}..HEAD" --oneline --no-decorate
```

If `version.txt` and `extension.yaml` differ, flag it to the user before proceeding.

Summarize the changes grouped by type:
- **Added** — `feat:` commits
- **Fixed** — `fix:` commits
- **Changed** — `refactor:`, `chore:`, `docs:` commits
- **Removed** — any removal-related commits

Present the summary to the user for review.

Then update `CHANGELOG.md`. The changelog follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

Perform these updates (using a placeholder version `X.Y.Z` — the actual version is determined in Step 2):

1. **Move Unreleased content**: Move any items currently under `## [Unreleased]` into a staging area. If `[Unreleased]` is empty, populate from the git log summary gathered above.

2. **Populate from commits**: Prepare entries grouped under `### Added`, `### Fixed`, `### Changed` as appropriate based on the commits gathered above.

Hold these changelog entries — the new version section header and comparison links will be finalized after the version is determined in Step 2.

### Step 2: Determine Version

Based on the changes gathered in Step 1, **recommend** a version bump type using standard semver semantics:

- **major** — Breaking changes, removals of public API (`feat!:`, `BREAKING CHANGE:`) → `(MAJOR+1).0.0`
- **minor** — New features, backward compatible (`feat:`) → `MAJOR.(MINOR+1).0`
- **patch** — Bug fixes, docs, refactors, chores (`fix:`, `docs:`, `refactor:`, `chore:`) → `MAJOR.MINOR.(PATCH+1)`

Present the recommendation with rationale (e.g., "I see 3 `feat:` commits and no breaking changes — recommending a **minor** bump").

**ASK THE USER** to confirm the recommended bump or choose a different one.

Compute the new version and confirm with the user before proceeding.

Then finalize the changelog:

1. **Create new version section**: Insert a new section below `## [Unreleased]` with today's date:
   ```markdown
   ## [X.Y.Z] - YYYY-MM-DD
   ```

2. **Add the prepared entries** from Step 1 under the new version section.

3. **Update comparison links** at the bottom of the file:
   ```markdown
   [Unreleased]: https://github.com/spboyer/waza/compare/azd-ext-microsoft-azd-waza_X.Y.Z...HEAD
   [X.Y.Z]: https://github.com/spboyer/waza/compare/azd-ext-microsoft-azd-waza_PREVIOUS...azd-ext-microsoft-azd-waza_X.Y.Z
   ```

4. **Clear the Unreleased section**: Leave `## [Unreleased]` with empty subsections or blank.

### Step 3: Update Version Files

Update these files with the new version:

1. **`version.txt`** — Replace contents with new version string
2. **`extension.yaml`** — Update the `version:` field

### Step 4: Review Changes

Show the user a summary of all changes made:
- New version number
- Files modified: `version.txt`, `extension.yaml`, `CHANGELOG.md`
- Show the diff with `git diff`

### Step 5: Ask About PR Creation

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
