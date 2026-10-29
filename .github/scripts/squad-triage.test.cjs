const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const test = require('node:test');

const root = fs.realpathSync(path.resolve(__dirname, '../..'));
const workflow = fs.readFileSync(path.join(root, '.github/workflows/squad-triage.yml'), 'utf8')
  .replace(/\r\n/g, '\n');
const script = workflow.match(/          script: \|\n([\s\S]+)/)[1]
  .split('\n').map((line) => line.replace(/^ {12}/, '')).join('\n');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const runWorkflow = new AsyncFunction('require', 'github', 'context', 'core', script);

async function invoke(t, title, options = {}) {
  const scratch = fs.mkdtempSync(path.join(root, '.squad-triage-test-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  const directory = options.legacy ? '.ai-team' : '.squad';
  const copies = [
    '.github/scripts/squad-triage.cjs',
    '.squad/templates/ralph-triage.js',
  ];
  for (const file of copies) {
    const destination = path.join(scratch, file);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.copyFileSync(path.join(root, file), destination);
  }
  fs.mkdirSync(path.join(scratch, directory), { recursive: true });
  let team = fs.readFileSync(path.join(root, '.squad/team.md'), 'utf8');
  if (options.coordinatorLead) team = team.replace('| Rusty | Lead / Architect |', '| Rusty | Coordinator |');
  if (options.onlyCoordinator) team = team.replace(/Documentation Lead \/ Information Architecture/g, 'Documentation Specialist');
  if (options.autoAssign) team = team.replace('copilot-auto-assign: false', 'copilot-auto-assign: true');
  if (options.noCopilot) team = team.replace('🤖 Coding Agent', 'Disabled coding agent');
  if (options.noLead) team = team.replace(/lead|architect|coordinator/gi, 'specialist');
  if (options.genericQualifiedExclusions) team = team
    .replace('Ambiguous requirements needing clarification', 'Changes needing clarification')
    .replace('Multi-system integration requiring coordination', 'Changes requiring coordination');
  if (options.inlineCapabilities) {
    team = team.replace(/### Capabilities[\s\S]*?(?=\n## )/,
      '### Capabilities\n\n🟢 Good fit: mechanical task, simple task\n' +
      '🟡 Needs review: custom refactor\n🔴 Not suitable: dangerous task\n');
  }
  if (options.realInlineCapabilities) {
    team = team.replace(/\r\n/g, '\n').replace(
      /(\*\*[🟢🟡🔴][^\n]*\n)((?:- [^\n]*\n)+)/gu,
      (_, heading, bullets) => `${heading.trimEnd()} ${bullets.trim().split('\n')
        .map((line) => line.replace(/^- /, '')).join(', ')}\n`);
  }
  if (options.unbalancedExamples) team = team.replace('(auth, encryption, access control)', '(auth, encryption, access control');
  if (options.nestedExamples) team = team.replace('(auth, encryption, access control)', '(auth, (encryption), access control)');
  if (options.missingCapabilities) team = team.replace(/### Capabilities[\s\S]*?(?=\n## )/, '');
  if (options.emptyTier) team = team.replace(/\*\*🔴 Not suitable[^\n]+\n(?:- [^\n]+\n)*/, '**🔴 Not suitable:**\n');
  if (options.malformedCapabilities) team = team.replace('🟢 Good fit', 'Good');
  if (options.duplicateTier) team = team.replace('## Key Decisions', '**🟢 Good fit:** extra task\n\n## Key Decisions');
  if (options.punctuationTier) team = team.replace(/\*\*🔴 Not suitable[^\n]+\n(?:- [^\n]+\n)*/, '**🔴 Not suitable:** !!!\n');
  if (options.crlf) team = team.replace(/\r?\n/g, '\r\n');
  if (!options.missingTeam) fs.writeFileSync(path.join(scratch, directory, 'team.md'), team);
  if (!options.missingRouting) fs.copyFileSync(path.join(root, '.squad/routing.md'),
    path.join(scratch, directory, 'routing.md'));
  const context = {
    eventName: options.eventName ?? 'issues',
    repo: { owner: 'example', repo: 'fixture' },
    payload: {
      action: options.action ?? 'labeled',
      label: { name: options.label ?? 'squad' },
      issue: { number: 42, title, body: '', state: 'open', labels: ['squad'], assignees: [],
        ...options.issue },
    },
  };
  const calls = [];
  const warnings = [];
  const github = { rest: { issues: Object.fromEntries(
    ['addLabels', 'addAssignees', 'createComment'].map((method) =>
      [method, async (params) => {
        calls.push({ method, params });
        if (options.apiFailure === method) throw new Error('fixture API rejection');
      }]),
  ) } };
  const core = { warning: (message) => warnings.push(message), info: () => {} };
  const previous = process.cwd();
  try {
    process.chdir(scratch);
    await runWorkflow(createRequire(path.join(scratch, 'workflow-entry.cjs')), github, context, core);
  } finally {
    process.chdir(previous);
  }
  return { calls, warnings };
}

test('production triage workflow preserves its event and permission gates', () => {
  assert.match(workflow, /issues:\n\s+types: \[labeled\]/);
  assert.match(workflow, /if: github\.event\.label\.name == 'squad'/);
  assert.match(workflow, /issues: write\n\s+contents: read/);
  assert.match(workflow, /timeout-minutes: 5/);
  assert.match(script, /triageLabeledIssue/);
});

for (const [title, owner] of [
  ['Build Docker image', 'virgil'],
  ['Rebuild Docker image', 'virgil'],
  ['Fix frontend React navigation', 'yen'],
  ['web/src/components/RunDetail.tsx keyboard navigation', 'yen'],
  ['internal/faultfixture/adapters.go source admission', 'linus'],
  ['Fix Go allocation contention', 'turk'],
  ['Improve navigation', 'saul'],
  ['frontend allocation', 'rusty'],
  ['Unclassified request', 'rusty'],
  ['Prepare release media for v0.40.0', 'tess'],
  ['Capture dashboard screen recordings', 'tess'],
  ['Export short-form video', 'tess'],
  ['Draft demo narratives', 'danny'],
]) {
  test(`actual labeled handler routes "${title}" to ${owner}`, async (t) => {
    const { calls } = await invoke(t, title);
    assert.deepEqual(calls[0], { method: 'addLabels', params: {
      owner: 'example', repo: 'fixture', issue_number: 42, labels: [`squad:${owner}`],
    } });
    assert.deepEqual(calls[1].params.labels, ['go:needs-research']);
    assert.equal(calls.at(-1).method, 'createComment');
    assert.equal(calls.filter((call) => call.method === 'addAssignees').length, 0);
  });
}
for (const [name, options] of [
  ['other event', { eventName: 'pull_request' }],
  ['other action', { action: 'opened' }],
  ['other label', { label: 'bug' }],
  ['closed issue', { issue: { state: 'closed' } }],
  ['pull request', { issue: { pull_request: {} } }],
  ['existing object owner label', { issue: { labels: [{ name: 'squad:linus' }] } }],
  ['existing string owner label', { issue: { labels: ['squad:virgil'] } }],
  ['release-media owner awaiting readiness', { issue: {
    title: 'Prepare release media for v0.40.0',
    body: 'Capture deferred until verified release readiness.',
    labels: ['squad:tess'],
  } }],
]) {
  test(`actual handler does not mutate ${name}`, async (t) => {
    assert.deepEqual((await invoke(t, 'Build Docker image', options)).calls, []);
  });
}
for (const [name, options] of [
  ['missing roster', { missingTeam: true }],
  ['missing lead', { noLead: true }],
]) {
  test(`actual handler fails closed with ${name}`, async (t) => {
    const result = await invoke(t, 'Build Docker image', options);
    assert.deepEqual(result.calls, []);
    assert.equal(result.warnings.length, 1);
  });
}
test('legacy team path still routes via shared rules', async (t) => {
  const result = await invoke(t, 'Build Docker image', { legacy: true });
  assert.deepEqual(result.calls[0].params.labels, ['squad:virgil']);
});
test('missing routing falls back to lead rather than substring UI matching', async (t) => {
  const result = await invoke(t, 'Build Docker image', { missingRouting: true });
  assert.deepEqual(result.calls[0].params.labels, ['squad:rusty']);
});
test('coding-agent eligibility and explicit assignment opt-in are preserved', async (t) => {
  const disabled = await invoke(t, 'bug fix');
  assert.deepEqual(disabled.calls[0].params.labels, ['squad:copilot']);
  assert.ok(!disabled.calls.some((call) => call.method === 'addAssignees'));
  const enabled = await invoke(t, 'bug fix', { autoAssign: true });
  assert.deepEqual(enabled.calls.find((call) => call.method === 'addAssignees').params.assignees, ['copilot']);
  const existing = await invoke(t, 'bug fix', { autoAssign: true, issue: { assignees: [{ login: 'human' }] } });
  assert.ok(!existing.calls.some((call) => call.method === 'addAssignees'));
});
test('needs-review routing retains review warning and assignment failures are surfaced', async (t) => {
  const result = await invoke(t, 'medium feature', { autoAssign: true, apiFailure: 'addAssignees' });
  assert.match(result.calls.at(-1).params.body, /PR review recommended/);
  assert.match(result.warnings[0], /fixture API rejection/);
});
test('unsuitable capability cannot become a coding-agent assignment', async (t) => {
  const result = await invoke(t, 'security architecture bug fix', { autoAssign: true });
  assert.deepEqual(result.calls[0].params.labels, ['squad:rusty']);
  assert.ok(!result.calls.some((call) => call.method === 'addAssignees'));
});
test('absent coding agent uses shared domain routing', async (t) => {
  const result = await invoke(t, 'Build Docker image', { noCopilot: true });
  assert.deepEqual(result.calls[0].params.labels, ['squad:virgil']);
});
test('explicit inline capability tiers retain their declared keywords', async (t) => {
  const result = await invoke(t, 'mechanical task', { inlineCapabilities: true });
  assert.deepEqual(result.calls[0].params.labels, ['squad:copilot']);
});
test('label failures propagate instead of claiming successful triage', async (t) => {
  await assert.rejects(invoke(t, 'Build Docker image', { apiFailure: 'addLabels' }),
    /fixture API rejection/);
});

for (const onlyCoordinator of [false, true]) {
  for (const missingRouting of [false, true]) {
    test(`resolved Coordinator owns fallback with alternate leads=${!onlyCoordinator}, routing=${!missingRouting}`, async (t) => {
      const result = await invoke(t, 'Unclassified request', {
        coordinatorLead: true, onlyCoordinator, missingRouting,
      });
      assert.deepEqual(result.calls[0].params.labels, ['squad:rusty']);
      assert.match(result.calls.at(-1).params.body, /Assigned to:\*\* Rusty \(Coordinator\)/);
      assert.match(result.calls.at(-1).params.body, /resolved team lead/);
    });
  }
}
test('Coordinator fallback does not override a valid module or routing owner', async (t) => {
  for (const [title, label] of [['Build Docker image', 'virgil'],
    ['web/src/components/RunDetail.tsx keyboard navigation', 'yen']]) {
    const result = await invoke(t, title, { coordinatorLead: true, onlyCoordinator: true });
    assert.deepEqual(result.calls[0].params.labels, [`squad:${label}`]);
  }
});

for (const crlf of [false, true]) {
  for (const autoAssign of [false, true]) {
    for (const title of [
      'Bug fix: ambiguous requirements needing clarification',
      'Bug fix: multi-system integration requiring coordination',
      'Bug fix: auth',
      'Bug fix: encryption',
      'Bug fix: access control',
      'Bug fixes with clear reproduction steps: ambiguous requirements needing clarification',
      'Bug fixes with clear reproduction steps: multi-system integration requiring coordination',
      'Bug fixes with clear reproduction steps: security-critical changes (auth, encryption, access control)',
      'Bug fixes with clear reproduction steps: architecture decisions and system design',
      'Bug fixes with clear reproduction steps: performance-critical paths requiring benchmarking',
      'Bug fixes with clear reproduction steps: changes requiring cross-team discussion',
    ]) {
      test(`real roster unsuitable wins for "${title}" (CRLF=${crlf}, opt-in=${autoAssign})`, async (t) => {
        const result = await invoke(t, title, { crlf, autoAssign });
        assert.notDeepEqual(result.calls[0].params.labels, ['squad:copilot']);
        assert.ok(!result.calls.some((call) => call.method === 'addAssignees'));
        assert.match(result.calls.at(-1).params.body, /Not suitable - outside/);
        assert.deepEqual(result.warnings, []);
      });
    }
    for (const [title, review] of [
      ['Bug fixes with clear reproduction steps', false],
      ['Bug fix: repair report formatting', false],
      ['Bug fix: author display', false],
      ['Test coverage (adding missing tests, fixing flaky tests)', false],
      ['Lint/format fixes and code style cleanup', false],
      ['Dependency updates and version bumps', false],
      ['Small isolated features with clear specs', false],
      ['Boilerplate/scaffolding generation', false],
      ['Documentation fixes and README updates', false],
      ['Medium features with clear specs and acceptance criteria', true],
      ['Refactoring with existing test coverage', true],
      ['CLI command additions following established patterns', true],
      ['Internal package additions following established patterns', true],
    ]) {
      test(`real roster routes declared "${title}" (CRLF=${crlf}, opt-in=${autoAssign})`, async (t) => {
        const result = await invoke(t, title, { crlf, autoAssign });
        assert.deepEqual(result.calls[0].params.labels, ['squad:copilot']);
        assert.equal(result.calls.some((call) => call.method === 'addAssignees'), autoAssign);
        assert.equal(/PR review recommended/.test(result.calls.at(-1).params.body), review);
        assert.deepEqual(result.warnings, []);
      });
    }
  }
}
for (const [name, option] of [
  ['missing', 'missingCapabilities'], ['empty tier', 'emptyTier'],
  ['malformed tier', 'malformedCapabilities'], ['duplicate tier', 'duplicateTier'],
  ['punctuation-only tier', 'punctuationTier'],
]) {
  for (const crlf of [false, true]) {
    test(`${name} capability policy fails closed (CRLF=${crlf})`, async (t) => {
      for (const autoAssign of [false, true]) {
        const result = await invoke(t, 'bug fix', { [option]: true, autoAssign, crlf });
        assert.notDeepEqual(result.calls[0].params.labels, ['squad:copilot']);
        assert.ok(!result.calls.some((call) => call.method === 'addAssignees'));
        assert.match(result.warnings.join('\n'), /capability profile.*disabled/i);
        assert.match(result.calls.at(-1).params.body, /capability profile.*disabled/i);
      }
    });
  }
}
for (const [title, copilot, review] of [
  ['mechanical task', true, false], ['custom refactor', true, true],
  ['dangerous task mechanical task', false, false], ['bug fix', false, false],
  ['mechanical task custom refactor', true, true],
]) {
  test(`inline profile remains authoritative for "${title}"`, async (t) => {
    const result = await invoke(t, title, { inlineCapabilities: true, autoAssign: true });
    assert.equal(result.calls[0].params.labels[0] === 'squad:copilot', copilot);
    assert.equal(result.calls.some((call) => call.method === 'addAssignees'), copilot);
    assert.equal(/PR review recommended/.test(result.calls.at(-1).params.body), review);
    assert.deepEqual(result.warnings, []);
  });
}
test('real roster exclusions in the issue body override an otherwise good title', async (t) => {
  for (const body of ['Ambiguous requirements needing clarification',
    'Multi-system integration requiring coordination', 'Ambiguous requirements',
    'Multi-system integration', 'auth', 'encryption', 'access control']) {
    const result = await invoke(t, 'bug fix', { autoAssign: true, issue: { body } });
    assert.notDeepEqual(result.calls[0].params.labels, ['squad:copilot']);
    assert.ok(!result.calls.some((call) => call.method === 'addAssignees'));
    assert.match(result.calls.at(-1).params.body, /Not suitable - outside/);
  }
});

const deliveredRoster = fs.readFileSync(path.join(root, '.squad/team.md'), 'utf8').replace(/\r\n/g, '\n');
const redSection = deliveredRoster.split('**🔴 Not suitable')[1].split('\n## ')[0];
const declaredExclusions = [...redSection.matchAll(/^- (.+)$/gm)].map((match) => match[1]);
const declaredExamples = [...redSection.matchAll(/\(([^()]*)\)/g)]
  .flatMap((match) => match[1].split(',').map((value) => value.trim()));

test('real roster and routing agree on explicit security exclusion aliases', () => {
  assert.deepEqual(declaredExamples, ['auth', 'encryption', 'access control']);
  assert.equal(declaredExclusions.length, 6);
  const routing = fs.readFileSync(path.join(root, '.squad/routing.md'), 'utf8');
  assert.match(routing, /Auth, encryption, access control → always 🔴/);
});

for (const crlf of [false, true]) {
  for (const autoAssign of [false, true]) {
    for (const realInlineCapabilities of [false, true]) {
      const format = `CRLF=${crlf}, opt-in=${autoAssign}, inline=${realInlineCapabilities}`;
      const options = { crlf, autoAssign, realInlineCapabilities };
      test(`every real-roster exclusion and security alias defeats good-fit (${format})`, async (t) => {
        for (const exclusion of [...declaredExclusions, ...declaredExamples,
          'security', 'security-critical', 'Security-critical changes',
          'architecture decisions', 'system design', 'performance', 'performance-critical',
          'ambiguous requirements', 'multi-system integration',
          declaredExamples.join(', ')]) {
          const result = await invoke(t, `Bug fix: ${exclusion}`, options);
          assert.notDeepEqual(result.calls[0].params.labels, ['squad:copilot'], exclusion);
          assert.ok(!result.calls.some((call) => call.method === 'addAssignees'), exclusion);
          assert.match(result.calls.at(-1).params.body, /Not suitable - outside/, exclusion);
          assert.deepEqual(result.warnings, [], exclusion);
        }
      });
      test(`real-roster good-fit, generic subjects and whole-word near-misses stay eligible (${format})`, async (t) => {
        for (const title of ['Bug fix: pagination', 'Bug fix: author column width',
          'Bug fix: microencryption label typo', 'Bug fix: access controller caption',
          'Bug fix: changes to pagination']) {
          for (const genericQualifiedExclusions of [false, true]) {
            const result = await invoke(t, title, { ...options, genericQualifiedExclusions });
            assert.deepEqual(result.calls[0].params.labels, ['squad:copilot'], title);
            assert.equal(result.calls.some((call) => call.method === 'addAssignees'), autoAssign, title);
            assert.match(result.calls.at(-1).params.body, /Good fit for @copilot/, title);
            assert.deepEqual(result.warnings, [], title);
          }
        }
      });
      test(`positive parenthetical prose is not a standalone capability (${format})`, async (t) => {
        for (const title of ['adding missing tests', 'fixing flaky tests', 'clear reproduction steps',
          'acceptance criteria', 'established patterns', 'clear specs']) {
          const result = await invoke(t, title, options);
          assert.notDeepEqual(result.calls[0].params.labels, ['squad:copilot'], title);
          assert.ok(!result.calls.some((call) => call.method === 'addAssignees'), title);
          assert.deepEqual(result.warnings, [], title);
        }
      });
    }
  }
}
for (const option of ['unbalancedExamples', 'nestedExamples']) {
  test(`unsupported ${option} fail closed rather than dropping exclusions`, async (t) => {
    const result = await invoke(t, 'Bug fix: auth', { [option]: true, autoAssign: true });
    assert.notDeepEqual(result.calls[0].params.labels, ['squad:copilot']);
    assert.ok(!result.calls.some((call) => call.method === 'addAssignees'));
    assert.match(result.warnings.join('\n'), /capability profile.*disabled/i);
  });
}
