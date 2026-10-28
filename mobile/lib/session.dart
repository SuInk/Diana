// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 登录状态的持久化：服务器地址、用户名和信任的证书指纹放普通存储，会话 Cookie
// 放系统钥匙串（iOS Keychain / Android Keystore）。密码不存。

import 'package:flutter/foundation.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'api/client.dart';

class Session extends ChangeNotifier {
  static const _serverKey = 'server';
  static const _usernameKey = 'username';
  static const _fingerprintKey = 'certificate_fingerprint';
  static const _cookieKey = 'session_cookie';
  static const _secure = FlutterSecureStorage();

  DianaClient? client;
  String lastServer = '';
  String lastUsername = '';

  bool get loggedIn => client?.cookie != null;

  Future<void> load() async {
    final prefs = await SharedPreferences.getInstance();
    lastServer = prefs.getString(_serverKey) ?? '';
    lastUsername = prefs.getString(_usernameKey) ?? '';
    pinnedCertificates.fingerprint = prefs.getString(_fingerprintKey);
    final cookie = await _secure.read(key: _cookieKey).catchError((_) => null);
    if (lastServer.isNotEmpty && cookie != null) {
      client = DianaClient(Uri.parse(lastServer), cookie: cookie);
    }
  }

  /// 用户确认信任这张证书。只认一张：换证书后会再问一次。
  Future<void> trustCertificate(String fingerprint) async {
    pinnedCertificates.fingerprint = fingerprint;
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(_fingerprintKey, fingerprint);
  }

  Future<void> login(Uri server, String username, String password) async {
    final next = DianaClient(server);
    await next.login(username, password);
    client?.close();
    client = next;
    lastServer = server.toString();
    lastUsername = username;
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(_serverKey, lastServer);
    await prefs.setString(_usernameKey, username);
    await _secure.write(key: _cookieKey, value: next.cookie);
    notifyListeners();
  }

  Future<void> logout() async {
    client?.close();
    client = null;
    await _secure.delete(key: _cookieKey).catchError((_) {});
    notifyListeners();
  }
}
