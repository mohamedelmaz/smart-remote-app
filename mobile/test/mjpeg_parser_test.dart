import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:smart_remote/core/server_api.dart';

/// Builds the exact byte sequence `MJPEGWriter.WriteFrame` puts on the wire.
///
/// Reproducing the server's framing here rather than hand-writing fixtures is
/// deliberate: the parser and the writer are two halves of one contract, and a
/// hand-written fixture would keep passing even if the server changed its
/// framing. If the two ever disagree, these tests must fail.
List<int> part(Uint8List image, int sequence) {
  final head = StringBuffer()
    ..write('--${MjpegFrameParser.defaultBoundary}\r\n')
    ..write('Content-Type: image/jpeg\r\n')
    ..write('Content-Length: ${image.length}\r\n')
    ..write('X-Sequence: $sequence\r\n')
    ..write('\r\n');
  return <int>[...head.toString().codeUnits, ...image, 0x0D, 0x0A];
}

/// A minimal but structurally valid JPEG: SOI, some payload, EOI.
Uint8List jpeg({int payload = 64, int seed = 0}) {
  return Uint8List.fromList(<int>[
    0xFF,
    0xD8,
    for (var i = 0; i < payload; i++) (i * 7 + seed) & 0xFF,
    0xFF,
    0xD9,
  ]);
}

void main() {
  group('MjpegFrameParser', () {
    test('extracts a single frame from one chunk', () {
      final parser = MjpegFrameParser();
      final bytes = jpeg();

      final frames = parser.addChunk(part(bytes, 1));

      expect(frames, hasLength(1));
      expect(frames.first.bytes, bytes);
      expect(frames.first.sequence, 1);
    });

    test('extracts several frames arriving in one chunk', () {
      // A chunk can carry the tail of the previous frame plus several whole
      // ones whenever the network batches, so a single-shot parser would
      // silently fall behind by one frame per chunk.
      final parser = MjpegFrameParser();
      final stream = <int>[
        ...part(jpeg(seed: 1), 1),
        ...part(jpeg(seed: 2), 2),
        ...part(jpeg(seed: 3), 3),
      ];

      final frames = parser.addChunk(stream);

      expect(frames, hasLength(3));
      expect(frames.map((f) => f.sequence), [1, 2, 3]);
    });

    test('splits a byte-at-a-time delivery correctly', () {
      // The harshest framing case: this exercises every possible split point,
      // including ones landing inside the delimiter and inside the headers.
      final bytes = jpeg(payload: 40, seed: 5);
      final wire = part(bytes, 3);
      final parser = MjpegFrameParser();

      final frames = <MjpegFrame>[];
      for (final b in wire) {
        frames.addAll(parser.addChunk(<int>[b]));
      }

      expect(frames, hasLength(1));
      expect(frames.first.bytes, bytes);
      expect(frames.first.sequence, 3);
    });

    test('reassembles a frame split at awkward offsets', () {
      // Mirrors what a real network does: a ~300KB frame arrives in pieces and
      // the boundaries land wherever they land.
      final bytes = jpeg(payload: 3000);
      final wire = part(bytes, 7);
      final parser = MjpegFrameParser();

      final collected = <MjpegFrame>[];
      var offset = 0;
      for (final size in [1, 3, 7, 2, 17, 40, 5, 100, 250, 999, 4000]) {
        final end = offset + size;
        if (end > wire.length) break;
        collected.addAll(parser.addChunk(wire.sublist(offset, end)));
        offset = end;
      }
      collected.addAll(parser.addChunk(wire.sublist(offset)));

      expect(collected, hasLength(1));
      expect(collected.first.bytes, bytes);
      expect(collected.first.sequence, 7);
    });

    test('does not mistake JPEG payload for a boundary', () {
      // The reason Content-Length is the primary signal. A delimiter-looking
      // byte run inside entropy-coded data must not desynchronise the stream.
      final decoy = Uint8List.fromList(<int>[
        0xFF,
        0xD8,
        ...'--frameboundary\r\nnot a real part'.codeUnits,
        0xFF,
        0xD9,
      ]);

      final frames = MjpegFrameParser().addChunk(part(decoy, 1));

      expect(frames, hasLength(1));
      expect(frames.first.bytes, decoy);
    });

    test('handles consecutive frames with no gap between them', () {
      final parser = MjpegFrameParser();
      final a = jpeg(seed: 11);
      final b = jpeg(seed: 22);

      final frames = parser.addChunk(<int>[...part(a, 1), ...part(b, 2)]);

      expect(frames, hasLength(2));
      expect(frames[0].bytes, a);
      expect(frames[1].bytes, b);
    });

    test('falls back to the EOI marker when Content-Length is absent', () {
      // Hand-rolled or third-party servers often omit Content-Length. Falling
      // back keeps the viewer working there instead of showing a black panel.
      final bytes = jpeg(payload: 20);
      final wire = <int>[
        ...'--frameboundary\r\nContent-Type: image/jpeg\r\n\r\n'.codeUnits,
        ...bytes,
        0x0D,
        0x0A,
      ];

      final frames = MjpegFrameParser().addChunk(wire);

      expect(frames, hasLength(1));
      expect(frames.first.bytes, bytes);
    });

    test('drops a part that is not a JPEG and resynchronises', () {
      // One corrupt frame must not poison the rest of the session.
      final parser = MjpegFrameParser();
      final good = jpeg(seed: 9);

      final frames = parser.addChunk(<int>[
        ...part(Uint8List.fromList(<int>[1, 2, 3, 4, 5]), 1),
        ...part(good, 2),
      ]);

      expect(frames, hasLength(1));
      expect(frames.first.bytes, good);
      expect(frames.first.sequence, 2);
    });

    test('ignores leading noise before the first boundary', () {
      final bytes = jpeg();

      final frames = MjpegFrameParser().addChunk(<int>[
        ...'HTTP/1.1 200 OK\r\n\r\n'.codeUnits,
        ...part(bytes, 4),
      ]);

      expect(frames, hasLength(1));
      expect(frames.first.bytes, bytes);
    });

    test('counts frames so a hang-up can be told from a dead socket', () {
      final parser = MjpegFrameParser();
      expect(parser.frameCount, 0);

      parser.addChunk(part(jpeg(), 1));

      expect(parser.frameCount, 1);
    });

    test('resets cleanly for a reconnect', () {
      final parser = MjpegFrameParser();
      parser.addChunk(part(jpeg(), 1));

      parser.reset();

      expect(parser.frameCount, 0);
      expect(parser.buffered, 0);
    });

    test('throws when a declared frame never completes', () {
      // A server that announces a Content-Length it never sends would
      // otherwise grow the buffer until the process dies. Tearing the
      // connection down is the only honest outcome.
      final parser = MjpegFrameParser(maxBufferedBytes: 512);
      final head =
          '--frameboundary\r\nContent-Length: 999999\r\n\r\n'.codeUnits;

      expect(
        () => parser.addChunk(head),
        returnsNormally,
        reason: 'the ceiling is not reached by the header alone',
      );

      expect(
        () => parser.addChunk(List<int>.filled(1024, 0x41)),
        throwsA(isA<MjpegFramingException>()),
      );
    });

    test('matches the boundary the Go server actually writes', () {
      // Guards the constant coupling this file to
      // server/internal/remote/mjpeg.go. If the server's boundary changes and
      // this is not updated, the viewer connects and shows nothing at all -
      // exactly the failure being fixed.
      expect(MjpegFrameParser.defaultBoundary, 'frameboundary');
    });
  });

  group('screenUri', () {
    final api = ServerApi(host: '10.0.0.5', port: 9520);

    test('carries the PIN so the stream is authenticated', () {
      // The desktop feed is the whole screen of the PC, so the server refuses
      // it without a PIN. A viewer that forgot to send one would connect and be
      // answered with a 401 that looks like a dead server.
      final uri = api.screenUri(pin: '123456', fps: 15, quality: 70);

      expect(uri.queryParameters['pin'], '123456');
      expect(uri.queryParameters['fps'], '15');
      expect(uri.queryParameters['quality'], '70');
      expect(uri.path, '/screen');
    });

    test('URL-encodes a PIN rather than corrupting the query', () {
      // A malformed or hand-edited PIN must not be able to inject extra query
      // parameters or truncate the request.
      final uri = api.screenUri(pin: 'a b&x=1');

      expect(uri.queryParameters['pin'], 'a b&x=1');
      expect(uri.queryParameters.containsKey('x'), isFalse);
    });
  });
}