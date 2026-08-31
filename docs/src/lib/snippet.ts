/**
 * Extracts a named region from a source file imported with Vite's `?raw`.
 *
 * Regions are delimited by `// #region <name>` and `// #endregion` in the Go
 * source under `docs/snippets`, which compiles as part of the workspace. A
 * missing region throws, so renaming a marker fails the docs build rather
 * than silently publishing an empty code block.
 */
export function region(source: string, name: string): string {
	const lines = source.split('\n');
	const start = lines.findIndex((l) => l.trim() === `// #region ${name}`);
	if (start === -1) {
		throw new Error(`snippet region "${name}" not found`);
	}

	const rest = lines.slice(start + 1);
	const end = rest.findIndex((l) => l.trim().startsWith('// #endregion'));
	if (end === -1) {
		throw new Error(`snippet region "${name}" is not closed`);
	}

	const body = rest
		.slice(0, end)
		// Drop nested markers so overlapping regions stay readable.
		.filter((l) => !l.trim().startsWith('// #region') && !l.trim().startsWith('// #endregion'));

	return dedent(trimBlankEdges(body)).join('\n');
}

function trimBlankEdges(lines: string[]): string[] {
	let a = 0;
	let b = lines.length;
	while (a < b && lines[a]!.trim() === '') a++;
	while (b > a && lines[b - 1]!.trim() === '') b--;
	return lines.slice(a, b);
}

function dedent(lines: string[]): string[] {
	const indents = lines
		.filter((l) => l.trim() !== '')
		.map((l) => l.match(/^[\t ]*/)![0].length);
	const min = indents.length ? Math.min(...indents) : 0;
	return min ? lines.map((l) => l.slice(min)) : lines;
}
