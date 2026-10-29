## Summary

<!-- 2-4 sentences: describe the bug, feature request, documentation gap, or task. -->

Create this issue with the title `Release media: [release version] screenshots and short videos`. Prepare accurate feature screenshots and short promotional videos for **[release version]** after its release-candidate or shipped scope is verified. Include the dashboard `waza dashboard --lab` journey and other features actually present in the release. This is a reusable task body; create or reuse one issue per release, and do not begin capture until the readiness evidence below is recorded.

## Type of issue

- [ ] Bug
- [ ] Feature request
- [ ] Documentation
- [x] Maintenance
- [ ] Question or investigation

## Agent instructions

<!-- Make this actionable for a coding agent. Include constraints, likely files, and what not to change. -->

- Goal: Produce review-ready media drafts for [release version], tied to verified product behavior.
- In scope: Feature screenshot set; 15–45 second short-video drafts with readable 9:16 framing; dashboard `waza dashboard --lab` journey; other in-scope features; captions, transcripts, screenshot alt text, provenance, and reproduction notes.
- Out of scope: Product code changes, unmerged feature coverage, release verification, release blog/announcement, uploading, social posting, or publication.
- Likely files or areas: `docs/RELEASE-MEDIA.md`; verified release source and deterministic capture paths recorded below; use a release-specific artifact directory such as `release-media/[release-version]/` in the session Library or approved artifact storage, not tracked binary files.
- Dependencies or related issues: Release plan: [URL]; readiness evidence must be verified and recorded before capture. Search open and closed issues for an existing task before creating this one; reuse an appropriate issue rather than duplicating it.

## Current behavior

<!-- What happens today? For bugs, include the actual result and error output if available. -->

Release media is prepared through a manual, per-release task. No automated issue creator or capture workflow is implied.

## Expected behavior

<!-- What should happen instead? For feature requests, describe the desired outcome. -->

After readiness is established, prepare media only for features in the verified release scope. Show actual UI and accurately label synthetic, mock, cached, or live data; keep drafts distinct from assets approved for publication.

## Steps to reproduce

<!-- For bugs, provide the smallest reproducible example. Delete if not applicable. -->

1. Confirm this issue is the single media task for [release version] by searching open and closed issues; record the release-plan link above.
2. Have Virgil verify and record the full release-candidate commit SHA, or shipped tag and resolved SHA; required-check status and evidence source; merged feature scope; and usable deterministic fixture/capture paths with data provenance.
3. Wait for the readiness handoff before capture. If the source SHA changes, revalidate scope and readiness and recapture affected assets.

## Environment

<!-- Fill in what you know. Delete fields that are not relevant. -->

- OS: [record actual capture environment]
- Waza version or commit: [release version and verified full source SHA; do not guess]
- Go version: N/A unless needed by the verified capture path
- Command run: [record actual command(s) and result; do not claim unrun checks]

## Acceptance criteria

<!-- Use checkboxes so an agent can verify completion directly. -->

- [ ] Issue is the unique task for this release; open and closed issue search and release-plan URL are recorded.
- [ ] Virgil's readiness evidence is recorded: verified source/tag and full SHA, required-check evidence, actual merged feature scope, and usable deterministic capture paths/data provenance.
- [ ] Screenshot set and short-video drafts show only observed, in-scope behavior; dashboard `--lab` is shown with its actual opt-in state and limits.
- [ ] Videos have readable 9:16 framing, accurate captions and transcripts, a concrete feature action/value and concise CTA; screenshots have useful alt text.
- [ ] No private data, fabricated UI/results/claims, unapproved paid activity, or media with unverified rights is included.
- [ ] Draft assets, provenance, checksums when useful, capture environment/tool versions, reproduction notes, caveats, and review sign-off are organized in approved release-specific artifact storage; no large binaries are added to source.
- [ ] Source SHA changes, if any, have triggered revalidation and recapture of affected assets.
- [ ] Any public upload or publication remains pending until explicit approval through the existing release process.

## Validation expectations

<!-- List expected automated or manual checks, or say why validation is not applicable. -->

- [ ] `go test ./...`
- [ ] `make lint` or `golangci-lint run`
- [ ] Docs site checked, if docs changed
- [ ] Web/dashboard checks run, if `web/` changes
- [ ] Other: N/A for a media-only task. Manually verify each final asset against the recorded source SHA, feature scope, fixture provenance, accessibility, privacy, rights, and export playback; record exact checks actually performed.

## Additional context

<!-- Add links, screenshots, logs, related issues, or implementation notes. -->

Squad owner: Tess (Release Media Producer), `squad:tess`; this is AI-only ownership, not a GitHub human assignee. Role-fit model preferences: Tess `gpt-6.1-sol` for capture/production; Danny `gpt-6-luna` for narrative and copy; Rusty `gpt-5.6-terra` for independent technical/evidence review; Saul `gpt-6-luna` for optional editorial review only. See [`docs/RELEASE-MEDIA.md`](https://github.com/microsoft/waza/blob/main/docs/RELEASE-MEDIA.md) and [`.squad/model-policy.md`](https://github.com/microsoft/waza/blob/main/.squad/model-policy.md). These are preferences, not proof of host execution or model benchmarks. A monitor noticing this issue or its label does not satisfy readiness or authorize capture. Media drafts are not publication approval.
