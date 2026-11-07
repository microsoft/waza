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

const html = readFileSync(join(root, 'index.html'), 'utf8').replace(/<!--[\s\S]*?-->/g, '');
const tags = [...html.matchAll(/<(script|link)\b([^>]*)>/gi)];
assert(tags.length === [...html.matchAll(/<(?:script|link)\b/gi)].length,
  'malformed release dashboard script/link tag');
const refs = [];
for (const tag of tags) {
  const attributes = new Map();
  let remaining = tag[2];
  while (remaining.trim() && remaining.trim() !== '/') {
    const attribute = remaining.match(/^\s+([a-z_:][\w:.-]*)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/i);
    assert(attribute, `malformed release dashboard attributes: ${tag[0]}`);
    const name = attribute[1].toLowerCase();
    assert(!attributes.has(name), `duplicate release dashboard attribute: ${name}`);
    attributes.set(name, attribute[2] ?? attribute[3] ?? attribute[4] ?? '');
    remaining = remaining.slice(attribute[0].length);
  }
  const rel = (attributes.get('rel') ?? '').toLowerCase().split(/\s+/);
  let reference;
  if (tag[1].toLowerCase() === 'script' && attributes.has('src')) {
    reference = attributes.get('src');
  } else if (tag[1].toLowerCase() === 'link') {
    const href = attributes.get('href') ?? '';
    if (rel.includes('stylesheet') || rel.includes('modulepreload') ||
      (rel.includes('preload') && ['script', 'style'].includes((attributes.get('as') ?? '').toLowerCase())) ||
      /^(?:\/|\.\/)?assets\//.test(href)) reference = href;
  }
  if (reference === undefined) continue;
  assert(/^(?:\/|\.\/)?assets\//.test(reference),
    `unsupported release dashboard asset reference: ${reference}`);
  const relative = decodeURIComponent(reference.split(/[?#]/, 1)[0]).replace(/^(?:\/|\.\/)/, '');
  assert(relative.startsWith('assets/'), `invalid release dashboard reference: ${reference}`);
  file(relative);
  refs.push(relative);
}
assert(refs.some((name) => name.endsWith('.js')), 'release dashboard index must reference JavaScript');
assert(refs.some((name) => name.endsWith('.css')), 'release dashboard index must reference CSS');
