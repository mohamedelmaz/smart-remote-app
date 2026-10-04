import 'dart:async';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:multicast_dns/multicast_dns.dart';

import '../core/server_api.dart';
import '../models/models.dart';

/// Default port, matching the Go server.
const int kDefaultPort = 9520;

/// How often the pairing screen rescans for mDNS devices.
const Duration kDiscoveryInterval = Duration(seconds: 30);

/// Upper bound on concurrent subnet probes during a manual scan.
///
/// A /24 sweep with unbounded parallelism looks like a port scan to the
/// network and saturates a phone's radio, so concurrency is capped.
const int kMaxScanWorkers = 32;

/// The DNS-SD service the Go server advertises.
const String kServiceType = '_smartremote._tcp.local';

/// Discovers servers over mDNS, with a unicast subnet scan as a fallback.
class DiscoveryNotifier
    extends StateNotifier<AsyncValue<List<DiscoveredDevice>>> {
  DiscoveryNotifier() : super(const AsyncValue.loading()) {
    start();
  }

  MDnsClient? _client;
  bool _running = false;
  Timer? _rescan;

  /// Instance name -> device, keyed by the SRV instance label.
  final Map<String, DiscoveredDevice> _byInstance = {};

  void start() {
    _running = true;
    unawaited(_browseOnce());
    // A rescan keeps the list fresh: the PC may have started after the app.
    _rescan = Timer.periodic(kDiscoveryInterval, (_) => _browseOnce());
  }

  /// Performs one PTR query and resolves whatever it finds.
  Future<void> _browseOnce() async {
    if (!_running) return;
    try {
      // MDnsClient supports only one-shot queries and must be started before
      // lookup, so a fresh client per sweep avoids a stale cache and the
      // "must be started" error after a stop().
      final previous = _client;
      if (previous != null) {
        try {
          previous.stop();
        } catch (_) {
          // stop() throws if called while starting; the socket is discarded
          // either way.
        }
      }
      final client = MDnsClient();
      _client = client;
      await client.start();

      // Step 1: find instances of our service type.
      final instances = <String>[];
      await for (final ptr in client.lookup<PtrResourceRecord>(
        ResourceRecordQuery.serverPointer(kServiceType),
        timeout: const Duration(seconds: 4),
      )) {
        final name = ptr.domainName;
        if (!instances.contains(name)) instances.add(name);
      }

      // Steps 2 and 3: resolve each instance's SRV and TXT records.
      for (final instance in instances) {
        await _resolveInstance(client, instance);
      }

      state = AsyncValue.data(List.unmodifiable(_byInstance.values));
    } catch (_) {
      // Discovery failing is not fatal: manual entry and the subnet scan
      // both still work, so the UI must never block on mDNS.
      state = AsyncValue.data(List.unmodifiable(_byInstance.values));
    }
  }

  /// Resolves SRV (host and port) and TXT (pin and name) for one instance.
  Future<void> _resolveInstance(MDnsClient client, String instance) async {
    String? host;
    var port = 0;

    try {
      await for (final srv in client.lookup<SrvResourceRecord>(
        ResourceRecordQuery.service(instance),
        timeout: const Duration(seconds: 3),
      )) {
        host = srv.target.toString();
        port = srv.port;
      }
    } catch (_) {
      // Left unresolved; the instance is dropped below.
    }
    if (host == null || host.isEmpty || port == 0) return;

    final txt = <String, String>{};
    try {
      await for (final record in client.lookup<TxtResourceRecord>(
        ResourceRecordQuery.text(instance),
        timeout: const Duration(seconds: 3),
      )) {
        // TxtResourceRecord exposes a single text string. It may hold one
        // "key=value" pair or several concatenated without separators, so
        // split on commas first and fall back to scanning for key=value
        // pairs. Both forms occur in the wild depending on the responder.
        for (final chunk in record.text.split(',')) {
          final match =
              RegExp(r'([A-Za-z][A-Za-z0-9_]*)=([^=]*)').firstMatch(chunk);
          if (match != null) {
            txt[match.group(1)!] = match.group(2)!.trim();
          }
        }
      }
    } catch (_) {
      // TXT is optional: the PIN can still be typed by hand.
    }

    _byInstance[instance] = DiscoveredDevice(
      name: txt['name'] ?? instance.split('.').first,
      host: host,
      port: port,
      pin: txt['pin'] ?? '',
      service: instance,
    );
    state = AsyncValue.data(List.unmodifiable(_byInstance.values));
  }

  /// Probes the local subnet for servers that are not advertising mDNS.
  ///
  /// This is the fallback for a Public network profile, where multicast is
  /// blocked but unicast still works. Only the server's own port is probed,
  /// and at most [kMaxScanWorkers] probes run at once.
  Future<List<DiscoveredDevice>> scanSubnet({String? baseHost}) async {
    final base = baseHost ?? await _guessLocalSubnetBase();
    if (base == null) return const [];

    final results = <DiscoveredDevice>[];
    final queue = _AddressQueue();
    for (var host = 1; host <= 254; host++) {
      queue.add('$base.$host');
    }

    Future<void> worker() async {
      while (true) {
        final host = queue.next();
        if (host == null) return;
        final device = await _probe(host);
        if (device != null) results.add(device);
      }
    }

    // A fixed pool bounds concurrency and keeps the radio from being flooded.
    await Future.wait([
      for (var i = 0; i < kMaxScanWorkers; i++) worker(),
    ]);

    state = AsyncValue.data([..._byInstance.values, ...results]);
    return results;
  }

  /// Probes one host for the server's status endpoint.
  Future<DiscoveredDevice?> _probe(String host) async {
    final api = ServerApi(host: host, port: kDefaultPort);
    try {
      // A short timeout: a /24 sweep with a long per-host timeout would take
      // minutes to complete.
      final status = await api
          .fetchStatus()
          .timeout(const Duration(milliseconds: 900));
      if (status == null || !status.ok) return null;
      return DiscoveredDevice(
        name: status.hostname.isEmpty ? host : status.hostname,
        host: host,
        port: kDefaultPort,
        pin: status.pin,
        service: 'subnet-probe',
      );
    } catch (_) {
      return null;
    }
  }

  /// Derives the first three octets of the local address, e.g. "192.168.1".
  Future<String?> _guessLocalSubnetBase() async {
    try {
      final interfaces = await NetworkInterface.list(
        includeLoopback: false,
        type: InternetAddressType.IPv4,
      );
      for (final iface in interfaces) {
        for (final addr in iface.addresses) {
          final parts = addr.address.split('.');
          if (parts.length == 4) {
            return '${parts[0]}.${parts[1]}.${parts[2]}';
          }
        }
      }
    } catch (_) {
      return null;
    }
    return null;
  }

  @override
  void dispose() {
    _running = false;
    _rescan?.cancel();
    try {
      _client?.stop();
    } catch (_) {
      // Already stopped.
    }
    super.dispose();
  }
}

/// A bounded work queue shared by the scan workers.
class _AddressQueue {
  final List<String> _items = [];
  int _next = 0;

  void add(String item) => _items.add(item);

  String? next() {
    if (_next >= _items.length) return null;
    return _items[_next++];
  }
}

/// Devices discovered on the network.
final discoveryProvider =
    StateNotifierProvider<DiscoveryNotifier, AsyncValue<List<DiscoveredDevice>>>(
  (ref) => DiscoveryNotifier(),
);

