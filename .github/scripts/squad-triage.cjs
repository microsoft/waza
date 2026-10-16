const fs = require('node:fs');
const {
  parseRoster, parseRoutingRules, parseModuleOwnership, triageIssue,
} = require('../../.squad/templates/ralph-triage.js');

async function triageLabeledIssue({ github, context, core }) {
  const { issue, action, label } = context.payload;
  if (context.eventName !== 'issues' || action !== 'labeled' || label?.name !== 'squad' ||
      !issue || issue.pull_request || issue.state !== 'open' ||
      issue.labels?.some((entry) => (typeof entry === 'string' ? entry : entry.name).startsWith('squad:'))) {
    return;
  }
  const teamFile = fs.existsSync('.squad/team.md') ? '.squad/team.md' : '.ai-team/team.md';
  if (!fs.existsSync(teamFile)) {
    core.warning('No .squad/team.md or .ai-team/team.md found - cannot triage');
    return;
  }
  const content = fs.readFileSync(teamFile, 'utf8');
  const members = parseRoster(content);
  const lead = members.find((member) => /lead|architect|coordinator/i.test(member.role));
  if (!lead) {
    core.warning('No Lead role found in team roster - cannot triage');
    return;
  }
  const hasCopilot = content.includes('🤖 Coding Agent');
  const copilotAutoAssign = content.includes('<!-- copilot-auto-assign: true -->');
  const issueText = `${issue.title}\n${issue.body || ''}`.toLowerCase();
  const keywords = (pattern, fallback) => {
    const match = content.match(pattern);
    const values = match ? match[1].toLowerCase().replace(/\*/g, '').split(',')
      .map((value) => value.trim()).filter(Boolean) : [];
    return values.length ? values : fallback;
  };
  let assignedMember;
  let triageReason;
  let copilotTier;
  if (hasCopilot) {
    const unsuitable = keywords(/🔴\s*Not suitable[^:]*:\s*(.+)/i,
      ['architecture', 'system design', 'security', 'auth', 'encryption', 'performance']);
    const good = keywords(/🟢\s*Good fit[^:]*:\s*(.+)/i,
      ['bug fix', 'test coverage', 'lint', 'format', 'dependency update', 'small feature', 'scaffolding', 'doc fix', 'documentation']);
    const review = keywords(/🟡\s*Needs review[^:]*:\s*(.+)/i,
      ['medium feature', 'refactoring', 'api endpoint', 'migration']);
    if (unsuitable.some((word) => issueText.includes(word))) copilotTier = 'not-suitable';
    else if (good.some((word) => issueText.includes(word))) copilotTier = 'good-fit';
    else if (review.some((word) => issueText.includes(word))) copilotTier = 'needs-review';
    if (copilotTier === 'good-fit' || copilotTier === 'needs-review') {
      assignedMember = { name: '@copilot', role: 'Coding Agent', label: 'squad:copilot' };
      triageReason = copilotTier === 'good-fit' ? 'Good fit for @copilot - matches capability profile' :
        'Routing to @copilot (needs review) - a squad member should review the PR';
    }
  }
  if (!assignedMember) {
    const routingFile = fs.existsSync('.squad/routing.md') ? '.squad/routing.md' : '.ai-team/routing.md';
    const routing = fs.existsSync(routingFile) ? fs.readFileSync(routingFile, 'utf8') : '';
    const result = triageIssue(issue, parseRoutingRules(routing), parseModuleOwnership(routing), members);
    assignedMember = result.agent;
    triageReason = result.reason;
  }
  const target = { owner: context.repo.owner, repo: context.repo.repo, issue_number: issue.number };
  await github.rest.issues.addLabels({ ...target, labels: [assignedMember.label] });
  await github.rest.issues.addLabels({ ...target, labels: ['go:needs-research'] });
  const isCopilot = assignedMember.name === '@copilot';
  if (isCopilot && copilotAutoAssign && !issue.assignees?.length) {
    try {
      await github.rest.issues.addAssignees({ ...target, assignees: ['copilot'] });
    } catch (error) {
      core.warning(`Could not auto-assign @copilot: ${error.message}`);
    }
  }
  const comment = [
    `### Squad Triage - ${lead.name} (${lead.role})`,
    `**Issue:** #${issue.number} - ${issue.title}`,
    `**Assigned to:** ${assignedMember.name} (${assignedMember.role})`,
    `**Reason:** ${triageReason}`,
    copilotTier === 'needs-review' ? '**PR review recommended** - a squad member should review @copilot work.' : '',
    hasCopilot && !isCopilot ? `**@copilot evaluation:** ${copilotTier === 'not-suitable' ?
      'Not suitable - outside the coding agent capability profile.' : 'No strong capability match - routed to squad member.'}` : '',
    '**Team roster:**',
    ...members.map((member) => `- **${member.name}** (${member.role}) - label: \`${member.label}\``),
    hasCopilot ? '- **@copilot** (Coding Agent) - label: `squad:copilot`' : '',
    '> To reassign, remove the current `squad:*` label and add the correct one.',
  ].filter(Boolean).join('\n\n');
  await github.rest.issues.createComment({ ...target, body: comment });
  core.info(`Triaged issue #${issue.number} -> ${assignedMember.name} (${assignedMember.label})`);
}

module.exports = { triageLabeledIssue };
