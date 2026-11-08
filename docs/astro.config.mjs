// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';

// Deployed to GitHub Pages as a project page, so the site lives under /gas.
// Starlight prefixes sidebar entries with `base`; links written in pages,
// including the generated reference pages, carry /gas explicitly.
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
				{ label: 'Start', items: [{ label: 'Introduction', slug: 'index' }, { label: 'Installation', slug: 'start/installation' }] },
				{ label: 'Core', items: [{ label: 'gas', slug: 'reference/gas' }] },
				{ label: 'config', collapsed: true, items: [{ autogenerate: { directory: 'reference/config' } }] },
				{ label: 'log', collapsed: true, items: [{ autogenerate: { directory: 'reference/log' } }] },
				{ label: 'database', collapsed: true, items: [{ label: 'Overview', slug: 'reference/database' }] },
				{ label: 'migrate', collapsed: true, items: [{ autogenerate: { directory: 'reference/migrate' } }] },
				{ label: 'cache', collapsed: true, items: [{ autogenerate: { directory: 'reference/cache' } }] },
				{ label: 'auth', collapsed: true, items: [{ autogenerate: { directory: 'reference/auth' } }] },
				{ label: 'template', collapsed: true, items: [{ autogenerate: { directory: 'reference/template' } }] },
				{ label: 'ui', collapsed: true, items: [{ autogenerate: { directory: 'reference/ui' } }] },
				{ label: 'storage', collapsed: true, items: [{ autogenerate: { directory: 'reference/storage' } }] },
				{ label: 'email', collapsed: true, items: [{ autogenerate: { directory: 'reference/email' } }] },
				{ label: 'queue', collapsed: true, items: [{ autogenerate: { directory: 'reference/queue' } }] },
			],
		}),
	],
});
