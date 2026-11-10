// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';

// Deployed to GitHub Pages behind the custom domain gasmod.dev, so the site
// lives at the root and links written in pages, including the generated
// reference pages, start at /.
export default defineConfig({
	site: 'https://gasmod.dev',
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
				{ tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' } },
				{ tag: 'meta', attrs: { property: 'og:image', content: 'https://gasmod.dev/og.png' } },
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
			// Code is dark in both themes, framed as on the home page.
			expressiveCode: {
				themes: ['github-dark-default'],
				useStarlightUiThemeColors: false,
				styleOverrides: {
					borderRadius: '0.5rem',
					borderWidth: '0px',
					codeBackground: '#0d1117',
					codeFontSize: '0.875rem',
					codeLineHeight: '1.6',
					codePaddingBlock: '1rem',
					codePaddingInline: '1.25rem',
					frames: {
						shadowColor: 'transparent',
						editorBackground: '#0d1117',
						editorTabBarBackground: '#030712',
						editorActiveTabBackground: '#030712',
						editorActiveTabIndicatorTopColor: 'transparent',
						editorActiveTabIndicatorBottomColor: 'transparent',
						editorTabBarBorderBottomColor: 'transparent',
						editorActiveTabForeground: '#9ca3af',
						terminalBackground: '#0d1117',
						terminalTitlebarBackground: '#030712',
						terminalTitlebarBorderBottomColor: 'transparent',
						terminalTitlebarForeground: '#9ca3af',
						inlineButtonForeground: '#9ca3af',
					},
				},
			},
			// Start and Core stay open; a module's group opens when one of its
			// pages is open, so the sidebar fits the screen.
			sidebar: [
				{
					label: 'Start',
					items: [
						{ label: 'Introduction', slug: 'index' },
						{ label: 'Installation', slug: 'start/installation' },
						{ label: 'Project layout', slug: 'start/project-layout' },
					],
				},
				{ label: 'Core', items: [{ label: 'gas', slug: 'reference/gas' }] },
				{ label: 'config', collapsed: true, items: [{ autogenerate: { directory: 'reference/config' } }] },
				{ label: 'log', collapsed: true, items: [{ autogenerate: { directory: 'reference/log' } }] },
				{ label: 'database', collapsed: true, items: [{ label: 'Overview', slug: 'reference/database' }] },
				{ label: 'migrate', collapsed: true, items: [{ autogenerate: { directory: 'reference/migrate' } }] },
				{ label: 'auth', collapsed: true, items: [{ autogenerate: { directory: 'reference/auth' } }] },
				{ label: 'cache', collapsed: true, items: [{ autogenerate: { directory: 'reference/cache' } }] },
				{ label: 'storage', collapsed: true, items: [{ autogenerate: { directory: 'reference/storage' } }] },
				{ label: 'queue', collapsed: true, items: [{ autogenerate: { directory: 'reference/queue' } }] },
				{ label: 'email', collapsed: true, items: [{ autogenerate: { directory: 'reference/email' } }] },
				{ label: 'template', collapsed: true, items: [{ autogenerate: { directory: 'reference/template' } }] },
				{ label: 'ui', collapsed: true, items: [{ autogenerate: { directory: 'reference/ui' } }] },
			],
		}),
	],
});
