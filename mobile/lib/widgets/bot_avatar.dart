// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import 'package:flutter/material.dart';

/// 圆形头像：有图用图（带登录 Cookie），没图或加载失败用名字首字。
class BotAvatar extends StatelessWidget {
  const BotAvatar({super.key, required this.name, this.url, this.headers, this.size = 40, this.color});

  final String name;
  final String? url;
  final Map<String, String>? headers;
  final double size;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final initial = name.isEmpty ? '?' : String.fromCharCodes(name.runes.take(1));
    final fallback = Container(
      width: size,
      height: size,
      alignment: Alignment.center,
      decoration: BoxDecoration(shape: BoxShape.circle, color: color ?? _colorFor(name)),
      child: Text(initial, style: TextStyle(color: Colors.white, fontSize: size * 0.42, fontWeight: FontWeight.w600)),
    );
    if (url == null) return fallback;
    return ClipOval(
      child: Image.network(
        url!,
        headers: headers,
        width: size,
        height: size,
        fit: BoxFit.cover,
        errorBuilder: (_, _, _) => fallback,
      ),
    );
  }

  static const _palette = [Color(0xFF5B8FF9), Color(0xFF61DDAA), Color(0xFFF6BD16), Color(0xFF7262FD), Color(0xFFF08BB4), Color(0xFF78D3F8)];

  static Color _colorFor(String name) => _palette[name.codeUnits.fold(0, (a, b) => a + b) % _palette.length];
}
