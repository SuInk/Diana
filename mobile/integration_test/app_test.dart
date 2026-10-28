// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 对着真实的 Diana 服务端在模拟器上跑一遍：登录、机器人列表、发消息收回复、打电话
// 收到语音。服务器和账号从 --dart-define 传，不写进仓库：
//
//   flutter test integration_test -d <设备> \
//     --dart-define=DIANA_SERVER=http://127.0.0.1:18311 \
//     --dart-define=DIANA_USER=... --dart-define=DIANA_PASSWORD=...
//
// 安卓模拟器里本机地址是 10.0.2.2。通话要先给 App 授权麦克风，否则权限弹窗会卡住。

import 'dart:io';

import 'package:diana_mobile/api/client.dart';
import 'package:diana_mobile/call/call_controller.dart';
import 'package:diana_mobile/main.dart';
import 'package:diana_mobile/pages/chat_page.dart';
import 'package:diana_mobile/session.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';

const server = String.fromEnvironment('DIANA_SERVER');
const user = String.fromEnvironment('DIANA_USER');
const password = String.fromEnvironment('DIANA_PASSWORD');

Future<void> pumpUntil(WidgetTester tester, bool Function() done, {Duration timeout = const Duration(seconds: 90)}) async {
  final deadline = DateTime.now().add(timeout);
  while (!done()) {
    if (DateTime.now().isAfter(deadline)) throw TestFailure('等了 ${timeout.inSeconds} 秒还没等到');
    await tester.pump(const Duration(milliseconds: 250));
  }
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();
  HttpOverrides.global = pinnedCertificates;

  testWidgets('登录、看机器人、发消息收回复', (tester) async {
    final session = Session();
    await session.load();
    await session.logout();
    await tester.pumpWidget(DianaApp(session: session));
    await tester.pumpAndSettle();

    await tester.enterText(find.widgetWithText(TextField, '服务器地址'), server);
    await tester.enterText(find.widgetWithText(TextField, '账号'), user);
    await tester.enterText(find.widgetWithText(TextField, '密码'), password);
    await tester.tap(find.widgetWithText(FilledButton, '登录'));
    await pumpUntil(tester, () => find.byType(ListTile).evaluate().isNotEmpty);
    expect(session.loggedIn, isTrue);

    await tester.tap(find.byType(ListTile).first);
    await pumpUntil(tester, () => find.byType(ChatPage).evaluate().isNotEmpty && find.byType(CircularProgressIndicator).evaluate().isEmpty);
    final client = session.client!;
    final profile = (await client.bots()).first.id;
    // 记录最多返回最近 200 条，按条数比会被挤掉的老消息骗；用带时间的一句话认自己发的。
    final text = '在吗？用一句话回我（${DateTime.now().millisecondsSinceEpoch % 100000}）';

    await tester.enterText(find.byType(TextField).last, text);
    await tester.pump();
    await tester.tap(find.widgetWithText(FilledButton, '发送'));
    await pumpUntil(tester, () => find.text('对方正在输入…').evaluate().isNotEmpty, timeout: const Duration(seconds: 10));
    await pumpUntil(tester, () => find.text('对方正在输入…').evaluate().isEmpty, timeout: const Duration(seconds: 120));
    final messages = (await client.chat(profile)).$2;
    final mine = messages.lastIndexWhere((m) => m.mine && m.text == text);
    expect(mine, isNot(-1), reason: '自己发的这句应该进了私聊记录');
    expect(messages.skip(mine + 1).any((m) => !m.mine), isTrue, reason: '机器人应该回了至少一条');
    expect(find.text(text), findsOneWidget);
  });

  testWidgets('打电话：接通、打字说一句、收到语音', (tester) async {
    final session = Session();
    await session.load();
    if (!session.loggedIn) await session.login(normalizeServer(server), user, password);
    final client = session.client!;
    final profile = (await client.bots()).first.id;

    final call = CallController(client, profile);
    await call.dial();
    await pumpUntil(tester, () => call.inCall || call.ended, timeout: const Duration(seconds: 20));
    expect(call.problem, isNull);
    expect(call.inCall, isTrue);

    call.sendText('你好，听得到吗');
    await pumpUntil(tester, () => call.audioSentences > 0 || call.ended, timeout: const Duration(seconds: 120));
    expect(call.problem, isNull);
    expect(call.audioSentences, greaterThan(0));
    expect(call.turns.any((t) => t.bot.isNotEmpty), isTrue);
    call.hangup();
    call.dispose();
  });
}
