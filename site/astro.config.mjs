// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	base: '/waza',
	integrations: [
		starlight({
			title: 'waza',
			description: 'CLI tool for evaluating AI Agent Skills',
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/spboyer/waza' }
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
						{ label: 'Web Dashboard', slug: 'guides/dashboard' },
						{ label: 'CI/CD Integration', slug: 'guides/ci-cd' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI Commands', slug: 'reference/cli' },
						{ label: 'YAML Schema', slug: 'reference/schema' },
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
