const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const root = fs.realpathSync(path.resolve(__dirname, '../..'));
const read = (file) => fs.readFileSync(path.join(root, file), 'utf8').replace(/\r\n/g, '\n');
const prose = (text) => text.replace(/\s+/g, ' ');

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
  assert.match(prose(read('.squad/governance.md')),
    /different qualified revision owner.*does not implement, pair on, or advise.*focused rereview/);
  assert.match(prose(fs.readFileSync(path.join(scratch, '.squad/rai/policy.md'), 'utf8')),
    /different qualified revision owner.*not the locked-out author or rejecting gate.*concrete counter-evidence.*does not implement, pair on, or advise.*focused rereview/);
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
