// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// Deployed to GitHub Pages as a project page, so the site lives under /gas.
// Starlight prefixes internal links with `base` automatically; hero action
// links in index.mdx are the documented exception and carry it explicitly.
export default defineConfig({
	site: 'https://gasmod.github.io',
	base: '/gas',
	integrations: [
		starlight({
			title: 'Gas',
			description:
				'A modular Go framework for building micro-SaaS applications: dependency injection, routing, middleware, events, migrations, and service lifecycle.',
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/gasmod/gas' },
			],
			editLink: {
				baseUrl: 'https://github.com/gasmod/gas/edit/main/docs/',
			},
			customCss: ['./src/styles/custom.css'],
			sidebar: [
				{
					label: 'Start here',
					items: [
						{ label: 'Introduction', slug: '' },
						{ label: 'Getting started', slug: 'start/getting-started' },
						{ label: 'Your first service', slug: 'start/first-service' },
					],
				},
				{
					label: 'Guides',
					items: [
						{ label: 'Connect a database', slug: 'guides/database' },
						{ label: 'Authenticate requests', slug: 'guides/auth' },
						{ label: 'Serve HTML', slug: 'guides/html' },
						{ label: 'Store and serve files', slug: 'guides/files' },
						{ label: 'Send email', slug: 'guides/email' },
						{ label: 'Run background jobs', slug: 'guides/background-jobs' },
						{ label: 'Configure an app', slug: 'guides/configuration' },
						{ label: 'Structured logging', slug: 'guides/logging' },
						{ label: 'Handle errors', slug: 'guides/errors' },
						{ label: 'Test a service', slug: 'guides/testing' },
					],
				},
				{
					label: 'Concepts',
					items: [
						{ label: 'Overview', slug: 'concepts/overview' },
						{ label: 'Services and DI', slug: 'concepts/services-and-di' },
						{ label: 'Request scopes', slug: 'concepts/request-scopes' },
						{ label: 'Ownership and teardown', slug: 'concepts/ownership' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'Modules', slug: 'reference/modules' },
						{ label: 'Examples', slug: 'reference/examples' },
						{ label: 'Contributing', slug: 'contributing' },
					],
				},
			],
		}),
	],
});
