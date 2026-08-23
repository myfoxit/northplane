// Validate links into the generated REST reference.
//
// starlight-links-validator cannot see the pages starlight-openapi generates
// (they are not part of the docs content collection), so those links are
// excluded from its run in astro.config.mjs — and checked here instead:
// every `/docs/reference/api/operations/<operationId>/` link in the content
// must name an operationId that exists in src/assets/openapi.json, and every
// `/docs/reference/api/...` link must be one of the shapes the plugin emits.
// Runs before `astro build` (see package.json).
import { readFile, glob } from 'node:fs/promises';

const spec = JSON.parse(await readFile(new URL('./src/assets/openapi.json', import.meta.url), 'utf8'));
const operationIds = new Set();
const tags = new Set();
for (const item of Object.values(spec.paths)) {
	for (const [method, op] of Object.entries(item)) {
		if (!['get', 'post', 'put', 'patch', 'delete'].includes(method)) continue;
		operationIds.add(op.operationId.toLowerCase());
		for (const t of op.tags ?? []) tags.add(t.toLowerCase());
	}
}

const linkRe = /\(\s*(\/docs\/reference\/api\/[^)\s#]*)[^)]*\)|href=["'](\/docs\/reference\/api\/[^"'#]*)/g;
let bad = 0, checked = 0;
for await (const file of glob('src/content/docs/**/*.{md,mdx}')) {
	const text = await readFile(file, 'utf8');
	for (const m of text.matchAll(linkRe)) {
		const link = (m[1] ?? m[2]).replace(/\/$/, '');
		checked++;
		const rest = link.slice('/docs/reference/api'.length);
		if (rest === '') continue; // the overview page
		const op = rest.match(/^\/operations\/([^/]+)$/);
		const tag = rest.match(/^\/operations\/tags\/([^/]+)$/);
		if (tag) {
			if (!tags.has(tag[1].toLowerCase())) { bad++; console.error(`${file}: unknown tag page ${link}`); }
		} else if (op) {
			if (!operationIds.has(op[1].toLowerCase())) { bad++; console.error(`${file}: unknown operationId ${link}`); }
		} else {
			bad++; console.error(`${file}: unexpected API reference link ${link}`);
		}
	}
}
console.log(`[docs] ${checked} REST-reference links checked against ${operationIds.size} operations${bad ? `, ${bad} INVALID` : ''}`);
if (bad) process.exit(1);
