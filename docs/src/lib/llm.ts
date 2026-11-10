/**
 * What the site offers LLMs and coding agents, beside its HTML: each page as
 * Markdown, at the page's URL plus `index.md`, the form the llms.txt proposal
 * gives for a URL without a file name.
 */

/** The site's llms.txt, the index of the documentation for LLMs. */
export const llmsTxtPath = '/llms.txt';

/** The path of the Markdown version of the page with the given id. */
export function markdownPath(id: string): string {
	return `/${id}/index.md`;
}
