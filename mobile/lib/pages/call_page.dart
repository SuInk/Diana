// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 全屏通话界面，照手机通话做：头像、名字、通话时长，中间是最近两轮的实时字幕，
// 底下是静音、对讲（按住说话）、打字和挂断。完整记录在私聊里，这里只显示字幕。

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/models.dart';
import '../call/call_controller.dart';
import '../widgets/bot_avatar.dart';

class CallPage extends StatefulWidget {
  const CallPage({super.key, required this.client, required this.bot});

  final DianaClient client;
  final Bot bot;

  @override
  State<CallPage> createState() => _CallPageState();
}

class _CallPageState extends State<CallPage> {
  late final CallController _call = CallController(widget.client, widget.bot.id, name: widget.bot.name)..dial();
  final _typed = TextEditingController();
  bool _typing = false;

  @override
  void dispose() {
    _call.dispose();
    _typed.dispose();
    super.dispose();
  }

  void _hangup() {
    _call.hangup();
    Navigator.pop(context);
  }

  void _sendTyped() {
    _call.sendText(_typed.text);
    _typed.clear();
  }

  @override
  Widget build(BuildContext context) {
    return PopScope(
      onPopInvokedWithResult: (didPop, _) {
        if (didPop) _call.hangup();
      },
      child: Scaffold(
        backgroundColor: const Color(0xFF1F2A36),
        body: ListenableBuilder(
          listenable: _call,
          builder: (context, _) => SafeArea(
            child: Column(
              children: [
                const SizedBox(height: 48),
                BotAvatar(
                  name: _call.botName,
                  url: widget.bot.avatar == null ? null : widget.client.absolute(widget.bot.avatar!),
                  headers: widget.client.authHeaders,
                  size: 96,
                ),
                const SizedBox(height: 16),
                Text(_call.botName.isEmpty ? '机器人' : _call.botName, style: const TextStyle(color: Colors.white, fontSize: 24, fontWeight: FontWeight.w500)),
                const SizedBox(height: 8),
                Text(_call.statusLabel, style: const TextStyle(color: Colors.white70, fontSize: 15)),
                if (_call.problem != null)
                  Padding(
                    padding: const EdgeInsets.fromLTRB(32, 16, 32, 0),
                    child: Text(_call.problem!, textAlign: TextAlign.center, style: const TextStyle(color: Color(0xFFFFB4A9))),
                  ),
                Expanded(child: _Captions(call: _call)),
                if (_call.canInterrupt)
                  TextButton(
                    onPressed: _call.interrupt,
                    child: const Text('轻点打断', style: TextStyle(color: Colors.white70)),
                  ),
                if (_typing && _call.inCall) _typingField(),
                if (_call.pushToTalk && _call.inCall) _talkButton(),
                const SizedBox(height: 12),
                _controls(),
                const SizedBox(height: 28),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _typingField() => Padding(
        padding: const EdgeInsets.fromLTRB(24, 0, 24, 12),
        child: TextField(
          controller: _typed,
          autofocus: true,
          style: const TextStyle(color: Colors.white),
          textInputAction: TextInputAction.send,
          onSubmitted: (_) => _sendTyped(),
          decoration: InputDecoration(
            hintText: '打字说一句',
            hintStyle: const TextStyle(color: Colors.white38),
            filled: true,
            fillColor: Colors.white12,
            border: OutlineInputBorder(borderRadius: BorderRadius.circular(24), borderSide: BorderSide.none),
            suffixIcon: IconButton(icon: const Icon(Icons.arrow_upward, color: Colors.white), onPressed: _sendTyped),
          ),
        ),
      );

  Widget _talkButton() => GestureDetector(
        onTapDown: (_) => _call.pressTalk(),
        onTapUp: (_) => _call.releaseTalk(),
        onTapCancel: _call.releaseTalk,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 120),
          width: 108,
          height: 108,
          margin: const EdgeInsets.only(bottom: 12),
          alignment: Alignment.center,
          decoration: BoxDecoration(shape: BoxShape.circle, color: _call.holding ? const Color(0xFF12B7F5) : Colors.white24),
          child: Text(_call.holding ? '松开发送' : '按住说话', style: const TextStyle(color: Colors.white, fontSize: 15)),
        ),
      );

  Widget _controls() {
    if (!_call.inCall) {
      return _RoundButton(
        icon: Icons.call_end,
        label: _call.ended ? '关闭' : '挂断',
        color: const Color(0xFFFF4D4F),
        onTap: _hangup,
      );
    }
    return Row(
      mainAxisAlignment: MainAxisAlignment.spaceEvenly,
      children: [
        _RoundButton(icon: _call.muted ? Icons.mic_off : Icons.mic, label: '静音', active: _call.muted, onTap: _call.toggleMute),
        _RoundButton(icon: Icons.back_hand_outlined, label: '对讲', active: _call.pushToTalk, onTap: _call.togglePushToTalk),
        _RoundButton(icon: Icons.keyboard_outlined, label: '打字', active: _typing, onTap: () => setState(() => _typing = !_typing)),
        _RoundButton(icon: Icons.call_end, label: '挂断', color: const Color(0xFFFF4D4F), onTap: _hangup),
      ],
    );
  }
}

class _Captions extends StatelessWidget {
  const _Captions({required this.call});

  final CallController call;

  @override
  Widget build(BuildContext context) {
    // 字幕只留最近两轮，像通话里的实时字幕。
    final recent = call.turns.length > 2 ? call.turns.sublist(call.turns.length - 2) : call.turns;
    final lines = <Widget>[];
    for (final turn in recent) {
      if (turn.user.isNotEmpty) lines.add(_line(turn.user, mine: true));
      if (turn.bot.isNotEmpty) lines.add(_line(turn.bot.join(), mine: false, cut: turn.status == 'interrupted'));
    }
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 28),
      child: Column(mainAxisAlignment: MainAxisAlignment.end, children: lines),
    );
  }

  Widget _line(String text, {required bool mine, bool cut = false}) => Padding(
        padding: const EdgeInsets.only(bottom: 10),
        child: Text(
          text,
          textAlign: TextAlign.center,
          maxLines: 4,
          overflow: TextOverflow.ellipsis,
          style: TextStyle(
            color: mine ? Colors.white60 : (cut ? Colors.white54 : Colors.white),
            fontSize: mine ? 15 : 17,
            height: 1.4,
          ),
        ),
      );
}

class _RoundButton extends StatelessWidget {
  const _RoundButton({required this.icon, required this.label, required this.onTap, this.active = false, this.color});

  final IconData icon;
  final String label;
  final VoidCallback onTap;
  final bool active;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final background = color ?? (active ? Colors.white : Colors.white24);
    return Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        Material(
          color: background,
          shape: const CircleBorder(),
          child: InkWell(
            customBorder: const CircleBorder(),
            onTap: onTap,
            child: SizedBox(width: 64, height: 64, child: Icon(icon, color: active && color == null ? const Color(0xFF1F2A36) : Colors.white, size: 28)),
          ),
        ),
        const SizedBox(height: 8),
        Text(label, style: const TextStyle(color: Colors.white70, fontSize: 13)),
      ],
    );
  }
}
