# Waza casting transactions

This is a narrow **project-owned repair** to Squad 1.0.0 managed procedures.
Upstream add/remove/init/wiring guidance can describe registry-only writes, which
invalidate the manifest-backed pair installed here. These repaired files are not
pristine upstream bytes. Preserve this override and its tests when upgrading.

## One writer for the whole pair

After explicit approval for a team change, use
`.github/scripts/squad-casting.mjs`'s `updateCastingPair` for **every** local/worktree
casting mutation: initialization, migration, add, retire/status change, role/name/
universe changes, overflow, and re-casting. Never write `registry.json`,
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
   any durable transaction and rejecting corrupt/incomplete input.
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

For old-generation migration, first preserve the original bytes in a durable
archive, then construct schema-v1 records through this transaction callback; never
overwrite one half of an existing pair to make it look migratable. The existing
`legacy-v0.10.0/` archive is historical and must remain byte-identical.

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
Both platforms execute these checks in normal and genuine linked worktrees.
