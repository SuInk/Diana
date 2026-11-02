import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';
import path from 'node:path';

// Copies the two pieces the Go runtime embeds from integrity-locked npm packages:
// the QuickJS-NG WASM build and the pi-codemode prelude that defines tools,
// store/load, console and the host bridge inside the VM.
const root = path.dirname(fileURLToPath(import.meta.url));
const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
for (const [name, version] of [['@earendil-works/pi-codemode', '1.4.0'], ['quickjs-wasi', '3.6.2']]) {
  const directory = `node_modules/${name}`;
  const manifest = JSON.parse(await readFile(path.join(root, directory, 'package.json'), 'utf8'));
  if (manifest.version !== version || lock.packages[directory]?.version !== version) throw new Error(`unexpected version: ${name}`);
}
const { PRELUDE_SOURCE } = await import(pathToFileURL(path.join(root, 'node_modules/@earendil-works/pi-codemode/dist/runtime/prelude-source.js')).href);
const destination = path.join(root, '../../internal/codemode');
const files = {
  'quickjs.wasm': await readFile(path.join(root, 'node_modules/quickjs-wasi/quickjs.wasm')),
  'prelude.js': Buffer.from(PRELUDE_SOURCE),
  'licenses/quickjs-wasi-MIT.txt': await readFile(path.join(root, 'node_modules/quickjs-wasi/LICENSE')),
  'licenses/pi-codemode-MIT.txt': await readFile(path.join(root, 'licenses/pi-codemode-MIT.txt')),
};
for (const [name, data] of Object.entries(files)) {
  const target = path.join(destination, name);
  if (process.argv.includes('--check')) {
    if (!(await readFile(target)).equals(data)) throw new Error(`internal/codemode/${name} is stale; run npm run build`);
  } else {
    await writeFile(target, data);
  }
}
console.log(`${Object.keys(files).length} embedded Code Mode files ${process.argv.includes('--check') ? 'verified' : 'written'}`);
