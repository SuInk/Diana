import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import test from 'node:test';

async function run(start, onFrame = () => {}, raw = false) {
  const child = spawn(process.execPath, [new URL('./host.mjs', import.meta.url).pathname], {
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const frames = [];
  let stderr = '';
  let failure;
  const deadline = setTimeout(() => child.kill('SIGKILL'), 5000);
  child.stderr.setEncoding('utf8').on('data', chunk => { stderr += chunk; });
  child.stdin.on('error', error => { if (error.code !== 'EPIPE') failure = error; });
  const lines = createInterface({ input: child.stdout });
  lines.on('line', line => {
    try {
      const frame = JSON.parse(line);
      frames.push(frame);
      onFrame(frame, reply => child.stdin.write(JSON.stringify(reply) + '\n'), child);
    } catch (error) {
      failure = error;
      child.kill('SIGKILL');
    }
  });
  const exited = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', (code, signal) => resolve({ code, signal }));
  });
  child.stdin.write(raw ? start : JSON.stringify({ type: 'start', tools: [], ...start }) + '\n');
  try {
    const exit = await exited;
    if (failure) throw failure;
    assert.deepEqual(exit, { code: 0, signal: null }, stderr);
    const results = frames.filter(frame => frame.type === 'result');
    assert.equal(results.length, 1, JSON.stringify(frames));
    return { result: results[0], frames };
  } finally {
    clearTimeout(deadline);
    lines.close();
    child.kill();
  }
}

test('sidecar bridges concurrent calls and only returns selected values', async () => {
  const calls = [];
  const { result } = await run({ tools: ['read'], code: `
    const data = await Promise.all([1, 2].map(id => tools.read({id})));
    return data.map(row => row.title);
  ` }, (frame, reply) => {
    if (frame.type !== 'call') return;
    calls.push(frame);
    if (calls.length === 2) {
      for (const call of calls.toReversed()) reply({
        type: 'tool_result', id: call.id, ok: true,
        value: { title: `page-${call.input.id}`, unused: 'x'.repeat(10000) },
      });
    }
  });
  assert.equal(result.ok, true);
  assert.deepEqual(result.value, ['page-1', 'page-2']);
  assert.ok(JSON.stringify(result).length < 1000);
});

test('sidecar forwards tool errors into script catch', async () => {
  const { result } = await run({ tools: ['read'], code: 'try { await tools.read({}); } catch (e) { return e.message; }' }, (frame, reply) => {
    if (frame.type === 'call') reply({ type: 'tool_result', id: frame.id, ok: false, error: 'permission denied' });
  });
  assert.equal(result.ok, true);
  assert.match(result.value, /permission denied/);
});

test('call budget cannot be bypassed by catching tool errors', async () => {
  const { result, frames } = await run({ tools: ['read'], max_calls: 0, code: 'try { await tools.read({}); } catch {} return "escaped";' });
  assert.equal(result.ok, false);
  assert.equal(result.error.kind, 'host_limit');
  assert.equal(frames.filter(frame => frame.type === 'call').length, 0);
});

test('oversized script result is rejected without invalid JSON truncation', async () => {
  const { result } = await run({ code: 'return "x".repeat(40000);' });
  assert.equal(result.ok, false);
  assert.equal(result.error.kind, 'output_limit');
});

test('deadline cancels outstanding host calls and exits the worker', async () => {
  const { result, frames } = await run({ tools: ['wait'], timeout_ms: 100, code: 'await tools.wait({});' });
  assert.equal(result.ok, false);
  assert.equal(result.error.kind, 'timeout');
  const call = frames.find(frame => frame.type === 'call');
  assert.ok(call);
  assert.ok(frames.some(frame => frame.type === 'cancel' && frame.id === call.id));
});

test('malformed protocol and reserved names fail closed', async () => {
  for (const start of ['not json\n', '{"type":"wrong"}\n']) {
    assert.equal((await run(start, undefined, true)).result.ok, false);
  }
  const { result } = await run({ tools: ['constructor'], code: 'return 1;' });
  assert.equal(result.ok, false);
});

test('unterminated oversized frame is rejected before EOF', async () => {
  const { result } = await run('x'.repeat(1024 * 1024 + 1), undefined, true);
  assert.equal(result.ok, false);
});

test('parent EOF cancels a pending tool call', async () => {
  const { result, frames } = await run({ tools: ['wait'], code: 'await tools.wait({});' }, (frame, _, child) => {
    if (frame.type === 'call') child.stdin.end();
  });
  assert.equal(result.ok, false);
  assert.ok(frames.some(frame => frame.type === 'cancel'));
});

test('bounded store survives a fresh process and supports deletion', async () => {
  const first = await run({ code: `store('page', {text:'kept outside the model output'}); return 'saved';` });
  assert.equal(first.result.ok, true);
  assert.deepEqual(first.result.store, { page: { text: 'kept outside the model output' } });
  const second = await run({ store: first.result.store, code: `const page=load('page'); store('page', undefined); return page.text;` });
  assert.equal(second.result.value, 'kept outside the model output');
  assert.deepEqual(second.result.store, {});
  const isolated = await run({ code: `return typeof load('page');` });
  assert.equal(isolated.result.value, 'undefined');
});

test('failed or oversized store writes never commit', async () => {
  for (const code of [`store('x',1); throw Error('failed');`, `store('x','a'.repeat(524288)); return 'ok';`]) {
    const { result } = await run({ store: { old: 42 }, code });
    assert.equal(result.ok, false);
    assert.equal(result.store, undefined);
  }
});
