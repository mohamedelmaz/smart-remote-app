import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smart_remote/widgets/zoomable_frame.dart';

/// A 1x1 opaque PNG used to mint real ui.Images without network access.
Uint8List _pngBytes() => Uint8List.fromList(<int>[
      0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, //
      0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, //
      0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, //
      0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, //
      0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, //
      0x54, 0x08, 0xD7, 0x63, 0xF8, 0xFF, 0xFF, 0x3F, //
      0x00, 0x05, 0xFE, 0x02, 0xFE, 0xDC, 0xCC, 0x59, //
      0xE7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, //
      0x44, 0xAE, 0x42, 0x60, 0x82, //
    ]);

Future<ui.Image> _decode(Uint8List bytes) async {
  final codec = await ui.instantiateImageCodec(bytes);
  try {
    final info = await codec.getNextFrame();
    return info.image;
  } finally {
    codec.dispose();
  }
}

/// Pumps [ZoomableFrame] with [frame] inside a fixed-size panel, which is
/// what the GlassPanel viewport gives it in production.
Future<void> _pumpFrame(WidgetTester tester, ui.Image frame) async {
  await tester.pumpWidget(
    MediaQuery(
      data: const MediaQueryData(size: Size(400, 800)),
      child: MaterialApp(
        home: Scaffold(
          body: SizedBox(
            width: 300,
            height: 400,
            child: ZoomableFrame(frame: frame),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

/// Performs a full double-tap at [center] the way [GestureDetector.onDoubleTap]
/// expects: two taps separated by less than the double-tap timeout.
Future<void> _doubleTap(WidgetTester tester, Offset center) async {
  await tester.tapAt(center);
  await tester.pump(const Duration(milliseconds: 60));
  await tester.tapAt(center);
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('double-tap zoom survives a new frame (no reset)', (tester) async {
    final first = await tester.runAsync(() => _decode(_pngBytes()));
    final second = await tester.runAsync(() => _decode(_pngBytes()));
    expect(first, isNotNull);
    expect(second, isNotNull);
    addTearDown(() {
      first!.dispose();
      second!.dispose();
    });

    await _pumpFrame(tester, first!);
    expect(find.byType(InteractiveViewer), findsOneWidget);
    // Not zoomed yet: no reset pill.
    expect(find.textContaining('x'), findsNothing);

    // Double-tap to zoom in.
    final center = tester.getCenter(find.byType(InteractiveViewer));
    await _doubleTap(tester, center);
    expect(find.textContaining('x'), findsOneWidget);

    // A new decoded frame must not reset the transform.
    await _pumpFrame(tester, second!);
    expect(find.textContaining('x'), findsOneWidget);
  });

  testWidgets('second double-tap resets to 1x and hides the pill',
      (tester) async {
    final frame = await tester.runAsync(() => _decode(_pngBytes()));
    expect(frame, isNotNull);
    addTearDown(() => frame!.dispose());

    await _pumpFrame(tester, frame!);
    final center = tester.getCenter(find.byType(InteractiveViewer));
    await _doubleTap(tester, center);
    expect(find.textContaining('x'), findsOneWidget);

    // Second double-tap resets.
    await _doubleTap(tester, center);
    expect(find.textContaining('x'), findsNothing);
  });

  testWidgets('reset pill shows only while zoomed', (tester) async {
    final frame = await tester.runAsync(() => _decode(_pngBytes()));
    expect(frame, isNotNull);
    addTearDown(() => frame!.dispose());

    await _pumpFrame(tester, frame!);
    expect(find.textContaining('✕'), findsNothing);

    final center = tester.getCenter(find.byType(InteractiveViewer));
    await _doubleTap(tester, center);
    expect(find.textContaining('✕'), findsOneWidget);

    // Tapping the pill resets and hides it.
    await tester.tap(find.textContaining('✕'));
    await tester.pumpAndSettle();
    expect(find.textContaining('✕'), findsNothing);
  });
}
