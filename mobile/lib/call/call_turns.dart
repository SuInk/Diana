// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 通话下行事件折叠成字幕，和网页版 applyCallMessage 一致。和音频、界面都无关，能单测。

import 'dart:typed_data';

class CallTurn {
  CallTurn(this.turn);

  final int turn;
  String user = '';
  final List<String> bot = [];

  /// active / done / interrupted / merged / failed / empty。
  String status = 'active';
}

const _endStatus = {
  'done': 'done',
  'interrupted': 'interrupted',
  'merged': 'merged',
  'failed': 'failed',
  'no_speech': 'empty',
  'no_reply': 'empty',
};

/// 把一条下行事件并进对话记录。被并进下一句的那一轮（merged）不单独显示：它的文字
/// 会出现在下一轮的转写里。
void applyCallMessage(List<CallTurn> turns, Map<String, dynamic> msg) {
  final turn = (msg['turn'] as num?)?.toInt() ?? 0;
  if (turn == 0) return;
  final type = msg['type'];
  if (type != 'transcript' && type != 'reply' && type != 'turn_end') return;
  var current = turns.where((t) => t.turn == turn).firstOrNull;
  if (current == null) {
    current = CallTurn(turn);
    turns.add(current);
  }
  switch (type) {
    case 'transcript':
      current.user = (msg['text'] ?? '') as String;
    case 'reply':
      current.bot.add((msg['text'] ?? '') as String);
    case 'turn_end':
      current.status = _endStatus[msg['reason']] ?? 'done';
  }
  turns.removeWhere((t) => t.status == 'merged' || (t.status == 'empty' && t.user.isEmpty && t.bot.isEmpty));
}

/// 逐句的音频收集：audio_start 开始收，audio_end 交出整句。
class SentenceAudioCollector {
  final _parts = BytesBuilder(copy: false);
  String _mime = '';
  String _key = '';

  void start(int turn, int seq, String mime) {
    _parts.clear();
    _mime = mime;
    _key = '$turn.$seq';
  }

  void push(List<int> chunk) {
    if (_key.isNotEmpty) _parts.add(chunk);
  }

  /// 交出收齐的一句；对不上号（被打断后的残片）返回 null。
  ({String mime, Uint8List data})? end(int turn, int seq) {
    if (_key != '$turn.$seq') return null;
    final out = (mime: _mime, data: _parts.takeBytes());
    reset();
    return out;
  }

  void reset() {
    _parts.clear();
    _key = '';
  }
}

/// 下行音频按 mime 存临时文件时用的扩展名，播放器靠它认格式。
String audioExtension(String mime) {
  final base = mime.split(';').first.trim().toLowerCase();
  return switch (base) {
    'audio/mpeg' || 'audio/mp3' => 'mp3',
    'audio/wav' || 'audio/x-wav' || 'audio/wave' => 'wav',
    'audio/ogg' || 'audio/opus' => 'ogg',
    'audio/aac' => 'aac',
    'audio/mp4' || 'audio/m4a' || 'audio/x-m4a' => 'm4a',
    'audio/flac' => 'flac',
    'audio/webm' => 'webm',
    _ => 'mp3',
  };
}
