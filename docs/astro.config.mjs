// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';

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
			favicon: '/gas-icon.svg',
			head: [
				{ tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/gas/apple-touch-icon.png' } },
				{ tag: 'meta', attrs: { property: 'og:image', content: 'https://gasmod.github.io/gas/og.png' } },
				{ tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
				{ tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
			],
			components: {
				Header: './src/components/Header.astro',
				PageTitle: './src/components/PageTitle.astro',
				SiteTitle: './src/components/SiteTitle.astro',
			},
			editLink: {
				baseUrl: 'https://github.com/gasmod/gas/edit/main/docs/',
			},
			customCss: ['./src/styles/custom.css'],
			plugins: [
				starlightLinksValidator(),
				starlightLlmsTxt({
					// Heading anchor links would otherwise appear as "Section titled ..." text.
					customSelectors: { all: ['.sl-anchor-link'] },
				}),
			],
			sidebar: [
				{
					label: 'Getting started',
					items: [
						{ label: 'Introduction', slug: 'start/introduction' },
						{ label: 'Installation', slug: 'start/installation' },
						{ label: 'Tutorial: build a notes API', slug: 'start/tutorial' },
						{ label: 'Project structure', slug: 'start/project-structure' },
					],
				},
				{
					label: 'Core',
					items: [
						{ label: 'App lifecycle', slug: 'core/lifecycle' },
						{ label: 'Services and DI', slug: 'core/services' },
						{ label: 'Routing', slug: 'core/routing' },
						{ label: 'Handlers and Context', slug: 'core/handlers' },
						{ label: 'Middleware', slug: 'core/middleware' },
						{ label: 'Request scopes', slug: 'core/request-scopes' },
						{ label: 'Errors', slug: 'core/errors' },
						{ label: 'Events', slug: 'core/events' },
						{ label: 'Ownership and teardown', slug: 'core/ownership' },
						{ label: 'Workers', slug: 'core/workers' },
					],
				},
				{
					label: 'Modules',
					items: [
						{ label: 'Overview', slug: 'modules/overview' },
						{ label: 'Config', slug: 'modules/config' },
						{ label: 'Logging', slug: 'modules/logging' },
						{ label: 'Database', slug: 'modules/database' },
						{ label: 'Migrations', slug: 'modules/migrations' },
						{ label: 'Cache', slug: 'modules/cache' },
						{ label: 'Auth', slug: 'modules/auth' },
						{ label: 'Templates', slug: 'modules/templates' },
						{ label: 'UI', slug: 'modules/ui' },
						{ label: 'Storage', slug: 'modules/storage' },
						{ label: 'Email', slug: 'modules/email' },
						{ label: 'Queue', slug: 'modules/queue' },
					],
				},
				{
					label: 'Production',
					collapsed: true,
					items: [
						{ label: 'Testing', slug: 'production/testing' },
						{ label: 'Deployment', slug: 'production/deployment' },
					],
				},
				{
					label: 'Project',
					collapsed: true,
					items: [
						{ label: 'FAQ', slug: 'project/faq' },
						{ label: 'Known issues', slug: 'project/known-issues' },
						{ label: 'Examples', slug: 'project/examples' },
						{ label: 'Contributing', slug: 'project/contributing' },
						{
							label: 'Changelog',
							link: 'https://github.com/gasmod/gas/blob/main/CHANGELOG.md',
						},
					],
				},
			],
		}),
	],
});
