/// Wire models shared with the Go server.
///
/// Field names mirror the server's JSON tags exactly. A mismatch here is the
/// most likely cause of a silently empty macro list or a null PIN, so the
/// tags are kept adjacent to each field rather than relying on camelCase
/// conversion.
library;

/// A macro button from the server.
class Macro {
  const Macro({
    required this.id,
    required this.label,
    required this.icon,
    required this.kind,
    required this.builtIn,
    this.keys = const [],
    this.mods = const [],
    this.shell = '',
  });

  factory Macro.fromJson(Map<String, dynamic> json) => Macro(
        id: json['id'] as String? ?? '',
        // The label is the authoritative identifier. Falling back to the id
        // keeps a button readable even if the server omits the label.
        label: (json['label'] as String?)?.trim().isNotEmpty == true
            ? json['label'] as String
            : (json['id'] as String? ?? 'Macro'),
        icon: json['icon'] as String? ?? '',
        kind: json['kind'] as String? ?? 'keys',
        builtIn: json['builtIn'] as bool? ?? false,
        keys: (json['keys'] as List?)?.cast<String>() ?? const [],
        mods: (json['mods'] as List?)?.cast<String>() ?? const [],
        shell: json['shell'] as String? ?? '',
      );

  final String id;
  final String label;
  final String icon;
  final String kind;
  final bool builtIn;
  final List<String> keys;
  final List<String> mods;
  final String shell;

  /// True when the macro runs a shell command rather than a keystroke.
  bool get isShell => kind == 'shell';
}

/// Connection state reported by /api/panel/status.
class ServerStatus {
  const ServerStatus({
    required this.ok,
    required this.pin,
    required this.host,
    required this.port,
    required this.hostname,
    required this.version,
    required this.clients,
    required this.inputBlocked,
    required this.streamBusy,
    required this.macroCount,
    this.diagnosis = '',
    this.width = 0,
    this.height = 0,
  });

  factory ServerStatus.fromJson(Map<String, dynamic> json) {
    final display = (json['display'] as Map?)?.cast<String, dynamic>() ?? const {};
    return ServerStatus(
      ok: json['ok'] as bool? ?? false,
      pin: json['pin'] as String? ?? '',
      host: json['host'] as String? ?? '',
      port: (json['port'] as num?)?.toInt() ?? 0,
      hostname: json['hostname'] as String? ?? '',
      version: json['version'] as String? ?? '',
      clients: (json['clients'] as num?)?.toInt() ?? 0,
      inputBlocked: json['inputBlocked'] as bool? ?? false,
      streamBusy: json['streamBusy'] as bool? ?? false,
      macroCount: (json['macroCount'] as num?)?.toInt() ?? 0,
      diagnosis: json['diagnosis'] as String? ?? '',
      width: (display['width'] as num?)?.toInt() ?? 0,
      height: (display['height'] as num?)?.toInt() ?? 0,
    );
  }

  final bool ok;
  final String pin;
  final String host;
  final int port;
  final String hostname;
  final String version;
  final int clients;
  final bool inputBlocked;
  final bool streamBusy;
  final int macroCount;

  /// Explanation from the server's circuit breaker when input is blocked.
  final String diagnosis;
  final int width;
  final int height;
}

/// Network diagnostics from /api/net.
class NetDiagnostics {
  const NetDiagnostics({
    required this.primaryIp,
    required this.firewallOk,
    required this.profileIsPrivate,
    required this.mdnsActive,
    required this.advice,
  });

  factory NetDiagnostics.fromJson(Map<String, dynamic> json) => NetDiagnostics(
        primaryIp: json['primaryIp'] as String? ?? '',
        firewallOk: json['firewallOk'] as bool? ?? false,
        profileIsPrivate: json['networkProfileIsPrivate'] as bool? ?? false,
        mdnsActive: json['mdnsActive'] as bool? ?? false,
        advice: (json['advice'] as List?)?.cast<String>() ?? const [],
      );

  final String primaryIp;
  final bool firewallOk;
  final bool profileIsPrivate;
  final bool mdnsActive;

  /// Actionable next steps, phrased so the user can act without a manual.
  final List<String> advice;
}

/// A device found by mDNS discovery.
class DiscoveredDevice {
  const DiscoveredDevice({
    required this.name,
    required this.host,
    required this.port,
    required this.pin,
    this.service = '',
  });

  factory DiscoveredDevice.fromService({
    required String name,
    required String host,
    required String service,
    required Map<String, String> txt,
  }) {
    return DiscoveredDevice(
      name: txt['name'] ?? name,
      host: host,
      // The TXT port is authoritative; the SRV port is the fallback.
      port: int.tryParse(txt['port'] ?? '') ?? 0,
      pin: txt['pin'] ?? '',
      service: service,
    );
  }

  final String name;
  final String host;
  final int port;
  final String pin;
  final String service;

  /// The base URL of the device's HTTP API.
  String get baseUrl => 'http://$host:$port';
}
