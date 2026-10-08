import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../core/remote_link.dart';
import '../core/device_name.dart';
import '../core/server_api.dart';
import '../models/models.dart';
import '../models/pairing.dart';

/// Stores the current pairing and rebuilds the link when it changes.
class PairingNotifier extends StateNotifier<AsyncValue<Pairing?>> {
  PairingNotifier({required this.onLinkChanged})
    : super(const AsyncValue.data(null));

  final void Function() onLinkChanged;

  RemoteLink? _link;
  Timer? _statusTimer;

  /// The live link, or null when not paired.
  RemoteLink? get link => _link;

  /// Name this phone reports to the PC (shown on the dashboard device list).
  ///
  /// Empty means "not chosen": connections fall back to the pairing label so
  /// existing installs behave exactly as before. It is read at connect time,
  /// so renaming applies on the next connection and never disturbs a live
  /// session.
  String _deviceName = '';
  String get deviceName => _deviceName;

  /// OS-reported phone name (e.g. "OPPO CPH1923"), resolved once and cached.
  ///
  /// Local call, milliseconds, no permissions - but still done once: a name
  /// must never add latency to every reconnect.
  String? _autoName;
  bool _autoTried = false;

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
    _deviceName = _sanitizeDeviceName(prefs.getString(_deviceKey));
    final saved = Pairing.load(prefs);
    if (saved == null) return;
    state = AsyncValue.data(saved);
    unawaited(_connect(saved));
  }

  /// Loads the saved phone name (used to prefill the pairing form).
  Future<String> loadDeviceName() async {
    final prefs = await SharedPreferences.getInstance();
    _deviceName = _sanitizeDeviceName(prefs.getString(_deviceKey));
    return _deviceName;
  }

  /// Persists the phone name. Takes effect on the next connection; the
  /// active link is deliberately left alone so renaming never drops input.
  Future<void> setDeviceName(String name) async {
    _deviceName = _sanitizeDeviceName(name);
    final prefs = await SharedPreferences.getInstance();
    if (_deviceName.isEmpty) {
      await prefs.remove(_deviceKey);
    } else {
      await prefs.setString(_deviceKey, _deviceName);
    }
  }

  /// A dashboard row stays readable: one line, bounded, matching what the
  /// server enforces (it truncates at 64 as defence in depth).
  static String _sanitizeDeviceName(String? name) {
    var clean = (name ?? '').trim().replaceAll(RegExp(r'\s+'), ' ');
    if (clean.length > 64) clean = clean.substring(0, 64).trim();
    return clean;
  }

  static const String _deviceKey = 'smart_remote.device_name';

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
    onLinkChanged();

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
    onLinkChanged();

    // Poll status while paired so the header can show client count, input
    // blocking and stream busy state without a server push for each.
    _statusTimer = Timer.periodic(const Duration(seconds: 5), (_) async {
      final status = await _apiFor(pairing).fetchStatus();
      if (status != null) _lastStatus = status;
    });

    await link.connect(
        pin: pairing.pin, deviceName: await _resolvedDeviceName(pairing));
  }

  /// The name sent in the auth frame, by priority:
  /// 1. the user's explicit choice (rename wins over everything),
  /// 2. the phone's own OS-reported name (the actual device),
  /// 3. the pairing label exactly as before (zero behaviour change when
  ///    neither is available, e.g. very old installs or exotic devices).
  Future<String> _resolvedDeviceName(Pairing pairing) async {
    if (_deviceName.isNotEmpty) return _deviceName;
    if (!_autoTried) {
      _autoTried = true;
      _autoName = await DeviceNames.auto();
    }
    final auto = (_autoName ?? '').trim();
    if (auto.isNotEmpty) return auto;
    return pairing.label;
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
      (ref) => PairingNotifier(
        onLinkChanged: () {
          ref.read(remoteLinkGenerationProvider.notifier).state++;
        },
      ),
    );

/// Changes whenever the active RemoteLink instance is replaced.
final remoteLinkGenerationProvider = StateProvider<int>((ref) => 0);

/// The active control link, or null when not paired.
final remoteLinkProvider = Provider<RemoteLink?>((ref) {
  // Watching the pairing keeps this in sync with pair and unpair.
  ref.watch(pairingProvider);
  // Pairing state may be unchanged while reconnect() replaces the socket link.
  ref.watch(remoteLinkGenerationProvider);
  return ref.read(pairingProvider.notifier).link;
});

/// Live lifecycle state for the active control socket.
///
/// The pairing can remain unchanged while the socket drops and reconnects, so
/// screens that only watch [remoteLinkProvider] otherwise keep stale controls.
final remoteLinkStateProvider = StreamProvider.autoDispose<LinkState>((ref) {
  final link = ref.watch(remoteLinkProvider);
  if (link == null) return Stream.value(LinkState.idle);

  return Stream<LinkState>.multi((controller) {
    controller.add(link.state);
    final subscription = link.states.listen(
      controller.add,
      onError: controller.addError,
      onDone: controller.close,
    );
    controller.onCancel = subscription.cancel;
  });
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
