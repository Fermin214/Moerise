import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const source = fileURLToPath(new URL('./src/', import.meta.url));
const output = fileURLToPath(new URL('./dist/', import.meta.url));
await rm(output, { recursive: true, force: true });
await mkdir(output, { recursive: true });
await cp(source, output, { recursive: true });
// Compile the prototype's static style attributes for the same-origin CSP.
const rules = [];
const html = (await readFile(`${output}/index.html`, 'utf8')).replace(/<[^>]+\sstyle="[^"]*"[^>]*>/g, tag => {
  const style = /\sstyle="([^"]*)"/.exec(tag)[1];
  const name = `layout-${rules.length}`;
  rules.push(`#app .${name}{${style}}`);
  const cleaned = tag.replace(/\sstyle="[^"]*"/, '');
  return /\sclass="/.test(cleaned)
    ? cleaned.replace(/\sclass="([^"]*)"/, ` class="$1 ${name}"`)
    : cleaned.replace(/^<([^\s>]+)/, `<$1 class="${name}"`);
});
await writeFile(`${output}/layout.css`, rules.join('\n') + '\n');
await writeFile(`${output}/index.html`, html.replace('</head>', '  <link rel="stylesheet" href="/layout.css">\n</head>'));
console.log('Web built: web/dist');
