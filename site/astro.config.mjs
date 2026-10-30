// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// Normalize base path: ensure leading slash, strip trailing slashes.
const base = (() => {
	const raw = process.env.ASTRO_BASE ?? '/waza';
	if (!raw || raw === '/') return '/';
	const normalized = raw.startsWith('/') ? raw : `/${raw}`;
	return normalized.replace(/\/+$/, '') || '/';
})();

// https://astro.build/config
export default defineConfig({
	site: process.env.ASTRO_SITE || 'https://microsoft.github.io/waza',
	base,
	integrations: [
		starlight({
			title: 'waza',
			description: 'Evaluate agent workflows, skills, and custom agents',
			components: {
				Header: './src/components/Header.astro',
			},
			expressiveCode: {
				themes: ['github-light', 'github-dark'],
			},
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/microsoft/waza' }
			],
			customCss: ['./src/styles/custom.css'],
			sidebar: [
				{
					label: 'Concepts and Learning Path',
					slug: 'concepts',
				},
				{
					label: 'Quick Start',
					slug: 'quick-start',
				},
				{
					label: 'Getting Started',
					slug: 'getting-started',
				},
				{
					label: 'Guides',
					items: [
						{ label: 'Writing Eval Specs', slug: 'guides/eval-yaml' },
						{ label: 'Offline Eval Preflight', slug: 'guides/preflight' },
						{ label: 'Dependency Recovery', slug: 'guides/dependency-recovery' },

						{ label: 'Controlled comparisons', slug: 'guides/controlled-comparisons' },
						{ label: 'Preserving Existing Workflows', slug: 'guides/compatibility' },
						{ label: 'Evaluating Custom Agents', slug: 'guides/custom-agents' },
						{ label: 'Validators & Graders', slug: 'guides/graders' },
						{ label: 'Spec Verification', slug: 'guides/spec-verify' },
						{ label: 'Token Limits', slug: 'guides/token-limits' },
						{ label: 'Web Dashboard', slug: 'guides/dashboard' },
						{ label: 'Dashboard Lab (Prototype)', slug: 'guides/dashboard-lab' },
						{ label: 'Explore the Dashboard', slug: 'guides/dashboard-explore' },
						{ label: 'CI/CD Integration', slug: 'guides/ci-cd' },
						{ label: 'OpenTelemetry Tracing', slug: 'guides/otel' },
						{ label: 'Adversarial Harness', slug: 'guides/adversarial' },
						{ label: 'Snapshot & Replay', slug: 'guides/snapshot-replay' },
						{ label: 'Azure Storage', slug: 'guides/azure-storage' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI Commands', slug: 'reference/cli' },
						{ label: 'Project Configuration', slug: 'reference/waza-yaml' },
						{ label: 'Statistical Fields', slug: 'reference/statistical-fields' },
						{ label: 'YAML Schema', slug: 'reference/schema' },
						{ label: 'Rubric Library', slug: 'reference/rubrics' },
						{ label: 'Schema Changes', slug: 'reference/schema-changes' },
						{
							label: 'Releases',
							items: [
								{ label: 'Overview & downloads', slug: 'reference/releases' },
								{ label: 'Waza v0.39.0', slug: 'releases/v0-39-0' },
								{ label: 'Introducing Dashboard Lab', slug: 'releases/dashboard-lab' },
							],
						},
					],
				},
				{
					label: 'About',
					slug: 'about',
				},
			],
		}),
	],
});
