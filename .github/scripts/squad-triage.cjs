const fs = require('node:fs');
const {
  parseRoster, parseRoutingRules, parseModuleOwnership, triageIssue,
} = require('../../.squad/templates/ralph-triage.js');

function parseCapabilities(content) {
  const tiers = {};
  let inProfile = false;
  let current;
  const addKeywords = (text) => {
    if (/[()]/.test(text.replace(/\([^()]*\)/g, ''))) return false;
    for (const entry of text.split(/,(?![^()]*\))/).map((value) => value.trim()).filter(Boolean)) {
      const subjects = entry.replace(/\([^()]*\)/g, '').split(/\s+(?:with|following)\s+/i)[0]
        .split(/\s+and\s+/i).map((part) => part.split(/-critical\b/i)[0].trim());
      tiers[current].push(entry, ...subjects.filter(Boolean));
      // Exclusion examples constrain eligibility; positive explanatory prose cannot grant it.
      if (current === '🔴') {
        // Keep specific exclusion subjects, not bare generic words such as "Changes".
        const subject = entry.replace(/\([^()]*\)/g, '')
          .match(/^(\S+\s+.+?)\s+(?:needing|requiring)\s+\S/i)?.[1];
        if (subject) tiers[current].push(subject);
        for (const example of entry.matchAll(/\(([^()]*)\)/g)) {
          tiers[current].push(...example[1].split(',').map((value) => value.trim()));
        }
      }
    }
    return true;
  };
  for (const raw of content.replace(/\r\n/g, '\n').split('\n')) {
    const line = raw.trim().replace(/\*\*/g, '');
    if (/^###\s+Capabilities\s*$/i.test(line)) {
      inProfile = true;
      continue;
    }
    if (!inProfile) continue;
    if (/^#{1,3}\s/.test(line)) break;
    if (!line) continue;
    const heading = line.match(/^(🟢 Good fit|🟡 Needs review|🔴 Not suitable)(?:\s+[—–-][^:]*)?:\s*(.*)$/i);
    if (heading) {
      current = heading[1].split(' ')[0];
      if (tiers[current]) return null;
      tiers[current] = [];
      if (!addKeywords(heading[2])) return null;
    } else if (current && /^[-*]\s+/.test(line)) {
      if (!addKeywords(line.replace(/^[-*]\s+/, ''))) return null;
    } else {
      return null;
    }
  }
  return ['🟢', '🟡', '🔴'].every((tier) =>
    tiers[tier]?.length && tiers[tier].every((word) => /[a-z0-9]/i.test(word))) ? tiers : null;
}

function capabilityText(text) {
  return ` ${text.toLowerCase().replace(/\bfixes\b/g, 'fix')
    .replace(/\b([a-z]{3,})s\b/g, '$1').replace(/[^a-z0-9]+/g, ' ').trim()} `;
}

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
    core.warning('No Lead, Architect or Coordinator role found in team roster - cannot triage');
    return;
  }
  const hasCopilot = content.includes('🤖 Coding Agent');
  const copilotAutoAssign = content.includes('<!-- copilot-auto-assign: true -->');
  const issueText = capabilityText(`${issue.title}\n${issue.body || ''}`);
  let assignedMember;
  let triageReason;
  let copilotTier;
  if (hasCopilot) {
    const capabilities = parseCapabilities(content);
    const matches = (tier) => capabilities[tier].some((word) => issueText.includes(capabilityText(word)));
    if (!capabilities) {
      copilotTier = 'disabled';
      core.warning('Coding agent capability profile missing or malformed - Copilot routing and assignment disabled');
    } else if (matches('🔴')) copilotTier = 'not-suitable';
    else if (matches('🟡')) copilotTier = 'needs-review';
    else if (matches('🟢')) copilotTier = 'good-fit';
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
    if (!result || result.source === 'lead-fallback') {
      assignedMember = lead;
      triageReason = 'No module or routing match - routed to resolved team lead';
    } else {
      assignedMember = result.agent;
      triageReason = result.reason;
    }
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
    hasCopilot && !isCopilot ? `**@copilot evaluation:** ${copilotTier === 'disabled' ?
      'Capability profile missing or malformed - Copilot routing and assignment disabled.' : copilotTier === 'not-suitable' ?
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
