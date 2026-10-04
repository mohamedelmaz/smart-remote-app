import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:web_socket_channel/web_socket_channel.dart';

/// Result of sending a command to the PC.
///
/// The server always answers with an ok/err pair, so failures are reported to
/// the user instead of being silently swallowed.
class Ack {
  const Ack({required this.ok, this.error = '', this.nonce = ''});

  factory Ack.fromJson(Map<String, dynamic> json) => Ack(
        ok: json['ok'] as bool? ?? false,
        error: json['err'] as String? ?? '',
        nonce: json['nonce'] as String? ?? '',
      );

  final bool ok;
  final String error;
  final String nonce;
}

/// Lifecycle of the control socket.
enum LinkState {
  /// No socket, never connected.
  idle,

  /// Socket is open but the PIN has not been accepted yet.
  connecting,

  /// Paired and able to send commands.
  ready,

  /// Lost the connection; a reconnect is in progress.
  reconnecting,

  /// The server refused the PIN, or the connection failed terminally.
  failed,
}

/// Handles the WebSocket control channel to the PC.
///
/// One instance owns one socket. Reconnection uses exponential backoff with a
/// cap, and the pending-command map lets a caller await the server's ack for a
/// specific nonce instead of guessing whether a command landed.
class RemoteLink {
  RemoteLink({required this.host, required this.port});

  final String host;
  final int port;

  WebSocketChannel? _channel;
  StreamSubscription<dynamic>? _sub;

  /// Broadcast so widgets can render link state without polling.
  final StreamController<LinkState> _stateCtrl =
      StreamController<LinkState>.broadcast();
  Stream<LinkState> get states => _stateCtrl.stream;
  LinkState get state => _state;

  LinkState _state = LinkState.idle;

  /// Server-pushed events, e.g. a regenerated PIN or a blocked-input notice.
  final StreamController<Map<String, dynamic>> _eventsCtrl =
      StreamController<Map<String, dynamic>>.broadcast();
  Stream<Map<String, dynamic>> get events => _eventsCtrl.stream;

  /// Acks keyed by nonce, completed when the matching reply arrives.
  final Map<String, Completer<Ack>> _pending = {};

  String? _pin;
  String? _deviceName;
  Timer? _reconnectTimer;
  int _attempt = 0;
  bool _disposed = false;
  int _nonceCounter = 0;

  /// Connects and pairs, then keeps the socket alive with reconnects.
  ///
  /// [pin] is sent as the first message; the server rejects everything else
  /// until it accepts it.
  Future<void> connect({required String pin, required String deviceName}) async {
    _pin = pin;
    _deviceName = deviceName;
    _disposed = false;
    await _openSocket();
  }

  Uri get _wsUri => Uri.parse('ws://$host:$port/ws');

  Future<void> _openSocket() async {
    if (_disposed) return;
    _setState(_attempt == 0 ? LinkState.connecting : LinkState.reconnecting);

    try {
      final channel = WebSocketChannel.connect(_wsUri);
      _channel = channel;

      // ready must complete before the listener is attached, otherwise a fast
      // server reply can arrive before anything is listening for it.
      await channel.ready;

      _sub = channel.stream.listen(
        _onData,
        onError: (Object error) => _scheduleReconnect('socket error: $error'),
        onDone: () => _scheduleReconnect('socket closed by peer'),
        cancelOnError: true,
      );

      await _authenticate(_pin, _deviceName);
    } catch (error) {
      _scheduleReconnect('connect failed: $error');
    }
  }

  /// Sends the auth message and waits for the server's verdict.
  ///
  /// Waiting here means [connect] does not return until the PC has either
  /// accepted or rejected the PIN, so the caller gets a definite result
  /// instead of an optimistic "paired".
  Future<void> _authenticate(String? pin, String? name) async {
    final channel = _channel;
    if (channel == null) return;

    final ack = await request(
      {'type': 'auth', 'pin': pin, 'name': name},
      timeout: const Duration(seconds: 6),
    );
    if (ack.ok) {
      _attempt = 0;
      _setState(LinkState.ready);
    } else {
      _setState(LinkState.failed);
      _eventsCtrl.add({'kind': 'auth_failed', 'msg': ack.error});
      // Re-arm the backoff. Without this a failed authentication left the link
      // permanently dead: nothing else ever moved it out of `failed`, so every
      // button on every screen stayed dead until the app was force-closed.
      //
      // The backoff still applies, so a genuinely wrong PIN retries slowly
      // rather than hammering the server. That is the right trade: a stale PIN
      // heals by itself the moment the PC is restarted or the PIN regenerated,
      // and the user is never left with a remote that cannot recover.
      _reconnectTimer?.cancel();
      _reconnectTimer = null;
      _scheduleReconnect('auth rejected');
    }
  }

  /// Handles one inbound frame.
  void _onData(dynamic raw) {
    if (raw is! String) return;
    Map<String, dynamic> json;
    try {
      json = jsonDecode(raw) as Map<String, dynamic>;
    } catch (_) {
      // A malformed frame is ignored rather than tearing down the link; the
      // protocol is versioned, so a newer server may add message kinds.
      return;
    }

    // An ack carries ok/err; an event carries kind.
    if (json.containsKey('ok')) {
      final ack = Ack.fromJson(json);
      final completer = _pending.remove(ack.nonce);
      if (completer != null && !completer.isCompleted) {
        completer.complete(ack);
      }
      return;
    }

    _eventsCtrl.add(json);
  }


  /// Sends a JSON command without waiting for its ack.
  ///
  /// Used for high-rate input such as touchpad moves, where waiting for a
  /// round trip per event would add visible latency.
  void send(Map<String, dynamic> message) {
    final channel = _channel;
    if (channel == null) {
      // No socket. Try to recover rather than discarding the command: a link
      // sitting in `failed` never re-armed itself on its own, which is why a
      // dropped connection stayed dead until the app was restarted.
      _scheduleReconnect('send with no socket');
      return;
    }
    try {
      channel.sink.add(jsonEncode(message));
    } catch (_) {
      _scheduleReconnect('send failed');
    }
  }

  /// Sends a command and waits for the server's ack.
  ///
  /// Returns an error Ack on timeout rather than hanging, so a caller can
  /// always show the user a definite outcome.
  Future<Ack> request(Map<String, dynamic> message,
      {Duration timeout = const Duration(seconds: 4)}) async {
    _nonceCounter++;
    final nonce = 'n$_nonceCounter';
    message['nonce'] = nonce;

    final channel = _channel;
    if (channel == null) {
      return const Ack(ok: false, error: 'Not connected');
    }

    final completer = Completer<Ack>();
    _pending[nonce] = completer;

    try {
      channel.sink.add(jsonEncode(message));
    } catch (error) {
      _pending.remove(nonce);
      return Ack(ok: false, error: 'Send failed: $error');
    }

    try {
      return await completer.future.timeout(timeout);
    } on TimeoutException {
      // Drop the waiter so the map cannot grow without bound when a command
      // is lost in flight.
      _pending.remove(nonce);
      return const Ack(ok: false, error: 'No reply from PC');
    }
  }

  /// Sends raw PCM audio as a binary frame prefixed with the audio marker byte.
  void sendAudio(Uint8List pcm) {
    final channel = _channel;
    if (channel == null) return;
    final frame = Uint8List(pcm.length + 1);
    frame[0] = 0x01;
    frame.setRange(1, frame.length, pcm);
    try {
      channel.sink.add(frame);
    } catch (_) {
      _scheduleReconnect('audio send failed');
    }
  }

  void _setState(LinkState next) {
    if (_state == next) return;
    _state = next;
    if (!_stateCtrl.isClosed) _stateCtrl.add(next);
  }

  /// Reconnects with exponential backoff after an unexpected disconnect.
  void _scheduleReconnect(String reason) {
    if (_disposed || _reconnectTimer != null) return;
    _teardownSocket();

    _attempt++;
    // 1s, 1.7s, 2.9s ... capped at 15s so a stopped server is not hammered.
    final delayMs =
        (1000 * _pow(1.7, _attempt)).clamp(1000.0, 15000.0).toInt();
    _setState(LinkState.reconnecting);

    _reconnectTimer = Timer(Duration(milliseconds: delayMs), () {
      _reconnectTimer = null;
      if (_disposed) return;
      _openSocket();
    });

    // Surface the reason so the UI can explain an otherwise silent stall.
    _eventsCtrl.add({'kind': 'link_down', 'msg': reason});
  }

  void _teardownSocket() {
    _sub?.cancel();
    _sub = null;
    try {
      _channel?.sink.close();
    } catch (_) {
      // Already closed.
    }
    _channel = null;
    // Any command still waiting will never be answered.
    for (final completer in _pending.values) {
      if (!completer.isCompleted) {
        completer.complete(const Ack(ok: false, error: 'Disconnected'));
      }
    }
    _pending.clear();
  }

  /// Reconnects immediately, used by the "Retry" button.
  void retryNow() {
    if (_disposed) return;
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    _attempt = 0;
    _openSocket();
  }

  /// Closes the socket for good.
  Future<void> dispose() async {
    _disposed = true;
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    _teardownSocket();
    _setState(LinkState.idle);
    await _stateCtrl.close();
    await _eventsCtrl.close();
  }
}

/// Local power helper for the backoff factor, avoiding a math import.
double _pow(double base, int exp) {
  double result = 1;
  for (var i = 0; i < exp; i++) {
    result *= base;
  }
  return result;
}
