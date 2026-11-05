# RAI Policy

> Responsible AI policy for this project. Rai enforces these standards.

**Waza project override (Squad only):** Follow the active charter and `.squad/governance.md`.
Review is on request or selected as a bounded gate, not an automatic extra reviewer.
Ordinary Copilot work does not acquire these coordination rules.
Charter output/persistence restrictions take precedence over this policy and generic
history, audit-trail or decision-inbox instructions. Return findings in the current review response.
Only an authorized coordinator or Scribe handles required persistence under the current state
contract; no routine review logging is required.

## Principles

1. **Safety first** — No output should cause harm to individuals or groups.
2. **Transparency** — Users should know when they're interacting with AI-generated content.
3. **Fairness** — Systems should not discriminate based on protected characteristics.
4. **Privacy** — Personal data must be handled with minimal exposure and explicit consent.
5. **Accountability** — Every decision has an owner; every finding has a remediation path.

## Critical Violations (🔴 — Always Blocked)

These CANNOT be shipped. No opt-out. No exceptions.

### Credentials & Secrets
- Hardcoded API keys, tokens, passwords, connection strings
- Private keys committed to source control
- Secrets in environment variable defaults or config templates

### Injection Vulnerabilities
- SQL injection (unsanitized user input in queries)
- Command injection (user input in shell commands)
- Path traversal (user input in file paths without validation)

### Harmful Content
- Hate speech, slurs, or derogatory language targeting groups
- Content promoting violence or self-harm
- Sexually explicit content without appropriate context/gating

### Deceptive Patterns
- Ungrounded factual claims presented as authoritative
- Hallucinated citations, references, or statistics
- Instructions that bypass AI safety guidelines or content filters

## Advisory Concerns (🟡 — Flagged, Not Blocked)

These are recommendations. Work proceeds with suggestions attached.

### Privacy & Data
- PII (names, emails, phone numbers) in logs or responses
- Overly broad data collection without stated purpose
- Missing data retention or deletion policies

### Bias & Fairness
- Algorithms using demographic features (age, gender, race) without justification
- Proxy attributes that correlate with protected characteristics
- Training data with known representation gaps

### Inclusive Language
- Gendered terms where neutral alternatives exist (e.g., "guys" → "everyone")
- Ableist language (e.g., "blind spot" → "oversight", "sanity check" → "validation")
- Culturally assumptive terms (e.g., assuming Western holidays, naming conventions)

### Security Posture
- Missing rate limiting on user-facing endpoints
- Overly permissive CORS or authentication policies
- Insufficient input validation on public interfaces

### Accessibility
- Missing alt text on images
- Insufficient color contrast
- Missing ARIA labels on interactive elements

## Terminology Standards

| Avoid | Prefer | Reason |
|-------|--------|--------|
| whitelist/blacklist | allowlist/blocklist | Racial connotation |
| master/slave | primary/replica | Racial connotation |
| sanity check | validation, smoke test | Ableist |
| dummy value | placeholder, sample | Potentially offensive |
| guys | everyone, team, folks | Gendered |
| man-hours | person-hours, effort | Gendered |

## Review Scope by Change Type

| Change Type | Review Level | Rationale |
|-------------|-------------|-----------|
| Source code (new features) | Full check suite | Highest risk surface |
| Source code (bug fixes) | Credential + injection checks | Targeted risk |
| Documentation | Content + terminology only | Lower risk |
| Test files | Credential checks only | Minimal risk |
| Dependency updates | Skip (fast-path) | No authored content |
| Configuration | Credential checks only | Secret exposure risk |

## Escalation Path

1. **🟢 Green** — No action needed. Work proceeds.
2. **🟡 Yellow** — Suggestions attached to work output. Author decides.
3. **🔴 Red** — Work blocked when Rai is the selected gate. Reviewer Rejection Protocol activates:
   - Original author locked out of revision
   - The Coordinator selects a different qualified revision owner, not the locked-out author or rejecting gate
   - Rai includes concrete counter-evidence, the violated requirement, affected scope and the minimum condition for approval in the rejection
   - Rai does not implement, pair on, or advise the immediate revision; only focused rereview of blockers, delta, and regressions follows
   - After successful focused rereview of the independently corrected revision, Rai may issue approval/PASS for the selected gate
   - This is a review verdict, not native repository approval, merge or publishing authority

## Policy Updates

This policy evolves through authorized project decisions, with no retroactive enforcement.
Rai returns findings in the review response and does not write audit trails or decision-inbox
entries. Only an authorized coordinator or Scribe records a required accepted decision or
necessary handoff through the approved state mechanism; no per-review logging.
