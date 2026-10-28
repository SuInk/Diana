// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import 'dart:typed_data';

import 'package:diana_mobile/api/client.dart';
import 'package:diana_mobile/call/call_turns.dart';
import 'package:diana_mobile/chat_time.dart';
import 'package:flutter_test/flutter_test.dart';

int at(DateTime t) => t.millisecondsSinceEpoch ~/ 1000;

void main() {
  group('normalizeServer', () {
    test('没写协议按 http，去掉路径', () {
      expect(normalizeServer(' 192.168.1.10:8080/chat ').toString(), 'http://192.168.1.10:8080');
      expect(normalizeServer('https://diana.example.com/').toString(), 'https://diana.example.com');
    });
    test('空地址和别的协议报错', () {
      expect(() => normalizeServer(''), throwsFormatException);
      expect(() => normalizeServer('ftp://x'), throwsFormatException);
    });
  });

  group('时间分隔', () {
    final now = DateTime(2026, 10, 28, 15, 0);
    test('今天、昨天、今年、往年', () {
      expect(chatTimeLabel(at(DateTime(2026, 10, 28, 9, 5)), now), '09:05');
      expect(chatTimeLabel(at(DateTime(2026, 10, 27, 23, 59)), now), '昨天 23:59');
      expect(chatTimeLabel(at(DateTime(2026, 3, 1, 8, 0)), now), '3月1日 08:00');
      expect(chatTimeLabel(at(DateTime(2025, 12, 31, 8, 0)), now), '2025年12月31日 08:00');
      expect(listTimeLabel(at(DateTime(2026, 10, 27, 23, 59)), now), '昨天');
    });
    test('第一条和隔五分钟以上的插分隔', () {
      expect(timeDividers([0, 60, 360, 400]), [true, false, true, false]);
    });
  });

  group('通话字幕', () {
    test('转写、回复、结束折叠成一轮，merged 和空轮不显示', () {
      final turns = <CallTurn>[];
      applyCallMessage(turns, {'type': 'transcript', 'turn': 1, 'text': '你好'});
      applyCallMessage(turns, {'type': 'reply', 'turn': 1, 'seq': 1, 'text': '在呢。'});
      applyCallMessage(turns, {'type': 'reply', 'turn': 1, 'seq': 2, 'text': '怎么啦？'});
      applyCallMessage(turns, {'type': 'turn_end', 'turn': 1, 'reason': 'done'});
      applyCallMessage(turns, {'type': 'transcript', 'turn': 2, 'text': '嗯'});
      applyCallMessage(turns, {'type': 'turn_end', 'turn': 2, 'reason': 'merged'});
      applyCallMessage(turns, {'type': 'turn_end', 'turn': 3, 'reason': 'no_speech'});
      applyCallMessage(turns, {'type': 'state', 'state': 'listening'});
      expect(turns.length, 1);
      expect(turns.single.user, '你好');
      expect(turns.single.bot.join(), '在呢。怎么啦？');
      expect(turns.single.status, 'done');
    });

    test('音频按 audio_start/audio_end 拼整句，对不上号的残片丢掉', () {
      final collector = SentenceAudioCollector()..start(1, 1, 'audio/mpeg');
      collector.push([1, 2]);
      collector.push([3]);
      final sentence = collector.end(1, 1)!;
      expect(sentence.mime, 'audio/mpeg');
      expect(sentence.data, Uint8List.fromList([1, 2, 3]));
      collector
        ..start(1, 2, 'audio/mpeg')
        ..push([9])
        ..reset();
      expect(collector.end(1, 2), isNull);
    });

    test('mime 对应扩展名', () {
      expect(audioExtension('audio/mpeg'), 'mp3');
      expect(audioExtension('audio/wav; codecs=1'), 'wav');
      expect(audioExtension('audio/unknown'), 'mp3');
    });
  });
}
