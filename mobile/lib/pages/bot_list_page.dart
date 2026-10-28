// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';
import '../chat_time.dart';
import '../session.dart';
import '../widgets/bot_avatar.dart';
import 'chat_page.dart';

/// 消息列表：所有机器人，每台显示最后一条私聊。
class BotListPage extends StatefulWidget {
  const BotListPage({super.key, required this.session});

  final Session session;

  @override
  State<BotListPage> createState() => _BotListPageState();
}

class _BotListPageState extends State<BotListPage> {
  List<Bot>? _bots;
  final Map<String, ChatMessage> _last = {};
  String? _error;

  DianaClient get _client => widget.session.client!;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final bots = await _client.bots();
      if (!mounted) return;
      setState(() {
        _bots = bots;
        _error = null;
      });
      await Future.wait(bots.map(_loadLast));
    } on ApiException catch (e) {
      if (e.unauthorized) return widget.session.logout();
      if (mounted) setState(() => _error = e.message);
    } catch (e) {
      if (mounted) setState(() => _error = e.toString());
    }
  }

  Future<void> _loadLast(Bot bot) async {
    try {
      final (_, messages) = await _client.chat(bot.id);
      if (mounted && messages.isNotEmpty) setState(() => _last[bot.id] = messages.last);
    } catch (_) {
      // 列表里的预览拿不到不影响进去聊。
    }
  }

  Future<void> _open(Bot bot) async {
    await Navigator.push(context, MaterialPageRoute(builder: (_) => ChatPage(session: widget.session, bot: bot)));
    await _loadLast(bot);
  }

  Future<void> _confirmLogout() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        content: Text('退出 ${widget.session.lastServer} 的登录？'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('取消')),
          TextButton(onPressed: () => Navigator.pop(context, true), child: const Text('退出')),
        ],
      ),
    );
    if (ok == true) await widget.session.logout();
  }

  @override
  Widget build(BuildContext context) {
    final bots = _bots;
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        backgroundColor: Colors.white,
        title: const Text('消息'),
        actions: [IconButton(tooltip: '退出登录', icon: const Icon(Icons.logout), onPressed: _confirmLogout)],
      ),
      body: RefreshIndicator(
        onRefresh: _load,
        child: bots == null
            ? ListView(children: [
                const SizedBox(height: 160),
                Center(child: _error == null ? const CircularProgressIndicator() : _Hint(_error!)),
              ])
            : bots.isEmpty
                ? ListView(children: const [SizedBox(height: 160), _Hint('还没有机器人，先在 WebUI 里配一台')])
                : ListView.separated(
                    itemCount: bots.length,
                    separatorBuilder: (_, _) => const Divider(height: 1, indent: 78),
                    itemBuilder: (context, i) {
                      final bot = bots[i];
                      final last = _last[bot.id];
                      return ListTile(
                        contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 6),
                        leading: BotAvatar(
                          name: bot.name,
                          url: bot.avatar == null ? null : _client.absolute(bot.avatar!),
                          headers: _client.authHeaders,
                          size: 50,
                        ),
                        title: Text(bot.name, style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w500)),
                        subtitle: Text(
                          last == null ? bot.platform : last.text.replaceAll('\n', ' '),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: const TextStyle(color: Colors.black45),
                        ),
                        trailing: last == null ? null : Text(listTimeLabel(last.at), style: const TextStyle(fontSize: 12, color: Colors.black38)),
                        onTap: () => _open(bot),
                      );
                    },
                  ),
      ),
    );
  }
}

class _Hint extends StatelessWidget {
  const _Hint(this.text);

  final String text;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.symmetric(horizontal: 32),
        child: Text(text, textAlign: TextAlign.center, style: const TextStyle(color: Colors.black45)),
      );
}
