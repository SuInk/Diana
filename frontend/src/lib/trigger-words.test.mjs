import assert from 'node:assert/strict';
import test from 'node:test';
import { parseTriggerWords, formatTriggerWords } from './trigger-words.ts';

test('splits the screenshot input and mixed pasted separators', () => {
  assert.deepEqual(parseTriggerWords('miku,Diana,嘉然 然然 diana'), ['miku', 'Diana', '嘉然', '然然', 'diana']);
  assert.deepEqual(parseTriggerWords(' miku，嘉然、然然；Diana;\tdiana\n嘉然　'), ['miku', '嘉然', '然然', 'Diana', 'diana']);
  assert.deepEqual(parseTriggerWords(' ,，、；;\n'), []);
});

test('quoted phrases and existing punctuation survive loading and saving', () => {
  assert.deepEqual(parseTriggerWords('“Hey Diana” "hello world",嘉然'), ['Hey Diana', 'hello world', '嘉然']);
  const words = ['Hey Diana', '你好，嘉然', 'say "hello"', '“嘉然”', 'a\\b', "don't", 'Diana', 'diana'];
  assert.deepEqual(parseTriggerWords(formatTriggerWords(words)), words);
});

test('incomplete quotes do not discard input', () => {
  assert.deepEqual(parseTriggerWords('"hello Diana'), ['"hello', 'Diana']);
});
