import assert from 'node:assert/strict';
import { lstatSync, readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const root = join(process.cwd(), 'web', 'dist');
function directory(path) {
  assert(lstatSync(path).isDirectory(), `release dashboard directory required: ${path}`);
}
function file(relative) {
  const parts = relative.split('/');
  assert(parts.every((part) => part && part !== '.' && part !== '..' && !part.includes('\\')),
    `invalid release dashboard asset path: ${relative}`);
  for (let i = 1; i < parts.length; i++) {
    directory(join(root, ...parts.slice(0, i)));
  }
  const path = join(root, ...parts);
  const stat = lstatSync(path);
  assert(stat.isFile() && stat.size > 0, `nonempty regular release dashboard file required: ${path}`);
}

directory(root);
directory(join(root, 'assets'));
file('index.html');
file('favicon.svg');
const bundles = readdirSync(join(root, 'assets')).filter((name) => /\.(js|css)$/.test(name));
assert(bundles.some((name) => name.endsWith('.js')), 'release dashboard JavaScript output required');
assert(bundles.some((name) => name.endsWith('.css')), 'release dashboard CSS output required');
for (const name of bundles) file(`assets/${name}`);

const html = readFileSync(join(root, 'index.html'), 'utf8');
const refs = [];
for (const tag of html.matchAll(/<(?:script|link)\b[^>]*>/gi)) {
  for (const attribute of tag[0].matchAll(/\b(?:src|href)\s*=\s*["'](\/assets\/[^"']+)["']/gi)) {
    const relative = decodeURIComponent(attribute[1].split(/[?#]/, 1)[0]).slice(1);
    assert(relative.startsWith('assets/'), `invalid release dashboard reference: ${attribute[1]}`);
    file(relative);
    refs.push(relative);
  }
}
assert(refs.some((name) => name.endsWith('.js')), 'release dashboard index must reference JavaScript');
assert(refs.some((name) => name.endsWith('.css')), 'release dashboard index must reference CSS');
