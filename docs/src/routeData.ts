// Adds to each page's <head> what search engines and LLMs read beside the
// tags Starlight writes: the robots rule, the link to the page's Markdown
// version, and its structured data.
import { defineRouteMiddleware } from '@astrojs/starlight/route-data';
import { markdownPath } from './lib/llm';

const repository = 'https://github.com/gasmod/gas';

export const onRequest = defineRouteMiddleware((context) => {
	const { entry, id, head, siteTitle } = context.locals.starlightRoute;

	// A missing page is not for an index, and describes nothing.
	if (id === '404') {
		head.push({ tag: 'meta', attrs: { name: 'robots', content: 'noindex' } });
		return;
	}

	head.push({
		tag: 'meta',
		attrs: { name: 'robots', content: 'index, follow, max-image-preview:large, max-snippet:-1' },
	});

	// Starlight writes the canonical URL only when `site` is set, and the
	// tags below need the same absolute URLs.
	const canonical = head.find((tag) => tag.tag === 'link' && tag.attrs?.rel === 'canonical');
	const page = canonical?.attrs?.href;
	if (!context.site || typeof page !== 'string') return;

	const base = import.meta.env.BASE_URL.replace(/\/$/, '');
	const site = new URL(`${base}/`, context.site).href;
	const { title, description, template } = entry.data;

	const website = { '@type': 'WebSite', '@id': `${site}#website`, url: site, name: siteTitle };
	const software = {
		'@type': 'SoftwareSourceCode',
		'@id': `${site}#software`,
		name: siteTitle,
		url: site,
		codeRepository: repository,
		programmingLanguage: 'Go',
		license: `${repository}/blob/main/LICENSE`,
	};

	// The landing page describes the site and the framework. Every other
	// page is an article about the framework, with its Markdown version and
	// its place in the site.
	let graph: object[];
	if (template === 'splash') {
		graph = [
			{ ...website, description, inLanguage: 'en' },
			{ ...software, description },
		];
	} else {
		head.push({
			tag: 'link',
			attrs: {
				rel: 'alternate',
				type: 'text/markdown',
				href: new URL(`${base}${markdownPath(id)}`, context.site).href,
			},
		});

		// A sub-package's page sits under its module's page.
		const crumbs = [{ name: siteTitle, url: site }];
		const [section, module, sub] = id.split('/');
		if (section === 'reference' && module && sub) {
			crumbs.push({ name: module, url: `${site}reference/${module}/` });
		}
		crumbs.push({ name: title, url: page });

		graph = [
			website,
			software,
			{
				'@type': 'TechArticle',
				'@id': `${page}#article`,
				headline: title,
				description,
				url: page,
				mainEntityOfPage: page,
				inLanguage: 'en',
				image: new URL(`${base}/og.png`, context.site).href,
				isPartOf: { '@id': website['@id'] },
				about: { '@id': software['@id'] },
			},
			{
				'@type': 'BreadcrumbList',
				itemListElement: crumbs.map((crumb, i) => ({
					'@type': 'ListItem',
					position: i + 1,
					name: crumb.name,
					item: crumb.url,
				})),
			},
		];
	}

	head.push({
		tag: 'script',
		attrs: { type: 'application/ld+json' },
		// `<` is escaped so that no text in a title or description can close
		// the script element.
		content: JSON.stringify({ '@context': 'https://schema.org', '@graph': graph }).replace(/</g, '\\u003c'),
	});
});
