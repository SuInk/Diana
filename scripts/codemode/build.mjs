import { readFile, readdir, writeFile } from 'node:fs/promises';
import { gzipSync } from 'node:zlib';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = path.dirname(fileURLToPath(import.meta.url));
const files = {};
async function add(name, source = name) {
  files[name] = (await readFile(path.join(root, source))).toString('base64');
}
async function javascript(directory) {
  for (const entry of (await readdir(path.join(root, directory), { withFileTypes: true })).sort((a, b) => a.name < b.name ? -1 : 1)) {
    const name = `${directory}/${entry.name}`;
    if (entry.isDirectory()) await javascript(name);
    else if (entry.name.endsWith('.js')) await add(name);
  }
}
const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
for (const [name, version] of [['@earendil-works/pi-codemode', '1.4.0'], ['quickjs-wasi', '3.6.2']]) {
  const directory = `node_modules/${name}`;
  const manifest = JSON.parse(await readFile(path.join(root, directory, 'package.json'), 'utf8'));
  if (manifest.version !== version || lock.packages[directory]?.version !== version) throw new Error(`unexpected version: ${name}`);
  await add(`${directory}/package.json`);
  await javascript(`${directory}/dist`);
}
await add('host.mjs');
await add('node_modules/quickjs-wasi/quickjs.wasm');
await add('node_modules/quickjs-wasi/LICENSE');
await add('node_modules/@earendil-works/pi-codemode/LICENSE', 'licenses/pi-codemode-MIT.txt');
const sorted = Object.fromEntries(Object.entries(files).sort(([a], [b]) => a < b ? -1 : 1));
const archive = gzipSync(Buffer.from(JSON.stringify(sorted)), { level: 9 });
const destination = path.join(root, '../../internal/codemode/runtime.json.gz');
if (process.argv.includes('--check')) {
  if (!(await readFile(destination)).equals(archive)) throw new Error('embedded runtime is stale; run npm run build');
} else {
  await writeFile(destination, archive);
}
console.log(`${Object.keys(files).length} runtime files, ${archive.length} compressed bytes`);
