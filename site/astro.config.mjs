// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	site: 'https://microsoft.github.io/waza',
	base: '/waza',
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
