---
name: release-reel
description: |
  Make a Waza release highlight video in the established dark navy/teal style:
  caption-led 1080p landscape, real release evidence, contributor thanks, repo
  and release-post CTA, and optional original upbeat electronic music.
  USE FOR: "release reel", "release highlight video", "make a release video",
  "promo video", "same format/style as the 0.40.0 reel".
  DO NOT USE FOR: cutting a release, changing product code, automatic posting.
metadata:
  author: spboyer
  version: "1.0"
---

# Release Reel

Produce the same format as the approved v0.40.0 electronic reel, with **new,
verified release content**, not a replay of old claims. This is a manual
production skill, not a release watcher or permission to publish.

Follow [the canonical release-media process](../../../docs/RELEASE-MEDIA.md)
and its readiness, privacy, rights, and review requirements. In an actual Squad
session, preserve Tess/Danny/Virgil/Rusty ownership; loading this skill does not
start a Squad session or authorize delegation.

## Creative defaults

- 60 seconds, 1920x1080, 24 fps, H.264/yuv420p MP4, fast-start.
- Dark navy background, subtle technical grid, teal and blue accents, bold sans
  headlines, monospace terminal/code, rounded cards, numbered progress footer.
- Seven beats: hook (5s), exact feature demonstration (13s), second feature
  (10s), real UI (12s), evidence/improvements (8s), contributors (5s), CTA (7s).
  Replace these subjects when the new release calls for different features.
- Short captions, natural wording, no decorative emoji, no voiceover by default.
- Original **120 BPM electronic** music: digital arpeggios, syncopated bass,
  light kick/snare/hi-hats, brief breakdown and fades. Energetic developer-demo
  feel, not mellow lounge or overpowering club music. Keep a silent master.
- Thank verified contributors by public name/GitHub handle. Do not infer
  employer affiliation or LinkedIn/X handles.
- Close with the actual release-post URL, repo URL, and a lighthearted star ask.
  Keep the released feature scope and known limitations accurate.

This landscape reel is an additional format; it does not replace the canonical
15-45 second 9:16 deliverables where those are requested.

## 1. Establish release evidence

Read the release body, tagged release post and comparison, plus current caveats
about the published artifacts. Resolve the tag to its full commit SHA. Do not
use current main behavior as proof of what shipped. Fetch the exact example
and synthetic fixtures from that tag, and verify downloaded binary checksums.
Keep the tag/version/source, commands, fixture provenance and captured outputs
in a release-specific session artifact directory outside the checkout.

Use the released binary for CLI/UI capture where possible. Check its real
`--help` (Lab is served using `waza serve --lab --no-browser`). Verify server
readiness and stop only processes you started. No paid evals or live providers.

For runner footage, save the **actual** terminal JSON, then show selected fields
with an on-screen label such as "Actual mock/text output; selected fields".
Capture actual released UI with tagged fixtures. Label synthetic data on screen;
do not reconstruct missing conversations or fabricate UI or measurements.
Code snippets are illustrative configuration, not evidence of execution.

## 2. Prepare inputs and tools

All bundled paths below are relative to this skill directory. Use an existing
Python 3.11+ runtime, Pillow and FFmpeg with libx264/AAC, or `imageio-ffmpeg`.
The renderer also needs a font directory containing `LiberationSans-Regular.ttf`,
`LiberationSans-Bold.ttf`, and `LiberationMono-Regular.ttf`.
Check dependencies first; report missing tools and obtain approval before
installing any. Do not globally install packages, connect services, or expand
permissions because this skill mentions them.

Copy `assets/storyboard.example.json` into the release's session artifact
directory. It is an **authored v0.40.0 example**, not current release evidence.
Replace version, date, full source SHA, links, feature copy, contributors,
provenance and scene contents. Point the terminal scene at captured JSON and the
image scene at a real capture; paths resolve relative to the storyboard.
Record capture provenance for each scene. Don't copy this session's remote paths.
Do not use the example's empty source SHA or placeholder assets for delivery.

## 3. Render

```bash
# SKILL_DIR is this skill's absolute directory; ARTIFACTS is an approved session directory.
python "$SKILL_DIR/scripts/render.py" "$ARTIFACTS/storyboard.json" \
  --output "$ARTIFACTS/highlights.mp4" \
  --font-dir /usr/share/fonts/truetype/liberation

# Preserve the silent master; write a separate, original-music version.
python "$SKILL_DIR/scripts/music.py" "$ARTIFACTS/highlights.mp4" \
  --output "$ARTIFACTS/highlights-electronic.mp4"
```

The renderer also writes a poster, SRT captions and a JSON manifest with input
hashes and tool versions. It refuses to overwrite output files. It does not
validate truth or rights: metadata is authored provenance, not attestation.
Use a private artifact directory. Output sets are not transactional; a failure
can leave partial files for inspection. Paths are not a confinement boundary.
The music script synthesizes all notes/drums locally, uses no third-party
recordings or services, preserves the video stream, and records the output hash
and synthesis settings in a separate JSON sidecar.

## 4. Review the actual export

- Decode the entire MP4 with FFmpeg. Check duration, resolution, frame rate,
  pixel format, audio presence, fades, levels and no clipping.
- Inspect every scene, transitions, code line wrapping, UI legibility and
  mobile-sized playback. Listen to the full track; successful decoding alone
  does not prove a good soundtrack. State if playback/listening is unavailable.
- Check the captions/transcript, contributor spellings, exact links, version
  labels, fixture disclaimers and released feature claims.
- Keep independent review and missing evidence explicit. Do not present an
  unreviewed draft as approved for publication.

## 5. Deliver a working file

Preparing a draft is not permission to commit, push, upload, publish or post.
Obtain explicit approval for each outbound action.

Prefer approved artifact storage with a downloadable URL. **Verify the URL
actually returns the MP4**, not just an upload success response. Download and
compare bytes/checksums. Do not present remote filesystem paths, internal host
IDs or fabricated SSH addresses as browser download links.

If explicitly asked to attach to a release, use:

```bash
gh release upload "$TAG" "$ARTIFACTS/highlights-electronic.mp4" --repo microsoft/waza
```

Do not use `--clobber` without replacement approval. A policy denial such as
"Copilot Cloud Agent is forbidden from modifying releases" is a hard boundary;
report it without retrying through another identity or transport.

If attachments return 404 or release writes are blocked, offer a **separate
user-approved** branch-only download: add only the selected final MP4 to the
current session branch, commit and push without opening a PR. Verify its
`raw.githubusercontent.com` URL returns matching bytes. Do not commit tools,
dependencies, scratch captures, fixture outputs or all alternate cuts. Never
promise a local-path link will work in the desktop app.

## Maintenance checks

```bash
python -m unittest discover -s "$SKILL_DIR/scripts" -p 'test_*.py'
```

For a renderer change, also render and fully decode a temporary storyboard with
actual input files; exercise the music command and confirm video stream hashes
match. Use the same installed tools as production. Report blocked checks honestly.
