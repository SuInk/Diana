// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 和一台机器人的私聊，照 QQ：头像、左右气泡、五分钟一条时间分隔、「对方正在输入…」。
// 记录是主人和这台机器人的私聊，平台上聊过的、通话里说的都在里面。

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';
import '../chat_time.dart';
import '../main.dart';
import '../session.dart';
import '../widgets/bot_avatar.dart';
import 'call_page.dart';

class ChatPage extends StatefulWidget {
  const ChatPage({super.key, required this.session, required this.bot});

  final Session session;
  final Bot bot;

  @override
  State<ChatPage> createState() => _ChatPageState();
}

class _ChatPageState extends State<ChatPage> {
  final _input = TextEditingController();
  final _messages = <ChatMessage>[];
  ChatInfo? _info;
  String? _error;
  bool _loading = true;
  int _sending = 0;
  int _localCounter = 0;

  DianaClient get _client => widget.session.client!;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _input.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final (info, messages) = await _client.chat(widget.bot.id);
      if (!mounted) return;
      setState(() {
        _info = info;
        // 本地还没确认或发失败的留着，其余以服务端记录为准。
        final local = _messages.where((m) => m.pending || m.failed).toList();
        _messages
          ..clear()
          ..addAll(messages)
          ..addAll(local);
        _error = null;
      });
    } on ApiException catch (e) {
      if (e.unauthorized) {
        if (mounted) Navigator.popUntil(context, (r) => r.isFirst);
        return widget.session.logout();
      }
      if (mounted) setState(() => _error = e.message);
    } catch (e) {
      if (mounted) setState(() => _error = e.toString());
    } finally {
      if (mounted) setState(() => _loading = false);
    }
  }

  Future<void> _send([ChatMessage? retry]) async {
    final text = retry?.text ?? _input.text.trim();
    if (text.isEmpty) return;
    final mine = retry ??
        ChatMessage(
          id: 'local-${_localCounter++}',
          role: 'user',
          text: text,
          at: DateTime.now().millisecondsSinceEpoch ~/ 1000,
          pending: true,
        );
    setState(() {
      if (retry == null) {
        _input.clear();
        _messages.add(mine);
      }
      mine
        ..pending = true
        ..failed = false;
      _sending++;
    });
    try {
      await for (final reply in _client.send(widget.bot.id, text)) {
        if (!mounted) return;
        setState(() {
          mine.pending = false;
          if (!_messages.any((m) => m.id == reply.id)) _messages.add(reply);
        });
      }
      if (!mounted) return;
      mine.pending = false;
      // 用服务端记录换掉本地这句，顺带拿到正式的编号和时间。
      await _load();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        mine
          ..pending = false
          ..failed = true;
      });
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.toString())));
    } finally {
      if (mounted) setState(() => _sending--);
    }
  }

  void _call() {
    Navigator.push(
      context,
      MaterialPageRoute(fullscreenDialog: true, builder: (_) => CallPage(client: _client, bot: widget.bot)),
    ).then((_) => _load());
  }

  @override
  Widget build(BuildContext context) {
    final name = _info?.botName.isNotEmpty == true ? _info!.botName : widget.bot.name;
    final dividers = timeDividers(_messages.map((m) => m.at).toList());
    return Scaffold(
      appBar: AppBar(
        title: Column(
          children: [
            Text(name, style: const TextStyle(fontSize: 17)),
            if (_sending > 0) const Text('对方正在输入…', style: TextStyle(fontSize: 12, color: Colors.black45)),
          ],
        ),
        actions: [IconButton(tooltip: '语音通话', icon: const Icon(Icons.call_outlined), onPressed: _call)],
      ),
      body: Column(
        children: [
          if (_info?.problem != null) _Banner(_info!.problem!),
          Expanded(
            child: _loading
                ? const Center(child: CircularProgressIndicator())
                : _error != null && _messages.isEmpty
                    ? Center(child: TextButton(onPressed: _load, child: Text('$_error\n点这里重试', textAlign: TextAlign.center)))
                    : RefreshIndicator(
                        onRefresh: _load,
                        child: ListView.builder(
                          reverse: true,
                          physics: const AlwaysScrollableScrollPhysics(),
                          padding: const EdgeInsets.symmetric(vertical: 12),
                          itemCount: _messages.length,
                          itemBuilder: (context, index) {
                            final i = _messages.length - 1 - index;
                            final message = _messages[i];
                            return Column(
                              children: [
                                if (dividers[i])
                                  Padding(
                                    padding: const EdgeInsets.symmetric(vertical: 8),
                                    child: Text(chatTimeLabel(message.at), style: const TextStyle(fontSize: 12, color: Colors.black38)),
                                  ),
                                _Bubble(
                                  message: message,
                                  botName: name,
                                  avatar: widget.bot.avatar == null ? null : _client.absolute(widget.bot.avatar!),
                                  client: _client,
                                  onRetry: () => _send(message),
                                ),
                              ],
                            );
                          },
                        ),
                      ),
          ),
          _InputBar(controller: _input, onSend: _send),
        ],
      ),
    );
  }
}

class _Bubble extends StatelessWidget {
  const _Bubble({required this.message, required this.botName, required this.avatar, required this.client, required this.onRetry});

  final ChatMessage message;
  final String botName;
  final String? avatar;
  final DianaClient client;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    final mine = message.mine;
    final avatarWidget = mine
        ? const BotAvatar(name: '我', size: 38, color: qqBlue)
        : BotAvatar(name: botName, url: avatar, headers: client.authHeaders, size: 38);
    final bubble = Container(
      constraints: BoxConstraints(maxWidth: MediaQuery.of(context).size.width * 0.68),
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 9),
      decoration: BoxDecoration(
        color: mine ? qqBlue : Colors.white,
        borderRadius: BorderRadius.only(
          topLeft: Radius.circular(mine ? 14 : 4),
          topRight: Radius.circular(mine ? 4 : 14),
          bottomLeft: const Radius.circular(14),
          bottomRight: const Radius.circular(14),
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (message.text.isNotEmpty)
            SelectableText(message.text, style: TextStyle(fontSize: 15.5, height: 1.35, color: mine ? Colors.white : Colors.black87)),
          for (final image in message.images)
            Padding(
              padding: const EdgeInsets.only(top: 6),
              child: ClipRRect(
                borderRadius: BorderRadius.circular(8),
                child: Image.network(client.absolute(image), headers: client.authHeaders, errorBuilder: (_, _, _) => const Icon(Icons.broken_image_outlined)),
              ),
            ),
        ],
      ),
    );
    final status = message.pending
        ? const SizedBox(width: 14, height: 14, child: CircularProgressIndicator(strokeWidth: 1.6))
        : message.failed
            ? GestureDetector(onTap: onRetry, child: const Icon(Icons.error, color: Colors.redAccent, size: 20))
            : null;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
      child: Row(
        mainAxisAlignment: mine ? MainAxisAlignment.end : MainAxisAlignment.start,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: mine
            ? [if (status != null) Padding(padding: const EdgeInsets.only(right: 8, top: 10), child: status), bubble, const SizedBox(width: 8), avatarWidget]
            : [avatarWidget, const SizedBox(width: 8), bubble],
      ),
    );
  }
}

class _InputBar extends StatelessWidget {
  const _InputBar({required this.controller, required this.onSend});

  final TextEditingController controller;
  final VoidCallback onSend;

  @override
  Widget build(BuildContext context) {
    return Container(
      color: const Color(0xFFF7F7F9),
      padding: EdgeInsets.fromLTRB(10, 8, 10, 8 + MediaQuery.of(context).padding.bottom),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.end,
        children: [
          Expanded(
            child: TextField(
              controller: controller,
              minLines: 1,
              maxLines: 5,
              textInputAction: TextInputAction.send,
              onSubmitted: (_) => onSend(),
              decoration: InputDecoration(
                isDense: true,
                filled: true,
                fillColor: Colors.white,
                contentPadding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
                border: OutlineInputBorder(borderRadius: BorderRadius.circular(8), borderSide: BorderSide.none),
              ),
            ),
          ),
          const SizedBox(width: 8),
          ListenableBuilder(
            listenable: controller,
            builder: (context, _) => FilledButton(
              onPressed: controller.text.trim().isEmpty ? null : onSend,
              style: FilledButton.styleFrom(minimumSize: const Size(60, 40), shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(8))),
              child: const Text('发送'),
            ),
          ),
        ],
      ),
    );
  }
}

class _Banner extends StatelessWidget {
  const _Banner(this.text);

  final String text;

  @override
  Widget build(BuildContext context) => Container(
        width: double.infinity,
        color: const Color(0xFFFFF4E5),
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
        child: Text(text, style: const TextStyle(fontSize: 13, color: Color(0xFF8A5300))),
      );
}
