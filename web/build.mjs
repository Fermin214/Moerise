import { cp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { brotliCompress, constants, gzip } from 'node:zlib';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const source = fileURLToPath(new URL('./src/', import.meta.url));
const output = fileURLToPath(new URL('./dist/', import.meta.url));
await rm(output, { recursive: true, force: true });
await mkdir(output, { recursive: true });
await cp(source, output, { recursive: true });
// Compile static inline styles for the same-origin CSP.
const rules = [];
let html = (await readFile(`${output}/index.html`, 'utf8')).replace(/<[^>]+\sstyle="[^"]*"[^>]*>/g, tag => {
  const style = /\sstyle="([^"]*)"/.exec(tag)[1];
  const name = `layout-${rules.length}`;
  rules.push(`#app .${name}{${style}}`);
  const cleaned = tag.replace(/\sstyle="[^"]*"/, '');
  return /\sclass="/.test(cleaned)
    ? cleaned.replace(/\sclass="([^"]*)"/, ` class="$1 ${name}"`)
    : cleaned.replace(/^<([^\s>]+)/, `<$1 class="${name}"`);
});
await writeFile(`${output}/layout.css`, rules.join('\n') + '\n');
const modules = new Map();
for (const name of (await readdir(source)).filter(name => name.endsWith('.js')).sort()) {
  modules.set(name, await readFile(`${source}/${name}`, 'utf8'));
}
const styles = await readFile(`${source}/styles.css`, 'utf8');
const combined = [];
for (const match of styles.matchAll(/^@import url\('\/([a-z-]+\.css)'\);$/gm)) {
  combined.push(await readFile(`${source}/${match[1]}`, 'utf8'));
}
combined.push(styles.replace(/^@import url\('\/[a-z-]+\.css'\);\n?/gm, ''), rules.join('\n'));
const contents = new Map([
  ['index.html', html], ['styles.css', combined.join('\n') + '\n'],
  ...modules, ['favicon.svg', await readFile(`${source}/favicon.svg`, 'utf8')],
]);
const hash = createHash('sha256');
hash.update(await readFile(fileURLToPath(import.meta.url)));
for (const [name, body] of contents) hash.update(name + '\0' + body + '\0');
const version = hash.digest('hex').slice(0, 20);
const versioned = name => name.replace(/(\.[a-z]+)$/, `.${version}$1`);
const preload = new Set();
function dependencies(name) {
  for (const match of modules.get(name).matchAll(/\bfrom\s+['"]\.\/([a-z][a-z0-9-]*\.js)['"]/g)) {
    if (preload.has(match[1])) continue;
    preload.add(match[1]); dependencies(match[1]);
  }
}
dependencies('app.js');
html = html.replace(/\b(href|src)="\/([^"/]+)"/g, (match, attribute, name) =>
  contents.has(name) ? `${attribute}="/${versioned(name)}"` : match);
html = html.replace('</head>', [...preload].map(name =>
  `  <link rel="modulepreload" href="/${versioned(name)}">`).join('\n') + '\n</head>');
contents.set('index.html', html);
const compressBrotli = promisify(brotliCompress), compressGzip = promisify(gzip);
await Promise.all([...contents].map(async ([name, content]) => {
  if (name.endsWith('.js')) {
    content = content.replace(/(['"])\.\/([a-z][a-z0-9-]*\.js)\1/g,
      (_, quote, dependency) => `${quote}./${versioned(dependency)}${quote}`);
  }
  const body = Buffer.from(content);
  const [br, gz] = await Promise.all([
    compressBrotli(body, { params: { [constants.BROTLI_PARAM_QUALITY]: 11 } }),
    compressGzip(body, { level: 9 }),
  ]);
  const names = name === 'index.html' ? [name] : [name, versioned(name)];
  await Promise.all(names.flatMap(file => [
    writeFile(`${output}/${file}`, body), writeFile(`${output}/${file}.br`, br), writeFile(`${output}/${file}.gz`, gz),
  ]));
}));
console.log('Web built: web/dist');
