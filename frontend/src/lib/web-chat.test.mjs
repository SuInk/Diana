import test from 'node:test';
import assert from 'node:assert/strict';
import {callClock,callRecordClock,chatTimeLabel,groupCallTurns,mergeWebChatHistory,readWebChatFrames,timeDividers} from './web-chat.ts';

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

test('call turns fold under their record, live call turns stay apart', () => {
  const m = (id, extra = {}) => ({id, role: 'user', text: id, at: 1, ...extra});
  const {entries, live} = groupCallTurns([
    m('a'),
    m('call-x-1', {call_id: 'x'}),
    m('call-x-out-1', {call_id: 'x', role: 'assistant'}),
    m('qq', {}),
    m('local-call-x', {kind: 'call', call_id: 'x'}),
    m('call-old-1', {call_id: 'old'}),
    m('call-y-1', {call_id: 'y'})
  ], 'y');
  assert.deepEqual(entries.map(e => e.message.id), ['a', 'qq', 'local-call-x', 'call-old-1']);
  assert.deepEqual(entries[2].turns.map(t => t.id), ['call-x-1', 'call-x-out-1']);
  assert.deepEqual(live.map(t => t.id), ['call-y-1']);
});

test('call clock and history merge', () => {
  assert.equal(callClock(75), '01:15');
  assert.equal(callClock(3725), '1:02:05');
  const failed = {id: 'local-1', role: 'user', text: 'x', at: 2, failed: true};
  assert.deepEqual(mergeWebChatHistory([{id: 's', role: 'user', text: 's', at: 1}], [failed]).map(m => m.id), ['s', 'local-1']);
});

test('call record text becomes a QQ-style clock', () => {
  assert.equal(callRecordClock('[语音通话] 通话 3 分 12 秒'), '03:12');
  assert.equal(callRecordClock('[语音通话] 通话 8 秒'), '00:08');
  assert.equal(callRecordClock('[语音通话] 通话 1 小时 5 分'), '1:05:00');
  assert.equal(callRecordClock('[语音通话] 通话 0 分 0 秒'), '00:00');
  assert.equal(callRecordClock('随便一句话'), '');
});
