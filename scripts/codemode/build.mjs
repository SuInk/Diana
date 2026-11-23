import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';
import path from 'node:path';

// Copies what the Go runtime embeds from the integrity-locked pi-codemode
// package: the prelude that defines tools, store/load, console and the host
// bridge inside the moejs runtime.
const root = path.dirname(fileURLToPath(import.meta.url));
const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
for (const [name, version] of [['@earendil-works/pi-codemode', '1.4.0']]) {
  const directory = `node_modules/${name}`;
  const manifest = JSON.parse(await readFile(path.join(root, directory, 'package.json'), 'utf8'));
  if (manifest.version !== version || lock.packages[directory]?.version !== version) throw new Error(`unexpected version: ${name}`);
}
const { PRELUDE_SOURCE } = await import(pathToFileURL(path.join(root, 'node_modules/@earendil-works/pi-codemode/dist/runtime/prelude-source.js')).href);
const destination = path.join(root, '../../internal/codemode');
const files = {
  'prelude.js': Buffer.from(PRELUDE_SOURCE),
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
