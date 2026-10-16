const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const root = fs.realpathSync(path.resolve(__dirname, '../..'));
const read = (file) => fs.readFileSync(path.join(root, file), 'utf8').replace(/\r\n/g, '\n');
const prose = (text) => text.replace(/\s+/g, ' ');
const charterFiles = [
  '.squad/agents/rai/charter.md',
  '.squad/templates/Rai-charter.md',
  '.squad/agents/fact-checker/charter.md',
  '.squad/templates/fact-checker-charter.md',
];
const policyFiles = [
  '.squad/rai/policy.md',
  '.squad/templates/rai-policy.md',
  '.squad/templates/fact-checker-policy.md',
];
const callerFiles = [
  '.squad/governance.md',
  '.github/skills/coordinator-init-mode/SKILL.md',
  '.github/skills/reviewer-protocol/SKILL.md',
  '.github/skills/agent-collaboration/SKILL.md',
];

function assertClearance(text) {
  const source = prose(text);
  assert.match(source,
    /After successful focused rereview.*independently corrected revision.*may issue approval\/PASS/);
  assert.match(source, /not native repository approval, merge or publishing authority/);
  assert.doesNotMatch(source, /(?:or |not )approve the immediate revision/);
}

function assertPersistence(text) {
  const source = prose(text);
  assert.match(source, /Charter output\/persistence restrictions take precedence/);
  assert.match(source, /[Rr]eturn findings in the current review response/);
  assert.match(source,
    /[Oo]nly an authorized coordinator or Scribe handles required persistence under the current state contract/);
  assert.doesNotMatch(source,
    /findings .*are logged|Decisions that affect other agents go to|Justification logged to|Each agent's `history.md` starts/);
}

function assertCharter(text) {
  const source = prose(text);
  assertClearance(text);
  for (const pattern of [
    /no automatic/, /selected.*gate/,
    /does not implement, advise, or pair on the immediate revision/,
    /different qualified revision owner, neither the locked-out author nor the rejecting gate/,
    /original author remains locked out throughout that revision cycle/,
    /[Rr]eturn findings and review verdicts in the current review response/,
    /Do not create histories, audit trails, verification logs, proposals, or decision-inbox entries/,
    /Only an authorized coordinator or Scribe handles required persistence under the current state contract/,
  ]) assert.match(source, pattern);
}

function assertPolicy(text) {
  const source = prose(text);
  assertClearance(text);
  assertPersistence(text);
  for (const pattern of [
    /on request|on-demand/, /selected.*gate/,
    /different qualified revision owner/, /locked-out author/, /rejecting gate/,
    /does not implement, pair on, or advise the immediate revision/,
    /concrete counter-evidence/, /violated requirement/, /affected scope/,
  ]) assert.match(source, pattern);
  assert.doesNotMatch(source, /spawned automatically|runs in background by default|blocking by default/);
}

function assertRejectionPolicy(text) {
  const policy = prose(text.split('## Integration with Reviewer Rejection Protocol')[1] || '');
  for (const pattern of [
    /Squad only/, /ordinary Copilot work/,
    /concrete counter-evidence/, /violated requirement/, /affected scope/,
    /minimum condition for approval/, /different qualified revision owner/,
    /not the locked-out author or the rejecting gate/,
    /does not implement, pair on, or advise the immediate revision/,
    /Focused rereview/, /blockers, delta, and regressions/,
  ]) assert.match(policy, pattern);
  assert.doesNotMatch(policy, /usually the agent that produced|Pair mode|revise with grounding/i);
}

test('actual Init Mode seeds governance-aligned Fact Checker charter and review policies', (t) => {
  const init = read('.github/skills/coordinator-init-mode/SKILL.md');
  const seeds = [...init.matchAll(/`([^`]+)` is seeded from `([^`]+)`/g)]
    .map((match) => ({ destination: match[1], template: match[2] }));
  assert.deepEqual(seeds, [
    { destination: '.squad/agents/rai/charter.md', template: 'Rai-charter.md' },
    { destination: '.squad/rai/policy.md', template: 'rai-policy.md' },
    { destination: '.squad/agents/fact-checker/charter.md', template: 'fact-checker-charter.md' },
    { destination: '.squad/fact-checker/policy.md', template: 'fact-checker-policy.md' },
  ]);
  assert.match(prose(init), /Waza reviewer seeding override \(Squad only\)/);
  assert.match(init, /not pristine upstream replacements/);
  assert.match(init, /Ordinary Copilot work does/);
  const scratch = fs.mkdtempSync(path.join(root, '.squad-governance-test-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  for (const { destination, template } of seeds) {
    assert.ok(fs.readdirSync(path.join(root, '.squad/templates')).includes(template),
      `case-exact tracked template: ${template}`);
    const source = path.join(root, '.squad/templates', template);
    const target = path.join(scratch, destination);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.copyFileSync(source, target);
    assert.deepEqual(fs.readFileSync(target), fs.readFileSync(source));
  }
  const seededPolicy = fs.readFileSync(path.join(scratch, '.squad/fact-checker/policy.md'), 'utf8');
  assertRejectionPolicy(seededPolicy.replace(/\r\n/g, '\n'));
  const activeCharter = read('.squad/agents/fact-checker/charter.md');
  const seededCharter = fs.readFileSync(path.join(scratch, '.squad/agents/fact-checker/charter.md'), 'utf8');
  const activation = (text) => prose(text.split('## Activation')[1].split('## Output')[0]).trim();
  assert.equal(activation(seededCharter), activation(activeCharter));
  for (const member of ['rai', 'fact-checker']) {
    const seeded = fs.readFileSync(path.join(scratch, `.squad/agents/${member}/charter.md`), 'utf8');
    const active = read(`.squad/agents/${member}/charter.md`);
    assertCharter(seeded);
    assertCharter(active);
    const output = (text) => prose(text.split('## Output')[1].split('## Boundaries')[0]).trim();
    assert.equal(output(seeded), output(active));
    assert.equal(activation(seeded), activation(active));
    const policy = fs.readFileSync(path.join(scratch, `.squad/${member}/policy.md`), 'utf8');
    assertPolicy(policy);
  }
  assert.match(prose(read('.squad/governance.md')),
    /different qualified revision owner.*does not implement, pair on, or advise.*focused rereview/);
  assert.match(prose(fs.readFileSync(path.join(scratch, '.squad/rai/policy.md'), 'utf8')),
    /different qualified revision owner.*not the locked-out author or rejecting gate.*concrete counter-evidence.*does not implement, pair on, or advise.*focused rereview/);
});

test('reviewer consistency matrix covers active and seeded roles, policies, gates, outputs and callers', (t) => {
  for (const file of charterFiles) assertCharter(read(file));
  for (const file of policyFiles) assertPolicy(read(file));
  for (const file of [...policyFiles, ...callerFiles]) {
    const text = read(file);
    assertClearance(text);
    assertPersistence(text);
    assert.match(prose(text), /different qualified revision owner/, file);
    assert.match(prose(text), /ordinary Copilot work|ordinary Copilot workflow/i, file);
  }
  for (const file of ['.github/agents/squad.agent.md', '.squad/templates/squad.agent.md.template']) {
    const source = read(file);
    for (const [start, end] of [
      ['## Rai — RAI Reviewer', '## Fact Checker —'],
      ['## Fact Checker —', '## PRD Mode'],
    ]) {
      const role = source.split(start)[1].split(end)[0];
      assertClearance(role);
      assertPersistence(role);
      assert.match(prose(role), /on request or selection as a bounded gate, never automatic review/);
      assert.doesNotMatch(role, /spawned automatically|runs in background by default|auto-trigger|audit-trail\.md|history\.md|inbox\/fact-checker-/);
      assert.match(prose(role), /ordinary Copilot work/);
    }
    t.diagnostic(`${file}: both roles agree on bounded activation, gate clearance and output ownership`);
  }
  const protocol = prose(read('.github/skills/reviewer-protocol/SKILL.md'));
  assert.match(protocol, /locked-out author may NOT contribute.*not as a co-author, advisor, or pair/);
  assert.match(protocol, /Quality Engineer reviews v2 → approves/);
  t.diagnostic(`Consistency matrix: ${charterFiles.length} active/template charters, ${policyFiles.length} policies, ${callerFiles.length} callers and both coordinator role sections`);
});

test('reviewer contracts reject permanent gate lockout, self-revision, authoring and forbidden persistence', () => {
  for (const file of charterFiles) {
    const good = read(file);
    for (const [before, after] of [
      ['may issue approval/PASS', 'must never approve the immediate revision'],
      ['does not implement, advise, or pair on', 'implements, advises, and pairs on'],
      ['neither the locked-out author\nnor the rejecting gate', 'including the locked-out author or rejecting gate'],
      ['original author remains locked out', 'original author may self-revise'],
      ['Do not create histories,\naudit trails, verification logs, proposals, or decision-inbox entries.',
        'Create histories, audit trails and decision-inbox entries.'],
      ['not native repository approval, merge or publishing authority', 'native repository approval and merge authority'],
    ]) {
      assert.ok(good.includes(before), `${file}: negative control source`);
      assert.throws(() => assertCharter(good.replace(before, after)), assert.AssertionError);
    }
  }
  for (const file of policyFiles) {
    const good = read(file);
    assertPolicy(good);
    for (const [before, after] of [
      ['Charter output/persistence restrictions take precedence', 'Policy overrides charter output restrictions'],
      ['Only an authorized coordinator or Scribe handles required persistence', 'Reviewer must write audit trails and decision-inbox entries'],
    ]) {
      assert.ok(good.includes(before), file);
      assert.throws(() => assertPersistence(good.replace(before, after)), assert.AssertionError);
    }
    assert.throws(() => assertPersistence(`${good}\nAll findings are logged by Fact Checker.`),
      assert.AssertionError);
    assert.throws(() => assertPolicy(`${good}\nReviewer runs in background by default.`),
      assert.AssertionError);
  }
});

test('source contract rejects re-admitted authors, reviewer pairing and missing rejection evidence', () => {
  const good = read('.squad/templates/fact-checker-policy.md');
  assertRejectionPolicy(good);
  for (const [before, after] of [
    ['different qualified revision owner', 'original author'],
    ['does not implement, pair on, or advise', 'pairs on and implements'],
    ['concrete counter-evidence', 'unsupported assertions'],
  ]) {
    assert.ok(good.includes(before));
    assert.throws(() => assertRejectionPolicy(good.replace(before, after)), assert.AssertionError);
  }
});

test('active and delivered rejection callers preserve independent ownership and focused rereview', () => {
  for (const file of [
    '.github/agents/squad.agent.md',
    '.squad/templates/squad.agent.md.template',
    '.github/skills/reviewer-protocol/SKILL.md',
    '.github/skills/agent-collaboration/SKILL.md',
    '.squad/templates/rai-policy.md',
    '.squad/templates/workflow-wiring-guide.md',
    '.squad/templates/workflow-wiring-appendix-a-code-reviewer.md',
    '.squad/templates/issue-lifecycle.md',
  ]) {
    const text = prose(read(file));
    assert.match(text, /different qualified revision owner/, file);
    assert.match(text, /concrete counter-evidence/, file);
    assert.match(text, /focused rereview/i, file);
    assert.doesNotMatch(text,
      /original author addresses feedback|\{Author\} revises|Pair mode|pair-mode guidance|enters pair mode/i, file);
  }
});
