import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../core/remote_link.dart';
import '../core/server_api.dart';
import '../models/models.dart';
import '../models/pairing.dart';

/// Stores the current pairing and rebuilds the link when it changes.
class PairingNotifier extends StateNotifier<AsyncValue<Pairing?>> {
  PairingNotifier() : super(const AsyncValue.data(null));

  RemoteLink? _link;
  Timer? _statusTimer;

  /// The live link, or null when not paired.
  RemoteLink? get link => _link;

  ServerStatus? _lastStatus;

  /// The most recent status seen by the background poller.
  ServerStatus? get lastStatus => _lastStatus;

  ServerApi _apiFor(Pairing p) => ServerApi(host: p.host, port: p.port);

  /// Loads a saved pairing and connects if one exists.
  ///
  /// The socket connect is deliberately *not* awaited. The stored pairing is
  /// already known at that point, and awaiting a connect to a PC that may be
  /// asleep would hold the app on its splash screen until the WebSocket
  /// timeout. [RemoteLink] reconnects on its own, so the UI can render the
  /// remote shell immediately and show the real link state in the header.
  Future<void> restore() async {
    final prefs = await SharedPreferences.getInstance();
    final saved = Pairing.load(prefs);
    if (saved == null) return;
    state = AsyncValue.data(saved);
    unawaited(_connect(saved));
  }

  /// Pairs with a device and starts the control link.
  ///
  /// Returns null on success, or a message explaining the failure. The pairing
  /// is only stored once the PC has been reached and the PIN confirmed, so a
  /// failed attempt never leaves a broken pairing behind.
  Future<String?> pair(Pairing pairing) async {
    final api = _apiFor(pairing);

    final status = await api.fetchStatus();
    if (status == null) {
      return 'Cannot reach ${pairing.host}:${pairing.port}. Check the PC is '
          'running and both devices are on the same network.';
    }
    if (pairing.pin.isNotEmpty && status.pin != pairing.pin) {
      return 'Incorrect PIN. The PC currently shows ${status.pin}.';
    }

    final prefs = await SharedPreferences.getInstance();
    await pairing.save(prefs);

    state = AsyncValue.data(pairing);
    await _connect(pairing);
    return null;
  }

  /// Disconnects and forgets the saved pairing.
  Future<void> unpair() async {
    _statusTimer?.cancel();
    _statusTimer = null;
    await _link?.dispose();
    _link = null;

    final prefs = await SharedPreferences.getInstance();
    await Pairing.clear(prefs);
    state = const AsyncValue.data(null);
  }

  /// Re-establishes the link after a failure.
  Future<void> reconnect() async {
    final pairing = state.valueOrNull;
    if (pairing == null) return;
    await _link?.dispose();
    await _connect(pairing);
  }

  Future<void> _connect(Pairing pairing) async {
    await _link?.dispose();
    _statusTimer?.cancel();

    final link = RemoteLink(host: pairing.host, port: pairing.port);
    _link = link;

    // Poll status while paired so the header can show client count, input
    // blocking and stream busy state without a server push for each.
    _statusTimer = Timer.periodic(const Duration(seconds: 5), (_) async {
      final status = await _apiFor(pairing).fetchStatus();
      if (status != null) _lastStatus = status;
    });

    await link.connect(pin: pairing.pin, deviceName: pairing.label);
  }

  @override
  void dispose() {
    _statusTimer?.cancel();
    _link?.dispose();
    super.dispose();
  }
}

/// The pairing state and the live control link.
final pairingProvider =
    StateNotifierProvider<PairingNotifier, AsyncValue<Pairing?>>(
  (ref) => PairingNotifier(),
);

/// The active control link, or null when not paired.
final remoteLinkProvider = Provider<RemoteLink?>((ref) {
  // Watching the pairing keeps this in sync with pair and unpair.
  ref.watch(pairingProvider);
  return ref.read(pairingProvider.notifier).link;
});

/// Latest server status, refreshed on demand while paired.
final serverStatusProvider = FutureProvider<ServerStatus?>((ref) async {
  final pairing = ref.watch(pairingProvider).valueOrNull;
  if (pairing == null) return null;
  return ServerApi(host: pairing.host, port: pairing.port).fetchStatus();
});

/// The macro deck, reloadable from the deck screen.
final macrosProvider = FutureProvider<List<Macro>>((ref) async {
  final pairing = ref.watch(pairingProvider).valueOrNull;
  if (pairing == null) return const [];
  return ServerApi(host: pairing.host, port: pairing.port).fetchMacros();
});

/// Network diagnostics for the pairing screen's help panel.
final diagnosticsProvider = FutureProvider<NetDiagnostics?>((ref) async {
  final pairing = ref.watch(pairingProvider).valueOrNull;
  if (pairing == null) return null;
  return ServerApi(host: pairing.host, port: pairing.port).fetchDiagnostics();
});
