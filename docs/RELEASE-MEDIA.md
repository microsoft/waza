# Release Media

This process prepares accurate, reusable product media for **every Waza
release**. It covers feature screenshots and short promotional videos, including
a dashboard `waza dashboard --lab` journey and other features actually included
in the release. It does not create an announcement, publish assets, or change
the release workflow.

For the initial rollout, the planning target is v0.40.0. Issue #754 and #757
through #762 are planning references only; verify the eventual merged release
scope and readiness before selecting features or recording anything. This
document does not assert that v0.40.0 is ready or that any referenced work has
shipped.

The concrete v0.40.0 media follow-up is [issue #783](https://github.com/microsoft/waza/issues/783),
labeled `squad:tess` and `type:chore`, currently unassigned, with capture
explicitly deferred until readiness is verified. This is a release-media
follow-up, not core roadmap scope.

## Plan one task per release

During release planning, search both open and closed GitHub issues for an
existing media task for the target version. Reuse the appropriate task rather
than creating a duplicate. If none exists, create one from
[`templates/release-media-task.md`](templates/release-media-task.md), then add
its URL to the release plan. This is a **manual checkpoint**; there is no
automatic task creator or release watcher.

Use the title `Release media: vX.Y.Z screenshots and short videos`. For the
initial v0.40.0 task, replace the template's version placeholder with `v0.40.0`
and keep the references above explicitly conditional on readiness and merged
scope.

The accountable Squad owner is Tess, Release Media Producer (`squad:tess`).
This is AI-only issue ownership; do not assign a GitHub human. Tess owns capture
and production, not product UI/code, release verification, or publication.
Roster/model policy changes are maintained by the coordinator in Squad-owned
files; this document does not configure runtime dispatch.

## Readiness handoff

The issue may be tracked before the release candidate is ready, but media work
must wait until Virgil verifies and records:

- The full release-candidate commit SHA, or the shipped tag and its resolved
  full SHA.
- Required-check status and the source of that evidence.
- The merged features actually in the release scope. Do not promise or depict
  work that is still unmerged or otherwise absent from that source.
- Usable deterministic fixture and capture paths for each planned screen or
  journey. Record how each fixture is populated and whether its content is
  synthetic, mock, cached, or live.

This handoff is not a new code-merge or release blocker. A `squad:tess` label
only identifies ownership: an automated monitor noticing the issue does not
authorize capture or satisfy readiness. If the source SHA changes, repeat the
scope/readiness check and revalidate or recapture affected assets.

## Prepare the media

1. List the release features, user value, and supported claims. Tie each
   screenshot or video beat to an in-scope feature and observed product behavior.
2. Plan a concise hook, feature action, value, and call to action for each
   short. Use clear captions and transcripts; provide useful alt text for
   stills. Keep important UI and captions readable in a 9:16 crop.
3. Capture real product UI from the verified release source using deterministic,
   public or synthetic fixtures. A clean landscape source recording may be
   retained where it helps produce vertical shorts. For Lab, show its actual
   opt-in state and limits; never imply that a demo, mock, cache, or authored
   result is a live evaluation.
4. Produce a feature screenshot set and short promotional cuts, generally
   15–45 seconds each. Select features based on the verified release scope;
   do not require a fixed number of clips or claim every unmerged item is
   covered.
5. Review the unedited captures and final exports against the evidence,
   privacy, accessibility, and rights checks below. Keep drafts separate from
   anything approved for publication.

Model routing follows the role-fit preferences in
[`.squad/model-policy.md`](../.squad/model-policy.md); those preferences do not
prove which model a host actually ran or establish a benchmark ranking:

The selection is based on task fit, not a claim that one model is universally
best: Tess's `gpt-6.1-sol` preference is for repeatable browser/CLI capture,
editing automation, and export checks; Danny's `gpt-6-luna` preference fits
narrative and copy; Rusty's `gpt-5.6-terra` remains the independent technical
and evidence gate.

| Work | Owner and model preference |
|---|---|
| Capture, editing workflow, export checks, and artifact organization | Tess — `gpt-6.1-sol` |
| Hooks, storyboard, narrative, captions, and social-copy drafts | Danny — `gpt-6-luna` |
| Independent technical, product-claim, and evidence review | Rusty — `gpt-5.6-terra` |
| Editorial or documentation-style check, only when useful | Saul — `gpt-6-luna` |

Rusty's review is an independent technical/evidence gate. Saul's editorial
check is optional and is not a second routine blocking review. A host without
per-task model control must report that limitation rather than claim these
preferences were enforced. Do not invent runtime task-model overrides.

## Evidence, privacy, and rights checks

- Show only behavior observed in the verified source and supported by its
  fixtures. Do not fabricate UI, results, metrics, customer stories, or
  performance claims.
- Identify whether displayed data is synthetic, mock, cached, or live, and
  avoid presenting one category as another. Do not run paid evaluations or
  access paid services just to improve the media.
- Exclude private prompts, user data, credentials, personal information, and
  session history. Use only material approved for this release and audience.
- Do not use copyrighted music or stock media, or a person's voice or likeness,
  without verified rights and any required consent. Do not assume a license
  from availability in a tool.
- Include accurate captions and a transcript for videos, and descriptive alt
  text for screenshots. Check that captions and key interface text remain
  legible at the intended short-video aspect ratio.
- Record any caveats, missing paths, or unsupported claims. Mark a deliverable
  blocked or deferred instead of substituting an unverified example.

## Deliverables and review

Keep deliverables in a release-specific directory such as
`release-media/vX.Y.Z/` in the session Library or approved artifact storage;
do not add large binary captures or exports to the source repository. Organize
the reviewed drafts with:

- Feature screenshots and final short-video exports.
- Captions and transcripts for each video, plus alt text for screenshots.
- A concise manifest or task record linking each asset to its feature, source
  SHA, fixture/data type, capture environment, tool versions, and any verified
  rights or caveats. Include checksums when available and useful.
- Reproduction notes for the verified capture path and review sign-off. Keep
  source recordings only when needed and approved; protect them to the same
  privacy and rights standard as the exports.

Do not assume FFmpeg or any other editor is installed, provide global
installation steps, or claim an export was validated unless it was actually
checked in the recorded environment. Keep incomplete work clearly marked.
Tess's production handoff and Rusty's independent evidence review produce
drafts for release stakeholders; they do not authorize upload, social posting,
or publication. Obtain explicit approval through the existing release process
before any public use. Virgil retains release verification and publication
boundaries.
