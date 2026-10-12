// Finishes out/_headers: the bundler emits a small inline bootstrap script, and
// rather than allowing all inline scripts, exactly the ones in this build are
// allowed, by hash. The otelgenie program serves the page under these headers.
import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const out = new URL('../out/', import.meta.url).pathname;
const pages = [];
const walk = (dir) => {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) walk(path);
    else if (name.endsWith('.html')) pages.push(path);
  }
};
walk(out);

const hashes = new Set();
for (const page of pages) {
  const html = readFileSync(page, 'utf8');
  for (const m of html.matchAll(/<script(?![^>]*\bsrc=)(?![^>]*type="application\/json")[^>]*>([\s\S]*?)<\/script>/g)) {
    hashes.add(`'sha256-${createHash('sha256').update(m[1]).digest('base64')}'`);
  }
}
const headers = join(out, '_headers');
writeFileSync(headers, readFileSync(headers, 'utf8').replace('__INLINE_SCRIPT_HASHES__', [...hashes].join(' ')));
console.log(`_headers: ${hashes.size} inline script hash(es) allowed`);
