// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Diana 服务端的 HTTP 和 WebSocket 客户端。鉴权和 WebUI 一样靠登录 Cookie：
// 登录拿到的 Cookie 手动带在每个请求和通话的 WebSocket 握手上。原生 WebSocket
// 不带 Origin 头，服务端的同源检查直接放行，不用改服务端。

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';

import 'models.dart';

class ApiException implements Exception {
  ApiException(this.message, {this.unauthorized = false});

  final String message;

  /// 登录过期或没登录，应该回登录页。
  final bool unauthorized;

  @override
  String toString() => message;
}

/// 服务器用的是系统不认的证书（自签、内网 CA）。界面拿指纹问用户信不信。
class UntrustedCertificateException implements Exception {
  UntrustedCertificateException(this.host, this.fingerprint);

  final String host;
  final String fingerprint;

  @override
  String toString() => '$host 的证书不受系统信任';
}

String certificateFingerprint(X509Certificate cert) =>
    sha256.convert(cert.der).bytes.map((b) => b.toRadixString(16).padLeft(2, '0').toUpperCase()).join(':');

/// 让本进程所有 HttpClient（包括图片加载）都认用户确认过的那张证书。系统认的证书
/// 照常走系统校验，这里只管系统不认的。
class PinnedCertificateOverrides extends HttpOverrides {
  String? fingerprint;

  @override
  HttpClient createHttpClient(SecurityContext? context) {
    final client = super.createHttpClient(context);
    client.badCertificateCallback = (cert, host, port) => fingerprint != null && certificateFingerprint(cert) == fingerprint;
    return client;
  }
}

final pinnedCertificates = PinnedCertificateOverrides();

/// 用户填的服务器地址整理成 `scheme://host:port`：没写协议按 http，去掉末尾的路径。
Uri normalizeServer(String input) {
  var text = input.trim();
  if (text.isEmpty) throw const FormatException('先填服务器地址');
  if (!text.contains('://')) text = 'http://$text';
  final uri = Uri.parse(text);
  if (uri.host.isEmpty || (uri.scheme != 'http' && uri.scheme != 'https')) {
    throw const FormatException('服务器地址不对，形如 http://192.168.1.10:8080');
  }
  return Uri(scheme: uri.scheme, host: uri.host, port: uri.hasPort ? uri.port : null);
}

/// 握手失败时取服务器证书的指纹。只读证书，不信任它。
Future<String?> probeCertificate(Uri server) async {
  X509Certificate? seen;
  try {
    final socket = await SecureSocket.connect(server.host, server.port, timeout: const Duration(seconds: 8), onBadCertificate: (cert) {
      seen = cert;
      return false;
    });
    seen ??= socket.peerCertificate;
    socket.destroy();
  } catch (_) {
    // 握手按预期被拒；证书已经在回调里拿到了。
  }
  final cert = seen;
  return cert == null ? null : certificateFingerprint(cert);
}

class DianaClient {
  DianaClient(this.server, {this.cookie});

  final Uri server;
  String? cookie;
  final HttpClient _http = HttpClient()..connectionTimeout = const Duration(seconds: 10);

  Uri resolve(String path, [Map<String, String>? query]) => server.replace(path: path, queryParameters: query);

  /// 头像、图片地址：相对地址按服务器补全。
  String absolute(String url) => Uri.parse(url).hasScheme ? url : server.resolve(url).toString();

  Map<String, String> get authHeaders => {HttpHeaders.cookieHeader: ?cookie};

  Future<void> login(String username, String password) async {
    final response = await _send('POST', '/api/auth/login', body: {'username': username, 'password': password}, auth: false);
    final cookies = response.cookies.where((c) => c.value.isNotEmpty).map((c) => '${c.name}=${c.value}').join('; ');
    await _drain(response);
    if (cookies.isEmpty) throw ApiException('登录成功了但没拿到会话 Cookie，确认服务器是 Diana WebUI');
    cookie = cookies;
  }

  Future<List<Bot>> bots() async {
    final json = await _json('GET', '/api/assistant/config');
    final profiles = (json['profiles'] as List?)?.cast<Map<String, dynamic>>();
    final list = (profiles != null && profiles.isNotEmpty) ? profiles : [json];
    return list.map(Bot.fromJson).where((b) => b.id.isNotEmpty).toList();
  }

  Future<(ChatInfo, List<ChatMessage>)> chat(String profile) async {
    final json = await _json('GET', '/api/assistant/chat', query: {'profile': profile});
    final messages = ((json['messages'] as List?) ?? const []).cast<Map<String, dynamic>>().map(ChatMessage.fromJson).toList();
    return (ChatInfo.fromJson((json['info'] as Map?)?.cast<String, dynamic>() ?? const {}), messages);
  }

  /// 发一句话，回复按条推出来。连接中途断了或服务端报错都抛 [ApiException]。
  Stream<ChatMessage> send(String profile, String text) async* {
    final response = await _send('POST', '/api/assistant/chat', body: {'profile': profile, 'text': text});
    var done = false;
    String? failure;
    // HTTP 分块可能把一行 JSON 或一个多字节字符切开，按行解码后再解析。
    await for (final line in response.transform(utf8.decoder).transform(const LineSplitter())) {
      if (line.trim().isEmpty) continue;
      final frame = jsonDecode(line) as Map<String, dynamic>;
      switch (frame['type']) {
        case 'message':
          yield ChatMessage.fromJson((frame['message'] as Map).cast<String, dynamic>());
        case 'done':
          done = true;
          final error = frame['error'];
          if (error is String && error.isNotEmpty) failure = error;
      }
    }
    if (!done) throw ApiException('连接中断了，下拉刷新看看她回了没有');
    if (failure != null) throw ApiException(failure);
  }

  Future<CallInfo> callInfo(String profile) async =>
      CallInfo.fromJson(await _json('GET', '/api/assistant/call', query: {'profile': profile}));

  Future<WebSocket> openCall(String profile) async {
    final scheme = server.scheme == 'https' ? 'wss' : 'ws';
    final url = server.replace(scheme: scheme, path: '/api/assistant/call/ws', queryParameters: {'profile': profile});
    try {
      final socket = await WebSocket.connect(url.toString(), headers: authHeaders, customClient: _http);
      socket.pingInterval = const Duration(seconds: 20);
      return socket;
    } on WebSocketException catch (e) {
      // 不满足前提时服务端回 409 不升级，dart:io 拿不到正文，再问一次原因。
      final info = await callInfo(profile).catchError((_) => CallInfo(botName: '', stt: false, tts: false));
      throw ApiException(info.problem ?? '没接通：${e.message}');
    }
  }

  void close() => _http.close(force: true);

  Future<Map<String, dynamic>> _json(String method, String path, {Map<String, String>? query}) async {
    final response = await _send(method, path, query: query);
    final text = await response.transform(utf8.decoder).join();
    final decoded = jsonDecode(text);
    return decoded is Map<String, dynamic> ? decoded : <String, dynamic>{};
  }

  Future<HttpClientResponse> _send(String method, String path, {Map<String, String>? query, Object? body, bool auth = true}) async {
    final HttpClientResponse response;
    try {
      final request = await _http.openUrl(method, resolve(path, query));
      if (auth) authHeaders.forEach(request.headers.set);
      request.headers.set(HttpHeaders.acceptHeader, 'application/json, application/x-ndjson');
      if (body != null) {
        request.headers.contentType = ContentType.json;
        request.write(jsonEncode(body));
      }
      response = await request.close();
    } on HandshakeException {
      final fingerprint = server.scheme == 'https' ? await probeCertificate(server) : null;
      if (fingerprint != null) throw UntrustedCertificateException(server.host, fingerprint);
      throw ApiException('和 ${server.host} 的 HTTPS 握手失败');
    } on SocketException catch (e) {
      throw ApiException('连不上 ${server.host}:${server.port}（${e.osError?.message ?? e.message}）');
    } on TimeoutException {
      throw ApiException('连接 ${server.host} 超时');
    }
    if (response.statusCode >= 200 && response.statusCode < 300) return response;
    final text = await response.transform(utf8.decoder).join().catchError((_) => '');
    String? message;
    try {
      final decoded = jsonDecode(text);
      if (decoded is Map && decoded['error'] is String) message = decoded['error'] as String;
    } catch (_) {}
    throw ApiException(message ?? '请求失败：HTTP ${response.statusCode}', unauthorized: response.statusCode == 401);
  }

  Future<void> _drain(HttpClientResponse response) => response.drain<void>().catchError((_) {});
}
