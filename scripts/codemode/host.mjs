import { CodemodeSandbox } from '@earendil-works/pi-codemode';

// This trusted host is a one-execution JSONL sidecar. Model code only enters
// QuickJS; tools are RPC requests to the Go parent, never local implementations.
const MAX_FRAME_BYTES = 1024 * 1024;
const MAX_ARGUMENT_BYTES = 64 * 1024;
const MAX_OUTPUT_BYTES = 32 * 1024;
const MAX_STORE_BYTES = 512 * 1024;
const pending = new Map();
const controller = new AbortController();
let inputBuffer = Buffer.alloc(0);
let started = false;
let finished = false;
let stopReason = '';
let nextID = 0;

function emit(frame) {
  process.stdout.write(JSON.stringify(frame) + '\n');
}

function stop(reason) {
  stopReason ||= reason;
  controller.abort();
}

function integer(value, fallback, min, max) {
  if (value === undefined) return fallback;
  if (!Number.isSafeInteger(value) || value < min || value > max) {
    throw new Error(`limit must be an integer between ${min} and ${max}`);
  }
  return value;
}

function validateStart(frame) {
  if (frame.type !== 'start' || typeof frame.code !== 'string' ||
      Buffer.byteLength(frame.code) > 64 * 1024 || !Array.isArray(frame.tools) ||
      frame.tools.length > 1024) throw new Error('invalid start frame');
  if (frame.store !== undefined && (frame.store === null || typeof frame.store !== 'object' ||
      Array.isArray(frame.store) || Buffer.byteLength(JSON.stringify(frame.store)) > MAX_STORE_BYTES)) {
    throw new Error('invalid or oversized store');
  }
  const seen = new Set();
  for (const name of frame.tools) {
    if (typeof name !== 'string' || name.length === 0 || name.length > 128 ||
        ['__proto__', 'constructor', 'prototype'].includes(name) || seen.has(name)) {
      throw new Error('invalid or duplicate tool name');
    }
    seen.add(name);
  }
  return {
    maxCalls: integer(frame.max_calls, 32, 0, 32),
    timeoutMs: integer(frame.timeout_ms, 30000, 1, 120000),
  };
}

function callParent(name, args, signal, maxCalls) {
  if (signal.aborted) return Promise.reject(new Error('execution stopped'));
  if (++nextID > maxCalls) {
    stop('script tool-call budget exceeded');
    return Promise.reject(new Error(stopReason));
  }
  if (args === null || typeof args !== 'object' || Array.isArray(args) ||
      Buffer.byteLength(JSON.stringify(args)) > MAX_ARGUMENT_BYTES) {
    return Promise.reject(new Error('tool arguments must be a bounded JSON object'));
  }
  const id = nextID;
  return new Promise((resolve, reject) => {
    const onAbort = () => {
      if (!pending.delete(id)) return;
      emit({ type: 'cancel', id });
      reject(new Error('execution stopped'));
    };
    signal.addEventListener('abort', onAbort, { once: true });
    pending.set(id, {
      resolve, reject,
      cleanup: () => signal.removeEventListener('abort', onAbort),
    });
    emit({ type: 'call', id, name, input: args });
  });
}

function boundedResult(result, previousStore) {
  const output = result.output.map(item => item.type === 'text'
    ? { type: 'text', text: item.text }
    : { type: 'text', text: '[image omitted: script output is text only]' });
  const value = result.ok
    ? { ok: true, value: result.value, output }
    : { ok: false, error: result.error, output };
  if (stopReason) return { ok: false, error: { kind: 'host_limit', message: stopReason } };
  // Never split serialized JSON or forward the sandbox's store as model output.
  if (Buffer.byteLength(JSON.stringify(value)) > MAX_OUTPUT_BYTES) {
    return { ok: false, error: { kind: 'output_limit', message: 'script output exceeds 32768 bytes; select fewer fields or rows' } };
  }
  if (result.ok) {
    const store = Object.assign(Object.create(null), previousStore);
    for (const key of result.storeWrites.delete) delete store[key];
    for (const [key, data] of Object.entries(result.storeWrites.set)) store[key] = data;
    if (Buffer.byteLength(JSON.stringify(store)) > MAX_STORE_BYTES) {
      return { ok: false, error: { kind: 'store_limit', message: 'store exceeds 524288 bytes; delete unused keys with store(key, undefined)' } };
    }
    // Transport-only state. The Go bridge removes it before returning to the model.
    value.store = store;
  }
  return value;
}

async function execute(frame) {
  let sandbox;
  const workers = [];
  try {
    const { maxCalls, timeoutMs } = validateStart(frame);
    sandbox = new CodemodeSandbox({
      memoryLimitBytes: 32 * 1024 * 1024,
      timeoutMs,
      onWorkerStarted: exited => workers.push(exited),
      tools: frame.tools.map(name => ({
        name,
        execute: (args, { signal }) => callParent(name, args, signal, maxCalls),
      })),
    });
    const result = await sandbox.execute(frame.code, { signal: controller.signal, store: frame.store });
    finished = true;
    emit({ type: 'result', ...boundedResult(result, frame.store) });
  } catch (error) {
    finished = true;
    emit({ type: 'result', ok: false, error: { kind: 'host', message: String(error.message || error).slice(0, 2048) } });
  } finally {
    if (sandbox) await sandbox.close();
    await Promise.all(workers);
    process.stdin.destroy();
  }
}

function receiveLine(line) {
  if (finished) return;
  try {
    if (Buffer.byteLength(line) > MAX_FRAME_BYTES) throw new Error('parent frame too large');
    const frame = JSON.parse(line);
    if (!started) {
      started = true;
      void execute(frame);
      return;
    }
    if (frame.type !== 'tool_result' || !Number.isSafeInteger(frame.id) || typeof frame.ok !== 'boolean') {
      throw new Error('invalid tool result frame');
    }
    const call = pending.get(frame.id);
    if (!call) return; // A cancelled tool may finish after the script has moved on.
    pending.delete(frame.id);
    call.cleanup();
    if (frame.ok) call.resolve(frame.value);
    else call.reject(new Error(String(frame.error || 'tool failed').slice(0, 4096)));
  } catch (error) {
    stop(String(error.message || error));
    if (!started) {
      started = true;
      void execute({});
    }
  }
}

// Bound the frame before decoding or waiting for a newline. readline would
// otherwise accumulate an arbitrarily large unterminated frame in the host.
process.stdin.on('data', chunk => {
  if (finished) return;
  let offset = 0;
  while (offset < chunk.length) {
    const newline = chunk.indexOf(10, offset);
    const end = newline < 0 ? chunk.length : newline;
    const part = chunk.subarray(offset, end);
    if (inputBuffer.length + part.length > MAX_FRAME_BYTES) {
      stop('parent frame too large');
      inputBuffer = Buffer.alloc(0);
      process.stdin.pause();
      if (!started) {
        started = true;
        void execute({});
      }
      return;
    }
    inputBuffer = Buffer.concat([inputBuffer, part]);
    if (newline < 0) return;
    receiveLine(inputBuffer.toString('utf8'));
    inputBuffer = Buffer.alloc(0);
    if (finished) return;
    offset = newline + 1;
  }
});
process.stdin.on('end', () => {
  if (finished) return;
  stop('parent input closed');
  if (!started) {
    started = true;
    void execute({});
  }
});
process.stdin.on('error', () => stop('parent input failed'));
process.on('SIGTERM', () => stop('parent cancelled execution'));
process.on('SIGINT', () => stop('parent cancelled execution'));
