import 'dart:io';

import 'package:device_info_plus/device_info_plus.dart';

/// The phone's own name, read from the OS (e.g. "OPPO CPH1923").
///
/// This is what the PC dashboard shows per connected device, replacing the
/// misleading fallback (the PC's own advertised name reflected back). It is
/// strictly informational: connection, pairing and PIN logic never depend on
/// it, so a failure here degrades to an empty string and the caller falls
/// back to the previous label - never to an error.
///
/// No permission is required: only the public model/manufacturer fields are
/// read, never identifiers (no Android ID, no serial).
class DeviceNames {
  /// Reads the OS-reported device name, or '' when unavailable.
  ///
  /// Never throws: every platform call is guarded because a name must not
  /// break pairing on an exotic device.
  static Future<String> auto() async {
    try {
      if (Platform.isAndroid) {
        final info = await DeviceInfoPlugin().androidInfo;
        return _join(info.manufacturer, info.model);
      }
      if (Platform.isIOS) {
        final info = await DeviceInfoPlugin().iosInfo;
        final name = info.name.trim();
        if (name.isNotEmpty) return _clean(name);
        return _join('Apple', info.model);
      }
    } catch (_) {
      // Fall through to ''.
    }
    return '';
  }

  /// "OPPO" + "CPH1923" -> "OPPO CPH1923", without duplicating a
  /// manufacturer the model already contains.
  static String _join(String maker, String model) {
    final m = _clean(maker);
    final d = _clean(model);
    if (d.isEmpty) return m;
    if (m.isEmpty) return d;
    if (d.toLowerCase().startsWith(m.toLowerCase())) return d;
    return _clean('$m $d');
  }

  /// Single line, bounded - mirroring what the server enforces (64 chars).
  static String _clean(String name) {
    var clean = name.trim().replaceAll(RegExp(r'\s+'), ' ');
    if (clean.length > 64) clean = clean.substring(0, 64).trim();
    return clean;
  }
}
