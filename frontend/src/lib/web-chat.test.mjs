import test from 'node:test';
import assert from 'node:assert/strict';
import {chatTimeLabel,readWebChatFrames,timeDividers} from './web-chat.ts';

const stream = (chunks) => new ReadableStream({start(c){for (const chunk of chunks) c.enqueue(new TextEncoder().encode(chunk)); c.close();}});

test('frames split across chunks are reassembled', async () => {
  const frames = [];
  await readWebChatFrames(stream(['{"type":"message","message":{"id":"a","role":"assi', 'stant","text":"在呢","at":1}}\n{"type":"heart', 'beat"}\n{"type":"done","replies":1}\n']), f => frames.push(f));
  assert.deepEqual(frames.map(f => f.type), ['message','heartbeat','done']);
  assert.equal(frames[0].message.text, '在呢');
});

test('missing done frame and server errors are failures', async () => {
  await assert.rejects(readWebChatFrames(stream(['{"type":"message","message":{"id":"a","role":"assistant","text":"x","at":1}}\n']), () => {}), /中断/);
  await assert.rejects(readWebChatFrames(stream(['{"type":"done","error":"回复失败：boom"}\n']), () => {}), /boom/);
});

test('time labels and dividers follow QQ conventions', () => {
  const now = new Date(2026, 9, 27, 20, 0);
  const at = (d) => Math.floor(d.getTime() / 1000);
  assert.equal(chatTimeLabel(at(new Date(2026, 9, 27, 9, 5)), now), '09:05');
  assert.equal(chatTimeLabel(at(new Date(2026, 9, 26, 23, 1)), now), '昨天 23:01');
  assert.equal(chatTimeLabel(at(new Date(2026, 2, 3, 8, 0)), now), '3月3日 08:00');
  assert.equal(chatTimeLabel(at(new Date(2025, 2, 3, 8, 0)), now), '2025年3月3日 08:00');
  assert.deepEqual(timeDividers([{at:0},{at:60},{at:400},{at:401}]), [true,false,true,false]);
});
