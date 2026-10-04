import 'dart:convert';
import 'dart:typed_data';

import 'package:http/http.dart' as http;

import '../models/models.dart';

/// REST client for the PC server.
///
/// Status and macro calls use short timeouts: these drive UI refreshes, and a
/// hung request would leave the interface showing stale data with no
/// explanation.
class ServerApi {
  ServerApi({required this.host, required this.port, http.Client? client})
      : _client = client ?? http.Client();

  final String host;
  final int port;
  final http.Client _client;

  /// Short timeout for status polling.
  static const Duration _short = Duration(seconds: 4);

  String get baseUrl => 'http://$host:$port';

  Uri _uri(String path, [Map<String, String>? query]) =>
      Uri.parse('$baseUrl$path').replace(queryParameters: query);

  /// Fetches the server status, or null when the PC is unreachable.
  Future<ServerStatus?> fetchStatus() async {
    try {
      final res = await _client.get(_uri('/api/panel/status')).timeout(_short);
      if (res.statusCode != 200) return null;
      return ServerStatus.fromJson(jsonDecode(res.body) as Map<String, dynamic>);
    } catch (_) {
      // An unreachable PC is an expected state, not an exception worth
      // propagating: the UI simply shows "disconnected".
      return null;
    }
  }

  /// Confirms the server is alive without touching the dispatcher.
  Future<bool> ping() async {
    try {
      final res = await _client.get(_uri('/healthz')).timeout(_short);
      return res.statusCode == 200;
    } catch (_) {
      return false;
    }
  }

  /// Fetches the macro deck.
  Future<List<Macro>> fetchMacros() async {
    try {
      final res = await _client.get(_uri('/api/macros')).timeout(_short);
      if (res.statusCode != 200) return const [];
      final list = jsonDecode(res.body) as List;
      return list
          .map((e) => Macro.fromJson((e as Map).cast<String, dynamic>()))
          .toList();
    } catch (_) {
      return const [];
    }
  }

  /// Fetches network diagnostics for the troubleshooting panel.
  Future<NetDiagnostics?> fetchDiagnostics() async {
    try {
      final res = await _client.get(_uri('/api/net')).timeout(_short);
      if (res.statusCode != 200) return null;
      return NetDiagnostics.fromJson(
          jsonDecode(res.body) as Map<String, dynamic>);
    } catch (_) {
      return null;
    }
  }

  /// The MJPEG stream URL.
  ///
  /// Frames are framed by Content-Length, which is what [MjpegFrameParser]
  /// relies on.
  ///
  /// [pin] is required, not optional: the server authenticates the desktop feed,
  /// so a request without it is refused with 401. It rides as a query parameter
  /// because this is a plain streaming GET with no other way to carry
  /// credentials, which is also what the mentor's server expects.
  Uri screenUri({required String pin, int fps = 15, int quality = 70}) =>
      _uri('/screen', {
        'pin': pin,
        'fps': '$fps',
        'quality': '$quality',
      });

  /// Checks a typed PIN against the one the PC displays on its dashboard.
  Future<bool> verifyPin(String pin) async {
    final status = await fetchStatus();
    return status != null && status.pin.isNotEmpty && status.pin == pin;
  }

  /// Regenerates the PIN on the PC.
  Future<String?> regeneratePin() async {
    try {
      final res = await _client.post(_uri('/api/pin')).timeout(_short);
      if (res.statusCode != 200) return null;
      return (jsonDecode(res.body) as Map)['pin'] as String?;
    } catch (_) {
      return null;
    }
  }

  void close() => _client.close();
}


/// One complete JPEG frame pulled out of the stream.
class MjpegFrame {
  const MjpegFrame(this.bytes, this.sequence);

  final Uint8List bytes;
  final int sequence;
}

/// The byte stream can no longer be split into frames.
///
/// Thrown rather than returned so the caller tears the connection down: a
/// parser that keeps politely waiting for a boundary that is never coming is
/// exactly how a screen viewer ends up frozen on a blank panel forever.
class MjpegFramingException implements Exception {
  const MjpegFramingException(this.message);

  final String message;

  @override
  String toString() => message;
}

/// Splits a multipart/x-mixed-replace MJPEG byte stream into JPEG frames.
///
/// WHY CONTENT-LENGTH IS THE PRIMARY SIGNAL
///
/// Each part declares its body length, so no scanning is needed and no
/// assumption about image content is made. Scanning for the JPEG SOI/EOI
/// markers instead is a trap: those byte pairs occur by chance *inside*
/// entropy-coded image data on a busy screen, so marker scanning
/// desynchronises the stream and yields garbage forever. The EOI scan is
/// therefore only a fallback for a part that omits Content-Length, and SOI is
/// used purely to verify a body already delimited by length.
///
/// WHY CONSUMPTION IS TRACKED WITH AN OFFSET
///
/// The straightforward implementation rebuilds its accumulator every chunk: it
/// copies the unconsumed tail into a fresh list, clears the original, and
/// copies the tail back. TCP delivers a ~300 KB frame in roughly 16 KB chunks,
/// so that is ~20 increasingly large copies per frame, 15 times a second, on
/// the UI isolate - quadratic in frame size, which freezes the app. Dead bytes
/// are dropped here with a single in-place `setRange`, and only when worth
/// reclaiming, keeping steady-state parsing linear.
class MjpegFrameParser {
  MjpegFrameParser({
    this.boundary = defaultBoundary,
    this.maxBufferedBytes = 8 * 1024 * 1024,
  });

  /// Must match `StreamBoundary` in server/internal/remote/mjpeg.go.
  ///
  /// Kept in one place so the constant cannot drift out of step with the
  /// server and silently stop matching, which presents as a viewer that
  /// connects successfully and then shows nothing at all.
  static const defaultBoundary = 'frameboundary';

  /// Ceiling on buffered-but-unparsed bytes. A healthy stream never
  /// approaches this; a framing mismatch would otherwise grow the buffer
  /// without limit until the process is killed.
  final int maxBufferedBytes;

  final String boundary;

  final List<int> _bytes = <int>[];

  /// Index of the first byte not yet consumed.
  int _consumed = 0;

  /// The opening delimiter exactly as it appears on the wire.
  late final List<int> _delimiter = '--$boundary'.codeUnits;

  int _frameCount = 0;

  /// Frames successfully extracted. Used to tell "the PC hung up" apart from
  /// "the connection never produced anything": different failures, different
  /// fixes, and only one of them is worth retrying.
  int get frameCount => _frameCount;

  /// Bytes held but not yet forming a complete frame.
  int get buffered => _bytes.length - _consumed;

  /// Feeds one chunk and returns any frames it completed.
  ///
  /// A chunk usually completes no frame or exactly one, but it can also carry
  /// the tail of the previous frame plus several whole ones, so this loops.
  ///
  /// Throws [MjpegFramingException] if the stream stops being parseable.
  List<MjpegFrame> addChunk(List<int> chunk) {
    _bytes.addAll(chunk);
    final frames = <MjpegFrame>[];

    while (true) {
      final start = _indexOf(_delimiter, _consumed);
      if (start < 0) {
        // No delimiter yet. Only the bytes that could still be the *start* of
        // one are kept - discarding everything would lose a delimiter split
        // across two chunks, which is exactly what happens when a frame is
        // delivered a byte at a time.
        _consumed = _bytes.length - (_delimiter.length - 1);
        if (_consumed < 0) _consumed = 0;
        _compact();
        return frames;
      }

      // Step past the CRLF (or bare LF) ending the delimiter line.
      var p = start + _delimiter.length;
      if (_bytes.length < p + 2) {
        _hold(start);
        return frames;
      }
      if (_bytes[p] == 0x0D && _bytes[p + 1] == 0x0A) {
        p += 2;
      } else if (_bytes[p] == 0x0A) {
        p += 1;
      } else {
        // Not a delimiter line after all - a byte run that merely looked like
        // one. Resynchronise one byte along instead of giving up.
        _consumed = start + 1;
        continue;
      }

  final bodyStart = _indexOfHeaderEnd(p);
      if (bodyStart < 0) {
        _hold(start);
        return frames;
      }

      final header =
          String.fromCharCodes(_bytes.sublist(p, bodyStart)).toLowerCase();
      final declared = _parseIntHeader(header, 'content-length');
      final sequence = _parseIntHeader(header, 'x-sequence');

      int bodyLength;
      if (declared >= 0) {
        // Content-Length makes the body length unambiguous, so no scan is
        // needed at all.
        if (_bytes.length - bodyStart < declared) {
          // The body is still arriving. Buffering without bound here means a
          // server declaring a frame it never sends would grow the buffer
          // until the process dies, so the ceiling is enforced on this path
          // too, not only on the EOI fallback.
          if (buffered > maxBufferedBytes) {
            throw const MjpegFramingException(
              'Stream framing lost: a declared frame never completed.',
            );
          }
          _hold(start);
          return frames;
        }
        bodyLength = declared;
      } else {
        // No declared length: fall back to the JPEG end-of-image marker.
        final eoi = _indexOfEoi(bodyStart);
        if (eoi < 0) {
          if (buffered > maxBufferedBytes) {
            throw const MjpegFramingException(
              'Stream framing lost: no frame end in the buffered data.',
            );
          }
          _hold(start);
          return frames;
        }
        bodyLength = eoi - bodyStart;
      }

      final payload = Uint8List.fromList(
        _bytes.sublist(bodyStart, bodyStart + bodyLength),
      );

      // Advance past the body plus its trailing CRLF - but only as far as the
      // bytes that have actually arrived. That final CRLF is the first thing
      // that can be missing, since the frame is complete without it, and
      // overshooting would make the next compaction compute a negative length.
      _consumed = bodyStart + bodyLength + 2;
      if (_consumed > _bytes.length) _consumed = _bytes.length;

      // A body not beginning with SOI is not a JPEG. Dropping just this part
      // and resynchronising on the next delimiter is what stops one bad frame
      // from poisoning the rest of the session.
      if (payload.length >= 2 && payload[0] == 0xFF && payload[1] == 0xD8) {
        _frameCount++;
        frames.add(MjpegFrame(payload, sequence < 0 ? _frameCount : sequence));
      }
      _compact();
    }
  }

  /// Keeps the bytes from [start] onward, waiting for more data.
  ///
  /// Rewinding [_consumed] to the delimiter is deliberate: a part may be split
  /// across any number of chunks, and the next call must resume parsing from
  /// the beginning of that part rather than from wherever it stopped.
  void _hold(int start) {
    _consumed = start;
    _compact();
  }

  /// Drops the consumed prefix in place.
  ///
  /// Amortised on purpose: compaction is the only place that copies, so it runs
  /// only when there are dead bytes to reclaim.
  void _compact() {
    if (_consumed <= 0) return;
    if (_consumed >= _bytes.length) {
      _bytes.clear();
      _consumed = 0;
      return;
    }
    final live = _bytes.length - _consumed;
    _bytes.setRange(0, live, _bytes, _consumed);
    _bytes.length = live;
    _consumed = 0;
  }

  /// Index of the first occurrence of [pattern] at or after [from], or -1.
  int _indexOf(List<int> pattern, int from) {
    final first = pattern[0];
    final limit = _bytes.length - pattern.length;
    for (var i = from < 0 ? 0 : from; i <= limit; i++) {
      if (_bytes[i] != first) continue;
      var j = 1;
      while (j < pattern.length && _bytes[i + j] == pattern[j]) {
        j++;
      }
      if (j == pattern.length) return i;
    }
    return -1;
  }

  /// Index just past the blank line that ends the part headers.
  ///
  /// That blank line is what separates headers from the image body. Without it
  /// the first body bytes get parsed as a header and the stream silently never
  /// produces a frame.
  int _indexOfHeaderEnd(int from) {
    for (var i = from; i + 1 < _bytes.length; i++) {
      if (_bytes[i] == 0x0D && _bytes[i + 1] == 0x0A) {
        if (i + 3 < _bytes.length &&
            _bytes[i + 2] == 0x0D &&
            _bytes[i + 3] == 0x0A) {
          return i + 4;
        }
        continue;
      }
      if (_bytes[i] == 0x0A && _bytes[i + 1] == 0x0A) return i + 2;
    }
    return -1;
  }

  /// Index just past the JPEG end-of-image marker at or after [from], or -1.
  ///
  /// Only consulted when a part omits Content-Length, so the linear scan is an
  /// acceptable fallback rather than the main path.
  int _indexOfEoi(int from) {
    for (var i = from; i + 1 < _bytes.length; i++) {
      if (_bytes[i] == 0xFF && _bytes[i + 1] == 0xD9) return i + 2;
    }
    return -1;
  }

  /// Reads an integer header, or -1 when absent or unparsable.
  int _parseIntHeader(String header, String name) {
    final match = RegExp('$name:\\s*(\\d+)').firstMatch(header);
    if (match == null) return -1;
    return int.tryParse(match.group(1)!) ?? -1;
  }

  /// Drops buffered data, used when the stream restarts.
  void reset() {
    _bytes.clear();
    _consumed = 0;
    _frameCount = 0;
  }
}
