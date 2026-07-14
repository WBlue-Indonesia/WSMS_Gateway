import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:web_socket_channel/io.dart';

import 'config.dart';
import 'push.dart';
import 'storage.dart';
import 'telephony.dart';

enum ConnState { disconnected, connecting, connected }

/// Gateway holds the persistent WebSocket to the server, reports SIMs, and turns
/// send_command frames into real SMS sends with ack + delivery reporting.
///
/// Send lifecycle (amendments F1/F5):
///   1. On send_command, consult the on-device ledger. Already 'sent' -> reply duplicate.
///   2. Expired -> reply rejected (never send a stale OTP).
///   3. Hand to SmsManager. accepted -> write ledger 'sent' THEN reply accepted.
///      not accepted / threw -> reply rejected (radio never took it; server may reroute).
///   4. Forward SENT/DELIVERED/FAILED from the native receivers as delivery_report frames.
class Gateway {
  Gateway(this._storage, this._telephony);

  final Storage _storage;
  final Telephony _telephony;

  final state = ValueNotifier<ConnState>(ConnState.disconnected);
  final log = ValueNotifier<List<String>>([]);

  IOWebSocketChannel? _channel;
  StreamSubscription? _telephonySub;
  Timer? _heartbeat;
  Timer? _reconnect;
  bool _stopped = false;
  int _backoffSecs = 1;

  Future<void> start() async {
    _stopped = false;
    _telephonySub ??= _telephony.events().listen(_onTelephonyEvent);
    await _connect();
  }

  Future<void> stop() async {
    _stopped = true;
    _heartbeat?.cancel();
    _reconnect?.cancel();
    await _telephonySub?.cancel();
    _telephonySub = null;
    await _channel?.sink.close();
    _channel = null;
    state.value = ConnState.disconnected;
  }

  Future<void> _connect() async {
    if (_stopped) return;
    final url = await _storage.serverUrl;
    final id = await _storage.deviceId;
    final secret = await _storage.deviceSecret;
    if (url == null || id == null || secret == null) {
      _appendLog('not enrolled');
      return;
    }
    state.value = ConnState.connecting;
    final wsUrl = '${_toWsUrl(url)}/v1/device/ws';
    try {
      final ch = IOWebSocketChannel.connect(
        Uri.parse(wsUrl),
        headers: {'Authorization': 'Bearer dev_$id.$secret'},
        pingInterval: const Duration(seconds: 20),
      );
      _channel = ch;
      ch.stream.listen(
        _onData,
        onDone: _onClosed,
        onError: (_) => _onClosed(),
        cancelOnError: true,
      );
      state.value = ConnState.connected;
      _backoffSecs = 1;
      _appendLog('connected to $wsUrl');
      await _sendHello();
      await reportSims();
      _startHeartbeat();
    } catch (e) {
      _appendLog('connect failed: $e');
      _onClosed();
    }
  }

  void _onClosed() {
    _heartbeat?.cancel();
    state.value = ConnState.disconnected;
    if (_stopped) return;
    _appendLog('disconnected, retrying in ${_backoffSecs}s');
    _reconnect?.cancel();
    _reconnect = Timer(Duration(seconds: _backoffSecs), _connect);
    _backoffSecs = (_backoffSecs * 2).clamp(1, 30);
  }

  void _startHeartbeat() {
    _heartbeat?.cancel();
    _heartbeat = Timer.periodic(const Duration(seconds: 15), (_) {
      _send(FrameType.heartbeat, null);
    });
  }

  Future<void> _sendHello() async {
    _send(FrameType.hello, {
      'app_version': '0.1.0',
      'os': 'android',
      'model': 'unknown',
      if (Push.token != null) 'push_token': Push.token,
    });
  }

  /// Reads SIMs from the OS and reports them so the server can route on-net.
  Future<void> reportSims() async {
    final sims = await _telephony.listSims();
    _send(FrameType.simReport, {'sims': sims.map((s) => s.toReportJson()).toList()});
    _appendLog('reported ${sims.length} SIM(s)');
  }

  void _onData(dynamic raw) {
    Map<String, dynamic> frame;
    try {
      frame = jsonDecode(raw as String) as Map<String, dynamic>;
    } catch (_) {
      return;
    }
    final type = frame['type'] as String?;
    final data = (frame['data'] as Map?)?.cast<String, dynamic>() ?? {};
    switch (type) {
      case FrameType.sendCommand:
        _onSendCommand(data);
        break;
      case FrameType.cancel:
        _onCancel(data);
        break;
      case FrameType.ping:
        _send(FrameType.heartbeat, null);
        break;
      case FrameType.config:
        if (data['action'] == 'report_sims') reportSims();
        break;
    }
  }

  Future<void> _onSendCommand(Map<String, dynamic> d) async {
    final messageId = d['message_id'] as String? ?? '';
    final target = d['target'] as String? ?? '';
    final body = d['body'] as String? ?? '';
    final subId = (d['subscription_id'] ?? -1) as int;
    final expiresAtMs = (d['expires_at_ms'] ?? 0) as int;

    // 1. Dedup against the local ledger.
    final phase = await _storage.ledgerPhase(messageId);
    if (phase != null) {
      _sendAck(messageId, AckResult.duplicate, 'already $phase');
      _sendDelivery(messageId, phase == 'delivered' ? DeliveryStatus.delivered : DeliveryStatus.sent);
      return;
    }

    // 2. Expiry guard (F7): never send a stale message.
    if (expiresAtMs > 0 && DateTime.now().millisecondsSinceEpoch > expiresAtMs) {
      _sendAck(messageId, AckResult.rejected, 'expired');
      return;
    }

    // 3. Hand to SmsManager.
    final res = await _telephony.sendSms(
      messageId: messageId,
      subscriptionId: subId,
      target: target,
      body: body,
    );
    if (res.accepted) {
      await _storage.setLedgerPhase(messageId, 'sent'); // commit AFTER acceptance (F5)
      _sendAck(messageId, AckResult.accepted, null);
      _appendLog('sending $messageId -> $target (sub $subId, ${res.parts} part)');
    } else {
      _sendAck(messageId, AckResult.rejected, res.error ?? 'not accepted by radio');
      _appendLog('rejected $messageId: ${res.error}');
    }
  }

  void _onCancel(Map<String, dynamic> d) {
    final messageId = d['message_id'] as String? ?? '';
    // Best-effort: if we have not committed it, we can drop it. Once handed to SmsManager
    // we cannot recall it (amendment F9) -> report already_sent.
    _storage.ledgerPhase(messageId).then((phase) {
      _send(FrameType.cancelAck, {
        'message_id': messageId,
        'result': phase == null ? 'cancelled' : 'already_sent',
      });
    });
  }

  void _onTelephonyEvent(TelephonyEvent e) {
    switch (e.phase) {
      case 'sent':
        _sendDelivery(e.messageId, DeliveryStatus.sent);
        break;
      case 'delivered':
        _storage.setLedgerPhase(e.messageId, 'delivered');
        _sendDelivery(e.messageId, DeliveryStatus.delivered);
        break;
      case 'failed':
        _sendDelivery(e.messageId, DeliveryStatus.failed, e.reason);
        _appendLog('delivery failed ${e.messageId}: ${e.reason}');
        break;
    }
  }

  void _sendAck(String messageId, String result, String? reason) {
    _send(FrameType.sendAck, {'message_id': messageId, 'result': result, if (reason != null) 'reason': reason});
  }

  void _sendDelivery(String messageId, String status, [String? reason]) {
    _send(FrameType.deliveryReport, {'message_id': messageId, 'status': status, if (reason != null) 'reason': reason});
  }

  void _send(String type, Map<String, dynamic>? data) {
    final ch = _channel;
    if (ch == null) return;
    final frame = {
      'type': type,
      'id': DateTime.now().microsecondsSinceEpoch.toString(),
      'ts': DateTime.now().millisecondsSinceEpoch,
      if (data != null) 'data': data,
    };
    ch.sink.add(jsonEncode(frame));
  }

  String _toWsUrl(String httpUrl) {
    if (httpUrl.startsWith('https://')) return 'wss://${httpUrl.substring(8)}';
    if (httpUrl.startsWith('http://')) return 'ws://${httpUrl.substring(7)}';
    return httpUrl;
  }

  void _appendLog(String line) {
    final ts = DateTime.now().toIso8601String().substring(11, 19);
    final next = [...log.value, '$ts  $line'];
    log.value = next.length > 200 ? next.sublist(next.length - 200) : next;
  }
}
