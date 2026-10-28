// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 一通电话：连 /api/assistant/call/ws，上行 16kHz pcm_s16le，下行逐句音频顺序播放。
// 协议见 docs/voice-call.md，行为和网页版 VoiceCallScreen.vue 对齐。
//
// 回声消除交给系统：iOS 录音走 VoiceProcessingIO（record 的 echoCancel），安卓用
// VOICE_COMMUNICATION 音源加通话模式。外放仍有回声时用「对讲」，播报期间不上行。

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:just_audio/just_audio.dart';
import 'package:record/record.dart';

import '../api/client.dart';
import 'call_turns.dart';

/// 上行采样率。识别接口都按 16kHz 单声道处理，再高只是多传数据。
const callSampleRate = 16000;

class CallController extends ChangeNotifier {
  CallController(this.client, this.profile, {String? name}) : botName = name ?? '';

  final DianaClient client;
  final String profile;

  String botName;
  String? problem;
  bool connecting = true;
  bool inCall = false;
  bool ended = false;

  /// listening / thinking / speaking。
  String callState = 'listening';
  bool userSpeaking = false;
  bool holding = false;
  bool muted = false;
  bool pushToTalk = false;
  final List<CallTurn> turns = [];
  int elapsed = 0;

  /// 收齐并排进播放队列的回复句数。
  int audioSentences = 0;

  WebSocket? _socket;
  final _recorder = AudioRecorder();
  StreamSubscription<Uint8List>? _mic;
  final _player = AudioPlayer();
  final _collector = SentenceAudioCollector();
  Future<void> _playback = Future.value();
  int _epoch = 0;
  int _fileCounter = 0;
  Timer? _clock;
  Directory? _audioDir;
  bool _disposed = false;

  bool get canInterrupt => inCall && (callState == 'speaking' || callState == 'thinking');

  String get duration => '${(elapsed ~/ 60).toString().padLeft(2, '0')}:${(elapsed % 60).toString().padLeft(2, '0')}';

  String get statusLabel {
    if (problem != null) return ended && elapsed > 0 ? '通话结束 $duration' : '未接通';
    if (ended) return '通话结束 $duration';
    if (connecting) return '正在呼叫…';
    if (muted) return '$duration · 已静音';
    if (userSpeaking || holding) return '$duration · 在听';
    if (callState == 'thinking') return '$duration · 在想…';
    if (callState == 'speaking') return '$duration · 在说话';
    return duration;
  }

  Future<void> dial() async {
    try {
      final info = await client.callInfo(profile);
      if (info.botName.isNotEmpty) botName = info.botName;
      if (info.problem != null) return _fail('${info.problem}。到 WebUI「提供商」页的模型分配里设置后再打。');
      if (!await _recorder.hasPermission()) return _fail('没有麦克风权限，到系统设置里允许后再打');
      if (ended) return;
      final socket = await client.openCall(profile);
      if (ended) {
        await socket.close();
        return;
      }
      _socket = socket;
      socket.listen(_onFrame, onDone: () {
        if (_socket != socket) return;
        if (connecting) problem = socket.closeReason?.isNotEmpty == true ? socket.closeReason : '没接通：确认已登录，并且语音识别和语音合成都配好了';
        _end();
      }, onError: (Object e) => _fail('通话断了：$e'));
      // 一直按服务端分句、允许打断开始：按住说话和静音都在本地控制上行，通话中随时能切。
      _send({'type': 'start', 'format': 'pcm_s16le', 'sample_rate': callSampleRate, 'segmentation': 'server_vad', 'barge_in': true});
    } catch (e) {
      _fail(e.toString());
    }
  }

  void _onFrame(dynamic data) {
    if (data is String) {
      _handle(jsonDecode(data) as Map<String, dynamic>);
    } else if (data is List<int>) {
      _collector.push(data);
    }
  }

  void _handle(Map<String, dynamic> msg) {
    applyCallMessage(turns, msg);
    final turn = (msg['turn'] as num?)?.toInt() ?? 0;
    final seq = (msg['seq'] as num?)?.toInt() ?? 0;
    switch (msg['type']) {
      case 'ready':
        connecting = false;
        inCall = true;
        final name = (msg['ready'] as Map?)?['bot_name'];
        if (name is String && name.isNotEmpty) botName = name;
        _clock = Timer.periodic(const Duration(seconds: 1), (_) {
          elapsed++;
          notifyListeners();
        });
        _startCapture();
      case 'state':
        final state = msg['state'];
        if (state is String) callState = state;
      case 'speech':
        userSpeaking = msg['event'] == 'start';
      case 'audio_start':
        _collector.start(turn, seq, (msg['mime'] as String?) ?? 'audio/mpeg');
      case 'audio_end':
        final sentence = _collector.end(turn, seq);
        if (sentence != null) {
          audioSentences++;
          _enqueue(sentence.mime, sentence.data);
        }
      case 'interrupted':
        _stopPlayback();
      case 'error':
        if (msg['fatal'] == true) _fail((msg['text'] ?? msg['code'] ?? '通话出错') as String);
    }
    notifyListeners();
  }

  Future<void> _startCapture() async {
    try {
      final stream = await _recorder.startStream(const RecordConfig(
        encoder: AudioEncoder.pcm16bits,
        sampleRate: callSampleRate,
        numChannels: 1,
        echoCancel: true,
        noiseSuppress: true,
        autoGain: true,
        androidConfig: AndroidRecordConfig(
          audioSource: AndroidAudioSource.voiceCommunication,
          audioManagerMode: AudioManagerMode.modeInCommunication,
          speakerphone: true,
        ),
        iosConfig: IosRecordConfig(categoryOptions: [IosAudioCategoryOption.defaultToSpeaker, IosAudioCategoryOption.allowBluetooth]),
      ));
      _mic = stream.listen((chunk) {
        final socket = _socket;
        if (socket == null || ended) return;
        // 按住说话时只在按着的时候上行；静音时照常发静音帧，服务端靠它判断说完没有。
        if (pushToTalk && !holding) return;
        socket.add(muted ? Uint8List(chunk.length) : chunk);
      });
    } catch (e) {
      _fail('拿不到麦克风：$e');
    }
  }

  // 一句一句排着放：合成完的句子可能比上一句还没放完的时候先到。
  void _enqueue(String mime, Uint8List data) {
    final epoch = _epoch;
    _playback = _playback.then((_) async {
      if (epoch != _epoch || ended) return;
      _audioDir ??= await Directory.systemTemp.createTemp('diana-call');
      final file = File('${_audioDir!.path}/${_fileCounter++}.${audioExtension(mime)}');
      try {
        await file.writeAsBytes(data, flush: true);
        if (epoch != _epoch || ended) return;
        await _player.setFilePath(file.path);
        if (epoch != _epoch || ended) return;
        // play() 在放完、暂停或停止时返回。
        await _player.play();
      } catch (e) {
        debugPrint('播放失败：$e');
      } finally {
        unawaited(file.delete().then((_) {}, onError: (_) {}));
      }
    });
  }

  void _stopPlayback() {
    _epoch++;
    _collector.reset();
    unawaited(_player.stop().then((_) {}, onError: (_) {}));
  }

  void _send(Map<String, Object?> message) {
    if (_socket?.readyState == WebSocket.open) _socket!.add(jsonEncode(message));
  }

  void interrupt() {
    _stopPlayback();
    _send({'type': 'interrupt'});
  }

  void toggleMute() {
    muted = !muted;
    notifyListeners();
  }

  void togglePushToTalk() {
    pushToTalk = !pushToTalk;
    // 切过去时正在说的半句立即送出去，不留在服务端等静音。
    if (pushToTalk) _send({'type': 'commit'});
    notifyListeners();
  }

  void pressTalk() {
    holding = true;
    if (callState != 'listening') interrupt();
    notifyListeners();
  }

  void releaseTalk() {
    if (!holding) return;
    holding = false;
    _send({'type': 'commit'});
    notifyListeners();
  }

  void sendText(String text) {
    final trimmed = text.trim();
    if (trimmed.isEmpty) return;
    _stopPlayback();
    _send({'type': 'text', 'text': trimmed});
  }

  void hangup() {
    if (ended) return;
    _send({'type': 'hangup'});
    _end();
  }

  void _fail(String message) {
    problem ??= message;
    _end();
  }

  void _end() {
    if (!ended) {
      ended = true;
      inCall = false;
      connecting = false;
      _clock?.cancel();
      _stopPlayback();
      unawaited(_mic?.cancel());
      unawaited(_recorder.stop().then((_) {}, onError: (_) {}));
      unawaited(_socket?.close());
    }
    notifyListeners();
  }

  @override
  void notifyListeners() {
    if (!_disposed) super.notifyListeners();
  }

  @override
  void dispose() {
    hangup();
    _disposed = true;
    _clock?.cancel();
    unawaited(_recorder.dispose());
    unawaited(_player.dispose());
    unawaited(_audioDir?.delete(recursive: true).then((_) {}, onError: (_) {}));
    super.dispose();
  }
}
