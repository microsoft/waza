# Waza casting transactions

This is a narrow **project-owned repair** to Squad 1.0.0 managed procedures.
Upstream add/remove/init/wiring guidance can describe registry-only writes, which
invalidate the manifest-backed pair installed here. These repaired files are not
pristine upstream bytes. Preserve this override and its tests when upgrading.

## One writer for the whole pair

After explicit approval for a team change, use
`.github/scripts/squad-casting.mjs`'s `updateCastingPair` for **every** local/worktree
canonical casting mutation: initialization into an empty target, add,
retire/status change, role/name/universe changes, overflow, and re-casting.
Old v0.10 state requires the separate archive-and-initialize maintenance procedure
below, not an in-place normalization callback. Never write `registry.json`,
`history.json`, or `registry-history.commit.json` independently with file/state
tools. Do not remove the manifest, weaken hash checks, normalize bytes on read, or
regenerate checksums to conceal a unilateral edit.

The helper imports the verified `@bradygaster/squad-sdk@1.0.0` package's actual
`dist/casting/durable-registry.js` API. Set `SQUAD_SDK_READER` to its absolute path;
the package must be acquired and integrity-verified as described in
`meet-the-squad.md` / Squad Tools CI. Package identity/version checks in the helper
are not a substitute for that tarball-integrity check. No dependency installation,
Copilot client, model, or provider is involved in this filesystem-only writer.

The helper:

1. Calls `acquireCastingRegistryLock(castingDir, purpose)`.
2. Calls `prepareCastingRegistryPairLocked(castingDir)` while locked, recovering
   supported durable transactions and rejecting invalid legacy or corrupt/
   incomplete input **before the mutation callback runs**.
3. Clones both roots (or initializes schema-v1 roots if both are absent), advances
   the registry revision and history revision together, and runs a **synchronous**
   in-memory mutation callback. Preserve existing identities and append-only
   snapshots/usage. Retire identities; do not delete/reuse reserved names.
4. Validates actual SDK provenance, then calls
   `commitCastingRegistryPair(castingDir, previousRegistryRaw, registry,
   previousHistoryRaw, history, targetRevision)`. The SDK writes the journal,
   both payloads, transaction IDs, hashes, and commit manifest.
5. Re-reads with `readCastingRegistryPair` and releases the lock in `finally`.

The callback changes objects, **not files**. An invalid callback or provenance/
history shape is rejected before commit. Do not announce success or update the
roster/routing/capabilities if the pair cannot be accepted. If a commit is in doubt,
stop and report; do not roll back files by hand. The SDK owns roll-forward recovery.

## Reviewed mutation example

This is a template for an approved new identity, not an instruction to add this
sample member to the live team. Initialization uses the same callback/transaction
path, not hand-written legacy JSON. Adapt identity, role, universe and snapshot key
to the approved assignment. All required provenance fields must remain present.

```js
import { updateCastingPair } from './.github/scripts/squad-casting.mjs';

await updateCastingPair('.squad/casting', (registry, history) => {
  const timestamp = registry.generated_at;
  registry.agents.reuben = {
    display_name: 'Reuben',
    persistent_name: 'Reuben',
    role: 'Infrastructure Support',
    universe: "Ocean's Eleven",
    created_at: timestamp,
    updated_at: timestamp,
    legacy_named: false,
    status: 'active',
  };
  history.assignment_cast_snapshots[`member-add-r${registry.revision}`] = {
    universe: "Ocean's Eleven",
    created_at: timestamp,
    agents: ['reuben'],
  };
  history.universe_usage_history.push({
    universe: "Ocean's Eleven",
    used_at: timestamp,
  });
});
```

For retirement, the callback changes the existing record's `status` to `"retired"`
and sets both `retired_at` and `updated_at` to `registry.generated_at`; it leaves
the record and all historical snapshots intact. Even a status-only change must
use `updateCastingPair`, which commits both roots and the new manifest together.
Role/name/universe changes likewise update the existing record in a paired
transaction; maintain `persistent_name === display_name` and append an assignment
snapshot when the cast changes.

## Archive and initialize the known v0.10 state

`prepareCastingRegistryPairLocked` rejects the frozen v0.10 registry because it
lacks a positive revision and schema-v1 provenance. The callback cannot normalize
those files in place. Do **not** treat any validation failure as a legacy detector:
managed pairs, missing manifests, transaction journals, unexpected files, unknown
legacy versions or different bytes require investigation, not this procedure.

The supported procedure below is deliberately limited to the exact three frozen
`legacy-v0.10.0/` files. It is a maintenance recipe, **not a general legacy importer
or a command to run on this repository's already-managed live casting state**.

1. Obtain explicit approval and quiesce **all** old writers/readers and background
   workers. Ensure exclusive operator control of the source and archive parent.
   The SDK lock does not coordinate v0.10 writers. If they cannot be stopped,
   stop the migration.
2. Verify the complete old directory has exactly `registry.json`, `history.json`
   and `policy.json`, with the frozen byte fingerprints below. Reject symlinks.
   Never remove a manifest/journal or alter bytes to make this check pass.
3. Create a new exclusive archive container and move the **whole old directory**
   into it. Read back every original byte before proceeding. Keep that archive
   durable and version-controlled/backed up through the approved storage process;
   a successful local rename alone is not proof of remote archival.
4. Create a genuinely empty new active directory at the former path. Initialize
   canonical objects using `updateCastingPair` and the verified pinned SDK, then
   copy the unchanged policy exclusively. Only resume compatible readers/writers
   after accepted pair/provenance read-back and verified archive persistence.

**Downtime and continuity limits:** archive move, fresh-directory creation, SDK
commit and policy copy are separate operations, **not one atomic migration**.
The active path is absent/empty during the maintenance window. If any step fails,
keep clients quiesced, preserve the archive and report the partial state; do not
overwrite a newly managed pair or hand-restore individual files. SDK recovery
applies only to the new managed target, not the archive/move sequence.

The archive is the complete historical record. The live schema below is a
reviewed projection of these known inputs: it keeps IDs, names, universe labels
and asserted timestamps, supplies explicitly approved role metadata, marks names
as imported, and maps supported history fields. Legacy notes/capacity fields
remain only in the byte-preserved archive. This starts revision 1 with a new
manifest; it does **not** establish transactional, cryptographic or verified
historical provenance continuity with v0.10. Roles and old timestamps are not
independently verified by schema acceptance.

The following example is executed only on isolated copies by the regression
suite. The role map is an example requiring operator approval, not a recovered
historical fact. The source's known byte fingerprints make this recipe reject
even a managed pair whose manifest was removed.

```js
import fs from 'node:fs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { updateCastingPair } from './.github/scripts/squad-casting.mjs';

const active = '.squad/casting';
const archive = '.squad/casting-v0.10.0-approved-archive';
const fingerprints = {
  'registry.json': '64d9c852d087ae98acb72bbdf1652c36f247457a6c313805f91725bbfb11bf26',
  'history.json': '01f651483280eca3710175f928da165dc9e896e53b435f6f259e47ec58df0206',
  'policy.json': 'b1b95dbf0cb9e2473a59fef43c379ab4046d496ba23cecaea974051ad0fbc2d7',
};
assert.ok(fs.lstatSync(active).isDirectory(), 'Legacy source must be a real directory');
assert.deepEqual(fs.readdirSync(active).sort(), Object.keys(fingerprints).sort(),
  'Not the supported frozen v0.10 directory; do not classify managed/corrupt state as legacy');
const original = {};
for (const [file, fingerprint] of Object.entries(fingerprints)) {
  assert.ok(fs.lstatSync(`${active}/${file}`).isFile(), 'Legacy input must be a real file');
  original[file] = fs.readFileSync(`${active}/${file}`);
  assert.equal(createHash('sha256').update(original[file]).digest('hex'), fingerprint,
    'Not the supported frozen v0.10 bytes; stop without modifying state');
}
fs.mkdirSync(archive); // Exclusive: never overwrite an existing archive.
fs.renameSync(active, `${archive}/casting`);
for (const file of Object.keys(fingerprints)) {
  assert.deepEqual(fs.readFileSync(`${archive}/casting/${file}`), original[file]);
}
fs.mkdirSync(active); // New empty active target; the old pair is not left in place.
assert.deepEqual(fs.readdirSync(active), []);
const legacyRegistry = JSON.parse(original['registry.json']);
const legacyHistory = JSON.parse(original['history.json']);
const approvedRoles = {
  rusty: 'Lead / Architect', linus: 'Go Backend', basher: 'Test Quality',
  livingston: 'Technical Documentation', saul: 'Documentation Lead',
  scribe: 'Session Logger', turk: 'Performance Analysis',
};
await updateCastingPair(active, (registry, history) => {
  for (const [id, record] of Object.entries(legacyRegistry.agents)) {
    registry.agents[id] = {
      display_name: record.persistent_name,
      persistent_name: record.persistent_name,
      role: approvedRoles[id],
      universe: record.universe,
      created_at: record.created_at,
      updated_at: registry.generated_at,
      legacy_named: true,
      status: record.status,
    };
  }
  history.universe_usage_history = legacyHistory.universe_usage_history.map((entry) => ({
    universe: entry.universe, used_at: entry.timestamp,
  }));
  history.assignment_cast_snapshots = Object.fromEntries(
    Object.entries(legacyHistory.assignment_cast_snapshots).map(([id, entry]) => [
      id, { universe: entry.universe, created_at: entry.timestamp, agents: entry.cast },
    ]));
});
fs.copyFileSync(`${archive}/casting/policy.json`, `${active}/policy.json`,
  fs.constants.COPYFILE_EXCL);
```

The helper validates provenance and reads the committed pair before returning.
Archive/policy-copy failures are not silently rolled back. Keep all clients
stopped until the operator verifies both the complete archive and new target.
The repository's existing `legacy-v0.10.0/` files are immutable reference inputs;
`.gitattributes` disables checkout conversion for them, including on Windows.

For **non-local backends**, do not invoke this local filesystem helper or write
individual casting keys through generic state tools. Require a backend-supported
equivalent atomic paired transaction and verified read-back. If none is exposed,
stop and report the limitation; never silently fall back to local state.

## Member-change follow-through

Only after verified pair commit, update the approved charter/roster/routing/model
assignments and re-render Team Capabilities with the SDK as documented in
`meet-the-squad.md`. Do not run destructive `squad upgrade` merely to add/remove a
member: it overwrites customized workflows. A deliberate upgrade must preserve
the model/canary/ownership hooks, this writer override, the shared production
triage handler, and all other customized workflows.

`squad-casting.test.cjs` executes add, role change, retirement and initialization
on isolated copies with the actual pinned writer/reader and provenance parser.
It also proves unilateral raw edits are rejected, corrupt input remains rejected,
and failed mutations leave the old bytes intact and release the writer lock.
It executes the exact frozen archive-and-initialize recipe, checks archive and
policy bytes, proves preparation precedes callbacks, and rejects both corrupt
managed generations and existing archive collisions without moving source state.
Both platforms execute these checks in normal and genuine linked worktrees.
