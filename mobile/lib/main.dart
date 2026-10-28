// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Diana 手机客户端：登录 WebUI 管理员账号后和机器人私聊、语音通话。接口和网页聊天页
// 是同一套（docs/voice-call.md），服务端不区分客户端。

import 'dart:io';

import 'package:flutter/material.dart';

import 'api/client.dart';
import 'pages/bot_list_page.dart';
import 'pages/login_page.dart';
import 'session.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  HttpOverrides.global = pinnedCertificates;
  final session = Session();
  await session.load();
  runApp(DianaApp(session: session));
}

/// QQ 的主色。
const qqBlue = Color(0xFF12B7F5);

class DianaApp extends StatelessWidget {
  const DianaApp({super.key, required this.session});

  final Session session;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Diana',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: qqBlue, primary: qqBlue),
        scaffoldBackgroundColor: const Color(0xFFF2F3F5),
        appBarTheme: const AppBarTheme(backgroundColor: Color(0xFFF2F3F5), surfaceTintColor: Colors.transparent, centerTitle: true),
      ),
      home: ListenableBuilder(
        listenable: session,
        builder: (context, _) => session.loggedIn ? BotListPage(session: session) : LoginPage(session: session),
      ),
    );
  }
}
