import assert from 'node:assert/strict';
import test from 'node:test';
import { CodemodeSandbox } from '@earendil-works/pi-codemode';

async function withSandbox(options, run) {
  const workers = [];
  const sandbox = new CodemodeSandbox({
    timeoutMs: 2000,
    memoryLimitBytes: 32 * 1024 * 1024,
    ...options,
    onWorkerStarted: exited => workers.push(exited),
  });
  try {
    await run(sandbox);
  } finally {
    await sandbox.close();
    await Promise.all(workers);
  }
}

test('independent calls run concurrently and scripts return only selected data', { timeout: 10000 }, async () => {
  let active = 0;
  let peak = 0;
  const tools = [{
    name: 'read',
    execute: async ({ id }) => {
      active++;
      peak = Math.max(peak, active);
      await new Promise(resolve => setTimeout(resolve, 30));
      active--;
      return { id, title: `page-${id}`, unused: 'x'.repeat(10000) };
    },
  }];
  await withSandbox({ tools }, async sandbox => {
    const result = await sandbox.execute(`
      const pages = await Promise.all([1, 2].map(id => tools.read({ id })));
      return pages.map(page => page.title);
    `);
    assert.equal(result.ok, true);
    assert.deepEqual(result.value, ['page-1', 'page-2']);
    assert.equal(peak, 2);
    assert.equal(result.calls.length, 2);
    assert.ok(JSON.stringify(result).length < 1000);
  });
});

test('model scripts have no Node, network, module or host-constructor globals', { timeout: 10000 }, async () => {
  await withSandbox({}, async sandbox => {
    const result = await sandbox.execute(`return {
      process: typeof process, require: typeof require, fetch: typeof fetch,
      wasm: typeof WebAssembly, timer: typeof setTimeout,
      constructor: ({}).constructor.constructor('return typeof process')(),
    };`);
    assert.equal(result.ok, true);
    assert.deepEqual(Object.values(result.value), Array(6).fill('undefined'));
    const missing = await sandbox.execute('return await tools.not_registered({});');
    assert.equal(missing.ok, false);
  });
});

test('a runaway script times out without poisoning subsequent executions', { timeout: 10000 }, async () => {
  await withSandbox({ timeoutMs: 150 }, async sandbox => {
    const result = await sandbox.execute('while (true) {}');
    assert.equal(result.ok, false);
    assert.equal(result.error.kind, 'timeout');
    const next = await sandbox.execute('return 42;');
    assert.equal(next.ok, true);
    assert.equal(next.value, 42);
  });
});

test('VM allocations are bounded independently of the Node heap', { timeout: 10000 }, async () => {
  await withSandbox({ memoryLimitBytes: 8 * 1024 * 1024 }, async sandbox => {
    const result = await sandbox.execute('return new Array(4_000_000).fill("payload");');
    assert.equal(result.ok, false);
    const next = await sandbox.execute('return "alive";');
    assert.equal(next.ok, true);
    assert.equal(next.value, 'alive');
  });
});

test('cancellation reaches a pending host tool and terminates its VM', { timeout: 10000 }, async () => {
  let entered;
  const started = new Promise(resolve => { entered = resolve; });
  let aborted = false;
  const controller = new AbortController();
  await withSandbox({ tools: [{
    name: 'wait',
    execute: async (_, { signal }) => {
      entered();
      await new Promise(resolve => signal.addEventListener('abort', () => {
        aborted = true;
        resolve();
      }, { once: true }));
      return null;
    },
  }] }, async sandbox => {
    const pending = sandbox.execute('return await tools.wait({});', { signal: controller.signal });
    await started;
    controller.abort();
    const result = await pending;
    assert.equal(result.ok, false);
    assert.equal(result.error.kind, 'aborted');
    assert.equal(aborted, true);
  });
});
