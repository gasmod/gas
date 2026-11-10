// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';

// The modules and what each gives, worded as on the home page
// (src/components/Landing.astro). llms.txt lists a file of documentation
// for each.
const modules = [
	['config', 'configuration from environment variables, .env, JSON and AWS Secrets Manager, bound to structs'],
	['log', 'structured logging over slog or zerolog, with log shipping'],
	['database', 'PostgreSQL, MySQL and SQLite, with transaction helpers'],
	['migrate', 'database migrations owned by the services that need them'],
	['auth', 'JWTs, sessions, API keys and single-use tokens'],
	['cache', 'key-value caching in memory or on Valkey'],
	['storage', 'object storage on S3, a local directory or memory'],
	['queue', 'job queues on Amazon SQS or in memory'],
	['email', 'transactional email over Amazon SES or an in-memory outbox'],
	['template', 'template storage in memory, a directory, an embedded FS or a database'],
	['ui', 'HTML pages with layouts, partials, static files and HTMX fragments'],
];

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
				{ tag: 'meta', attrs: { property: 'og:image:type', content: 'image/png' } },
				{
					tag: 'meta',
					attrs: { property: 'og:image:alt', content: 'The Gas mark beside the text github.com/gasmod' },
				},
				// Where a client finds the index of the documentation for LLMs:
				// the link relation the llms.txt proposal gives for it. A static
				// host cannot send it as a Link header.
				{
					tag: 'link',
					attrs: { rel: 'describedby', type: 'text/plain', href: 'https://gasmod.dev/llms.txt', title: 'llms.txt' },
				},
				// The Context7 chat widget: a floating button that answers
				// questions from the documentation Context7 holds for Gas. Its
				// script adds the button to <body> as it runs, so it is deferred
				// until the page is parsed. It sends nothing until a reader asks
				// a question, and Context7 answers only on the domains allowed in
				// the library's chat settings there.
				{
					tag: 'script',
					attrs: {
						src: 'https://context7.com/widget.js',
						'data-library': '/gasmod/gas',
						// The accent of the light theme, which white text reads on
						// in both themes.
						'data-color': '#3d5afe',
						'data-placeholder': 'Ask about Gas',
						'data-welcome-message':
							'Ask a question about Gas. An AI answers from the documentation, so check what it says against the page.',
						defer: true,
					},
				},
			],
			// Per page: the robots rule, the link to the page's Markdown version
			// and its structured data.
			routeMiddleware: './src/routeData.ts',
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
					// What llms.txt opens with: what Gas is, then when to use it
					// and how to read these docs. An agent reads this before it
					// reads any page, so each statement is one the README or a
					// package's documentation makes.
					description:
						'Gas is a modular Go framework for SaaS backends. The core, github.com/gasmod/gas, gives dependency injection, routing, middleware, events and a service lifecycle. Configuration, logging, database access, migrations, auth, caching, object storage, job queues, email, template storage and HTML rendering are separate Go modules, so an app imports only what it uses.',
					details: [
						"Use Gas for a Go HTTP API or worker that needs this plumbing. Most modules implement an interface the core declares, so a service depends on the interface, not the module, and any module can be replaced by your own implementation or a test double. Each package's documentation is its contract: it states the package's behavior, defaults and errors, with the error messages verbatim, so code can be written against it without reading the source.",
						[
							'To work with these docs:',
							'',
							'- Read the gas package first. Every module builds on it.',
							'- Every page is also served as Markdown: add `index.md` to its URL, as in https://gasmod.dev/start/installation/index.md.',
							'- For exact signatures, run `go doc`, which matches the version in go.mod, such as `go doc github.com/gasmod/gas/storage.CopyObjectInput`.',
							'- A skill for coding agents, generated from the same documentation, is at https://github.com/gasmod/gas/tree/main/skills/gas.',
						].join('\n'),
					].join('\n\n'),
					// One file per part of the site, so an agent can take a module's
					// documentation without the rest.
					customSets: [
						{
							label: 'Start',
							paths: ['start/**'],
							description: 'installing Gas, registering modules, and laying out a project',
						},
						{
							label: 'gas',
							paths: ['reference/gas'],
							description:
								'the core package: services and the container, the lifecycle, routing, handlers, errors, middleware, events and configuration',
						},
						...modules.map(([name, description]) => ({
							label: name,
							paths: [`reference/${name}`, `reference/${name}/**`],
							description,
						})),
					],
					// The full and abridged files read in the order a person would:
					// the introduction, the guides, the core, then the modules.
					promote: ['index*', 'start/installation', 'start/project-layout', 'reference/gas'],
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
