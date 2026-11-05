# After Agent Reference

### After Agent Work

<!-- KNOWN PLATFORM BUGS: (1) "Silent Success" — ~7-10% of background spawns complete
     file writes but return no text. Mitigated by RESPONSE ORDER + filesystem checks.
     (2) "Server Error Retry Loop" — context overflow after fan-out. Mitigated by lean
     post-work turn + Scribe delegation + compact result presentation. -->

**⚡ Keep the post-work turn LEAN.** Present compact results and delegate accepted
decision merges only when needed. Do not spawn routine logging work.

**⚡ Context budget rule:** After collecting results from 3+ agents, use compact format
(agent + 1-line outcome). Retain details in agent responses and requested artifacts,
not automatic Scribe logs.

After each batch of agent work:

1. **Collect results** via `read_agent` (wait: true, timeout: 300).

2. **Silent success detection** — when `read_agent` returns empty/no response:
   - Check filesystem: history.md modified? New decision inbox files? Output files created?
   - Files found → `"⚠️ {Name} completed (files verified) but response lost."` Treat as DONE.
   - No files → `"❌ {Name} failed — no work product."` Consider re-spawn.

3. **Show compact results:** `{emoji} {Name} — {1-line summary of what they did}`

4. **Spawn Scribe** (background, never wait) only for an explicit Coordinator delegation
   of already-accepted durable decisions, not merely because agents ran or inbox files exist.
   Resolve the role model through `.squad/model-policy.md`; pass an override only if the
   host supports it, otherwise disclose the host-controlled limitation.

```
agent_type: "general-purpose"
mode: "background"
name: "scribe"
description: "📋 Scribe: Merge accepted decisions"
prompt: |
  You are the Scribe. Read .squad/agents/scribe/charter.md.
  TEAM ROOT: {team_root}
  CURRENT_DATETIME: <resolved CURRENT_DATETIME literal>
  STATE_BACKEND: {state_backend}
  ACCEPTED_DECISIONS: {inbox IDs already accepted by the Coordinator}

  SPAWN MANIFEST: {spawn_manifest}

  Tasks (in order):
  0. PRE-CHECK: Run `squad_state_health` when available. If state tools are unavailable,
     stop without mutating files or git state.
  0b. PRE-CHECK: Read `decisions.md` and list `decisions/inbox` with state tools.
     Record measurements.
  0c. Coordinator alone accepts decisions. Without explicit delegation and accepted
     inbox IDs, stop without writes; leave proposals untouched.
  1. DECISION INBOX: Use `squad_state_list` and `squad_state_read` on `decisions/inbox`.
     Merge only ACCEPTED_DECISIONS into `decisions.md` with `squad_state_write` and
     deduplicate without rewriting unrelated decisions. Before splicing an inbox body
     beneath an `###` entry, DEMOTE its headings so its shallowest heading lands at
     `####` (`##` -> `####`). Preserve relative structure. Never emit an `##` under an `###`.
  2. VERIFY: Re-read `decisions.md` and confirm every merged entry is present before
     deleting only its accepted inbox source with `squad_state_delete`.
  3. Return the merge result to the Coordinator: accepted IDs merged, unprocessed
     inbox entries and any measured blocker. Do not create logs or update specialist
     histories, audit trails or durable health reports. Leave archival to the Coordinator.

  Runtime state tools own persistence. Never switch branches, push note refs, reset
  `.squad/`, or commit mutable squad state from this prompt.

  Never speak to user. End with plain text summary after all tool calls.
```

### Coordinator-only archival reference

Archival is separate from the decision-only Scribe merge. The Coordinator arranges an
explicitly authorized writer when required by the current state contract; absent one,
stop and report rather than expanding Scribe's charter. Preserve existing historical records.
Measure before reporting any threshold outcome: decision-ledger gates remain 20480 bytes
(entries older than 30 days) and 51200 bytes (older than 7 days), and the history gate
remains 15360 bytes. These thresholds never authorize an unverified or out-of-scope move.

ARCHIVAL SAFETY RULES (apply to every authorized operation that moves content out of a file):
  A. VERIFY THE DESTINATION FOR THE CONFIGURED BACKEND. Confirm the backend with the state tools.
     For checkout-local/worktree storage only, run `git ls-files --error-unmatch <destination>`
     before writing. Exit 0 -> proceed. Non-zero -> redirect to an existing tracked archive file,
     or ABORT with a clear error. `.squad/` is git-excluded in many checkouts: already-tracked files
     still commit, but NEW files silently never do. Never assume a new local archive will commit.
     For non-local storage, checkout tracking is not a persistence requirement: do not run this
     Git gate. Use the configured state tools, require a successful persistence acknowledgement,
     and re-read the destination through that backend before removing any source entries.
     If the backend or persistence cannot be verified, STOP and report; never fall back to local
     files or delete source entries on an unverified move.
  B. APPEND FIRST, VERIFY, THEN DELETE. Append to the destination. Re-read the destination and
     confirm every moved heading is literally present AND the entry count grew by exactly the
     number moved. Only then remove from the source. If the append cannot be verified, DO NOT
     trim — leave the source intact and report the failure. Losing history is far worse than
     leaving a file over its size gate.
  C. COUNT ENTRIES, NOT BYTES. File size is not a valid integrity signal: a merge and an archive
     in the same pass move size in opposite directions, so a size delta proves nothing.
  D. NEVER REPORT A GATE OUTCOME YOU DID NOT MEASURE. "No archival required" must come from an
     actual measurement. A gate that reports without measuring is worse than no gate.
  E. If a state tool cannot perform these checks, STOP and report rather than proceeding with an
     unverified move.

5. **Immediately assess:** Does anything trigger follow-up work? Launch it NOW.

6. **Ralph check:** If Ralph is active (see Ralph — Work Monitor), after chaining any follow-up work, IMMEDIATELY run Ralph's work-check cycle (Step 1). Do NOT stop. Do NOT wait for user input. Ralph keeps the pipeline moving until the board is clear.
