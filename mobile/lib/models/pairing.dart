import 'package:shared_preferences/shared_preferences.dart';

/// A saved pairing with one PC.
///
/// The PIN is stored locally so the app can reconnect without re-pairing on
/// every launch. This is a deliberate trade-off: the PIN grants full control
/// of the PC's keyboard and mouse, so it is kept in the platform's private app
/// storage and never logged.
class Pairing {
  const Pairing({
    required this.host,
    required this.port,
    required this.pin,
    this.label = '',
  });

  factory Pairing.fromJson(Map<String, dynamic> json) => Pairing(
        host: json['host'] as String? ?? '',
        port: (json['port'] as num?)?.toInt() ?? 0,
        pin: json['pin'] as String? ?? '',
        label: json['label'] as String? ?? '',
      );

  final String host;
  final int port;
  final String pin;
  final String label;

  Map<String, dynamic> toJson() => {
        'host': host,
        'port': port,
        'pin': pin,
        'label': label,
      };

  /// A short human-readable name for this PC.
  String get displayName => label.isEmpty ? host : label;

  /// The base URL of the server's HTTP API.
  String get baseUrl => 'http://$host:$port';

  /// Loads the saved pairing, or null when nothing is stored.
  ///
  /// A corrupt entry is treated as absent so a bad write cannot permanently
  /// block the user from the app.
  static Pairing? load(SharedPreferences prefs) {
    final raw = prefs.getString(_key);
    if (raw == null || raw.isEmpty) return null;
    try {
      final decoded = _decode(raw);
      if (decoded.host.isEmpty) return null;
      return decoded;
    } catch (_) {
      return null;
    }
  }

  /// Persists this pairing.
  Future<void> save(SharedPreferences prefs) =>
      prefs.setString(_key, _encode(toJson()));

  /// Removes the saved pairing.
  static Future<void> clear(SharedPreferences prefs) =>
      prefs.remove(_key);

  /// Copies a pairing with a different PIN, used after a server-side
  /// regeneration.
  Pairing withPin(String newPin) => Pairing(
        host: host,
        port: port,
        pin: newPin,
        label: label,
      );

  static const String _key = 'smart_remote.pairing';

  static String _encode(Map<String, dynamic> json) =>
      Uri(queryParameters: {
        'host': json['host'] as String,
        'port': '${json['port']}',
        'pin': json['pin'] as String,
        if ((json['label'] as String? ?? '').isNotEmpty)
          'label': json['label'] as String,
      }).query;

  static Pairing _decode(String raw) {
    final query = Uri.splitQueryString(raw);
    return Pairing(
      host: query['host'] ?? '',
      port: int.tryParse(query['port'] ?? '') ?? 0,
      pin: query['pin'] ?? '',
      label: query['label'] ?? '',
    );
  }
}
