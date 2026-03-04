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
			description: 'CLI tool for evaluating AI Agent Skills',
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
					label: 'Getting Started',
					slug: 'getting-started',
				},
				{
					label: 'Guides',
					items: [
						{ label: 'Writing Eval Specs', slug: 'guides/eval-yaml' },
						{ label: 'Validators & Graders', slug: 'guides/graders' },
						{ label: 'Token Limits', slug: 'guides/token-limits' },
						{ label: 'Web Dashboard', slug: 'guides/dashboard' },
						{ label: 'Explore the Dashboard', slug: 'guides/dashboard-explore' },
						{ label: 'CI/CD Integration', slug: 'guides/ci-cd' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI Commands', slug: 'reference/cli' },
						{ label: 'YAML Schema', slug: 'reference/schema' },
						{ label: 'Releases', slug: 'reference/releases' },
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
