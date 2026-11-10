// Serves each documentation page as Markdown at the page's URL plus
// `index.md`, as the llms.txt proposal asks of a URL without a file name.
// The page's `<link rel="alternate" type="text/markdown">` points here, and
// the page's "Copy for LLM" and "Ask" actions read from here.
//
// The Markdown is the page's source, under its title and description, so it
// is what a reference page's Go doc generated or what a guide's author
// wrote. The landing page is a component, not Markdown, and has none.
import type { APIRoute, GetStaticPaths } from 'astro';
import { getCollection, type CollectionEntry } from 'astro:content';
import { llmsTxtPath } from '../../lib/llm';

export const prerender = true;

export const getStaticPaths = (async () => {
	const pages = await getCollection('docs');
	return pages
		.filter((entry) => entry.data.template !== 'splash')
		.map((entry) => ({ params: { slug: entry.id }, props: { entry } }));
}) satisfies GetStaticPaths;

export const GET: APIRoute<{ entry: CollectionEntry<'docs'> }> = ({ props, site }) => {
	if (!site) throw new Error('`site` must be set in the Astro configuration.');

	const { entry } = props;
	const base = import.meta.env.BASE_URL.replace(/\/$/, '');
	const page = new URL(`${base}/${entry.id}/`, site);
	const index = new URL(`${base}${llmsTxtPath}`, site);

	// A link written from the site's root, as the pages write them, would
	// lead nowhere once the Markdown is pasted elsewhere.
	const body = (entry.body ?? '').replace(/\]\(\/(?!\/)/g, `](${site.origin}/`).trim();

	const parts = [`# ${entry.data.title}`];
	if (entry.data.description) parts.push(`> ${entry.data.description}`);
	// At the top, since an agent may cut a long page short.
	parts.push(`The Markdown of ${page} from the Gas documentation, whose index for LLMs is ${index}`);
	parts.push(body);

	return new Response(parts.join('\n\n') + '\n', {
		headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
	});
};
