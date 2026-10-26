import assert from 'node:assert/strict';
import {test} from 'node:test';
import {applyCallMessage,callSocketURL,downsample,floatToPCM16,SentenceAudioCollector} from './voice-call.ts';

test('call socket URL follows page protocol and encodes the bot', () => {
  assert.equal(callSocketURL('bot a',{protocol:'https:',host:'diana.example'}),'wss://diana.example/api/assistant/call/ws?profile=bot%20a');
  assert.equal(callSocketURL('',{protocol:'http:',host:'127.0.0.1:8080'}),'ws://127.0.0.1:8080/api/assistant/call/ws');
});

test('downsample averages 48k into 16k and converts to little-endian PCM16', () => {
  const input=new Float32Array([0.3,0.3,0.3,-0.6,-0.6,-0.6,1,1,1]);
  const out=downsample(input,48000);
  assert.equal(out.length,3);
  assert.ok(Math.abs(out[0]-0.3)<1e-6);
  const pcm=new DataView(floatToPCM16(new Float32Array([1,-1,0,2])));
  assert.equal(pcm.getInt16(0,true),32767);
  assert.equal(pcm.getInt16(2,true),-32768);
  assert.equal(pcm.getInt16(4,true),0);
  assert.equal(pcm.getInt16(6,true),32767);
});

test('call transcript folds turns and hides merged ones', () => {
  let turns=[];
  turns=applyCallMessage(turns,{type:'transcript',turn:1,text:'我想问'});
  turns=applyCallMessage(turns,{type:'turn_end',turn:1,reason:'merged'});
  turns=applyCallMessage(turns,{type:'transcript',turn:2,text:'我想问 明天天气'});
  turns=applyCallMessage(turns,{type:'reply',turn:2,seq:1,text:'明天晴。'});
  turns=applyCallMessage(turns,{type:'reply',turn:2,seq:2,text:'适合出门。'});
  turns=applyCallMessage(turns,{type:'turn_end',turn:2,reason:'interrupted'});
  assert.deepEqual(turns,[{turn:2,user:'我想问 明天天气',bot:['明天晴。','适合出门。'],status:'interrupted'}]);
});

test('sentence audio collector joins chunks and drops stale sentences', () => {
  const collector=new SentenceAudioCollector();
  collector.start(1,1,'audio/mpeg');
  collector.push(new Uint8Array([1,2]).buffer);
  collector.push(new Uint8Array([3]).buffer);
  const sentence=collector.end(1,1);
  assert.equal(sentence.mime,'audio/mpeg');
  assert.deepEqual([...new Uint8Array(sentence.data)],[1,2,3]);
  collector.start(1,2,'audio/mpeg');
  collector.reset();
  assert.equal(collector.end(1,2),null);
});
