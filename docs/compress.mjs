// Pre-compress the static build for embedding into northplaned.
//
// The site is served by the Go binary from go:embed, which stores files
// verbatim — a 224-operation API reference alone is ~36 MB of HTML. Gzipping
// text assets in place (the .gz replaces the original) shrinks the embedded
// tree ~8×; the Go handler (internal/docs) serves the .gz directly to
// gzip-capable clients and inflates it for the rare client that is not.
// Already-compressed formats (images, fonts, Pagefind's .pf_* chunks) are
// left alone. Run via `npm run build:embed`.
import { readdir, readFile, writeFile, unlink } from 'node:fs/promises';
import { join, extname } from 'node:path';
import { gzipSync } from 'node:zlib';

const root = new URL('./dist/', import.meta.url).pathname;
const compressible = new Set(['.html', '.css', '.js', '.mjs', '.json', '.xml', '.svg', '.txt', '.map', '.webmanifest']);

async function* walk(dir) {
	for (const entry of await readdir(dir, { withFileTypes: true })) {
		const p = join(dir, entry.name);
		if (entry.isDirectory()) yield* walk(p);
		else yield p;
	}
}

let files = 0, before = 0, after = 0;
for await (const file of walk(root)) {
	if (!compressible.has(extname(file))) continue;
	const data = await readFile(file);
	const gz = gzipSync(data, { level: 9 });
	await writeFile(file + '.gz', gz);
	await unlink(file);
	files++; before += data.length; after += gz.length;
}
console.log(`[docs] pre-compressed ${files} files: ${(before / 1048576).toFixed(1)} MB → ${(after / 1048576).toFixed(1)} MB`);
