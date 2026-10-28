// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 和服务端对接的数据结构，字段名照 docs/voice-call.md。

class Bot {
  Bot({required this.id, required this.name, required this.platform, this.avatar});

  final String id;
  final String name;
  final String platform;
  final String? avatar;

  factory Bot.fromJson(Map<String, dynamic> json) {
    final id = (json['id'] ?? '') as String;
    final name = (json['name'] ?? '') as String;
    final avatar = (json['avatar_url'] ?? '') as String;
    return Bot(
      id: id,
      name: name.isEmpty ? id : name,
      platform: (json['platform'] ?? '') as String,
      avatar: avatar.isEmpty ? null : avatar,
    );
  }
}

class ChatInfo {
  ChatInfo({required this.botId, required this.botName, required this.platform, this.problem});

  final String botId;
  final String botName;
  final String platform;
  final String? problem;

  factory ChatInfo.fromJson(Map<String, dynamic> json) => ChatInfo(
        botId: (json['bot_id'] ?? '') as String,
        botName: (json['bot_name'] ?? '') as String,
        platform: (json['platform'] ?? '') as String,
        problem: _nonEmpty(json['problem']),
      );
}

class ChatMessage {
  ChatMessage({
    required this.id,
    required this.role,
    required this.text,
    required this.at,
    this.images = const [],
    this.pending = false,
    this.failed = false,
  });

  final String id;

  /// `user` 或 `assistant`。
  final String role;
  final String text;
  final List<String> images;

  /// Unix 秒。
  final int at;

  /// 本地刚发出、还没被服务端确认的那句。
  bool pending;
  bool failed;

  bool get mine => role == 'user';

  factory ChatMessage.fromJson(Map<String, dynamic> json) => ChatMessage(
        id: (json['id'] ?? '') as String,
        role: (json['role'] ?? 'assistant') as String,
        text: (json['text'] ?? '') as String,
        at: (json['at'] as num?)?.toInt() ?? 0,
        images: ((json['images'] as List?) ?? const []).whereType<String>().toList(),
      );
}

class CallInfo {
  CallInfo({required this.botName, required this.stt, required this.tts, this.problem});

  final String botName;
  final bool stt;
  final bool tts;
  final String? problem;

  factory CallInfo.fromJson(Map<String, dynamic> json) => CallInfo(
        botName: (json['bot_name'] ?? '') as String,
        stt: json['stt'] == true,
        tts: json['tts'] == true,
        problem: _nonEmpty(json['problem']),
      );
}

String? _nonEmpty(Object? value) => value is String && value.isNotEmpty ? value : null;
