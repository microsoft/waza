const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const {
  parseModuleOwnership,
  parseRoster,
  parseRoutingRules,
  resolveGithubApiBase,
  triageIssue,
} = require('../../.squad/templates/ralph-triage.js');

const root = path.resolve(__dirname, '../..');
const routing = fs.readFileSync(path.join(root, '.squad/routing.md'), 'utf8');
const roster = parseRoster(fs.readFileSync(path.join(root, '.squad/team.md'), 'utf8'));
const rules = parseRoutingRules(routing);
const modules = parseModuleOwnership(routing);

for (const [title, owner, source] of [
  ['web/src/components/RunDetail.tsx keyboard navigation', 'Yen', 'module-ownership'],
  ['web/e2e/evidence.spec.ts regression cases', 'Basher', 'module-ownership'],
  ['.github/workflows/go-ci.yml build changes', 'Virgil', 'module-ownership'],
  ['site/src/content/docs/reference/cli.mdx guide update', 'Livingston', 'module-ownership'],
  ['DEMO-SCRIPT.md announcement narrative', 'Danny', 'module-ownership'],
  ['docs/RELEASE-MEDIA.md capture checklist', 'Tess', 'module-ownership'],
  ['docs/templates/release-media-task.md release planning', 'Tess', 'module-ownership'],
  ['Prepare release media for v0.40.0', 'Tess', 'routing-rule'],
  ['Capture dashboard screen recordings', 'Tess', 'routing-rule'],
  ['Export short-form video', 'Tess', 'routing-rule'],
  ['Draft demo narratives', 'Danny', 'routing-rule'],
  ['screenshotTest helper', 'Rusty', 'lead-fallback'],
  ['internal/faultfixture/adapters.go source admission', 'Linus', 'module-ownership'],
  ['.squad/governance.md ownership', 'Rusty', 'module-ownership'],
  ['Improve reference accuracy and terminology', 'Saul', 'routing-rule'],
  ['Investigate allocation and contention', 'Turk', 'routing-rule'],
  ['Fix allocation contention', 'Turk', 'routing-rule'],
  ['Fix Go allocation contention', 'Turk', 'routing-rule'],
  ['Improve navigation', 'Saul', 'routing-rule'],
  ['Improve documentation navigation', 'Saul', 'routing-rule'],
  ['Fix frontend React navigation', 'Yen', 'routing-rule'],
  ['frontend allocation', 'Rusty', 'lead-fallback'],
  ['documentation', 'Rusty', 'lead-fallback'],
  ['Go', 'Rusty', 'lead-fallback'],
  ['Unclassified request', 'Rusty', 'lead-fallback'],
]) {
  test(`routes "${title}" to ${owner}`, () => {
    const result = triageIssue({ title }, rules, modules, roster);
    assert.equal(result.agent.name, owner);
    assert.equal(result.source, source);
  });
}

test('duplicate generated and normalized phrases do not break a genuine tie', () => {
  const duplicateRules = [
    {
      workType: 'Web design / frontend',
      agentName: 'Yen',
      keywords: ['FRONTEND', 'frontend', 'frontend.'],
    },
    { workType: 'Performance', agentName: 'Turk', keywords: ['allocation'] },
  ];
  const result = triageIssue({ title: 'frontend allocation' }, duplicateRules, [], roster);
  assert.equal(result.agent.name, 'Rusty');
  assert.equal(result.source, 'lead-fallback');
});

test('a specific phrase outranks several less-specific matches', () => {
  const specificRules = [
    { workType: 'Frontend', agentName: 'Yen', keywords: ['user interface'] },
    {
      workType: 'Documentation',
      agentName: 'Saul',
      keywords: ['navigation', 'terminology', 'reference'],
    },
  ];
  const result = triageIssue(
    { title: 'user interface navigation terminology reference' },
    specificRules,
    [],
    roster,
  );
  assert.equal(result.agent.name, 'Yen');
  assert.equal(result.source, 'routing-rule');
});

test('equally ranked rules for one specialist do not create an ambiguous owner', () => {
  const sameOwnerRules = [
    { workType: 'Frontend', agentName: 'Yen', keywords: [] },
    { workType: 'React', agentName: 'Yen', keywords: [] },
  ];
  const result = triageIssue({ title: 'frontend React' }, sameOwnerRules, [], roster);
  assert.equal(result.agent.name, 'Yen');
  assert.equal(result.source, 'routing-rule');
});

for (const [name, apiUrl, serverUrl, expected] of [
  ['local default', undefined, undefined, 'https://api.github.com'],
  ['public GitHub', undefined, 'https://github.com', 'https://api.github.com'],
  ['public GitHub trailing slash', undefined, 'https://github.com/', 'https://api.github.com'],
  ['Enterprise', undefined, 'https://github.example.com', 'https://github.example.com/api/v3'],
  ['Enterprise trailing slash', undefined, 'https://github.example.com/', 'https://github.example.com/api/v3'],
  ['explicit API precedence', 'https://api.example.com/custom///', 'https://github.com', 'https://api.example.com/custom'],
]) {
  test(`resolves the ${name} API endpoint`, () => {
    const previousApi = process.env.GITHUB_API_URL;
    const previousServer = process.env.GITHUB_SERVER_URL;
    try {
      if (apiUrl === undefined) delete process.env.GITHUB_API_URL;
      else process.env.GITHUB_API_URL = apiUrl;
      if (serverUrl === undefined) delete process.env.GITHUB_SERVER_URL;
      else process.env.GITHUB_SERVER_URL = serverUrl;
      assert.equal(resolveGithubApiBase(), expected);
    } finally {
      if (previousApi === undefined) delete process.env.GITHUB_API_URL;
      else process.env.GITHUB_API_URL = previousApi;
      if (previousServer === undefined) delete process.env.GITHUB_SERVER_URL;
      else process.env.GITHUB_SERVER_URL = previousServer;
    }
  });
}
