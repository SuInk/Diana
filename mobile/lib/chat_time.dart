// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 聊天里的时间分隔，和网页聊天页（frontend/src/lib/web-chat.ts）一致。

/// 两条消息隔了多久要插一条时间分隔，和 QQ 一样五分钟。
const timeDividerGap = 5 * 60;

String _two(int n) => n.toString().padLeft(2, '0');

/// 今天只写时分，昨天写「昨天」，更早写月日，跨年再写年。
String chatTimeLabel(int at, [DateTime? now]) {
  now ??= DateTime.now();
  final date = DateTime.fromMillisecondsSinceEpoch(at * 1000);
  final hm = '${_two(date.hour)}:${_two(date.minute)}';
  // 跨夏令时那天不是整 24 小时，按天四舍五入。
  final diff = (DateTime(now.year, now.month, now.day).difference(DateTime(date.year, date.month, date.day)).inHours / 24).round();
  if (diff == 0) return hm;
  if (diff == 1) return '昨天 $hm';
  if (date.year == now.year) return '${date.month}月${date.day}日 $hm';
  return '${date.year}年${date.month}月${date.day}日 $hm';
}

/// 哪些消息前面要插时间分隔：第一条和隔了五分钟以上的。
List<bool> timeDividers(List<int> times) =>
    [for (var i = 0; i < times.length; i++) i == 0 || times[i] - times[i - 1] >= timeDividerGap];

/// 机器人列表里最后一条消息的时间：今天写时分，其他写日期。
String listTimeLabel(int at, [DateTime? now]) {
  final label = chatTimeLabel(at, now);
  return label.contains(' ') ? label.split(' ').first : label;
}
